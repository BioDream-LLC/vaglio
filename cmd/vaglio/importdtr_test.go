package main

import (
	"encoding/base64"
	"encoding/json"
	"github.com/biodream-llc/perfuse/pas"
	cqlengine "github.com/biodream-llc/vaglio/internal/cql"
	"github.com/biodream-llc/vaglio/internal/terminology"
	"net/http"
	"net/http/httptest"
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

// TestImportDTRLibrariesAndValueSets: a library the package includes but lacks comes from -libraries, and a value set
// it names comes from a terminology server; both are written, pinned and run.
func TestImportDTRLibrariesAndValueSets(t *testing.T) {
	lib := "library O2 version '1'\nusing FHIR version '4.0.1'\ninclude FHIRHelpers version '4.0.1' called FHIRHelpers\n" +
		"include O2Common version '1' called Common\nvalueset \"COPD codes\": 'http://example.org/vs/copd'\ncontext Patient\n" +
		"define \"Has COPD\": exists ([Condition: \"COPD codes\"])\ndefine \"Common\": Common.\"Any\"\n"
	common := "library O2Common version '1'\nusing FHIR version '4.0.1'\ncontext Patient\ndefine \"Any\": true\n"
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	bundle := map[string]any{"resourceType": "Bundle", "type": "collection", "entry": []any{
		map[string]any{"resource": map[string]any{"resourceType": "Questionnaire", "url": "https://plan.example/Q/o2",
			"title": "Oxygen", "item": []any{
				map[string]any{"linkId": "copd", "type": "boolean", "text": "Does the patient have COPD?",
					"extension": []any{map[string]any{
						"url":             "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-initialExpression",
						"valueExpression": map[string]any{"language": "text/cql", "expression": `%O2."Has COPD"`}}}}}}},
		map[string]any{"resource": map[string]any{"resourceType": "Library", "name": "O2",
			"content": []any{map[string]any{"contentType": "text/cql", "data": b64(lib)}}}},
	}}
	raw, _ := json.Marshal(bundle)
	// Without them: not run, and said why.
	src, notes, _, err := importDTRFiles(raw, "o2", "hcpcs E1390", "", "cql/o2")
	if err != nil || strings.Contains(src, "find cql") || !strings.Contains(notes, "lacks a library they include") ||
		!strings.Contains(notes, "not expanded") {
		t.Fatalf("%v\n%s", err, notes)
	}
	tx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") != "http://example.org/vs/copd" {
			w.WriteHeader(404)
			w.Write([]byte(`{"resourceType":"OperationOutcome"}`))
			return
		}
		w.Write([]byte(`{"resourceType":"ValueSet","version":"1","expansion":{"total":1,"contains":[` +
			`{"system":"http://hl7.org/fhir/sid/icd-10-cm","code":"J44.9"}]}}`))
	}))
	defer tx.Close()
	opt := dtrOptions{libraries: map[string][]byte{"O2Common": []byte(common)},
		tx: &terminology.Server{Base: tx.URL}}
	src, notes, files, err := importDTRWith(raw, "o2", "hcpcs E1390", "", "cql/o2", opt)
	if err != nil || !strings.Contains(src, `find cql O2 "Has COPD"`) || !strings.Contains(notes, "from -libraries: O2Common 1") ||
		!strings.Contains(notes, "1 value set(s) expanded") {
		t.Fatalf("%v\n%s\n%s", err, notes, src)
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
	prog, err := cqlrun.Load(p, dir)
	if err != nil {
		t.Fatalf("the written files do not load: %v", err)
	}
	if prog.ValueSets["http://example.org/vs/copd"] == nil {
		t.Error("the expanded value set was not loaded")
	}
}

// VAGLIO_TEST_CQL=DIR (the real DTR libraries) and VAGLIO_TEST_TX=https://tx.fhir.org/r4: the Inferno kit's home oxygen
// package, which lacks two libraries it includes, runs once they and its VSAC value sets are given.
func TestImportDTRHomeOxygenComplete(t *testing.T) {
	libDir, txBase := os.Getenv("VAGLIO_TEST_CQL"), os.Getenv("VAGLIO_TEST_TX")
	if libDir == "" || txBase == "" {
		t.Skip("VAGLIO_TEST_CQL and VAGLIO_TEST_TX not both set")
	}
	raw, err := os.ReadFile("testdata/dtr-home-oxygen.json")
	if err != nil {
		t.Fatal(err)
	}
	opt := dtrOptions{libraries: map[string][]byte{}, tx: &terminology.Server{Base: txBase}}
	names, _ := filepath.Glob(filepath.Join(libDir, "*.cql"))
	for _, f := range names {
		b, _ := os.ReadFile(f)
		if l, err := cqlengine.Compile(string(b)); err == nil {
			opt.libraries[l.Name] = b
		}
	}
	src, notes, files, err := importDTRWith(raw, "home_o2", "hcpcs E1390", "https://plan.example/Q/o2", "cql/home_o2", opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(notes)
	if strings.Contains(notes, "lacks a library") || !strings.Contains(src, "find cql") {
		t.Fatalf("still not runnable:\n%s", notes)
	}
	dir := t.TempDir()
	for name, body := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), body, 0o644)
	}
	p, err := policy.Parse("home_o2.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := cqlrun.Load(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d value sets loaded", len(prog.ValueSets))
	// Over the invented patient the oracle uses: the pre-fills give answers, not "unaddressed" for want of a value set
	// or a library.
	rawP, err := os.ReadFile(filepath.Join(libDir, "patient.json"))
	if err != nil {
		t.Skip("no patient.json beside the libraries")
	}
	var b struct {
		Entry []map[string]any `json:"entry"`
	}
	json.Unmarshal(rawP, &b)
	findings, notesRun := prog.Run(p, &pas.Request{Entries: b.Entry, Claim: map[string]any{"created": "2026-10-24"}})
	for _, f := range findings {
		t.Logf("%s: %v (%v)", f.Argument, f.Finding, f.Citation)
	}
	for _, n := range notesRun {
		t.Log("note:", n)
		if strings.Contains(n, "not given") {
			t.Error(n)
		}
	}
	if len(findings) == 0 {
		t.Error("no finding from the home oxygen CQL")
	}
}
