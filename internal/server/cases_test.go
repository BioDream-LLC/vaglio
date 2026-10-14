package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/pastest"
)

func TestSignInAndRoles(t *testing.T) {
	e := newEnv(t, "")
	if w := e.raw("GET", "/api/policies", "", nil, ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	if w := e.raw("POST", "/api/login", `{"name":"viewer","password":"wrong"}`, nil, "application/json"); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}
	viewer := e.login(t, "viewer")
	if !viewer.HttpOnly || viewer.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %+v", viewer)
	}
	if w := e.raw("GET", "/api/me", "", viewer, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"role": "viewer"`) {
		t.Errorf("me: %d %s", w.Code, w.Body)
	}
	if w := e.raw("GET", "/api/audit", "", viewer, ""); w.Code != 403 {
		t.Errorf("a viewer read the audit log: %d", w.Code)
	}
	if w := e.raw("POST", "/api/cases/x/review", `{"decision":"approve","reason":"r"}`, viewer, "application/json"); w.Code != 403 {
		t.Errorf("a viewer reviewed: %d", w.Code)
	}
	if w := e.raw("GET", "/api/audit", "", e.login(t, "admin"), ""); w.Code != 200 {
		t.Errorf("an admin could not read the audit log: %d", w.Code)
	}
	if w := e.raw("POST", "/api/logout", `{}`, viewer, "application/json"); w.Code != 204 {
		t.Errorf("logout: %d", w.Code)
	}
	if w := e.raw("GET", "/api/me", "", viewer, ""); w.Code != 401 {
		t.Errorf("a signed-out session still works: %d", w.Code)
	}

	actions := map[string]int{}
	for _, r := range e.log.Records(0, 0) {
		actions[r.Action]++
		if strings.Contains(string(r.Data), e.passwords["viewer"]) {
			t.Fatal("a password reached the audit log")
		}
	}
	if actions["user.signed_in"] != 2 || actions["user.sign_in_failed"] != 1 || actions["user.signed_out"] != 1 {
		t.Errorf("audited %v", actions)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	e := newEnv(t, "")
	c := e.login(t, "reviewer")
	body := `{"policy":"lumbar_mri","evidence":[]}`
	if w := e.raw("POST", "/api/decide", body, c, "text/plain"); w.Code != 403 {
		t.Errorf("a form-like content type: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/decide", strings.NewReader(body))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("another origin: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/decide", strings.NewReader(body))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:8740")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("this origin: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("an API response may be cached")
	}
}

func pendedCase(t *testing.T, e *env, requestID string) PASAnswer {
	t.Helper()
	therapy := pastest.Answer{LinkID: "conservative-therapy", Text: "Therapy failed?", Value: true}
	prior := pastest.Answer{LinkID: "prior-imaging-6mo", Text: "MRI in six months?", Value: true}
	b := pastest.Bundle([]string{"72148"}, qr(therapy, prior), pastest.Document("d1", "Clinic note"))
	claim := pastest.Map(pastest.Entries(b)[0])
	claim["identifier"] = []any{map[string]any{"system": "http://provider.example/claims", "value": "C-1"}}
	r := httptest.NewRequest("POST", "/api/pas/decide?item=1", strings.NewReader(pastest.JSON(b)))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("X-PAS-Request-Id", requestID)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var a PASAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || a.Decision != "pend" || a.Case == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return a
}

func TestPendedCaseIsReviewedAndDelivered(t *testing.T) {
	e := newEnv(t, "")
	a := pendedCase(t, e, "resp-1")
	if !strings.Contains(a.Why, "A reviewer will read the 1 document(s)") {
		t.Errorf("why %q", a.Why)
	}
	viewer, reviewer := e.signedIn(t, "viewer"), e.signedIn(t, "reviewer")

	w := do(viewer, "GET", "/api/cases?status=pended", "")
	var list []CaseSummary
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != a.Case || list[0].Claim != "http://provider.example/claims|C-1" ||
		list[0].Service != pastest.CPT+"|72148" || list[0].Reason != "needs_information" {
		t.Fatalf("list %s", w.Body)
	}
	if w := do(viewer, "GET", "/api/cases?status=open", ""); w.Code != 400 {
		t.Errorf("an unknown status: %d", w.Code)
	}

	w = do(viewer, "GET", "/api/cases/"+a.Case, "")
	var detail CaseDetail
	json.Unmarshal(w.Body.Bytes(), &detail)
	if w.Code != 200 || detail.Request != "resp-1" || len(detail.Evidence.Unread) != 1 || detail.Patient != "Patient/p1" {
		t.Fatalf("detail %d %s", w.Code, w.Body)
	}
	if w := do(viewer, "GET", "/api/cases/"+a.Case+"/bundle", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Clinic note") {
		t.Errorf("bundle %d", w.Code)
	}

	for name, body := range map[string]string{
		"no reason":   `{"decision":"deny","reason":""}`,
		"bad NPI":     `{"decision":"deny","reason":"r","reviewerNpi":"1234567890"}`,
		"no decision": `{"reason":"r"}`,
	} {
		if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/review", body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w = do(reviewer, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"approve","reason":"Prior study was of the cervical spine.","reviewerNpi":"1234567893"}`)
	if w.Code != 200 {
		t.Fatalf("review %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"deny","reason":"again"}`); w.Code != 409 {
		t.Errorf("a second review: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/nope/review", `{"decision":"deny","reason":"x"}`); w.Code != 404 {
		t.Errorf("an unknown case: %d", w.Code)
	}
	if len(e.delivered) != 1 || e.delivered[0].Review.ReviewerNPI != "1234567893" {
		t.Fatalf("delivered %+v", e.delivered)
	}

	var actors []string
	for _, r := range e.cases.History(a.Case) {
		actors = append(actors, r.Actor+" "+r.Action)
	}
	want := "pas case.opened|user:viewer case.viewed|user:viewer case.viewed|user:reviewer case.reviewed"
	if got := strings.Join(actors, "|"); got != want {
		t.Errorf("history\n%s\nwant\n%s", got, want)
	}

	admin := e.signedIn(t, "admin")
	if w := do(admin, "GET", "/api/audit/verify", ""); !strings.Contains(w.Body.String(), `"ok": true`) {
		t.Errorf("verify %s", w.Body)
	}
	w = do(admin, "GET", "/api/audit?after=0&limit=2", "")
	var page AuditPage
	json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Records) != 2 || page.Total < 6 {
		t.Errorf("audit page %d of %d", len(page.Records), page.Total)
	}
}

func TestNPI(t *testing.T) {
	for npi, ok := range map[string]bool{"1234567893": true, "1234567890": false, "123456789": false, "12345678a3": false} {
		if validNPI(npi) != ok {
			t.Errorf("%s: %v", npi, !ok)
		}
	}
}

func TestPolicySource(t *testing.T) {
	w := do(handler(t), "GET", "/api/policies/bariatric_surgery/source", "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "policy bariatric_surgery\n") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = do(handler(t), "GET", "/api/policies/lumbar_mri/check", "")
	if !strings.Contains(w.Body.String(), `"ok": true`) || !strings.Contains(w.Body.String(), `"warnings": []`) {
		t.Errorf("check %s", w.Body)
	}
}
