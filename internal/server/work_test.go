package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
)

func TestWorkQueue(t *testing.T) {
	e := newEnv(t, "")
	a := pendedCase(t, e, "resp-1")
	reviewer, admin := e.signedIn(t, "reviewer"), e.signedIn(t, "admin")
	pw, _ := e.users.Set("doc", auth.Reviewer)
	e.passwords["doc"] = pw
	e.users.SetClinician("doc", "1234567893", "MD")
	doc := e.signedIn(t, "doc")

	var q []QueueItem
	json.Unmarshal(do(reviewer, "GET", "/api/queue", "").Body.Bytes(), &q)
	if len(q) != 1 || q[0].ID != a.Case || !q[0].MayTakeThis || len(q[0].Rank.Why) == 0 {
		t.Fatalf("queue %+v", q)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/assign", `{"to":"admin"}`); w.Code != 403 {
		t.Errorf("a reviewer gave a case to someone else: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/queue/next", ""); w.Code != 200 {
		t.Fatalf("next: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(do(admin, "GET", "/api/queue?who=unassigned", "").Body.Bytes(), &q)
	if len(q) != 0 {
		t.Errorf("unassigned after taking: %+v", q)
	}
	if w := do(doc, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"approve","reason":"ok"}`); w.Code != 409 {
		t.Errorf("deciding another reviewer's case: %d", w.Code)
	}
	// Sent to a physician: the reviewer, not a clinician, loses it and may not decide it.
	w := do(reviewer, "POST", "/api/cases/"+a.Case+"/escalate", `{"note":"Imaging read needed."}`)
	var c cases.Case
	json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c.Escalated == nil || (c.Assigned != nil && c.Assigned.To != "") {
		t.Fatalf("escalate: %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"approve","reason":"ok"}`); w.Code != 403 {
		t.Errorf("a non-clinician decided a physician's case: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/queue/next", ""); w.Code != 404 {
		t.Errorf("a non-clinician took a physician's case: %d", w.Code)
	}
	json.Unmarshal(do(doc, "GET", "/api/queue?tier=physician", "").Body.Bytes(), &q)
	if len(q) != 1 || !q[0].Physician || !q[0].MayTakeThis {
		t.Fatalf("physician queue %+v", q)
	}
	at := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if w := do(doc, "POST", "/api/cases/"+a.Case+"/peer-to-peer", `{"at":"`+at+`","with":"Dr. A, 555-0100"}`); w.Code != 200 {
		t.Fatalf("peer to peer: %d %s", w.Code, w.Body)
	}
	if w := do(doc, "POST", "/api/queue/next", ""); w.Code != 200 {
		t.Fatalf("doc next: %d %s", w.Code, w.Body)
	}
	var wl []Workload
	json.Unmarshal(do(admin, "GET", "/api/workload", "").Body.Bytes(), &wl)
	if len(wl) != 1 || wl[0].Reviewer != "doc" || wl[0].Open != 1 || wl[0].Physician != 1 {
		t.Errorf("workload %+v", wl)
	}
	if w := do(doc, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"deny","reason":"No therapy recorded."}`); w.Code != 200 {
		t.Fatalf("doc review: %d %s", w.Code, w.Body)
	}

	// The appeal: taken in, then decided by someone other than the denier; upheld only by a clinician.
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/appeal/received",
		`{"from":"provider","reason":"Therapy notes attached.","expedited":true}`); w.Code != 200 {
		t.Fatalf("appeal received: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(do(reviewer, "GET", "/api/queue", "").Body.Bytes(), &q)
	if len(q) != 1 || q[0].Appeal == nil || !q[0].Physician || q[0].MayTakeThis {
		t.Fatalf("appeal in queue %+v", q)
	}
	if w := do(doc, "POST", "/api/cases/"+a.Case+"/appeal", `{"outcome":"overturned","reason":"x"}`); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "someone else") {
		t.Errorf("the denier decided the appeal: %d %s", w.Code, w.Body)
	}
	// The same clinician under a second account (the same NPI) does not decide it either.
	pw2, _ := e.users.Set("doc2", auth.Reviewer)
	e.users.SetClinician("doc2", docNPI(t, e), "MD")
	e.passwords["doc2"] = pw2
	doc2 := e.signedIn(t, "doc2")
	if w := do(doc2, "POST", "/api/cases/"+a.Case+"/appeal", `{"outcome":"upheld","reason":"x"}`); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "under your NPI") {
		t.Errorf("the denier, under another account, decided the appeal: %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/appeal", `{"outcome":"upheld","reason":"x"}`); w.Code != http.StatusForbidden {
		t.Errorf("a non-clinician upheld a denial: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/appeal", `{"outcome":"overturned","reason":"Therapy documented."}`); w.Code != 200 {
		t.Fatalf("overturn: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(do(reviewer, "GET", "/api/queue", "").Body.Bytes(), &q)
	if len(q) != 0 {
		t.Errorf("queue after the appeal: %+v", q)
	}
}

// docNPI is the NPI the "doc" account was given.
func docNPI(t *testing.T, e *env) string {
	t.Helper()
	u, ok, err := e.users.Get("doc")
	if err != nil || !ok || u.NPI == "" {
		t.Fatal("doc has no NPI")
	}
	return u.NPI
}
