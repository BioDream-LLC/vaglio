// Package draft drafts a policy from a payer's coverage document with a language model, and refuses every draft that
// fails a check a machine can make.
//
// The model is asked for the policy in Vaglio's policy language. Each draft goes through these gates, and the failures of
// all of them go back to the model for another round:
//
//  1. It compiles and validates (errors with line and column).
//  2. Every clause is quoted from the document, word for word: a clause is the evidence for the rule, and a model's
//     paraphrase is not evidence of what the payer wrote.
//  3. Every code - services, conditions, procedures, observations, coded values - appears in the document. A model will
//     otherwise supply plausible codes from memory, and a plausible code is the most dangerous kind of wrong.
//  4. It passes the exhaustive check: it never contradicts itself or rejects everything, and every decision is reachable.
//  5. It has no lint warnings: evidence that changes nothing, criteria that always hold, a question read twice.
//
// What passes is written with status draft, which the server refuses to load. The gates prove the draft is well formed
// and grounded in the document; they cannot prove its logic says what the document means. A person checks that, and signs
// it off with vaglio signoff.
package draft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Model is a chat completion endpoint.
type Model interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI is any OpenAI-compatible chat completions API: OpenRouter, a local server, and so on.
type OpenAI struct {
	Base  string // e.g. https://openrouter.ai/api/v1
	Key   string
	Model string
	HTTP  *http.Client
}

// Complete asks for one completion at temperature 0.
func (m *OpenAI) Complete(ctx context.Context, messages []Message) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": m.Model, "messages": messages, "temperature": 0})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(m.Base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.Key != "" {
		req.Header.Set("Authorization", "Bearer "+m.Key)
	}
	hc := m.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("the model answered %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("the model's answer had no completion in it")
	}
	return out.Choices[0].Message.Content, nil
}

// Request is what to draft.
type Request struct {
	ID       string // the policy's id
	Document string // the coverage document, as text
	Name     string // what to call the document in the draft's header
	Rounds   int    // drafts to try before giving up; 0 is 4
	// OnRound, when set, is told of each round as it ends: for a console showing progress.
	OnRound func(round int, a Attempt)
}

// Attempt is one round: the model's answer and what was wrong with it.
type Attempt struct {
	Answer   string
	Failures []string
}

// Result is a draft that passed every gate, and every round it took.
type Result struct {
	Policy   *policy.Policy
	Source   string // the policy file to write: a header, then the policy in the language
	Attempts []Attempt
	// Policies and Sources are every policy the draft holds, Policy and Source the first: a document that holds
	// different services to different criteria is drafted as one policy per service (ids ID_name).
	Policies []*policy.Policy
	Sources  map[string]string
}

// ErrNoDraft is every round failing.
var ErrNoDraft = errors.New("no draft passed every check")

// Draft runs the loop.
func Draft(ctx context.Context, m Model, req Request) (*Result, error) {
	rounds := req.Rounds
	if rounds <= 0 {
		rounds = 4
	}
	messages := []Message{
		{Role: "system", Content: guide},
		{Role: "user", Content: fmt.Sprintf("Write the policy with id %s for this coverage document.\n\n<document>\n%s\n</document>",
			req.ID, req.Document)},
	}
	res := &Result{}
	for i := 0; i < rounds; i++ {
		answer, err := m.Complete(ctx, messages)
		if err != nil {
			return res, err
		}
		ps, failures := GateAll(req, answer)
		res.Attempts = append(res.Attempts, Attempt{Answer: answer, Failures: failures})
		if req.OnRound != nil {
			req.OnRound(i+1, res.Attempts[i])
		}
		if len(failures) == 0 {
			sum := sha256.Sum256([]byte(req.Document))
			res.Sources = map[string]string{}
			for _, p := range ps {
				p.Status = policy.Draft
				res.Sources[p.ID] = fmt.Sprintf(header, req.Name, hex.EncodeToString(sum[:8]),
					time.Now().UTC().Format("2006-01-02"), i+1) + policy.Format(p)
			}
			res.Policies, res.Policy, res.Source = ps, ps[0], res.Sources[ps[0].ID]
			return res, nil
		}
		messages = append(messages,
			Message{Role: "assistant", Content: answer},
			Message{Role: "user", Content: "That draft fails these checks. Fix every one and send the whole policy again, " +
				"in one vaglio block.\n\n- " + strings.Join(failures, "\n- ")})
	}
	return res, ErrNoDraft
}

