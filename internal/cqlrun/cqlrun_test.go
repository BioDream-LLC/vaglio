package cqlrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

const library = `library OxygenExample version '1.0.0'
using FHIR version '4.0.1'
include FHIRHelpers version '4.0.1' called FHIRHelpers

codesystem "LOINC": 'http://loinc.org'
valueset "Qualifying conditions": 'http://example.org/ValueSet/oxygen-conditions'
code "SpO2": '59408-5' from "LOINC"

context Patient

define "Qualifying condition": exists ([Condition: "Qualifying conditions"] C where C.clinicalStatus.coding.code = 'active')
define "Readings": [Observation: "SpO2"] O where O.status.value = 'final'
    and (O.effective as FHIR.dateTime).value in Interval[Now() - 3 months, Now()]
define "Lowest SpO2": Min("Readings" O return FHIRHelpers.ToQuantity(O.value as FHIR.Quantity))
define "Low oxygen": "Lowest SpO2" <= 88 '%'
define "Not a boolean": "Lowest SpO2"
`

const valueSet = `{"resourceType":"ValueSet","url":"http://example.org/ValueSet/oxygen-conditions",
 "compose":{"include":[{"system":"http://hl7.org/fhir/sid/icd-10-cm","concept":[{"code":"J44.9"},{"code":"J44.1"}]}]}}`

func setup(t *testing.T, defines ...string) (*policy.Policy, string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cql"), 0o755)
	os.WriteFile(filepath.Join(dir, "cql", "oxygen.cql"), []byte(library), 0o644)
	os.WriteFile(filepath.Join(dir, "cql", "conditions.json"), []byte(valueSet), 0o644)
	pin1, _ := Pin(dir, "cql/oxygen.cql")
	pin2, _ := Pin(dir, "cql/conditions.json")
	src := "policy oxygen\n  title \"Home oxygen\"\n  version 1.0\n  source \"Example\"\n  service hcpcs E1390\n" +
		"  " + pin1 + "\n  " + pin2 + "\n\n"
	for i, d := range defines {
		src += "evidence e" + string(rune('a'+i)) + "\n  text \"E\"\n  clause \"C\"\n  find cql OxygenExample \"" + d + "\"\n\n"
	}
	src += "criterion no_a\n  text \"N\"\n  clause \"C\"\n  defeated by ea\n\n" +
		"decision approve approves\n  text \"A\"\n  clause \"C\"\n  defeated by no_a\n\n" +
		"decision deny denies\n  text \"D\"\n  clause \"C\"\n  defeated by ea\n"
	for i := 1; i < len(defines); i++ {
		src = strings.Replace(src, "defeated by no_a\n", "defeated by no_a e"+string(rune('a'+i))+"\n", 1)
	}
	p, err := policy.Parse("oxygen.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := policy.Parse("oxygen.vaglio", policy.Format(p)); err != nil || len(again.CQL) != 2 {
		t.Fatalf("the pins do not survive formatting: %v", err)
	}
	return p, dir
}

func request(t *testing.T, extra ...map[string]any) *pas.Request {
	t.Helper()
	b := pastest.Bundle([]string{"94760"}, extra...)
	raw, _ := json.Marshal(b)
	var m map[string]any
	json.Unmarshal(raw, &m)
	req, problems := pas.Read(m, false)
	if req == nil {
		t.Fatalf("%v", problems)
	}
	return req
}

