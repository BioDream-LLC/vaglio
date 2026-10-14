package receipt

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
)

func TestCodeAndNormalize(t *testing.T) {
	h := "0123456789abcdef0123"
	c := Code(h)
	if c != "VG-0123-4567-89AB-CDEF" {
		t.Fatalf("code %q", c)
	}
	for _, in := range []string{c, strings.ToLower(c), "0123 4567 89ab cdef", "vg0123456789ABCDEF"} {
		if Normalize(in) != h[:16] {
			t.Errorf("Normalize(%q) = %q", in, Normalize(in))
		}
	}
	for _, bad := range []string{"", "VG-0123", "VG-0123-4567-89AB-CDEZ", "VG-0123-4567-89AB-CDEF-0"} {
		if Normalize(bad) != "" {
			t.Errorf("Normalize(%q) accepted", bad)
		}
	}
}

func TestFind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	open := cases.Opening{Policy: "lumbar_mri", PolicyVersion: "1.0.0", PolicyTitle: "MRI", Patient: "Patient/secret",
		Answer: cases.Answer{Decision: "pend", Why: "needs info"},
		Result: &decide.Result{Outcome: decide.Pended, Proof: &decide.Node{Clause: "not issued"}}}
	opened, _ := l.Append("pas", cases.ActOpened, "c1", open)
	l.Append("user:dr.lee", cases.ActViewed, "c1", nil)
	reviewed, _ := l.Append("user:dr.lee", cases.ActReviewed, "c1",
		cases.Review{By: "dr.lee", Decision: "approve", Reason: "red flag", ReviewerNPI: "1234567893", Credential: "MD"})
	viewed, _ := l.Append("user:dr.lee", cases.ActViewed, "c1", nil)

	records, err := audit.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Find(records, Code(opened.Hash))
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision != "pend" || c.By != "engine" || c.Clause != "" || c.Policy != "lumbar_mri" || c.Record.Seq != 1 ||
		!c.Chain.Verified || c.Chain.Records != 4 || c.Chain.Head != viewed.Hash {
		t.Errorf("opened: %+v", c)
	}
	if len(c.Later) != 1 || c.Later[0].Decision != "approve" || c.Later[0].By != "clinician" ||
		c.Later[0].Receipt != Code(reviewed.Hash) {
		t.Errorf("later: %+v", c.Later)
	}
	r, err := Find(records, Code(reviewed.Hash))
	if err != nil || r.Decision != "approve" || r.By != "clinician" || r.Credential != "MD" || len(r.Later) != 0 ||
		r.PolicyTitle != "MRI" {
		t.Errorf("reviewed: %+v %v", r, err)
	}
	// Nothing about the patient, the reviewer's name or the reason comes back.
	for _, leak := range []string{"secret", "dr.lee", "red flag", "needs info"} {
		if strings.Contains(strings.ToLower(jsonOf(t, c)+jsonOf(t, r)), leak) {
			t.Errorf("a check reveals %q", leak)
		}
	}
	if _, err := Find(records, Code(viewed.Hash)); err != ErrNotFound {
		t.Errorf("a record that decides nothing has a receipt: %v", err)
	}
	if _, err := Find(records, "nonsense"); err == nil {
		t.Error("nonsense accepted")
	}
}
