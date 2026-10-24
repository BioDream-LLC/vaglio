package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/adoption"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/library"
)

// esiRequest is an epidural steroid injection request from Washington, under Medicare Advantage: inside the library
// policy's scope, so it decides it (whatever it decides).
func esiRequest() string {
	b := withCoverage(pastest.Bundle([]string{"62321"}), "111")
	for _, e := range pastest.Entries(b) {
		if r := pastest.Map(e); r["resourceType"] == "Patient" {
			r["address"] = []any{map[string]any{"state": "WA"}}
		}
	}
	return pastest.JSON(b)
}

// decides is the status the PAS decision service answers an ESI request with: 404 is "decides nothing".
func decides(e *env) int {
	r := httptest.NewRequest("POST", "/api/pas/decide?item=1", strings.NewReader(esiRequest()))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w.Code
}

// adoptionEnv is a server with the library, adoption required by two clinicians, reviews due yearly.
func adoptionEnv(t *testing.T, reg **adoption.Registry, now *time.Time) *env {
	t.Helper()
	lib, err := library.Policies()
	if err != nil {
		t.Fatal(err)
	}
	return newEnvWith(t, "", func(c *Config) {
		c.Library = lib
		c.Policies["esi"] = lib["esi"]
		if *reg == nil {
			if *reg, err = adoption.Open(c.Audit, filepath.Join(t.TempDir(), "adopted")); err != nil {
				t.Fatal(err)
			}
		}
		c.Adoption, c.RequireAdoption, c.AdoptionSignatures = *reg, true, 2
		c.ReviewEvery = 365 * 24 * time.Hour
		c.Now = func() time.Time { return *now }
	})
}

// TestAdoption: with adoption required, a library policy decides nothing until two of the plan's clinicians adopt its
// exact text; a non-clinician cannot; withdrawing stops it again; the review falls due a year after the last look and is
// recorded; the packet shows the source with each clause found in it.
func TestAdoption(t *testing.T) {
	var reg *adoption.Registry
	now := time.Now() // records carry the log's own time, so the clock moves on from it
	e := adoptionEnv(t, &reg, &now)
	if err := e.users.SetClinician("reviewer", testNPI, "MD, physiatry"); err != nil {
		t.Fatal(err)
	}
	addClinician(t, e, "director", "1245319599", "MD, medical director")
	if got := decides(e); got != 404 {
		t.Fatalf("not adopted, it decided: %d", got)
	}
	viewer := e.signedIn(t, "viewer")
	var st AdoptionState
	w := do(viewer, "GET", "/api/policies/esi/adoption", "")
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Required || st.Deciding || st.Kind != "library" || st.Need != 2 || st.Hash == "" {
		t.Fatalf("%+v", st)
	}

	// The packet: the policy's text, the LCD beside it, every clause found in it word for word.
	var pk Packet
	w = do(viewer, "GET", "/api/policies/esi/packet", "")
	json.Unmarshal(w.Body.Bytes(), &pk)
	if pk.SHA256 != st.Hash || !strings.Contains(pk.Document, "L39240") || len(pk.Clauses) == 0 || !pk.Check.OK() {
		t.Fatalf("packet: %d %.200s", w.Code, w.Body)
	}
	for _, c := range pk.Clauses {
		if c.At < 0 {
			t.Errorf("clause of %s not found in the source: %q", c.Argument, c.Clause)
		}
	}

	admin := e.signedIn(t, "admin")
	if w := do(admin, "POST", "/api/policies/esi/adopt", `{}`); w.Code != 403 {
		t.Fatalf("a non-clinician adopted: %d %s", w.Code, w.Body)
	}
	reviewer := e.signedIn(t, "reviewer")
	body := `{"as":"physiatrist","reference":"P&T minutes 2026-10-19","sha256":"` + pk.SHA256 + `"}`
	if w := do(reviewer, "POST", "/api/policies/esi/adopt", body); w.Code != 200 {
		t.Fatalf("adopt: %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/policies/esi/adopt", body); w.Code != 400 {
		t.Errorf("the same clinician twice: %d", w.Code)
	}
	if got := decides(e); got != 404 {
		t.Fatalf("one of two adoptions, it decided: %d", got)
	}
	director := e.signedIn(t, "director")
	if w := do(director, "POST", "/api/policies/esi/adopt", `{"as":"medical director","sha256":"stale"}`); w.Code != 409 {
		t.Errorf("a stale text was adopted: %d", w.Code)
	}
	if w := do(director, "POST", "/api/policies/esi/adopt", `{"as":"medical director"}`); w.Code != 200 {
		t.Fatalf("second adoption: %d %s", w.Code, w.Body)
	}
	if got := decides(e); got != 200 {
		t.Fatalf("adopted by two, it does not decide: %d", got)
	}

	// The review falls due a year on; recorded, it is due a year after that.
	now = now.Add(340 * 24 * time.Hour)
	st = AdoptionState{}
	json.Unmarshal(do(viewer, "GET", "/api/policies/esi/adoption", "").Body.Bytes(), &st)
	if !st.DueSoon || st.Overdue {
		t.Errorf("340 days on: %+v", st)
	}
	now = now.Add(30 * 24 * time.Hour)
	var rep AdoptionReport
	json.Unmarshal(do(viewer, "GET", "/api/adoption", "").Body.Bytes(), &rep)
	if rep.Overdue != 1 {
		t.Errorf("370 days on, overdue: %d", rep.Overdue)
	}
	if w := do(director, "POST", "/api/policies/esi/review", `{"outcome":"change"}`); w.Code != 400 {
		t.Errorf("a change with no note: %d", w.Code)
	}
	if w := do(director, "POST", "/api/policies/esi/review", `{"outcome":"kept","as":"medical director","reference":"UM committee 2027-10"}`); w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body)
	}
	now = time.Now() // the review was recorded now: due a year from it
	st = AdoptionState{}
	json.Unmarshal(do(viewer, "GET", "/api/policies/esi/adoption", "").Body.Bytes(), &st)
	if st.Overdue || st.DueSoon || len(st.History) != 3 {
		t.Errorf("after the review: %+v", st)
	}

	// Withdrawn, it decides nothing again; the log has it all.
	if w := do(director, "POST", "/api/policies/esi/withdraw", `{}`); w.Code != 400 {
		t.Errorf("a withdrawal with no reason: %d", w.Code)
	}
	if w := do(director, "POST", "/api/policies/esi/withdraw", `{"note":"The LCD's new version changes the four-week rule."}`); w.Code != 200 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body)
	}
	if got := decides(e); got != 404 {
		t.Fatalf("withdrawn, it decided: %d", got)
	}
	n := 0
	for _, r := range e.log.Records(0, 0) {
		if strings.HasPrefix(r.Action, "policy.adopt") || r.Action == adoption.ActReviewed {
			n++
		}
	}
	if n != 4 {
		t.Errorf("%d adoption records", n)
	}
}

