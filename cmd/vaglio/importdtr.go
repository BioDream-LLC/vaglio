package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// importDTRCmd starts a policy from content a plan already has: a Da Vinci DTR Questionnaire (or a questionnaire
// package Bundle, with its CQL Libraries), as a draft. Each yes/no question becomes a piece of evidence found in the
// provider's answers (find question LINKID) under the questionnaire's canonical URL, with its words as the clause. The
// CQL is not run. Its define statements are listed in the draft's header, and where a question's pre-fill reaches a
// record lookup of a shape import-dtr reads (cql.go), a find line is suggested beside it, as a comment, for the author
// to read the rules
// from. The draft has no rules (criteria and decisions): an author writes them, and a clinician signs the policy off. It
// decides nothing until then.
func importDTRCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("import-dtr", flag.ExitOnError)
	id := fs.String("id", "", "the policy's id")
	out := fs.String("out", "", "the .vaglio file to write; it must not exist")
	canonical := fs.String("questionnaire", "", "the questionnaire's canonical URL, when it carries none (answers are read by it)")
	service := fs.String("service", "", "what the policy decides: SYSTEM CODE..., such as \"cpt 72148 72149\"")
	fs.Parse(args)
	if *id == "" || *out == "" || *service == "" || fs.NArg() != 1 {
		return errors.New("usage: vaglio import-dtr -id ID -service \"SYSTEM CODE...\" -out FILE.vaglio QUESTIONNAIRE-OR-BUNDLE.json")
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	src, notes, err := importDTR(raw, *id, *service, *canonical)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s, a draft with %s\n", *out, notes)
	return nil
}

type qItem struct {
	LinkID     string  `json:"linkId"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`
	Item       []qItem `json:"item"`
	Extensions []struct {
		URL   string `json:"url"`
		Value *struct {
			Expression string `json:"expression"`
		} `json:"valueExpression"`
	} `json:"extension"`
}

