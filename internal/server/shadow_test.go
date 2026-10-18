package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/report"
)

// A shadow pilot: every request decided and recorded, answered 404 so the PAS server goes on without Vaglio; the
// endpoints that would answer anyone are off; the plan's decisions arrive on the outcomes feed (JSON or CSV); a
// clinician rules on a disagreement; and the report counts agreement and kappa over what both decided.
func TestShadowPilot(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) { c.Shadow, c.OutcomesToken = true, "out-token" })
	send := func(b map[string]any) ShadowAnswer {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/pas/decide?item=1", strings.NewReader(pastest.JSON(b)))
		r.Host = "127.0.0.1:8740"
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		var a ShadowAnswer
		if json.Unmarshal(w.Body.Bytes(), &a); w.Code != 404 || !a.Shadow || a.Case == "" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return a
	}
	var claims []string
	for _, ans := range [][]pastest.Answer{approves, approves, denies, denies, pends} {
		a := send(request(testNPI, "", "", ans...))
		c, _ := e.cases.Get(a.Case)
		claims = append(claims, c.Claim)
	}
	if n := len(e.cases.List("")); n != 5 {
		t.Fatalf("%d cases recorded", n)
	}
	for _, p := range []string{"/api/pas/advise", "/api/pas/eligibility", "/api/pas/ncpdp", "/api/portal/requests", "/api/intake"} {
		if w := e.raw("POST", p, "{}", nil, "application/json"); w.Code != 404 || !strings.Contains(w.Body.String(), "shadow") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}

	feed := func(ct, body string) []OutcomeResult {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/feeds/outcomes", strings.NewReader(body))
		r.Host = "127.0.0.1:8740"
		r.Header.Set("Authorization", "Bearer out-token")
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		var out []OutcomeResult
		if json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out
	}
	// The plan: approved the first, denied the second (overturned on appeal), denied the third, approved the fourth,
	// denied the pended fifth. The second is given by the identifier's value alone.
	_, v1, _ := strings.Cut(claims[1], "|")
	res := feed("application/json", fmt.Sprintf(`[{"claim":%q,"decision":"approve"},{"claim":%q,"decision":"deny","by":"nurse reviewer","appeal":"overturned"}]`, claims[0], v1))
	if res[0].Status != "recorded" || res[1].Status != "recorded" {
		t.Fatalf("%+v", res)
	}
	res = feed("text/csv", fmt.Sprintf("claim,item,decision,by\n%s,1,deny,medical director\n%s,1,approve,vendor\n%s,1,A3,\nnope|9,1,deny,\n%s,1,deny,\n",
		claims[2], claims[3], claims[4], claims[0]))
	if res[0].Status != "recorded" || res[2].Status != "recorded" || res[3].Status != "refused" || res[4].Status != "refused" {
		t.Fatalf("%+v", res)
	}
	if again := feed("application/json", fmt.Sprintf(`[{"claim":%q,"decision":"approve"}]`, claims[0])); again[0].Status != "already-recorded" {
		t.Errorf("%+v", again)
	}

	// Rulings: a clinician only, on a disagreement only, with a reason.
	c1 := e.cases.Select(func(c *cases.Case) bool { return c.Claim == claims[1] })[0]
	reviewer := e.signedIn(t, "reviewer")
	rule := `{"supports":"vaglio","note":"Six weeks of therapy are documented; nothing in the record supports the denial."}`
	if w := do(reviewer, "POST", "/api/cases/"+c1.ID+"/ruling", rule); w.Code != 403 {
		t.Fatalf("a non-clinician ruled: %d", w.Code)
	}
	e.users.SetClinician("reviewer", testNPI, "MD, radiology")
	c0 := e.cases.Select(func(c *cases.Case) bool { return c.Claim == claims[0] })[0]
	if w := do(reviewer, "POST", "/api/cases/"+c0.ID+"/ruling", rule); w.Code != 409 {
		t.Errorf("ruled where both agree: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/"+c1.ID+"/ruling", `{"supports":"vaglio","note":" "}`); w.Code != 409 {
		t.Errorf("ruled without a reason: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/"+c1.ID+"/ruling", rule); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	var rep report.Shadow
	w := do(reviewer, "GET", "/api/reports/shadow", "")
	json.Unmarshal(w.Body.Bytes(), &rep)
	// Both decided four: agreed on two (approve/approve, deny/deny). Vaglio approved what the plan denied once,
	// denied what it approved once; kappa is 0 (both approve half the time and agree half the time).
	if rep.Cases != 5 || rep.WithOutcome != 5 || rep.BothDecided != 4 || rep.Agreed != 2 || rep.VaglioPended != 1 ||
		rep.Agreement != 0.5 || rep.Kappa != 0 || rep.Overturned != 1 || rep.Rulings["vaglio"] != 1 || rep.Unruled != 1 ||
		len(rep.Disagree) != 2 || rep.Disagree[0].Ruling != nil || rep.Disagree[0].Vaglio != "deny" {
		t.Errorf("%+v", rep)
	}
	csv := do(reviewer, "GET", "/api/reports/shadow?format=csv", "").Body.String()
	if strings.Count(csv, "\n") != 3 || !strings.Contains(csv, "nothing in the record supports") {
		t.Errorf("%s", csv)
	}
	if w := e.raw("GET", "/api/me", "", e.login(t, "viewer"), ""); !strings.Contains(strings.ReplaceAll(w.Body.String(), " ", ""), `"shadow":true`) {
		t.Errorf("%s", w.Body)
	}
}