const header = `# DRAFT, machine-drafted from %s (sha256 %s...) on %s, in %d round(s).
#
# Checked by machine: it compiles; every clause is quoted word for word from the document; every code appears in the
# document; it decides every combination of evidence without contradicting itself; it has no lint warnings.
# NOT checked: that its logic says what the document means, that its evidence is all the document asks for, and that the
# questionnaire linkIds are the payer's. A clinician checks those, and signs the policy off with vaglio signoff.

`

var block = regexp.MustCompile("(?s)```(?:vaglio)?\\s*\\n(.*?)```")

// GateAll checks every policy in one answer against the document: one policy, or one per service when the document
// holds different services to different criteria. Their ids are the request's, or the request's followed by _name;
// each is gated alone, and a service may be decided by only one of them. It returns the policies when nothing fails.
func GateAll(req Request, answer string) ([]*policy.Policy, []string) {
	srcs := Sources(answer)
	if len(srcs) <= 1 {
		p, f := Gate(req, answer)
		if len(f) > 0 {
			return nil, f
		}
		return []*policy.Policy{p}, nil
	}
	var out []*policy.Policy
	var failures []string
	ids, owner := map[string]bool{}, map[string]string{}
	for i, src := range srcs {
		id := fmt.Sprintf("the policy in block %d", i+1)
		sub := req
		if q, err := policy.Parse("draft.vaglio", src); err == nil {
			id = "policy " + q.ID
			if q.ID == req.ID || strings.HasPrefix(q.ID, req.ID+"_") {
				sub.ID = q.ID
			} else {
				failures = append(failures, fmt.Sprintf("%s: with several policies, each id is %s_ and a short name "+
					"for its service", id, req.ID))
			}
			if ids[q.ID] {
				failures = append(failures, fmt.Sprintf("%s: two policies have this id", id))
			}
			ids[q.ID] = true
			for _, sv := range q.Services {
				k := sv.System + "|" + sv.Code
				if o, ok := owner[k]; ok && o != q.ID {
					failures = append(failures, fmt.Sprintf("%s: code %s is also decided by policy %s; each service "+
						"belongs to one policy", id, sv.Code, o))
				}
				owner[k] = q.ID
			}
		}
		p, f := Gate(sub, "```vaglio\n"+src+"```")
		for _, x := range f {
			failures = append(failures, id+": "+x)
		}
		if p != nil {
			out = append(out, p)
		}
	}
	if len(failures) > 0 {
		return nil, failures
	}
	return out, nil
}

