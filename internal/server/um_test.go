package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/um"
)

// standInUM is a case-review system with its own JSON API, unlike Vaglio's: cases opened at POST /cases (answered
// {"data":{"caseId":...}}), read at GET /cases/{id}, decided by its reviewers with their own words for decisions.
type standInUM struct {
	mu     sync.Mutex
	cases  map[string]map[string]any
	opened []map[string]any
}

func (s *standInUM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("X-Api-Key") != "um-key" {
		http.Error(w, "no key", 401)
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/cases":
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		id := fmt.Sprintf("UM-%d", len(s.opened)+1)
		s.opened = append(s.opened, body)
		s.cases[id] = map[string]any{"caseId": id, "status": "IN_REVIEW", "externalId": body["externalId"]}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"caseId": id}})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/cases/"):
		c, ok := s.cases[strings.TrimPrefix(r.URL.Path, "/cases/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(c)
	default:
		http.NotFound(w, r)
	}
}

// decide is a reviewer deciding a case in the stand-in.
func (s *standInUM) decide(id, status, note, name, npi, cred string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cases[id]["status"] = status
	s.cases[id]["determination"] = map[string]any{"note": note, "by": map[string]any{"name": name, "npi": npi, "credential": cred}}
}

func umConfig(url string) um.Config {
	return um.Config{Name: "carecase", Connector: "rest", URL: url + "/cases", HeaderEnv: map[string]string{"X-Api-Key": "UM_KEY"},
		Console: "https://vaglio.plan.example",
		Fields: map[string]string{"externalId": "case", "summary": "why", "due": "due", "member.ref": "patient",
			"attachment.data": "proofPdf", "attachment.type": "=application/pdf", "vaglio.link": "link",
			"vaglio.receipt": "receipt", "clinicianOnly": "clinicianOnly", "missing": "missing"},
		RefField: "data.caseId", DecisionURL: url + "/cases/{ref}",
		DecisionFields: map[string]string{"ref": "caseId", "case": "externalId", "decision": "status",
			"reason": "determination.note", "reviewer": "determination.by.name", "npi": "determination.by.npi",
			"credential": "determination.by.credential"},
		DecisionValues: map[string]string{"APPROVED": "approve", "DENIED": "deny"}}
}

