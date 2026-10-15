package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

const otherNPI = "1245319599"

func portalForm(npi string, answers map[string]string) string {
	b, _ := json.Marshal(map[string]any{
		"patient":       map[string]string{"family": "Rivera", "given": "Ana", "birthDate": "1970-04-02", "memberId": "M1"},
		"provider":      map[string]string{"name": "Clinic", "npi": npi},
		"service":       map[string]string{"system": "cpt", "code": "72148"},
		"questionnaire": "http://vaglio.example/Questionnaire/lumbar_mri", "answers": answers,
	})
	return string(b)
}

func TestProviderPortal(t *testing.T) {
	e := newEnv(t, "")
	for name, npi := range map[string]string{"clinic": testNPI, "other": otherNPI} {
		pw, err := e.users.Set(name, auth.Provider)
		if err != nil {
			t.Fatal(err)
		}
		e.passwords[name] = pw
		if err := e.users.SetProviderNPIs(name, []string{npi}); err != nil {
			t.Fatal(err)
		}
	}
	clinic, other := e.signedIn(t, "clinic"), e.signedIn(t, "other")

	// A provider account reaches nothing of the plan's own screens.
	for _, path := range []string{"/api/cases", "/api/queue", "/api/policies", "/api/reports/metrics", "/api/audit", "/api/intake/form"} {
		if w := do(clinic, "GET", path, ""); w.Code != 403 {
			t.Errorf("a provider read %s: %d", path, w.Code)
		}
	}
	var me Me
	json.Unmarshal(do(clinic, "GET", "/api/me", "").Body.Bytes(), &me)
	if me.Role != auth.Provider {
		t.Errorf("me %+v", me)
	}
	if w := do(clinic, "POST", "/api/portal/requests", portalForm(otherNPI, nil)); w.Code != 400 {
		t.Errorf("a request for another office's NPI: %d", w.Code)
	}
	w := do(clinic, "POST", "/api/portal/requests", portalForm(testNPI, map[string]string{"conservative-therapy": "yes",
		"prior-imaging-6mo": "yes"}))
	var pr PortalRequest
	if json.Unmarshal(w.Body.Bytes(), &pr); w.Code != 200 || pr.Status != "waiting" || len(pr.Requested) == 0 || pr.Receipt == "" {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	c, _ := e.cases.Get(pr.ID)
	if c.Channel != "portal" || c.KeyedBy != "provider:clinic" {
		t.Errorf("case channel %q keyed by %q", c.Channel, c.KeyedBy)
	}
	var list []PortalRequest
	json.Unmarshal(do(clinic, "GET", "/api/portal/requests", "").Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("own list: %+v", list)
	}
	json.Unmarshal(do(other, "GET", "/api/portal/requests", "").Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("another office sees: %+v", list)
	}
	if w := do(other, "GET", "/api/portal/requests/"+pr.ID, ""); w.Code != 404 {
		t.Errorf("another office opened it: %d", w.Code)
	}
	doc := base64.StdEncoding.EncodeToString([]byte("Neurological deficit documented: foot drop."))
	if w := do(clinic, "POST", "/api/portal/requests/"+pr.ID+"/documents",
		`{"documents":[{"description":"Clinic note","contentType":"text/plain","data":"`+doc+`"}]}`); w.Code != 200 {
		t.Fatalf("documents: %d %s", w.Code, w.Body)
	}
	if c, _ := e.cases.Get(pr.ID); len(c.Received) != 1 {
		t.Errorf("received %+v", c.Received)
	}
	if w := do(clinic, "POST", "/api/portal/requests/"+pr.ID+"/appeal", `{"reason":"x"}`); w.Code != 409 {
		t.Errorf("an appeal of a waiting request: %d", w.Code)
	}
	reviewer := e.signedIn(t, "reviewer")
	if w := do(reviewer, "POST", "/api/cases/"+pr.ID+"/review", `{"decision":"deny","reason":"No red flag shown."}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = do(clinic, "POST", "/api/portal/requests/"+pr.ID+"/appeal", `{"reason":"Foot drop is a red flag; note attached."}`)
	if json.Unmarshal(w.Body.Bytes(), &pr); w.Code != 200 || pr.Status != "denied" || pr.Appeal != "received" || pr.Why != "No red flag shown." {
		t.Fatalf("appeal: %d %s", w.Code, w.Body)
	}
	if w := do(clinic, "GET", "/api/portal/requests/"+pr.ID+"/letter", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "appeal was received") {
		t.Errorf("letter: %d %s", w.Code, w.Body)
	}
	// Scopes written into a provider's account give it nothing.
	e.users.SetScopes("clinic", []string{"intake", "audit"})
	if w := do(e.signedIn(t, "clinic"), "GET", "/api/audit", ""); w.Code != 403 {
		t.Errorf("a provider with scopes read the audit log: %d", w.Code)
	}
	if w := do(reviewer, "GET", "/api/portal/requests", ""); w.Code != 403 {
		t.Errorf("staff used the portal: %d", w.Code)
	}
	_ = pastest.CPT
}
