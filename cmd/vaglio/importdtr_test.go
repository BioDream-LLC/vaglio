package main

import (
	"os"
	"strings"
	"testing"

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
