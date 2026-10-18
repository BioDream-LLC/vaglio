package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// FuzzRequest: a PAS request from outside, however malformed, goes through reading, finding the evidence and deciding
// without a panic; and every piece of evidence found present or absent cites where it was found.
func FuzzRequest(f *testing.F) {
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		f.Fatal(err)
	}
	files, _ := filepath.Glob("../../testdata/pas/*.json")
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var b map[string]any
		if json.Unmarshal(raw, &b) != nil {
			return
		}
		req, problems := pas.Read(b, false)
		if len(problems) > 0 || req == nil {
			return
		}
		x := ExtractWith(p, req, nil)
		r := x.Request(p.ID)
		for _, a := range r.Evidence {
			if (a.Finding == decide.Present || a.Finding == decide.Absent) && a.Citation.Reference == "" {
				t.Fatalf("%s found %v with no citation", a.Argument, a.Finding)
			}
		}
		_, _ = decide.Decide(p, r)
	})
}
