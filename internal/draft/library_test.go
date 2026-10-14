package draft

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// TestLibrary holds every library policy to the gates a machine draft passes, against its own source: whoever edits a
// policy by hand, or a source, is held to clauses quoted word for word and codes the source lists.
func TestLibrary(t *testing.T) {
	raw, err := os.ReadFile("../../library/library.json")
	if err != nil {
		t.Fatal(err)
	}
	var lib struct {
		Policies []struct{ File, Source, Basis, Category string } `json:"policies"`
	}
	if err := json.Unmarshal(raw, &lib); err != nil {
		t.Fatal(err)
	}
	if len(lib.Policies) == 0 {
		t.Fatal("the library lists nothing")
	}
	listed := map[string]bool{}
	for _, e := range lib.Policies {
		listed[e.File] = true
		src, err := os.ReadFile(filepath.Join("../../library", e.File))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := os.ReadFile(filepath.Join("../../library", e.Source))
		if err != nil {
			t.Fatal(err)
		}
		p, err := policy.Parse(e.File, string(src))
		if err != nil {
			t.Fatal(err)
		}
		if _, failures := Gate(Request{ID: p.ID, Document: string(doc)}, string(src)); len(failures) > 0 {
			t.Errorf("%s fails against %s:\n  %v", e.File, e.Source, failures)
		}
		if e.Basis == "" || e.Category == "" {
			t.Errorf("%s: the library entry needs its basis and category", e.File)
		}
	}
	files, _ := filepath.Glob("../../library/*" + policy.Ext)
	for _, f := range files {
		if !listed[filepath.Base(f)] {
			t.Errorf("%s is not listed in library.json, so nothing says what it was drafted from", filepath.Base(f))
		}
	}
}

// TestReadings holds the second readings in library/readings to the same gates against the source they read, named by
// their policy id: a reading is only worth comparing if it quotes the document as faithfully as the first.
func TestReadings(t *testing.T) {
	sources := map[string]string{"tka": "L36575-TKA.txt"}
	files, _ := filepath.Glob("../../library/readings/*" + policy.Ext)
	if len(files) == 0 {
		t.Fatal("no readings")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p, err := policy.Parse(f, string(src))
		if err != nil {
			t.Fatal(err)
		}
		name, ok := sources[p.ID]
		if !ok {
			t.Fatalf("%s: say which source it reads", f)
		}
		doc, _ := os.ReadFile(filepath.Join("../../library/sources", name))
		if _, failures := Gate(Request{ID: p.ID, Document: string(doc)}, string(src)); len(failures) > 0 {
			t.Errorf("%s fails against %s:\n  %v", f, name, failures)
		}
	}
}
