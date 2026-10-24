package adoption

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
)

// TestRegistry: adoptions count one per clinician and per text; a withdrawal of one text leaves another; the kept text
// of an older adopted version is found, and refused when edited; everything survives a replay of the log.
func TestRegistry(t *testing.T) {
	dir := t.TempDir()
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	r, err := Open(log, filepath.Join(dir, "adopted"))
	if err != nil {
		t.Fatal(err)
	}
	a := Clinician{Account: "a", Name: "Dr A", NPI: "1234567893", Credential: "MD"}
	b := Clinician{Account: "b", Name: "Dr B", NPI: "1245319599", Credential: "DO"}
	v1, v2 := []byte("policy p\n  version 1\n"), []byte("policy p\n  version 2\n")
	if _, err := r.Adopt(Clinician{Account: "x", Name: "X"}, "p", "1", v1, "", "", ""); err == nil {
		t.Fatal("a non-clinician adopted")
	}
	for _, c := range []Clinician{a, b} {
		if _, err := r.Adopt(c, "p", "1", v1, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Adopt(a, "p", "2", v2, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if !r.Adopted("p", Hash(v1), 2) || r.Adopted("p", Hash(v2), 2) || !r.Adopted("p", Hash(v2), 1) {
		t.Fatal("counts")
	}
	if src, h, ok := r.Kept("p", Hash(v2), 2); !ok || h != Hash(v1) || string(src) != string(v1) {
		t.Fatalf("kept %q %v", src, ok)
	}
	if _, err := r.Withdraw(a, "p", "2", Hash(v2), "wrong"); err != nil {
		t.Fatal(err)
	}
	if r.Adopted("p", Hash(v2), 1) || !r.Adopted("p", Hash(v1), 2) {
		t.Fatal("a withdrawal of one text touched another, or missed its own")
	}
	if _, err := r.Review(b, "p", "1", v1, "kept", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if last, act := r.LastLook("p"); last.IsZero() || act.Action != ActReviewed {
		t.Fatalf("%v %+v", last, act)
	}
	if due := r.Due("p", time.Time{}, 24*time.Hour); due.Before(time.Now()) {
		t.Errorf("due %v", due)
	}
	again, err := Open(log, filepath.Join(dir, "adopted"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Adopted("p", Hash(v1), 2) || again.Adopted("p", Hash(v2), 1) || len(again.History("p")) != 5 {
		t.Fatal("not the same after a replay")
	}
}