// Gate checks one policy against the document. It returns the policy when there are no failures.
func Gate(req Request, answer string) (*policy.Policy, []string) {
	src := Source(answer)
	p, err := policy.Parse("draft.vaglio", src)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "can be checked exhaustively") {
			msg += fmt.Sprintf(". Fit it in %d: the absence of a fact is never evidence (write criterion no_x defeated by x, "+
				"as in the example); alternatives that lead to the same result (any one of a list of indications or "+
				"diagnoses) may be one evidence argument with a find line for each, its clause the sentence that introduces "+
				"the list", policy.MaxEvidence)
		}
		return nil, []string{msg}
	}
	var failures []string
	if p.ID != req.ID {
		failures = append(failures, fmt.Sprintf("the policy id is %q; it must be %q", p.ID, req.ID))
	}
	for _, a := range p.Arguments {
		if !Quoted(req.Document, a.Clause) {
			msg := fmt.Sprintf("the clause of %s is not quoted word for word from the document: %q. "+
				"Copy the sentence it encodes exactly.", a.Name, a.Clause)
			if near := nearest(req.Document, a.Clause); near != "" {
				msg += fmt.Sprintf(" The nearest sentence in the document is %q: copy it whole, or the part of it the "+
					"argument encodes, character for character.", near)
			}
			failures = append(failures, msg)
		}
	}
	words := tokens(req.Document)
	for _, c := range codes(p) {
		if !words[c] {
			failures = append(failures, fmt.Sprintf("code %s does not appear in the document. Use only codes the document lists; "+
				"if it lists none for this, use a question instead.", c))
		}
	}
	rep := decide.Check(p)
	for i, d := range rep.Defects {
		if i == 3 {
			failures = append(failures, fmt.Sprintf("and %d more combinations of evidence like these", len(rep.Defects)-3))
			break
		}
		failures = append(failures, defect(p, d))
	}
	for _, u := range rep.Unreachable {
		failures = append(failures, fmt.Sprintf("decision %s is reached by no combination of evidence", u))
	}
	for _, l := range policy.Lint(p) {
		if !strings.HasPrefix(l, "status draft") {
			failures = append(failures, l)
		}
	}
	if len(failures) > 0 {
		return nil, failures
	}
	return p, nil
}

// Source is the policy in a model's answer: its vaglio block, or the whole answer when it has none.
func Source(answer string) string {
	if m := block.FindStringSubmatch(answer); m != nil {
		return m[1]
	}
	return answer
}

// Sources is every policy in a model's answer: its vaglio blocks, or the whole answer when it has none.
func Sources(answer string) []string {
	var out []string
	for _, m := range block.FindAllStringSubmatch(answer, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		return []string{answer}
	}
	return out
}

// Kind sorts a gate's failure for a reader: "contradiction" (approve and deny both hold), "gap" (no decision holds, or
// one is never reached), "quote" (a clause not word for word), "code" (a code not in the document), "syntax", or
// "lint". The first two may be holes in the document as much as in the draft; the rest are the draft's.
func Kind(failure string) string {
	switch {
	case strings.Contains(failure, "both hold at once") || strings.Contains(failure, "all hold at once"):
		return "contradiction"
	case strings.Contains(failure, "is reached by no combination") || strings.Contains(failure, "every decision is defeated") ||
		strings.Contains(failure, "go round in a circle"):
		return "gap"
	case strings.Contains(failure, "not quoted word for word"):
		return "quote"
	case strings.HasPrefix(failure, "code "):
		return "code"
	case strings.Contains(failure, ":") && (strings.Contains(failure, "line ") || strings.Contains(failure, "expected")):
		return "syntax"
	}
	return "lint"
}

// defect says what goes wrong on one combination of evidence, in terms the author can act on.
func defect(p *policy.Policy, present []string) string {
	req := decide.Request{Policy: p.ID, Evidence: []decide.Assertion{}}
	for _, n := range present {
		req.Evidence = append(req.Evidence, decide.Assertion{Argument: n, Finding: decide.Present,
			Citation: decide.Citation{Reference: "x", Quote: "x"}})
	}
	with := "with no evidence documented"
	if len(present) > 0 {
		with = "with only " + strings.Join(present, ", ") + " documented"
	}
	res, err := decide.Decide(p, req)
	if err != nil {
		return with + ": " + err.Error()
	}
	var holding []string
	for _, a := range p.Arguments {
		if a.Kind == policy.Decision && res.Labels[a.Name] == "in" {
			holding = append(holding, a.Name)
		}
	}
	switch {
	case len(holding) > 1:
		return fmt.Sprintf("%s, decisions %s all hold at once: an approval and a denial must never both hold, so "+
			"something that holds here must defeat one of them", with, strings.Join(holding, " and "))
	case len(holding) == 0 && res.Reason == decide.Indeterminate:
		return with + ", the attacks go round in a circle and settle nothing: remove the cycle"
	}
	return with + ", every decision is defeated, so nothing is decided: a denial must hold whenever the approval does not"
}