// TestRun: true documents present, false absent, a non-boolean or a missing value set leaves it unaddressed, and each
// finding cites the define and the resources it read.
func TestRun(t *testing.T) {
	p, dir := setup(t, "Qualifying condition", "Low oxygen", "Not a boolean")
	prog, err := Load(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	low := pastest.Observation("o1", "59408-5", "SpO2", "2026-09-20T09:00:00Z", pastest.Quantity(86, "%"))
	req := request(t, pastest.Condition("c1", "J44.9", "COPD", "active", "confirmed"), low)
	fs, notes := prog.Run(p, req)
	got := map[string]decide.Finding{}
	for _, f := range fs {
		got[f.Argument] = f.Finding
		if !strings.HasPrefix(f.Citation.Reference, Prefix+"OxygenExample.") {
			t.Errorf("citation %+v", f.Citation)
		}
	}
	if got["ea"] != decide.Present || got["eb"] != decide.Present {
		t.Fatalf("%+v %v", fs, notes)
	}
	if _, ok := got["ec"]; ok || len(notes) != 1 || !strings.Contains(notes[0], "not true or false") {
		t.Errorf("a quantity was taken as a finding: %+v %v", fs, notes)
	}
	if !strings.Contains(fs[0].Citation.Quote, "Condition/c1") {
		t.Errorf("quote %q", fs[0].Citation.Quote)
	}

	// The reading is older than three months before the request: Low oxygen is null (no readings), not addressed.
	old := pastest.Observation("o1", "59408-5", "SpO2", "2026-01-20T09:00:00Z", pastest.Quantity(86, "%"))
	fs, _ = prog.Run(p, request(t, pastest.Condition("c1", "I10", "HTN", "active", "confirmed"), old))
	got = map[string]decide.Finding{}
	for _, f := range fs {
		got[f.Argument] = f.Finding
	}
	if got["ea"] != decide.Absent {
		t.Errorf("a condition outside the value set: %+v", fs)
	}
	if _, ok := got["eb"]; ok {
		t.Errorf("no recent reading was taken as a finding: %+v", fs)
	}
}

// TestPins: a file changed after it was pinned, a missing define, or a library no file holds, refuse the policy.
func TestPins(t *testing.T) {
	p, dir := setup(t, "Low oxygen")
	os.WriteFile(filepath.Join(dir, "cql", "oxygen.cql"), []byte(library+"\ndefine X: true\n"), 0o644)
	if _, err := Load(p, dir); err == nil || !strings.Contains(err.Error(), "has changed since the policy pinned it") {
		t.Errorf("a changed library: %v", err)
	}
	p, dir = setup(t, "No such define")
	if _, err := Load(p, dir); err == nil || !strings.Contains(err.Error(), "does not define") {
		t.Errorf("a missing define: %v", err)
	}
	c := &Cache{Dir: dir}
	if _, err := c.For(p); err == nil {
		t.Error("the cache loaded a broken policy")
	}
	if _, err := policy.Parse("x.vaglio", "policy x\n  title \"T\"\n  version 1\n  source \"S\"\n  service cpt 1\n"+
		"evidence a\n  text \"A\"\n  clause \"C\"\n  find cql Lib \"D\"\ndecision ok approves\n  text \"O\"\n  clause \"C\"\n"+
		"  defeated by a\ndecision no denies\n  text \"N\"\n  clause \"C\"\n"); err == nil ||
		!strings.Contains(err.Error(), "pinned") {
		t.Errorf("a cql source with no pinned library: %v", err)
	}
}

// TestStoppedIsNotAddressed: a define stopped by the engine's limits (here, endless recursion) is not addressed,
// which never denies: no finding, and a note saying why.
func TestStoppedIsNotAddressed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "loop.cql"), []byte("library Loop\ndefine function F(x Integer): F(x + 1)\n"+
		"define \"Endless\": F(1) > 0\n"), 0o644)
	pin, _ := Pin(dir, "loop.cql")
	p, err := policy.Parse("loop.vaglio", "policy loop\n  title \"L\"\n  version 1\n  source \"S\"\n  service cpt 1\n  "+pin+
		"\nevidence a\n  text \"A\"\n  clause \"C\"\n  find cql Loop \"Endless\"\ncriterion no_a\n  text \"N\"\n  clause \"C\"\n"+
		"  defeated by a\ndecision ok approves\n  text \"O\"\n  clause \"C\"\n  defeated by no_a\n"+
		"decision no denies\n  text \"N\"\n  clause \"C\"\n  defeated by a\n")
	if err != nil {
		t.Fatal(err)
	}
	prog, err := Load(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, notes := prog.Run(p, request(t))
	if len(fs) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "deep") {
		t.Fatalf("%+v %v", fs, notes)
	}
}
