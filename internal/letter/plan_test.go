package letter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPlan(t *testing.T) {
	if _, err := LoadPlan("../../examples/letters.json"); err != nil {
		t.Fatalf("the example: %v", err)
	}
	for _, bad := range []string{
		`{"appeal":{"days":60}}`,                              // no name
		`{"name":"P","appeal":{"dayz":60}}`,                   // misspelt
		`{"name":"P","appeal":{"days":400}}`,                  // past a year
		`{"name":"P","appeal":{"portal":"http://x.example"}}`, // not https
		`{"name":"P","appeal":{"external":{"fr":"x"}}}`,       // no French letters
	} {
		p := filepath.Join(t.TempDir(), "l.json")
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := LoadPlan(p); err == nil {
			t.Errorf("accepted %s", bad)
		} else if !strings.Contains(err.Error(), "l.json") {
			t.Errorf("the error does not name the file: %v", err)
		}
	}
}