type questionnaire struct {
	ResourceType string  `json:"resourceType"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Version      string  `json:"version"`
	Item         []qItem `json:"item"`
}

var (
	cqlDefine = regexp.MustCompile(`(?m)^define\s+(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*:`)
	notWord   = regexp.MustCompile(`[^a-z0-9]+`)
)

// importDTR makes the draft's source, and says what it holds.
func importDTR(raw []byte, id, service, canonical string) (string, string, error) {
	var top struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return "", "", fmt.Errorf("not JSON: %w", err)
	}
	var q *questionnaire
	var defines []string
	libs := map[string]*cqlLib{}
	resources := []json.RawMessage{raw}
	if top.ResourceType == "Bundle" {
		resources = nil
		for _, e := range top.Entry {
			resources = append(resources, e.Resource)
		}
	}
	for _, r := range resources {
		var head struct {
			ResourceType string `json:"resourceType"`
			Content      []struct {
				ContentType string `json:"contentType"`
				Data        string `json:"data"`
			} `json:"content"`
		}
		json.Unmarshal(r, &head)
		switch head.ResourceType {
		case "Questionnaire":
			if q == nil {
				q = &questionnaire{}
				if err := json.Unmarshal(r, q); err != nil {
					return "", "", err
				}
			}
		case "Library":
			for _, c := range head.Content {
				if c.ContentType != "text/cql" {
					continue
				}
				cql, err := base64.StdEncoding.DecodeString(c.Data)
				if err != nil {
					continue
				}
				for _, m := range cqlDefine.FindAllStringSubmatch(string(cql), -1) {
					defines = append(defines, m[1]+m[2])
				}
				if l := parseCQL(string(cql)); l.name != "" {
					libs[l.name] = l
				}
			}
		}
	}
	if q == nil {
		return "", "", errors.New("no Questionnaire in it")
	}
	if canonical != "" {
		q.URL = canonical
	}
	if q.URL == "" {
		return "", "", errors.New("the Questionnaire has no canonical url, and answers are read by it: give one with -questionnaire")
	}
	// The yes/no questions, anywhere in the tree.
	type ev struct{ name, link, text, expr string }
	var evs []ev
	seen := map[string]bool{}
	var walk func([]qItem)
	walk = func(items []qItem) {
		for _, it := range items {
			if it.Type == "boolean" && strings.TrimSpace(it.Text) != "" {
				name := strings.Trim(notWord.ReplaceAllString(strings.ToLower(it.LinkID), "_"), "_")
				if name == "" || (name[0] >= '0' && name[0] <= '9') {
					name = "q_" + name
				}
				for seen[name] {
					name += "_"
				}
				seen[name] = true
				e := ev{name: name, link: it.LinkID, text: strings.TrimSpace(it.Text)}
				for _, x := range it.Extensions {
					if x.Value != nil && strings.Contains(x.URL, "initialExpression") {
						e.expr = x.Value.Expression
					}
				}
				evs = append(evs, e)
			}
			walk(it.Item)
		}
	}
	walk(q.Item)
	if len(evs) == 0 {
		return "", "", errors.New("the Questionnaire has no yes/no questions: there is nothing to read evidence from")
	}
	if len(evs) > policy.MaxEvidence {
		return "", "", fmt.Errorf("%d yes/no questions, more than a policy's %d pieces of evidence", len(evs), policy.MaxEvidence)
	}
	title := q.Title
	if title == "" {
		title = q.Name
	}
	if title == "" {
		title = id
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# DRAFT, imported from the DTR Questionnaire %s", q.URL)
	if q.Version != "" {
		fmt.Fprintf(&b, " version %s", q.Version)
	}
	b.WriteString(".\n#\n# Each yes/no question is a piece of evidence, found in the provider's answers. The rules (criteria and\n" +
		"# decisions) are not imported: until they are written from the plan's coverage policy, the placeholder at the end\n" +
		"# pends everything. Then a clinician signs it off; as a draft it decides nothing.\n")
	// Of the CQL's defines, the ones the questions' expressions use: where the answers come from in the record.
	var used []string
	for _, d := range defines {
		for _, e := range evs {
			if strings.Contains(e.expr, "."+d) || strings.Contains(e.expr, `."`+d+`"`) {
				used = append(used, d)
				break
			}
		}
	}
	sort.Strings(used)
	used = slices.Compact(used)
	if len(used) > 0 {
		b.WriteString("#\n# The package's CQL pre-fills these questions from the record (the defines below): read them for what the\n" +
			"# plan counts as each. Where a question's CQL looks up the record in a shape this reader knows, a find line is\n" +
			"# suggested beside it as a comment: uncomment it only if the plan counts that record as the answer.\n")
		for _, d := range used {
			fmt.Fprintf(&b, "#   %s\n", d)
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "policy %s\n  title %q\n  version 0.1.0\n  source %q\n  status draft\n  questionnaire %s\n  service %s\n",
		id, title, "DTR Questionnaire "+q.URL, q.URL, service)
	suggested := 0
	for _, e := range evs {
		fmt.Fprintf(&b, "\nevidence %s\n  text %q\n  clause %q\n  find question %s\n", e.name, e.text, e.text, e.link)
		if e.expr != "" {
			fmt.Fprintf(&b, "  # the questionnaire pre-fills it with: %s\n", strings.ReplaceAll(e.expr, "\n", " "))
			lib := libs[strings.Trim(strings.SplitN(e.expr, ".", 2)[0], `"`)]
			finds, notes := lib.suggest(e.expr)
			for _, f := range finds {
				fmt.Fprintf(&b, "  # suggested from its CQL: %s\n", f)
				suggested++
			}
			for _, n := range notes {
				fmt.Fprintf(&b, "  # note: %s\n", n)
			}
		}
	}
	// The one rule imported: nothing is approved until an author writes the rules. It keeps the draft a policy the
	// language accepts, and every request under it pends.
	b.WriteString(`
criterion rules_not_written
  text "The rules of this policy have not been written yet: replace this criterion and the decision below."
  clause "IMPORTED: the coverage rules are not in the questionnaire."

decision approve approves
  text "Approve."
  clause "IMPORTED: the coverage rules are not in the questionnaire."
  defeated by rules_not_written
`)
	return b.String(), fmt.Sprintf("%d pieces of evidence (%d pre-filled by CQL; %d find lines suggested from it, as "+
		"comments); no rules yet", len(evs), len(used), suggested), nil
}
