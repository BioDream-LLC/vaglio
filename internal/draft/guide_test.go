package draft

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/decide"
)

// The example in the guide is what the model copies: it must pass every gate itself.
func TestTheGuidesExamplePasses(t *testing.T) {
	m := regexp.MustCompile("(?s)Document \\(an example\\):\\n(.*?)\\n\\nPolicy:").FindStringSubmatch(guide)
	doc := strings.ReplaceAll(m[1], "  ", "")
	ex := block.FindStringSubmatch(guide)[1]
	p, f := Gate(Request{ID: "example", Document: doc}, "```vaglio\n"+ex+"```")
	if len(f) != 0 {
		t.Fatalf("the guide's example fails: %v", f)
	}
	if r := decide.Check(p); r.Outcomes["approved/decided"] != 2 {
		t.Errorf("approved on %d combinations, want 2: pain and therapy, with or without condition X", r.Outcomes["approved/decided"])
	}
}

// The committed machine draft still passes every gate against its document, so a change to the language or the gates
// that would break real drafts shows up here.
func TestTheCommittedDraftStillPasses(t *testing.T) {
	doc, err := os.ReadFile("../../testdata/documents/knee_arthroscopy.txt")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("../../testdata/drafts/knee_arthroscopy.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	if _, f := Gate(Request{ID: "knee_arthroscopy", Document: string(doc)}, string(src)); len(f) != 0 {
		t.Fatalf("%v", f)
	}
}
