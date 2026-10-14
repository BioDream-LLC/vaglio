package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/versions"
)

// The time machine: every version put in force is kept, and any one of them decides a stored case again beside the
// decision it was given.
func TestVersionsAndReplayCase(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"lumbar_mri", "bariatric_surgery"} {
		src, _ := os.ReadFile("../../policies/" + f + policy.Ext)
		os.WriteFile(filepath.Join(dir, f+policy.Ext), src, 0o644)
	}
	vs, err := versions.Open(filepath.Join(t.TempDir(), "versions"))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, "", func(c *Config) { c.PolicyDir, c.Versions = dir, vs })
	admin, reviewer := e.signedIn(t, "admin"), e.signedIn(t, "reviewer")
	body := func(src string) string { b, _ := json.Marshal(map[string]string{"source": src}); return string(b) }

	var list []versions.Version
	json.Unmarshal(do(reviewer, "GET", "/api/policies/lumbar_mri/versions", "").Body.Bytes(), &list)
	if len(list) != 1 || list[0].Version != "0.1.0" {
		t.Fatalf("at start: %+v", list)
	}
	d := submit(t, e, request("", "", "", denies...))

	// A new version, signed off, is kept beside the first.
	next := nextLumbar(t, "0.2.0")
	if w := do(admin, "PUT", "/api/drafts/lumbar_mri", body(next)); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	e.users.SetClinician("reviewer", testNPI, "MD")
	if w := do(reviewer, "POST", "/api/drafts/lumbar_mri/signoff", ""); w.Code != 200 {
		t.Fatalf("sign-off: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(do(reviewer, "GET", "/api/policies/lumbar_mri/versions", "").Body.Bytes(), &list)
	if len(list) != 2 || list[1].Version != "0.2.0" {
		t.Fatalf("after sign-off: %+v", list)
	}
	w := do(reviewer, "GET", "/api/policies/lumbar_mri/versions/"+list[0].SHA256, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "version 0.1.0") {
		t.Fatalf("the first version: %d %s", w.Code, w.Body)
	}
	first := w.Body.String()

	// The first version decides the case as it was decided.
	b, _ := json.Marshal(map[string]string{"source": first, "case": d.Case})
	var rc ReplayedCase
	if w := do(reviewer, "POST", "/api/replay/case", string(b)); w.Code != 200 {
		t.Fatalf("replay case: %d %s", w.Code, w.Body)
	} else {
		json.Unmarshal(w.Body.Bytes(), &rc)
	}
	if rc.Was == nil || rc.Now == nil || rc.Now.Outcome != rc.Was.Outcome || rc.Now.PolicyVersion != "0.1.0" {
		t.Errorf("%+v", rc)
	}
	if w := do(reviewer, "GET", "/api/policies/lumbar_mri/versions/"+strings.Repeat("0", 64), ""); w.Code != 404 {
		t.Errorf("an unknown version: %d", w.Code)
	}
}
