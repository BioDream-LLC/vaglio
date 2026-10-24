package cql

import (
	"os"
	"strings"
	"testing"
	"time"
)

const lib = `library Test version '1.0'
using FHIR version '4.0.1'
include FHIRHelpers version '4.0.1' called FHIRHelpers

codesystem "LOINC": 'http://loinc.org'
codesystem "CLIN": 'http://terminology.hl7.org/CodeSystem/condition-clinical'
valueset "COPD": 'http://example.org/vs/copd'
code "SpO2": '59408-5' from "LOINC" display 'Oxygen saturation'
code "Active": 'active' from "CLIN"
concept "Active concept": { "Active" }

parameter Threshold Integer default 88
parameter Given String

context Patient

define "COPD conditions": [Condition: "COPD"]
define "Has COPD": exists "COPD conditions"
define "Active COPD": "COPD conditions" C where C.clinicalStatus ~ "Active"
define "Any condition": Count([Condition])
define "SpO2 readings": [Observation: "SpO2"] O where O.status.value in {'final', 'amended', 'corrected'}
define "Lowest SpO2": Min("SpO2 readings" O return FHIRHelpers.ToQuantity(O.value as FHIR.Quantity))
define "Lowest value": "Lowest SpO2".value
define "Low enough": "Lowest SpO2" <= 88 '%'
define "Below threshold": "Lowest value" <= Threshold
define "Recent": [Observation: "SpO2"] O where (O.effective as FHIR.dateTime).value in Interval[Now() - 30 days, Now()]
define "Recent count": Count("Recent")
define "Latest": Last("SpO2 readings" O sort by (effective as FHIR.dateTime).value)
define "Latest id": "Latest".id.value
define "Age": AgeInYears()
define "Older than 70": AgeInYearsAt(@2026-10-24) >= 70
define "Period obs": [Observation] O where (O.effective as FHIR.Period) is not null
define "In 2025": "Period obs" O where FHIRHelpers.ToInterval(O.effective as FHIR.Period) during Interval[@2025-01-01, @2025-12-31]
define "Codes": flatten ([Condition] C return C.code.coding.code.value)
define "Distinct sorted": distinct({3, 1, 2, 1}) X sort desc
define "Tuple": Tuple { a: 1, b: 'x' }.b
define "Case": case when "Has COPD" then 'yes' else 'no' end
define "If": if "Any condition" > 5 then 'many' else 'few'
define "Null logic": null and false
define "Null or": null or true
define "Unknown": null and true
define "Date math": @2026-01-31 + 1 month
define "Difference": difference in days between @2026-01-01 and @2026-03-01
define "Uncertain": @2026-01 = @2026-01-15
define "Therapy": [Procedure] P where P.status.value = 'completed' and duration in days between start of FHIRHelpers.ToInterval(P.performed as FHIR.Period) and end of FHIRHelpers.ToInterval(P.performed as FHIR.Period) >= 42
define "With": [Condition] C with [Observation: "SpO2"] O such that (O.effective as FHIR.dateTime).value after C.recordedDate.value
define "Without": [Condition] C without [Observation: "SpO2"] O such that (O.effective as FHIR.dateTime).value after C.recordedDate.value
define "Lets": [Observation: "SpO2"] O let V: O.value as FHIR.Quantity where V.value.value > 90 return O.id.value
define "Given value": Given
define "Strings": 'a' + 'b' & 'c'
define "Equivalent": 'Hello  World' ~ 'hello world'
define "Missing VS": exists [Condition: "Nope"]
define function "Double"(x Integer): x * 2
define "Doubled": "Double"(21)
`

