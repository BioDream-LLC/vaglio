package draft

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const doc = `Plan Policy 7.1: Knee arthroscopy (CPT 29881).
Arthroscopy is covered when mechanical symptoms persist after conservative care.
Arthroscopy is not covered for osteoarthritis alone (ICD-10 M17.11).`

const good = "```vaglio\n" + `policy knee
  title "Knee arthroscopy"
  version 1
  source "Plan Policy 7.1"
  questionnaire https://payer.example/Q/knee
  service cpt 29881

evidence mechanical
  text "Mechanical symptoms after conservative care"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  find question mechanical

criterion unmet
  text "No indication"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by mechanical

decision approve approves
  text "Approve"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by unmet

decision deny denies
  text "Deny"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by mechanical
` + "```"

// scripted is a model that gives these answers in turn and keeps what it was sent.
type scripted struct {
	answers []string
	sent    [][]Message
}

func (s *scripted) Complete(_ context.Context, m []Message) (string, error) {
	s.sent = append(s.sent, append([]Message(nil), m...))
	a := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}
	return a, nil
}

func TestGates(t *testing.T) {
	req := Request{ID: "knee", Document: doc}
	if _, f := Gate(req, good); len(f) != 0 {
		t.Fatalf("a good draft failed: %v", f)
	}
	cases := map[string]struct{ edit, want string }{
		"paraphrased clause": {strings.Replace(good, "persist after conservative care.\"\n  find", "continue.\"\n  find", 1), "not quoted word for word"},
		"invented code":      {strings.Replace(good, "service cpt 29881", "service cpt 29881 29880", 1), "code 29880 does not appear"},
		"wrong id":           {strings.Replace(good, "policy knee", "policy knees", 1), `must be "knee"`},
		"does not compile":   {strings.Replace(good, "approves", "allows", 1), "draft.vaglio:"},
		"contradicts itself": {strings.Replace(good, "  defeated by mechanical\n```", "```", 1), "all hold at once"},
	}
	for name, c := range cases {
		_, f := Gate(req, c.edit)
		if !strings.Contains(strings.Join(f, "\n"), c.want) {
			t.Errorf("%s: %v, want %q", name, f, c.want)
		}
	}
	// Quotation is exact up to white space, case and typographic quotes.
	if _, f := Gate(Request{ID: "knee", Document: strings.ReplaceAll(doc, " ", "\n  ")}, good); len(f) != 0 {
		t.Errorf("reflowed document: %v", f)
	}
}

func TestTheLoopFeedsFailuresBack(t *testing.T) {
	bad := strings.Replace(good, "service cpt 29881", "service cpt 29881 29880", 1)
	m := &scripted{answers: []string{bad, good}}
	res, err := Draft(context.Background(), m, Request{ID: "knee", Document: doc, Name: "policy-7.1.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attempts) != 2 || len(m.sent) != 2 {
		t.Fatalf("%d attempts", len(res.Attempts))
	}
	last := m.sent[1][len(m.sent[1])-1].Content
	if !strings.Contains(last, "code 29880 does not appear") {
		t.Errorf("the failure was not fed back: %q", last)
	}
	if res.Policy.Status != "draft" || !strings.Contains(res.Source, "status draft") ||
		!strings.HasPrefix(res.Source, "# DRAFT, machine-drafted from policy-7.1.txt") || !strings.Contains(res.Source, "in 2 round(s)") {
		t.Errorf("source:\n%s", res.Source)
	}
	// What it writes is a policy file: it compiles back to the same draft, comments and all.
	if _, f := Gate(Request{ID: "knee", Document: doc}, res.Source); len(f) != 0 {
		t.Errorf("the written draft fails its own gates: %v", f)
	}
}

func TestGivesUp(t *testing.T) {
	m := &scripted{answers: []string{"I cannot help with that."}}
	res, err := Draft(context.Background(), m, Request{ID: "knee", Document: doc, Rounds: 3})
	if err != ErrNoDraft || len(res.Attempts) != 3 || res.Policy != nil {
		t.Fatalf("%v, %d attempts", err, len(res.Attempts))
	}
}

func TestOpenAIClient(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer ts.Close()
	m := &OpenAI{Base: ts.URL + "/v1", Key: "k", Model: "m"}
	out, err := m.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil || out != "hello" || got["model"] != "m" || got["temperature"] != 0.0 {
		t.Fatalf("%q %v %v", out, err, got)
	}
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); io.WriteString(w, "slow down") })
	if _, err := m.Complete(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("error %v", err)
	}
}

