package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/cqlrun"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// TestImportDTRSuggestsFinds: the home oxygen package's hematocrit question reaches an observation lookup in its CQL,
// and the suggested find line, uncommented, is one the language accepts; the mobility question's lookup is negated and
// by a value set, and says so.
func TestImportDTRSuggestsFinds(t *testing.T) {
	raw, err := os.ReadFile("testdata/dtr-home-oxygen.json")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := importDTR(raw, "home_o2", "hcpcs E1390", "https://plan.example/Q/o2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "# suggested from its CQL: find observation loinc 32354-3 within 91 days at least VALUE UNIT"
	if !strings.Contains(src, want) {
		t.Fatalf("no hematocrit suggestion in:\n%s", src)
	}
	for _, n := range []string{"compares no value", "replace VALUE UNIT", `value set "Immobilization"`, "has NONE of this"} {
		if !strings.Contains(src, n) {
			t.Errorf("no note %q", n)
		}
	}
	live := strings.Replace(src, want, "find observation loinc 32354-3 within 91 days at least 56 %", 1)
	p, err := policy.Parse("o2.vaglio", live)
	if err != nil {
		t.Fatalf("the suggestion, uncommented: %v", err)
	}
	found := false
	for _, a := range p.Arguments {
		for _, f := range a.Find {
			if f.Observation != nil && len(f.Observation.Codes) == 1 && f.Observation.Codes[0] == "32354-3" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the uncommented suggestion is not a find on the evidence")
	}
}

// TestImportDTRRunsCQL: a package whose CQL compiles and has what it includes is written beside the policy, pinned,
// and its yes/no questions' pre-fills become find cql lines the server can run.
func TestImportDTRRunsCQL(t *testing.T) {
	lib := "library O2 version '1'\nusing FHIR version '4.0.1'\ninclude FHIRHelpers version '4.0.1' called FHIRHelpers\n" +
		"include O2Common version '1' called Common\ncontext Patient\n" +
		"define \"Has COPD\": Common.\"COPD\"\ndefine \"Count\": Count([Condition])\n"
	common := "library O2Common version '1'\nusing FHIR version '4.0.1'\ncontext Patient\n" +
		"define \"COPD\": exists ([Condition] C where C.code.coding.code = 'J44.9')\n"
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	bundle := map[string]any{"resourceType": "Bundle", "type": "collection", "entry": []any{
		map[string]any{"resource": map[string]any{"resourceType": "Questionnaire", "url": "https://plan.example/Q/o2",
			"title": "Oxygen", "item": []any{
				map[string]any{"linkId": "copd", "type": "boolean", "text": "Does the patient have COPD?",
					"extension": []any{map[string]any{
						"url":             "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-initialExpression",
						"valueExpression": map[string]any{"language": "text/cql", "expression": `%O2."Has COPD"`}}}},
				map[string]any{"linkId": "walk", "type": "boolean", "text": "Can the patient walk?"}}}},
		map[string]any{"resource": map[string]any{"resourceType": "Library", "name": "O2",
			"content": []any{map[string]any{"contentType": "text/cql", "data": b64(lib)}}}},
		map[string]any{"resource": map[string]any{"resourceType": "Library", "name": "O2Common",
			"content": []any{map[string]any{"contentType": "text/cql", "data": b64(common)}}}},
	}}
	raw, _ := json.Marshal(bundle)
	src, notes, files, err := importDTRFiles(raw, "o2", "hcpcs E1390", "", "cql/o2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, `find cql O2 "Has COPD"`) || !strings.Contains(notes, "1 run by their CQL, from 2 library file(s)") {
		t.Fatalf("%s\n%s", notes, src)
	}
	dir := t.TempDir()
	for name, body := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), body, 0o644)
	}
	p, err := policy.Parse("o2.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cqlrun.Load(p, dir); err != nil {
		t.Fatalf("the written libraries do not load: %v", err)
	}
}