// codes is every code the policy names.
func codes(p *policy.Policy) []string {
	var out []string
	for _, s := range p.Services {
		out = append(out, s.Code)
	}
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			switch {
			case s.Condition != nil:
				out = append(out, s.Condition.Codes...)
			case s.Procedure != nil:
				out = append(out, s.Procedure.Codes...)
			case s.Medication != nil:
				out = append(out, s.Medication.Codes...)
			case s.Observation != nil:
				out = append(out, s.Observation.Codes...)
				if v := s.Observation.Values; v != nil {
					out = append(out, v.Present...)
					out = append(out, v.Absent...)
				}
			}
		}
	}
	return out
}

// normalize folds what a faithful quotation may differ in: runs of white space, curly quotes and dashes, case.
// Normalize is text as quotes are compared: typographic quotes and dashes made plain, spacing collapsed, lower case.
func Normalize(s string) string {
	s = strings.NewReplacer("\u2018", "'", "\u2019", "'", "\u201c", `"`, "\u201d", `"`, "\u2013", "-", "\u2014", "-",
		"\u00a0", " ").Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// listMarker is a list item's marker at the start of a line: a bullet, or a number or letter with a point or bracket.
var listMarker = regexp.MustCompile(`(?m)^[ \t]*(?:[•◦▪▫·‣⁃∙○●■□\-*o]|\(?(?:\d{1,2}|[a-zA-Z]|[ivxIVX]{1,4})[.)])[ \t]+`)

// Quoted is whether clause is in the document word for word: spacing, typographic quotes and dashes, case and list
// items' markers aside.
func Quoted(doc, clause string) bool {
	c := Normalize(clause)
	return strings.Contains(Normalize(doc), c) || strings.Contains(Normalize(withoutListMarkers(doc)), c)
}

// withoutListMarkers is the document with list items' markers dropped, so a clause quoting a sentence that runs on
// through its list ("The Beneficiary has EITHER: ... OR ...") is still quoted in the document's own words, in order:
// only the bullets are not repeated.
func withoutListMarkers(doc string) string { return listMarker.ReplaceAllString(doc, "") }

// tokens is every word in the document, split at anything a code cannot contain.
func tokens(doc string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(doc, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-')
	}) {
		out[strings.Trim(w, ".-")] = true
		out[w] = true
	}
	return out
}

// nearest is the document's sentence sharing the most words with a clause that failed the quote gate, when it shares at
// least half of the clause's words: a model told where it went wrong copies better than one told only that it did. It
// is advice in a failure message, never a reason to accept a clause.
func nearest(doc, clause string) string {
	want := map[string]bool{}
	for _, w := range strings.Fields(Normalize(clause)) {
		want[strings.Trim(w, ".,;:()\"'")] = true
	}
	if len(want) == 0 {
		return ""
	}
	best, score := "", 0
	for _, s := range sentences(doc) {
		n, seen := 0, map[string]bool{}
		for _, w := range strings.Fields(Normalize(s)) {
			w = strings.Trim(w, ".,;:()\"'")
			if want[w] && !seen[w] {
				seen[w] = true
				n++
			}
		}
		if n > score {
			best, score = s, n
		}
	}
	if 2*score < len(want) {
		return ""
	}
	if r := []rune(best); len(r) > 600 {
		best = string(r[:600]) + "..."
	}
	return best
}

// sentences splits a document into sentences and lines, kept as written.
func sentences(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		start := 0
		for i := 0; i < len(line); i++ {
			if (line[i] == '.' || line[i] == ';' || line[i] == '?') && (i+1 == len(line) || line[i+1] == ' ') {
				if t := strings.TrimSpace(line[start : i+1]); t != "" {
					out = append(out, t)
				}
				start = i + 1
			}
		}
		if t := strings.TrimSpace(line[start:]); t != "" {
			out = append(out, t)
		}
	}
	return out
}