// TestAdoptedVersionKeepsDeciding: when the program carries a library version the plan has not adopted, the version it
// did adopt goes on deciding; the packet shows what changed; once the new one is adopted, it decides instead.
func TestAdoptedVersionKeepsDeciding(t *testing.T) {
	var reg *adoption.Registry
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e := adoptionEnv(t, &reg, &now)
	src, _ := library.Source("esi")
	old := strings.Replace(string(src), "  version 0.1.0", "  version 0.0.9", 1)
	if old == string(src) {
		t.Fatal("no version line to change")
	}
	for _, c := range []adoption.Clinician{{Account: "a", Name: "Dr A", NPI: testNPI, Credential: "MD"},
		{Account: "b", Name: "Dr B", NPI: "1245319599", Credential: "MD"}} {
		if _, err := reg.Adopt(c, "esi", "0.0.9", []byte(old), "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	e = adoptionEnv(t, &reg, &now) // a restart, with the adoptions on record
	if got := decides(e); got != 200 {
		t.Fatalf("the adopted older version does not decide: %d", got)
	}
	viewer := e.signedIn(t, "viewer")
	var st AdoptionState
	json.Unmarshal(do(viewer, "GET", "/api/policies/esi/adoption", "").Body.Bytes(), &st)
	if st.Kind != "kept" || st.Version != "0.0.9" || st.Waiting == nil || st.Waiting.Version != "0.1.0" || !st.Deciding {
		t.Fatalf("%+v", st)
	}
	var pk Packet
	json.Unmarshal(do(viewer, "GET", "/api/policies/esi/packet", "").Body.Bytes(), &pk)
	if pk.Version != "0.1.0" || len(pk.Added) != 1 || len(pk.Removed) != 1 || !strings.Contains(pk.Removed[0], "0.0.9") {
		t.Fatalf("packet %s %v %v", pk.Version, pk.Added, pk.Removed)
	}
	addClinician(t, e, "dra", testNPI, "MD")
	addClinician(t, e, "drb", "1245319599", "MD")
	for _, who := range []string{"dra", "drb"} {
		if w := do(e.signedIn(t, who), "POST", "/api/policies/esi/adopt", `{"sha256":"`+pk.SHA256+`"}`); w.Code != 200 {
			t.Fatalf("%s: %d %s", who, w.Code, w.Body)
		}
	}
	st = AdoptionState{}
	json.Unmarshal(do(viewer, "GET", "/api/policies/esi/adoption", "").Body.Bytes(), &st)
	if st.Kind != "library" || st.Version != "0.1.0" || !st.Deciding || st.Waiting != nil {
		t.Fatalf("after adopting the new version: %+v", st)
	}
	if got := decides(e); got != 200 {
		t.Fatalf("the new version does not decide: %d", got)
	}
}

func addClinician(t *testing.T, e *env, name, npi, cred string) {
	t.Helper()
	pw, err := e.users.Set(name, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	e.passwords[name] = pw
	if err := e.users.SetClinician(name, npi, cred); err != nil {
		t.Fatal(err)
	}
}
