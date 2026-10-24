package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

func handler(t *testing.T) http.Handler { return newEnv(t, "").signedIn(t, "admin") }

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDecide(t *testing.T) {
	w := do(handler(t), "POST", "/api/decide", `{"policy":"lumbar_mri","evidence":[
		{"argument":"conservative_therapy_6wk","finding":"present","reference":"DocumentReference/1","quote":"8 weeks PT, no relief"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var res decide.Result
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != decide.Approved {
		t.Fatalf("outcome %s", res.Outcome)
	}
}

func TestRefusals(t *testing.T) {
	h := handler(t)
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown policy", "POST", "/api/decide", `{"policy":"nope"}`, 404},
		{"unknown field", "POST", "/api/decide", `{"policy":"lumbar_mri","extra":1}`, 400},
		{"evidence without provenance", "POST", "/api/decide", `{"policy":"lumbar_mri","evidence":[{"argument":"red_flag","finding":"present"}]}`, 422},
		{"wrong method", "GET", "/api/decide", ``, 405},
		{"unknown endpoint", "GET", "/api/nothing", ``, 404},
	}
	for _, c := range cases {
		if w := do(h, c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestHostChecked(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/policies", nil)
	r.Host = "evil.example:8740"
	w := httptest.NewRecorder()
	handler(t).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a foreign Host header got status %d", w.Code)
	}
}

func TestCheckEndpoint(t *testing.T) {
	w := do(handler(t), "GET", "/api/policies/lumbar_mri/check", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"combinations": 32`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func pasHandler(t *testing.T, token string) http.Handler {
	return newEnv(t, token).signedIn(t, "admin")
}

func postPAS(t *testing.T, h http.Handler, query, token string, bundle map[string]any) (int, PASAnswer, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/pas/decide"+query, strings.NewReader(pastest.JSON(bundle)))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Content-Type", "application/fhir+json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var a PASAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	return w.Code, a, w.Body.String()
}

func qr(answers ...pastest.Answer) map[string]any {
	return pastest.QR("q1", pastest.Questionnaire, "completed", answers...)
}

func TestPASDecisions(t *testing.T) {
	h := pasHandler(t, "")
	therapy := pastest.Answer{LinkID: "conservative-therapy", Text: "Six weeks of conservative therapy failed?", Value: true}
	priorYes := pastest.Answer{LinkID: "prior-imaging-6mo", Text: "Lumbar MRI in the last six months?", Value: true}
	priorNo := pastest.Answer{LinkID: "prior-imaging-6mo", Text: "Lumbar MRI in the last six months?", Value: false}
	noChange := pastest.Answer{LinkID: "new-or-worsening", Text: "New or worse since the last study?", Value: false}
	noFlag := pastest.Answer{LinkID: "red-flag", Text: "Red-flag finding?", Value: false}

	code, a, body := postPAS(t, h, "?item=1", "", pastest.Bundle([]string{"72148"}, qr(therapy, priorNo)))
	if code != 200 || a.Decision != "approve" || !strings.Contains(a.Why, "Approve MRI of the lumbar spine") {
		t.Fatalf("approve: %d %s", code, body)
	}

	_, a, body = postPAS(t, h, "", "", pastest.Bundle([]string{"72148"}, qr(therapy, priorYes)))
	if a.Decision != "pend" || len(a.Requested) != 2 || a.Result.Reason != "needs_information" {
		t.Fatalf("pend: %s", body)
	}

	_, a, body = postPAS(t, h, "", "", pastest.Bundle([]string{"72148"}, qr(therapy, priorYes, noChange, noFlag)))
	if a.Decision != "deny" || !strings.Contains(a.Why, "not indicated unless the condition has changed") ||
		!strings.Contains(a.Why, "Documented as absent:") || !strings.Contains(a.Why, "#new-or-worsening") {
		t.Fatalf("deny: %s", body)
	}

	_, a, body = postPAS(t, h, "", "", pastest.Bundle([]string{"72148"}, qr(therapy, priorYes, noChange, noFlag),
		pastest.Condition("k", "G83.4", "Cauda equina syndrome", "active", "confirmed")))
	if a.Decision != "pend" || a.Result.Reason != "contradictory_record" || !strings.Contains(a.Why, "contradictory") {
		t.Fatalf("contradiction: %s", body)
	}
}

func TestPASRefusals(t *testing.T) {
	h := pasHandler(t, "")
	if code, _, _ := postPAS(t, h, "", "", pastest.Bundle([]string{"99213"})); code != 404 {
		t.Errorf("a service no policy decides: %d, want 404", code)
	}
	if code, _, _ := postPAS(t, h, "", "", pastest.Bundle([]string{"72148", "72149"})); code != 400 {
		t.Errorf("several items and no ?item: %d, want 400", code)
	}
	if code, a, _ := postPAS(t, h, "?item=2", "", pastest.Bundle([]string{"99213", "72149"})); code != 200 || a.Item != "2" {
		t.Errorf("?item=2: %d %+v", code, a)
	}
	if code, _, _ := postPAS(t, h, "?item=7", "", pastest.Bundle([]string{"72148"})); code != 400 {
		t.Errorf("an item that does not exist: %d, want 400", code)
	}
	if code, _, _ := postPAS(t, h, "", "", map[string]any{"resourceType": "Claim"}); code != 400 {
		t.Errorf("not a Bundle: %d, want 400", code)
	}
}

func TestPASToken(t *testing.T) {
	h := pasHandler(t, "s3cret")
	b := pastest.Bundle([]string{"72148"})
	if code, _, _ := postPAS(t, h, "", "", b); code != 401 {
		t.Errorf("no token: %d, want 401", code)
	}
	if code, _, _ := postPAS(t, h, "", "wrong", b); code != 401 {
		t.Errorf("wrong token: %d, want 401", code)
	}
	if code, _, _ := postPAS(t, h, "", "s3cret", b); code != 200 {
		t.Errorf("right token: %d, want 200", code)
	}
	// The console API is not behind the token.
	if w := do(h, "GET", "/api/policies", ""); w.Code != 200 {
		t.Errorf("console API: %d", w.Code)
	}
}

// TestPASFixtures runs the synthetic requests in testdata/pas - the ones used to test Vaglio behind a real Perfuse PAS
// server - so they cannot drift from what they are said to show.
func TestPASFixtures(t *testing.T) {
	h := pasHandler(t, "")
	for file, want := range map[string]string{
		"approve.json": "approve/decided", "pend.json": "pend/needs_information",
		"deny.json": "deny/decided", "contradiction.json": "pend/contradictory_record",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pas", file))
		if err != nil {
			t.Fatal(err)
		}
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		_, a, body := postPAS(t, h, "", "", b)
		if a.Result == nil || a.Decision+"/"+string(a.Result.Reason) != want {
			t.Errorf("%s: %s", file, body)
		}
	}
}

// One address trying passwords against many names is refused after 20 failures, whatever the names.
func TestSignInRefusedPerAddress(t *testing.T) {
	e := newEnvWith(t, "", nil)
	for i := 0; i < signInFailsPerAddress; i++ {
		body := fmt.Sprintf(`{"name":"nobody%d","password":"wrong"}`, i)
		if w := e.raw("POST", "/api/login", body, nil, "application/json"); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	right := fmt.Sprintf(`{"name":"viewer","password":%q}`, e.passwords["viewer"])
	if w := e.raw("POST", "/api/login", right, nil, "application/json"); w.Code != 429 {
		t.Errorf("the 21st attempt, from the same address: %d", w.Code)
	}
}

// The lockout gap closed: a stranger's guesses lock a name, and fill the address's allowance, but the browser its owner
// signed in from before still signs in, and its failed attempt is recorded as from a known browser.
func TestKnownBrowserSignsInDespiteLock(t *testing.T) {
	e := newEnvWith(t, "", nil)
	right := fmt.Sprintf(`{"name":"viewer","password":%q}`, e.passwords["viewer"])
	w := e.raw("POST", "/api/login", right, nil, "application/json")
	var device *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == deviceCookie {
			device = c
		}
	}
	if w.Code != 200 || device == nil || !device.HttpOnly || device.Path != "/api/login" {
		t.Fatalf("first sign-in: %d, device cookie %+v", w.Code, device)
	}
	for i := 0; i < signInFailsPerAddress; i++ {
		e.raw("POST", "/api/login", `{"name":"viewer","password":"guess"}`, nil, "application/json")
	}
	if w := e.raw("POST", "/api/login", right, nil, "application/json"); w.Code == 200 {
		t.Fatalf("a stranger's browser signed in after the guesses: %d", w.Code)
	}
	if w := e.raw("POST", "/api/login", right, device, "application/json"); w.Code != 200 {
		t.Fatalf("the owner's browser was locked out: %d %s", w.Code, w.Body)
	}
	if w := e.raw("POST", "/api/login", `{"name":"viewer","password":"guess"}`, device, "application/json"); w.Code != 401 {
		t.Fatalf("a wrong password from the known browser: %d", w.Code)
	}
	recs := e.log.Records(0, 0)
	last := recs[len(recs)-1]
	if last.Action != "user.sign_in_failed" || !strings.Contains(string(last.Data), "a browser that has signed in as this name before") {
		t.Errorf("the failure was recorded as %s %s", last.Action, last.Data)
	}
}
