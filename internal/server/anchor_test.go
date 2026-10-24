package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/anchor"
)

// TestAnchoredReceipt: a decision's receipt answer carries its record's proof against the checkpoint that covers it,
// which verifies offline with the server's published key; before any checkpoint covers it, the proof says it is
// pending; a later checkpoint is shown to extend the earlier; witness files are served only from their folder.
func TestAnchoredReceipt(t *testing.T) {
	var a *anchor.Anchor
	e := newEnvWith(t, "", func(c *Config) {
		a = &anchor.Anchor{Dir: filepath.Join(t.TempDir(), "anchor"), Log: c.Audit, Name: "plan.example"}
		if err := a.Open(); err != nil {
			t.Fatal(err)
		}
		c.Anchor = a
	})
	pub := func(path string) (int, []byte) {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:8740"
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	ans := submit(t, e, request(testNPI, "", "", denies...))
	code := ReceiptOf(e.cases, ans.Case)
	var got ReceiptAnswer
	_, body := pub("/api/receipts/" + code)
	json.Unmarshal(body, &got)
	if got.Anchor == nil || !got.Anchor.Pending {
		t.Fatalf("before a checkpoint: %s", body)
	}
	c1, err := a.Checkpoint(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	got = ReceiptAnswer{}
	_, body = pub("/api/receipts/" + code)
	json.Unmarshal(body, &got)
	if got.Anchor == nil || got.Anchor.Checkpoint == nil {
		t.Fatalf("after: %s", body)
	}
	if err := anchor.VerifyReceipt(*got.Anchor, a.Public()); err != nil {
		t.Fatalf("the receipt's proof: %v", err)
	}
	if !strings.HasPrefix(got.Anchor.RecordHash, strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(code, "VG-"), "-", ""))) {
		t.Errorf("the proof is for another record: %s %s", code, got.Anchor.RecordHash)
	}
	submit(t, e, request(testNPI, "", "", approves...))
	c2, _ := a.Checkpoint(context.Background(), false)
	var cp anchor.ConsistencyProof
	status, body := pub(fmt.Sprintf("/api/anchor/consistency?from=%d&to=%d", c1.Size, c2.Size))
	json.Unmarshal(body, &cp)
	if status != 200 || anchor.VerifyConsistency(cp, a.Public()) != nil {
		t.Fatalf("consistency: %d %s", status, body)
	}
	var st anchor.Status
	_, body = pub("/api/anchor")
	json.Unmarshal(body, &st)
	if st.Checkpoints != 2 || st.Problem != "" || st.Last.Size != c2.Size {
		t.Errorf("status %+v", st)
	}
	if status, _ := pub("/api/anchor/witness/..%2Fsigning.key"); status != 404 {
		t.Errorf("a file outside the witness folder: %d", status)
	}
	admin := e.signedIn(t, "admin") // signing in is recorded: one checkpoint to make, then none
	if w := do(admin, "POST", "/api/anchor/checkpoint", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"size"`) {
		t.Errorf("a checkpoint now: %d %s", w.Code, w.Body)
	}
	if w := do(admin, "POST", "/api/anchor/checkpoint", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "covers the log already") {
		t.Errorf("a checkpoint of nothing new: %d %s", w.Code, w.Body)
	}
}
