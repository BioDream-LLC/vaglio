package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/pastest"
)

func advise(t *testing.T, e *env, body any) (Advice, int) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/pas/advise", strings.NewReader(string(raw)))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var a Advice
	json.Unmarshal(w.Body.Bytes(), &a)
	return a, w.Code
}

func TestAdviseBeforeSubmitting(t *testing.T) {
	e := newEnv(t, "tok")
	svc := map[string]string{"system": pastest.CPT, "code": "72148"}
	qr := func(answers ...pastest.Answer) map[string]any {
		return pastest.QR("q", pastest.Questionnaire, "completed", answers...)["resource"].(map[string]any)
	}
	patient := map[string]any{"resourceType": "Patient", "id": "p1"}

	// Nothing on record yet: the advice names what a request will need, and the questionnaire that asks.
	a, code := advise(t, e, map[string]any{"service": svc, "resources": []any{patient}})
	if code != 200 || !a.Decides || a.Answer != "pend" || len(a.Missing) == 0 || a.Questionnaire == "" {
		t.Fatalf("%d %+v", code, a)
	}
	// Answered: it would be approved.
	a, _ = advise(t, e, map[string]any{"service": svc, "resources": []any{patient, qr(approves...)}})
	if a.Answer != "approve" || !strings.Contains(a.Summary, "would be approved") {
		t.Errorf("%+v", a)
	}
	// A service no policy decides: the CRD service answers as it would without Vaglio.
	a, _ = advise(t, e, map[string]any{"service": map[string]string{"system": pastest.CPT, "code": "99999"}})
	if a.Decides {
		t.Errorf("%+v", a)
	}
	// Advice opens no case, but is in the log.
	if n := len(e.cases.Select(nil)); n != 0 {
		t.Errorf("advice opened %d cases", n)
	}
	logged := 0
	for _, r := range e.log.Records(0, 0) {
		if r.Action == "crd.advised" {
			logged++
		}
	}
	if logged != 2 {
		t.Errorf("logged %d", logged)
	}
	// It needs the PAS server's token.
	raw := `{"service":{"system":"x","code":"y"}}`
	r := httptest.NewRequest("POST", "/api/pas/advise", strings.NewReader(raw))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("without the token: %d", w.Code)
	}
}
