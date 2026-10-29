package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/anchor"
	"github.com/biodream-llc/vaglio/internal/audit"
)

// TestAnchorVerifyPromise: vaglio anchor verify reads a decision's answer with its promise, and with -promise holds a
// later receipt answer to it: kept for the honest proof, BROKEN PROMISE for one that puts another record there.
func TestAnchorVerifyPromise(t *testing.T) {
	dir := t.TempDir()
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 3; i++ {
		l.Append("system", "test.record", "", map[string]int{"i": i})
	}
	rec, _ := l.Append("system", "case.decided", "C-1", map[string]string{"decision": "deny"})
	a := &anchor.Anchor{Dir: filepath.Join(dir, "anchor"), Log: l, Name: "plan.example"}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	pr, _ := a.Promise(rec.Seq, rec.Hash)
	write := func(name string, v any) string {
		raw, _ := json.Marshal(v)
		f := filepath.Join(dir, name)
		os.WriteFile(f, raw, 0o600)
		return f
	}
	answer := write("answer.json", map[string]any{"decision": "deny", "promise": pr})
	var out bytes.Buffer
	if err := anchorCmd([]string{"verify", answer}, &out); err != nil || !strings.Contains(out.String(), "a promise") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	a.Checkpoint(context.Background(), false)
	proof, _ := a.Prove(rec.Seq)
	good := write("receipt.json", map[string]any{"anchor": proof})
	out.Reset()
	if err := anchorCmd([]string{"verify", "-promise", answer, good}, &out); err != nil || !strings.Contains(out.String(), "is kept") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	other, _ := a.Prove(rec.Seq - 1) // a real proof, of the record before it, relabelled as the promised one
	other.Seq = rec.Seq
	bad := write("bad.json", map[string]any{"anchor": other})
	out.Reset()
	if err := anchorCmd([]string{"verify", "-promise", answer, bad}, &out); err == nil || !strings.Contains(out.String(), "BROKEN PROMISE") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