func TestKind(t *testing.T) {
	for f, want := range map[string]string{
		"with only a documented, decisions approve and deny all hold at once: an approval and a denial must never both hold": "contradiction",
		"with nothing documented, every decision is defeated, so nothing is decided":                                         "gap",
		"decision deny_x is reached by no combination of evidence":                                                           "gap",
		`the clause of a is not quoted word for word from the document: "x". Copy the sentence it encodes exactly.`:          "quote",
		"code 12345 does not appear in the document.":                                                                        "code",
		"criterion c defeats nothing, so it can never change a decision":                                                     "lint",
	} {
		if got := Kind(f); got != want {
			t.Errorf("Kind(%q) = %s, want %s", f, got, want)
		}
	}
}

// TestNearestSentence: a clause cut short at a comma gets the whole sentence back as advice, and the gate still refuses it.
func TestNearestSentence(t *testing.T) {
	doc := "Evaluations are covered. They are necessary when the patient has an illness and/or symptoms sufficient to " +
		"cause maladaptive functioning in personal or social settings, which may be suggested by a referral.\nOther text."
	got := nearest(doc, "They are necessary when the patient has an illness and/or symptoms sufficient to cause "+
		"maladaptive functioning in personal or social settings.")
	if !strings.HasPrefix(got, "They are necessary") || !strings.HasSuffix(got, "by a referral.") {
		t.Fatalf("nearest = %q", got)
	}
	if nearest(doc, "Something the document never says about knees.") != "" {
		t.Fatal("an unrelated clause got a suggestion")
	}
}

// TestSeveralPolicies: a document holding two services to different criteria may be drafted as one policy per
// service; each is gated alone, its id must be the request's with a suffix, and a code belongs to one of them.
func TestSeveralPolicies(t *testing.T) {
	doc := "Covered: CPT 11111 and CPT 22222.\n" +
		"CPT 11111 is covered when the member has persistent pain.\n" +
		"CPT 22222 is covered when the member has a fracture.\n"
	one := func(id, code, ev, clause string) string {
		return "```vaglio\npolicy " + id + "\n  title \"T\"\n  version 1.0\n  source \"S\"\n" +
			"  questionnaire https://payer.example/Questionnaire/x\n  service cpt " + code + "\n\n" +
			"evidence " + ev + "\n  text \"E\"\n  clause \"" + clause + "\"\n  find question " + ev + "\n\n" +
			"criterion no_" + ev + "\n  text \"N\"\n  clause \"" + clause + "\"\n  defeated by " + ev + "\n\n" +
			"decision approve approves\n  text \"A\"\n  clause \"" + clause + "\"\n  defeated by no_" + ev + "\n\n" +
			"decision deny denies\n  text \"D\"\n  clause \"" + clause + "\"\n  defeated by " + ev + "\n```\n"
	}
	pain := one("knee_pain", "11111", "pain", "CPT 11111 is covered when the member has persistent pain.")
	frac := one("knee_fracture", "22222", "fracture", "CPT 22222 is covered when the member has a fracture.")
	req := Request{ID: "knee", Document: doc}
	ps, f := GateAll(req, pain+"\n"+frac)
	if len(f) != 0 || len(ps) != 2 || ps[0].ID != "knee_pain" || ps[1].ID != "knee_fracture" {
		t.Fatalf("%v %v", ps, f)
	}
	if _, f := GateAll(req, pain+"\n"+strings.Replace(frac, "knee_fracture", "other", 1)); len(f) == 0 ||
		!strings.Contains(strings.Join(f, " "), "each id is knee_") {
		t.Errorf("a stray id: %v", f)
	}
	if _, f := GateAll(req, pain+"\n"+strings.Replace(frac, "cpt 22222", "cpt 11111", 1)); len(f) == 0 ||
		!strings.Contains(strings.Join(f, " "), "also decided by policy knee_pain") {
		t.Errorf("a code in two policies: %v", f)
	}
	if _, f := GateAll(req, pain+"\n"+strings.Replace(frac, "has a fracture.\"", "is hurt.\"", 1)); len(f) == 0 ||
		!strings.HasPrefix(f[0], "policy knee_fracture: ") {
		t.Errorf("a failure not named by its policy: %v", f)
	}
}

// A clause quoting a sentence that runs on through its list is the document's own words: only the bullets are left
// out. A clause the document does not hold is still refused.
func TestQuoteThroughAList(t *testing.T) {
	doc := "Coverage requires that the Beneficiary has EITHER:\n  • Intractable pain which has failed 6 weeks of treatment\n  OR\n  2. Severe or rapidly progressive symptoms\n"
	for clause, want := range map[string]bool{
		"The Beneficiary has EITHER: Intractable pain which has failed 6 weeks of treatment OR Severe or rapidly progressive symptoms": true,
		"The Beneficiary has EITHER: Intractable pain which has failed 12 weeks of treatment":                                       false,
	} {
		if got := Quoted(doc, clause); got != want {
			t.Errorf("%q: quoted %v, want %v", clause, got, want)
		}
	}
}
