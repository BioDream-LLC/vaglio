package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse: no source makes the parser panic, and whatever it accepts formats to source that parses to the same
// formatting again (vaglio fmt is stable on everything it accepts).
func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob("../../policies/*" + Ext)
	lib, _ := filepath.Glob("../../library/*" + Ext)
	for _, p := range append(files, lib...) {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(string(b))
		}
	}
	f.Fuzz(func(t *testing.T, src string) {
		p, err := Parse("fuzz"+Ext, src)
		if err != nil {
			return
		}
		once := Format(p)
		q, err := Parse("fuzz"+Ext, once)
		if err != nil {
			t.Fatalf("formatted source no longer parses: %v\n%s", err, once)
		}
		if twice := Format(q); twice != once {
			t.Fatalf("formatting is not stable:\n%s\n---\n%s", once, twice)
		}
	})
}