func run(t *testing.T) *Env {
	t.Helper()
	l, err := Compile(lib)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("testdata/patient.json")
	rs, err := FromBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	vs := map[string]*ValueSet{}
	var data []map[string]any
	for _, r := range rs {
		if r["resourceType"] == "ValueSet" {
			b, _ := jsonOf(r)
			v, err := NewValueSet(b)
			if err != nil {
				t.Fatal(err)
			}
			vs[v.URL] = v
			continue
		}
		data = append(data, r)
	}
	return &Env{Libraries: map[string]*Library{"Test": l}, ValueSets: vs, Resources: data,
		Parameters: map[string]any{"Given": "here"}, Now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func TestDefines(t *testing.T) {
	env := run(t)
	want := map[string]string{
		"Has COPD":        "true",
		"Active COPD":     "{ Condition/c1 }",
		"Any condition":   "2",
		"Lowest SpO2":     "80 '%'",
		"Lowest value":    "80",
		"Low enough":      "true",
		"Below threshold": "true",
		"Recent count":    "2",
		"Latest id":       "'o1'",
		"Age":             "76",
		"Older than 70":   "true",
		"In 2025":         "{ Observation/o4 }",
		"Codes":           "{ 'J44.9', 'I10' }",
		"Distinct sorted": "{ 3, 2, 1 }",
		"Tuple":           "'x'",
		"Case":            "'yes'",
		"If":              "'few'",
		"Null logic":      "false",
		"Null or":         "true",
		"Unknown":         "null",
		"Date math":       "@2026-02-28",
		"Difference":      "59",
		"Uncertain":       "null",
		"Therapy":         "{ Procedure/pr1 }",
		"With":            "{ Condition/c1, Condition/c2 }",
		"Without":         "{ }",
		"Lets":            "{ 'o2' }",
		"Given value":     "'here'",
		"Strings":         "'abc'",
		"Equivalent":      "true",
		"Doubled":         "42",
	}
	for def, w := range want {
		v, err := env.Evaluate("Test", def)
		if err != nil {
			t.Errorf("%s: %v", def, err)
			continue
		}
		if got := Show(v); got != w {
			t.Errorf("%s = %s, want %s", def, got, w)
		}
	}
	if _, err := env.Evaluate("Test", "Missing VS"); err == nil {
		t.Error("an undeclared value set gave an answer")
	}
	if !strings.Contains(strings.Join(env.Read(), " "), "Condition/c1") {
		t.Errorf("read %v", env.Read())
	}
}

func TestValueSetNotGiven(t *testing.T) {
	env := run(t)
	env.ValueSets = nil
	_, err := env.Evaluate("Test", "Has COPD")
	if err == nil || !strings.Contains(err.Error(), "is not given") {
		t.Fatalf("%v", err)
	}
}

func TestErrors(t *testing.T) {
	for src, want := range map[string]string{
		"library X\ndefine A: (1 + ":          "ends early",
		"library X\ndefine A: 1\ndefine A: 2": "defined twice",
		"library X\ndefine A: 'open":          "never closes",
		"library X\ndefine A: @T10:00":        "not supported",
	} {
		_, err := Compile(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
}

// TestLimits: a library that recurses without end, or does too much work, is stopped with an error.
func TestLimits(t *testing.T) {
	l, err := Compile("library L\ndefine function F(x Integer): F(x + 1)\ndefine A: F(1)\n" +
		"define Big: Count(({1,2,3,4,5,6,7,8,9,10}) a, ({1,2,3,4,5,6,7,8,9,10}) b, ({1,2,3,4,5,6,7,8,9,10}) c, " +
		"({1,2,3,4,5,6,7,8,9,10}) d, ({1,2,3,4,5,6,7,8,9,10}) e2 where a + b + c + d + e2 > 0)\n")
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Libraries: map[string]*Library{"L": l}}
	if _, err := env.Evaluate("L", "A"); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("endless recursion: %v", err)
	}
	env = &Env{Libraries: map[string]*Library{"L": l}, MaxSteps: 50_000}
	if _, err := env.Evaluate("L", "Big"); err == nil || !strings.Contains(err.Error(), "limit of work") {
		t.Errorf("too much work: %v", err)
	}
}

// TestNesting: deep nesting in the text, or a parameter whose default names itself, is an error, not a crash.
func TestNesting(t *testing.T) {
	if _, err := Compile("library X\ndefine A: " + strings.Repeat("(", 100000) + "1" + strings.Repeat(")", 100000)); err == nil {
		t.Error("100,000 parentheses compiled")
	}
	if _, err := Compile("library X\ndefine A: " + strings.Repeat("not ", 100000) + "true"); err == nil {
		t.Error("100,000 nots compiled")
	}
	if _, err := Compile("library X\ndefine A: " + strings.Repeat("-", 100000) + "1"); err == nil {
		t.Error("100,000 minus signs compiled")
	}
	l, err := Compile("library X\nparameter P default P\ndefine A: P\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Env{Libraries: map[string]*Library{"X": l}}).Evaluate("X", "A"); err == nil {
		t.Error("a parameter defaulting to itself gave a value")
	}
}
