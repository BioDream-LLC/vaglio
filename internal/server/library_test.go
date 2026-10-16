package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/library"
)

// TestLibraryOverride: a library policy decides as published; a clinician overrides it with the plan's own version,
// which decides instead; removing the override puts the published one back. Every step is in the audit log.
func TestLibraryOverride(t *testing.T) {
	lib, err := library.Policies()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := newEnvWith(t, "", func(c *Config) {
		c.PolicyDir, c.PolicyRoot, c.Library = dir, dir, lib
		c.Policies["cpap_osa"] = lib["cpap_osa"]
	})
	admin, reviewer := e.signedIn(t, "admin"), e.signedIn(t, "reviewer")
	state := func() (string, string) {
		w := do(reviewer, "GET", "/api/policies/cpap_osa", "")
		var got struct {
			Version string        `json:"version"`
			Library *LibraryState `json:"library"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if got.Library == nil {
			t.Fatalf("no library state: %s", w.Body)
		}
		return got.Library.State, got.Version
	}
	if s, v := state(); s != "published" || v != lib["cpap_osa"].Version {
		t.Fatalf("as published: %s %s", s, v)
	}
	// Its source is the library's file, header and citation included.
	if w := do(reviewer, "GET", "/api/policies/cpap_osa/source", ""); !strings.HasPrefix(w.Body.String(), "# CPAP (E0601)") {
		t.Errorf("source: %.80s", w.Body)
	}
	// Removing an override that is not there is refused.
	if err := e.users.SetClinician("reviewer", testNPI, "MD, sleep medicine"); err != nil {
		t.Fatal(err)
	}
	if w := do(reviewer, "DELETE", "/api/policies/cpap_osa/override", ""); w.Code != 409 {
		t.Errorf("removed an override that was not there: %d", w.Code)
	}

	// Override: a draft numbered past the library's, ready as it stands, signed by a clinician.
	if w := do(admin, "POST", "/api/policies/cpap_osa/draft", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Override of the library policy") {
		t.Fatalf("override draft: %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/drafts/cpap_osa/signoff", ""); w.Code != 200 {
		t.Fatalf("sign-off: %d %s", w.Code, w.Body)
	}
	if s, v := state(); s != "overridden" || v == lib["cpap_osa"].Version {
		t.Errorf("overridden: %s %s", s, v)
	}
	if _, err := os.Stat(filepath.Join(dir, "cpap_osa"+policy.Ext)); err != nil {
		t.Errorf("the override is not in the policy directory: %v", err)
	}

	// Only a clinician removes it.
	if w := do(admin, "DELETE", "/api/policies/cpap_osa/override", ""); w.Code != 403 {
		t.Errorf("a non-clinician removed the override: %d", w.Code)
	}
	if w := do(reviewer, "DELETE", "/api/policies/cpap_osa/override", ""); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if s, v := state(); s != "published" || v != lib["cpap_osa"].Version {
		t.Errorf("back to published: %s %s", s, v)
	}
	if _, err := os.Stat(filepath.Join(dir, "cpap_osa"+policy.Ext)); !os.IsNotExist(err) {
		t.Errorf("the override file is still there: %v", err)
	}
	found := false
	for _, r := range e.log.Records(0, 0) {
		if r.Action == ActOverrideRemoved && strings.Contains(string(r.Data), `"published":"`+lib["cpap_osa"].Version+`"`) {
			found = true
		}
	}
	if !found {
		t.Error("the removal is not in the audit log")
	}
	// A policy not from the library has no library state.
	w := do(reviewer, "GET", "/api/policies/lumbar_mri", "")
	if strings.Contains(w.Body.String(), `"library"`) {
		t.Errorf("lumbar_mri says it is a library policy: %s", w.Body)
	}
}