// TestCaseReviewSystem: a pended case is opened in the plan's case-review system with its proof; the console then
// refuses to decide it; a nurse's approval there (polled) closes it here under her name; a denial by someone who is not
// a clinician is refused where only clinicians deny, and a physician's (pushed) is recorded with NPI and credential;
// a decision sent twice is recorded once, and one for a case decided already is refused.
func TestCaseReviewSystem(t *testing.T) {
	stand := &standInUM{cases: map[string]map[string]any{}}
	srv := httptest.NewServer(stand)
	defer srv.Close()
	conn, err := umConfig(srv.URL).Connect(func(env string) (string, error) {
		if env == "UM_KEY" {
			return "um-key", nil
		}
		return "", fmt.Errorf("no %s", env)
	})
	if err != nil {
		t.Fatal(err)
	}
	var link *um.Link
	e := newEnvWith(t, "", func(c *Config) {
		c.ClinicianDenials = true
		link = &um.Link{Name: "carecase", Conn: conn, Log: c.Audit, Cases: c.Cases,
			Cursor: filepath.Join(t.TempDir(), "um.cursor")}
		link.Packet = c.UMPacket("https://vaglio.plan.example")
		link.Record = func(d um.Decision) um.Result { return c.RecordUMDecision(d) }
		c.UM, c.UMToken = link, "um-push-token-0123"
	})
	if err := link.Load(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	a := pendedCase(t, e, "resp-1")                        // needs information, from a PAS server
	b := submit(t, e, request(testNPI, "", "", denies...)) // a proposed denial: clinicians only
	submit(t, e, request(testNPI, "", "", approves...))    // decided: nothing to open
	if n, err := link.Once(ctx); err != nil || n != 2 {
		t.Fatalf("opened %d: %v", n, err)
	}
	if n, _ := link.Once(ctx); n != 0 {
		t.Fatalf("opened again: %d", n)
	}
	ca, _ := e.cases.Get(a.Case)
	cb, _ := e.cases.Get(b.Case)
	if ca.UM == nil || ca.UM.Ref != "UM-1" || cb.UM == nil || cb.UM.Ref != "UM-2" {
		t.Fatalf("links %+v %+v", ca.UM, cb.UM)
	}
	sent := stand.opened[0]
	att, _ := sent["attachment"].(map[string]any)
	pdf, _ := att["data"].(string)
	if sent["externalId"] != a.Case || !strings.HasPrefix(sent["vaglio"].(map[string]any)["link"].(string),
		"https://vaglio.plan.example/#/cases/") || sent["vaglio"].(map[string]any)["receipt"] == "" ||
		att["type"] != "application/pdf" || !strings.HasPrefix(pdf, "JVBERi0") || sent["missing"] == nil {
		t.Fatalf("sent %v", sent)
	}
	if stand.opened[1]["clinicianOnly"] != true {
		t.Errorf("the proposed denial is not marked clinicians only: %v", stand.opened[1]["clinicianOnly"])
	}

	// The console refuses: the case is worked in the case-review system.
	reviewer := e.signedIn(t, "reviewer")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cases/"+a.Case+"/review", strings.NewReader(`{"decision":"approve","reason":"r"}`))
	r.Host = "127.0.0.1:8740"
	reviewer.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "carecase") {
		t.Fatalf("console review: %d %s", w.Code, w.Body)
	}

	// Polled: nothing decided yet, then the nurse approves the first.
	if n := link.PollOnce(ctx); n != 0 {
		t.Fatalf("recorded %d before anyone decided", n)
	}
	stand.decide("UM-1", "APPROVED", "The prior study was of the cervical spine.", "Nurse Avery", "", "")
	if n := link.PollOnce(ctx); n != 1 {
		t.Fatalf("recorded %d", n)
	}
	ca, _ = e.cases.Get(a.Case)
	if ca.Status != cases.Reviewed || ca.Review.By != "Nurse Avery" || ca.Review.Via != "carecase" ||
		ca.Review.Reference != "UM-1" || ca.Review.Decision != "approve" {
		t.Fatalf("review %+v", ca.Review)
	}
	if len(e.delivered) != 1 || e.delivered[0].ID != a.Case {
		t.Errorf("not delivered to the PAS server: %d", len(e.delivered))
	}
	hist := e.cases.History(a.Case)
	if last := hist[len(hist)-1]; last.Actor != "um:carecase" || last.Action != cases.ActReviewed {
		t.Errorf("recorded as %s %s", last.Actor, last.Action)
	}

	push := func(body string) []um.Result {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/feeds/um", strings.NewReader(body))
		r.Host = "127.0.0.1:8740"
		r.Header.Set("Authorization", "Bearer um-push-token-0123")
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		var out []um.Result
		if json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out
	}
	// Pushed: a denial by a nurse is refused here, where only clinicians deny; a physician's is recorded.
	res := push(`{"caseId":"UM-2","status":"DENIED","determination":{"note":"No new findings.","by":{"name":"Nurse Avery"}}}`)
	if res[0].Status != "refused" || !strings.Contains(res[0].Error, "clinician") {
		t.Fatalf("%+v", res)
	}
	doc := `{"caseId":"UM-2","status":"DENIED","determination":{"note":"No new findings since the last study.",` +
		`"by":{"name":"Dr. Rivera","npi":"1234567893","credential":"MD"}}}`
	if res = push(doc); res[0].Status != "recorded" || res[0].Case != b.Case {
		t.Fatalf("%+v", res)
	}
	cb, _ = e.cases.Get(b.Case)
	if cb.Review == nil || cb.Review.ReviewerNPI != "1234567893" || cb.Review.Credential != "MD" || cb.Review.Decision != "deny" {
		t.Fatalf("%+v", cb.Review)
	}
	if res = push(doc); res[0].Status != "already" {
		t.Errorf("again: %+v", res)
	}
	if res = push(`{"caseId":"UM-1","status":"DENIED","determination":{"note":"x","by":{"name":"Dr. Rivera","npi":"1234567893","credential":"MD"}}}`); res[0].Status != "refused" || !strings.Contains(res[0].Error, "decided already") {
		t.Errorf("a decided case: %+v", res)
	}
	if res = push(`{"caseId":"UM-99","status":"APPROVED","determination":{"note":"x","by":{"name":"n"}}}`); res[0].Status != "refused" {
		t.Errorf("an unknown case: %+v", res)
	}
	r = httptest.NewRequest("POST", "/api/feeds/um", strings.NewReader(`{}`))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("a wrong token: %d", w.Code)
	}
	if st := link.Status(); st.Opened != 2 || st.Recorded != 2 || st.Waiting != 0 {
		t.Errorf("status %+v", st)
	}
}
