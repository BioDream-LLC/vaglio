package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/eligibility"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

// insured gives a request a Coverage naming the member, and a service date.
func insured(b map[string]any, member, date string) map[string]any {
	claim := pastest.Map(pastest.Entries(b)[0])
	claim["insurance"] = []any{map[string]any{"sequence": 1, "focal": true, "coverage": map[string]any{"reference": "Coverage/cov1"}}}
	claim["item"].([]any)[0].(map[string]any)["servicedDate"] = date
	b["entry"] = append(pastest.Entries(b), map[string]any{"fullUrl": pastest.Base + "/Coverage/cov1", "resource": map[string]any{
		"resourceType": "Coverage", "id": "cov1", "status": "active", "subscriberId": member,
		"beneficiary": map[string]any{"reference": "Patient/p1"}}})
	return b
}

func TestEligibility(t *testing.T) {
	f := filepath.Join(t.TempDir(), "members.csv")
	os.WriteFile(f, []byte("member_id,plan,start,end,excluded\nM1,Gold PPO,2026-01-01,2026-06-30,\nM2,Silver,2026-01-01,,72148\n"), 0o600)
	m, err := eligibility.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, "", func(c *Config) { c.Members = m })
	if a := submit(t, e, insured(request("", "", "", denies...), "M1", "2026-03-01")); a.Decision != "deny" {
		t.Errorf("a covered member's denial: %s %s", a.Decision, a.Why)
	}
	a := submit(t, e, insured(request("", "", "", approves...), "M1", "2026-09-01"))
	if a.Decision != "pend" || !strings.Contains(a.Why, "not covered on 2026-09-01") {
		t.Errorf("an uncovered member's approval: %s %s", a.Decision, a.Why)
	}
	c, _ := e.cases.Get(a.Case)
	if c.Eligibility == nil || c.Eligibility.Status != eligibility.Inactive || c.Result.Outcome != "approved" {
		t.Errorf("case: %+v %s", c.Eligibility, c.Result.Outcome)
	}
	if a := submit(t, e, request("", "", "", approves...)); a.Decision != "pend" || !strings.Contains(a.Why, "no member id") {
		t.Errorf("no member id: %s %s", a.Decision, a.Why)
	}
	if a := submit(t, e, insured(request("", "", "", approves...), "M2", "2026-03-01")); a.Decision != "pend" ||
		!strings.Contains(a.Why, "leaves out 72148") {
		t.Errorf("an excluded benefit: %s %s", a.Decision, a.Why)
	}

	// The FHIR question: is the coverage in force, and does this service need prior authorization?
	cer := `{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation","auth-requirements"],
		"patient":{"reference":"#p"},"servicedDate":"2026-03-01","created":"2026-03-01","insurer":{"display":"Plan"},
		"contained":[{"resourceType":"Patient","id":"p"},{"resourceType":"Coverage","id":"c","subscriberId":"M1"}],
		"insurance":[{"coverage":{"reference":"#c"}}],
		"item":[{"productOrService":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148"}]}},
		        {"productOrService":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"99213"}]}}]}`
	r := httptest.NewRequest("POST", "/api/pas/eligibility", strings.NewReader(cer))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var resp struct {
		ResourceType string `json:"resourceType"`
		Insurance    []struct {
			Inforce bool `json:"inforce"`
			Item    []struct {
				AuthorizationRequired bool `json:"authorizationRequired"`
			} `json:"item"`
		} `json:"insurance"`
	}
	if json.Unmarshal(w.Body.Bytes(), &resp); w.Code != 200 || resp.ResourceType != "CoverageEligibilityResponse" ||
		len(resp.Insurance) != 1 || !resp.Insurance[0].Inforce || len(resp.Insurance[0].Item) != 2 ||
		!resp.Insurance[0].Item[0].AuthorizationRequired || resp.Insurance[0].Item[1].AuthorizationRequired {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
