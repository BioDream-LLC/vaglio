package cql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealLibraries compiles and runs real Da Vinci DTR libraries, kept outside the repository (they carry no licence):
// VAGLIO_TEST_CQL=DIR holds them (*.cql, FHIRHelpers among them), and DIR/patient.json the data to run them over.
func TestRealLibraries(t *testing.T) {
	dir := os.Getenv("VAGLIO_TEST_CQL")
	if dir == "" {
		t.Skip("VAGLIO_TEST_CQL not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.cql"))
	libs := map[string]*Library{}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		l, err := Compile(string(raw))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		libs[l.Name] = l
	}
	// The DTR reference implementation's own name for CDS Connect Commons differs from the library's.
	if l := libs["CDSConnectCommonsForFHIRv400"]; l != nil {
		libs["CDS_Connect_Commons_for_FHIRv400"] = l
	}
	pf := os.Getenv("VAGLIO_TEST_CQL_PATIENT")
	if pf == "" {
		pf = "patient.json"
	}
	raw, err := os.ReadFile(filepath.Join(dir, pf))
	if err != nil {
		t.Skip("no patient.json")
	}
	rs, _ := FromBundle(raw)
	vs := map[string]*ValueSet{}
	var data []map[string]any
	for _, r := range rs {
		if r["resourceType"] == "ValueSet" {
			b, _ := jsonOf(r)
			if v, err := NewValueSet(b); err == nil {
				vs[v.URL] = v
			}
			continue
		}
		data = append(data, r)
	}
	for _, name := range strings.Split(os.Getenv("VAGLIO_TEST_CQL_RUN"), ",") {
		l := libs[name]
		if l == nil {
			continue
		}
		var dr any
		for _, r := range data {
			if r["resourceType"] == "DeviceRequest" {
				dr = &Elem{V: r, Type: "DeviceRequest"}
			}
		}
		env := &Env{Libraries: libs, ValueSets: vs, Resources: data, Now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
			Parameters: map[string]any{"device_request": dr}}
		for _, d := range l.Defines() {
			v, err := env.Evaluate(name, d)
			if err != nil {
				t.Logf("%s.%s: ERROR %v", name, d, err)
				continue
			}
			t.Logf("%s.%s = %s", name, d, showJSON(v))
		}
	}
}

// showJSON is Show, with FHIR elements other than resources as their JSON (keys sorted), to compare with another engine.
func showJSON(v any) string {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return "{ }"
		}
		var out []string
		for _, e := range x {
			out = append(out, showJSON(e))
		}
		return "{ " + strings.Join(out, ", ") + " }"
	case *Elem:
		if m, ok := x.V.(map[string]any); ok && m["resourceType"] == nil {
			b, _ := json.Marshal(m)
			return "FHIR" + string(b)
		}
	}
	return Show(v)
}
