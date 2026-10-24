package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/seal"
)

func TestDataSeal(t *testing.T) {
	seal.SetDefault(nil)
	defer seal.SetDefault(nil)
	dir := t.TempDir()
	l, _ := audit.Open(filepath.Join(dir, "audit.jsonl"))
	cs, _ := cases.Open(l, filepath.Join(dir, "bundles"))
	c, err := cs.Open(cases.Opening{Policy: "p", PolicyVersion: "1", Item: "1", Claim: "x|1", Patient: "Patient/Rivera",
		Answer: cases.Answer{Decision: "pend"}}, []byte(`{"resourceType":"Bundle","name":"Rivera"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, head, _ := audit.Verify(filepath.Join(dir, "audit.jsonl"))
	l.Close()

	var out bytes.Buffer
	if err := dataCmd([]string{"seal", "-data", dir}, &out); err == nil {
		t.Error("sealed without a key")
	}
	k, _ := seal.Parse("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	seal.SetDefault(k)
	if err := dataCmd([]string{"seal", "-data", dir}, &out); err != nil || !strings.Contains(out.String(), "sealed 1 audit records, 0 files and 1 request Bundles in packs") {
		t.Fatalf("%v %s", err, out.String())
	}
	for _, f := range []string{"audit.jsonl", "bundles/pack-000001.vgp"} {
		raw, _ := os.ReadFile(filepath.Join(dir, f))
		if bytes.Contains(raw, []byte("Rivera")) {
			t.Errorf("%s still holds the patient's name", f)
		}
	}
	// Same records, same head (so the same receipts), and the case still opens with its request.
	if _, h, err := audit.Verify(filepath.Join(dir, "audit.jsonl")); err != nil || h != head {
		t.Errorf("head %s, was %s: %v", h, head, err)
	}
	l, err = audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cs, _ = cases.Open(l, filepath.Join(dir, "bundles"))
	got, ok := cs.Get(c.ID)
	if raw, err := cs.Bundle(got); !ok || err != nil || !bytes.Contains(raw, []byte("Rivera")) {
		t.Errorf("reading back: %v %s", err, raw)
	}
	// New writes are sealed too; without the key nothing opens.
	cs.Review(c.ID, "kim", cases.Review{Decision: "approve", Reason: "fine"})
	seal.SetDefault(nil)
	if _, _, err := audit.Verify(filepath.Join(dir, "audit.jsonl")); err == nil || !strings.Contains(err.Error(), "VAGLIO_DATA_KEY") {
		t.Errorf("without the key: %v", err)
	}
	out.Reset()
	dataCmd([]string{"status", "-data", dir}, &out)
	if !strings.Contains(out.String(), "2 records sealed, 0 plain") ||
		!strings.Contains(out.String(), "request Bundles in packs: 1 sealed, 0 plain") {
		t.Errorf("status: %s", out.String())
	}
}
