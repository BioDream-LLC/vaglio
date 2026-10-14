package policy

import (
	"strings"
	"testing"
)

const valid = `{
  "id": "p", "title": "t", "version": "1", "source": "s",
  "arguments": [
    {"name": "e", "kind": "evidence", "text": "t", "clause": "c"},
    {"name": "ok", "kind": "decision", "verdict": "approve", "text": "t", "clause": "c"}
  ],
  "attacks": [{"from": "e", "to": "ok"}]
}`

func TestValidLoads(t *testing.T) {
	if _, err := Load(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestRefused(t *testing.T) {
	cases := map[string]string{
		"unknown field":        strings.Replace(valid, `"title"`, `"titel"`, 1),
		"bad name":             strings.Replace(valid, `"name": "e"`, `"name": "E-1"`, 1),
		"duplicate":            strings.Replace(valid, `"name": "ok"`, `"name": "e"`, 1),
		"no clause":            strings.Replace(valid, `"text": "t", "clause": "c"}`, `"text": "t", "clause": ""}`, 1),
		"unknown kind":         strings.Replace(valid, `"kind": "evidence"`, `"kind": "fact"`, 1),
		"no approve":           strings.Replace(valid, `"verdict": "approve"`, `"verdict": "deny"`, 1),
		"bad verdict":          strings.Replace(valid, `"verdict": "approve"`, `"verdict": "maybe"`, 1),
		"undeclared attacker":  strings.Replace(valid, `{"from": "e"`, `{"from": "x"`, 1),
		"decision as attacker": strings.Replace(valid, `{"from": "e", "to": "ok"}`, `{"from": "ok", "to": "e"}`, 1),
		"verdict on evidence":  strings.Replace(valid, `"kind": "evidence",`, `"kind": "evidence", "verdict": "deny",`, 1),
	}
	for name, src := range cases {
		if _, err := Load(strings.NewReader(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSourcesAndServices(t *testing.T) {
	withFind := func(find, extra string) string {
		s := strings.Replace(valid, `{"name": "e", "kind": "evidence", "text": "t", "clause": "c"}`,
			`{"name": "e", "kind": "evidence", "text": "t", "clause": "c", "find": [`+find+`]}`, 1)
		return strings.Replace(s, `"source": "s",`, `"source": "s",`+extra, 1)
	}
	q := `"questionnaire": "https://payer.example/Questionnaire/q",`
	good := map[string]string{
		"question":           withFind(`{"question": "a"}`, q),
		"condition":          withFind(`{"condition": {"system": "x", "codes": ["1"]}}`, ""),
		"suspected accepted": withFind(`{"condition": {"system": "x", "codes": ["1"], "present": ["confirmed", "provisional"]}}`, ""),
		"services":           withFind(`{"question": "a"}`, q+`"services": [{"system": "x", "code": "1"}],`),
	}
	for name, src := range good {
		if _, err := Load(strings.NewReader(src)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]string{
		"question without questionnaire": withFind(`{"question": "a"}`, ""),
		"both":                           withFind(`{"question": "a", "condition": {"system": "x", "codes": ["1"]}}`, q),
		"neither":                        withFind(`{}`, ""),
		"no codes":                       withFind(`{"condition": {"system": "x", "codes": []}}`, ""),
		"no system":                      withFind(`{"condition": {"codes": ["1"]}}`, ""),
		"refuted as present":             withFind(`{"condition": {"system": "x", "codes": ["1"], "present": ["refuted"]}}`, ""),
		"service without code":           withFind(`{"question": "a"}`, q+`"services": [{"system": "x"}],`),
		"find on a decision": strings.Replace(valid, `"verdict": "approve", "text": "t", "clause": "c"}`,
			`"verdict": "approve", "text": "t", "clause": "c", "find": [{"condition": {"system": "x", "codes": ["1"]}}]}`, 1),
	}
	for name, src := range bad {
		if _, err := Load(strings.NewReader(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProcedureAndObservationSources(t *testing.T) {
	find := func(f string) string {
		return header + "evidence e\n  text \"t\"\n  clause \"c\"\n  find " + f + "\ndecision ok approves\n  text \"t\"\n  clause \"c\"\n  defeated by e\n"
	}
	for _, good := range []string{
		"procedure cpt 1",
		"procedure cpt 1 within 30 days lasting 42 days",
		"observation loinc 1 yes-no",
		"observation loinc 1 coded snomed absent 2",
	} {
		if _, err := Parse("p.vaglio", find(good)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for name, bad := range map[string]string{
		"no value test":          "observation loinc 1",
		"two value tests":        "observation loinc 1 yes-no at least 3 mg",
		"a code present and not": "observation loinc 1 coded snomed present 2 absent 2",
		"no coded values":        "observation loinc 1 coded snomed",
	} {
		if _, err := Parse("p.vaglio", find(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// JSON too: a threshold with no unit cannot be read against anything.
	noUnit := strings.Replace(valid, `"clause": "c"}`, `"clause": "c", "find": [{"observation": {"system": "x", "codes": ["1"], "atLeast": {"value": 3}}}]}`, 1)
	if _, err := Load(strings.NewReader(noUnit)); err == nil || !strings.Contains(err.Error(), "unit") {
		t.Errorf("a threshold without a unit: %v", err)
	}
}
