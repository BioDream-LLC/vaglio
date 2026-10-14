package policy

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const header = "policy p\n  title t\n  version 1\n  source s\n"

func parse(t *testing.T, src string) *Policy {
	t.Helper()
	p, err := Parse("p.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sameModel compares two policies as the engine sees them: attack order does not matter to grounded semantics.
func sameModel(t *testing.T, a, b *Policy) {
	t.Helper()
	norm := func(p *Policy) string {
		c := *p
		c.Attacks = append([]Attack(nil), p.Attacks...)
		sort.Slice(c.Attacks, func(i, j int) bool {
			return c.Attacks[i].From+">"+c.Attacks[i].To < c.Attacks[j].From+">"+c.Attacks[j].To
		})
		raw, _ := json.Marshal(c)
		return string(raw)
	}
	if norm(a) != norm(b) {
		t.Fatalf("not the same policy:\n%s\n%s", norm(a), norm(b))
	}
}

func TestEveryPolicyRoundTrips(t *testing.T) {
	files, _ := filepath.Glob("../../policies/*" + Ext)
	if len(files) == 0 {
		t.Fatal("no policies")
	}
	for _, f := range files {
		p, err := LoadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		again := parse(t, Format(p))
		sameModel(t, p, again)
		if Format(again) != Format(p) {
			t.Errorf("%s: Format is not stable", f)
		}
		// JSON is the same model: what the API serves loads back as the policy.
		raw, _ := json.Marshal(p)
		fromJSON, err := Load(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		sameModel(t, p, fromJSON)
	}
}

func TestEverySourceForm(t *testing.T) {
	p := parse(t, header+`  questionnaire https://q.example/Q
  service cpt 1 2
  service https://codes.example/x 9

evidence e   # every kind of source
  text "t \"quoted\" \\ here"
  clause "c"
  find question q1
  find condition icd10 A1 B2 when confirmed provisional
  find procedure cpt 72148 within 183 days
  find procedure cpt 97110 lasting 42 days not-done means absent
  find observation loinc 39156-5 within 365 days at least 40 kg/m2
  find observation loinc 1-1 at most 7.5 %
  find observation loinc 2-2 yes-no
  find observation loinc 72166-2 coded snomed present 77176002 absent 266919005 8517006

decision ok approves
  text "t"
  clause "c"
  defeated by e
`)
	if p.Arguments[0].Text != `t "quoted" \ here` {
		t.Errorf("text %q", p.Arguments[0].Text)
	}
	if !reflect.DeepEqual(p.Services, []Service{{Systems["cpt"], "1"}, {Systems["cpt"], "2"}, {"https://codes.example/x", "9"}}) {
		t.Errorf("services %+v", p.Services)
	}
	f := p.Arguments[0].Find
	want := []Source{
		{Question: "q1"},
		{Condition: &Codes{System: Systems["icd10"], Codes: []string{"A1", "B2"}, Present: []string{"confirmed", "provisional"}}},
		{Procedure: &Procedure{System: Systems["cpt"], Codes: []string{"72148"}, WithinDays: 183}},
		{Procedure: &Procedure{System: Systems["cpt"], Codes: []string{"97110"}, MinDays: 42, NotDoneIsAbsent: true}},
		{Observation: &Observation{System: Systems["loinc"], Codes: []string{"39156-5"}, WithinDays: 365, AtLeast: &Threshold{40, "kg/m2"}}},
		{Observation: &Observation{System: Systems["loinc"], Codes: []string{"1-1"}, AtMost: &Threshold{7.5, "%"}}},
		{Observation: &Observation{System: Systems["loinc"], Codes: []string{"2-2"}, Boolean: true}},
		{Observation: &Observation{System: Systems["loinc"], Codes: []string{"72166-2"}, Values: &ValueCodes{
			System: Systems["snomed"], Present: []string{"77176002"}, Absent: []string{"266919005", "8517006"}}}},
	}
	if !reflect.DeepEqual(f, want) {
		got, _ := json.MarshalIndent(f, "", " ")
		t.Errorf("sources:\n%s", got)
	}
	if !reflect.DeepEqual(p.Attacks, []Attack{{From: "e", To: "ok"}}) {
		t.Errorf("attacks %+v", p.Attacks)
	}
	sameModel(t, p, parse(t, Format(p)))
}

func TestParseErrorsSayWhere(t *testing.T) {
	cases := map[string]struct {
		src       string
		line, col int
		msg       string
	}{
		"first line":       {"title t\n", 1, 1, `starts with "policy <id>"`},
		"undeclared":       {header + "decision ok approves\n  text \"t\"\n  clause \"c\"\n  defeated by nobody\n", 8, 15, `"nobody" is not declared`},
		"declared twice":   {header + "evidence e\n  text \"t\"\n  clause \"c\"\nevidence e\n", 8, 10, "already declared at line 5"},
		"verdict":          {header + "decision ok allows\n", 5, 13, "approves or denies"},
		"unclosed string":  {header + "evidence e\n  text \"t\n", 6, 8, "not closed"},
		"unknown system":   {header + "evidence e\n  find condition icd9 1\n", 6, 18, `"icd9" is not a code system`},
		"no codes":         {header + "evidence e\n  find procedure cpt within 3 days\n", 6, 18, "at least one code"},
		"days":             {header + "evidence e\n  find procedure cpt 1 within three days\n", 6, 31, `"three" is not a number of days`},
		"find on decision": {header + "decision ok approves\n  find question q\n", 6, 3, "only evidence is found"},
		"text twice":       {header + "evidence e\n  text \"a\"\n  text \"b\"\n", 7, 3, "text is given twice"},
		"stray field":      {header + "evidence e\n  service cpt 1\n", 6, 3, `"service" is not an argument field`},
		"bad tail":         {header + "evidence e\n  find procedure cpt 1 within 3 days lately\n", 6, 38, `"lately" does not belong`},
		"threshold":        {header + "evidence e\n  find observation loinc 1 at least forty kg\n", 6, 37, `"forty" is not a number`},
	}
	for name, c := range cases {
		_, err := Parse("p.vaglio", c.src)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: %v, want a ParseError", name, err)
			continue
		}
		if pe.Line != c.line || pe.Col != c.col || !strings.Contains(pe.Msg, c.msg) {
			t.Errorf("%s: %v, want %d:%d %q", name, err, c.line, c.col, c.msg)
		}
	}
	// What parses but does not validate is reported against the file.
	_, err := Parse("p.vaglio", header+"evidence e\n  text \"t\"\n  clause \"c\"\n")
	if err == nil || !strings.HasPrefix(err.Error(), "p.vaglio: ") || !strings.Contains(err.Error(), "no decision approves") {
		t.Errorf("validation error: %v", err)
	}
}

func TestDraftStatus(t *testing.T) {
	src := header + "  status draft\n\ndecision ok approves\n  text \"t\"\n  clause \"c\"\n"
	if p := parse(t, src); p.Status != Draft {
		t.Errorf("status %q", p.Status)
	}
	if _, err := Parse("p.vaglio", strings.Replace(src, "draft", "final", 1)); err == nil {
		t.Error("an unknown status was accepted")
	}
}

func TestLint(t *testing.T) {
	p := parse(t, header+`  status draft
  questionnaire https://q.example/Q
evidence used
  text "t"
  clause "c"
  find question q
evidence unused
  text "t"
  clause "c"
  find question q
criterion always
  text "t"
  clause "c"
decision ok approves
  text "t"
  clause "c"
  defeated by used used always
`)
	got := strings.Join(Lint(p), "\n")
	for _, want := range []string{
		"used defeats ok twice",
		"evidence unused defeats nothing",
		"criterion always is defeated by nothing",
		"question q is read as both used and unused",
		"status draft",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lint is missing %q:\n%s", want, got)
		}
	}
	example, err := LoadFile("../../policies/lumbar_mri" + Ext)
	if err != nil {
		t.Fatal(err)
	}
	if w := Lint(example); len(w) != 0 {
		t.Errorf("the example policy has warnings: %v", w)
	}
}

func TestDrugsDocumentsAndSignOff(t *testing.T) {
	src := `policy p
  title "T"
  version 1
  source "S"
  signed "Dr A. Example, MD" 2026-10-13
  drug
  service rxnorm 327361
evidence trial
  text "A trial"
  clause "C"
  find medication rxnorm 105585 105586 within 365 days lasting 90 days not-taken means absent
  request document loinc 11506-3 18748-4
criterion unmet
  text "U"
  clause "C"
  defeated by trial
decision approve approves
  text "A"
  clause "C"
  defeated by unmet
decision deny denies
  text "D"
  clause "C"
  defeated by trial
`
	p, err := Parse("p.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	m := p.Arguments[0].Find[0].Medication
	if !p.Drug || p.Signed.By != "Dr A. Example, MD" || m == nil || m.WithinDays != 365 || m.MinDays != 90 ||
		!m.NotTakenIsAbsent || len(p.Arguments[0].Documents) != 2 {
		t.Fatalf("%+v %+v", p, m)
	}
	again, err := Parse("p.vaglio", Format(p))
	if err != nil || Format(again) != Format(p) {
		t.Fatalf("round trip: %v\n%s", err, Format(p))
	}
	for _, bad := range []struct{ from, to, want string }{
		{"request document loinc 11506-3 18748-4", "request document loinc 11506", "not a LOINC code"},
		{"request document loinc 11506-3 18748-4", "request document cpt 11506-3", "write: request document loinc"},
		{`signed "Dr A. Example, MD" 2026-10-13`, `signed "Dr A. Example, MD" 13/10/2026`, "YYYY-MM-DD"},
		{"  drug\n", "  drug yes\n", "drug takes no value"},
		{"not-taken means absent", "not-taken", "write: not-taken means absent"},
	} {
		_, err := Parse("p.vaglio", strings.Replace(src, bad.from, bad.to, 1))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: %v", bad.to, err)
		}
	}
	if _, err := Parse("p.vaglio", strings.Replace(src, `  signed "Dr A. Example, MD" 2026-10-13`, "  status draft\n  signed \"X\" 2026-10-13", 1)); err == nil {
		t.Error("a signed draft")
	}
}
