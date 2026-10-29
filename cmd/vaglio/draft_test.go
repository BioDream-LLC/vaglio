package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const draftDoc = `Plan Policy 7.1: Knee arthroscopy (CPT 29881).
Arthroscopy is covered when mechanical symptoms persist after conservative care.`

const draftPolicy = "```vaglio\n" + `policy knee
  title "Knee arthroscopy"
  version 1
  source "Plan Policy 7.1"
  questionnaire https://payer.example/Q/knee
  service cpt 29881

evidence mechanical
  text "Mechanical symptoms after conservative care"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  find question mechanical

criterion unmet
  text "No indication"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by mechanical

decision approve approves
  text "Approve"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by unmet

decision deny denies
  text "Deny"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by mechanical
` + "```"

// TestDraftTwice: vaglio draft -twice against a stand-in model writes the draft headed by the comparison; two runs
// that read the document the same way agree.
func TestDraftTwice(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": draftPolicy}}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	in := filepath.Join(dir, "doc.txt")
	os.WriteFile(in, []byte(draftDoc), 0o600)
	t.Setenv("VG_TEST_KEY", "k")
	out := filepath.Join(dir, "knee.vaglio")
	var w, errw bytes.Buffer
	if err := draftCmd([]string{"-id", "knee", "-out", out, "-twice", "-url", srv.URL, "-key-env", "VG_TEST_KEY", in}, &w, &errw); err != nil {
		t.Fatalf("%v\n%s", err, errw.String())
	}
	raw, _ := os.ReadFile(out)
	if calls.Load() != 2 || !strings.HasPrefix(string(raw), "# Drafted twice: the two drafts decide all 2 records the same way.") {
		t.Fatalf("%d calls\n%s", calls.Load(), raw)
	}
}
