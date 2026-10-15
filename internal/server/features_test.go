package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/fairness"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/reader"
	"github.com/biodream-llc/vaglio/internal/replay"
	"github.com/biodream-llc/vaglio/internal/report"
)

const testNPI = "1234567893"

var requests int

var (
	therapy  = pastest.Answer{LinkID: "conservative-therapy", Text: "Six weeks of conservative therapy failed?", Value: true}
	priorYes = pastest.Answer{LinkID: "prior-imaging-6mo", Text: "Lumbar MRI in the last six months?", Value: true}
	priorNo  = pastest.Answer{LinkID: "prior-imaging-6mo", Text: "Lumbar MRI in the last six months?", Value: false}
	noChange = pastest.Answer{LinkID: "new-or-worsening", Text: "New or worse since the last study?", Value: false}
	noFlag   = pastest.Answer{LinkID: "red-flag", Text: "Red-flag finding?", Value: false}
)

// request is a lumbar MRI request with these answers, from a provider with this NPI ("" for none), for a patient in
// this state ("" for no address), at this priority ("" for none).
func request(npi, state, priority string, answers ...pastest.Answer) map[string]any {
	b := pastest.Bundle([]string{"72148"}, qr(answers...))
	claim := pastest.Map(pastest.Entries(b)[0])
	requests++ // each its own Claim, so none supersedes another
	claim["identifier"] = []any{map[string]any{"system": "http://provider.example/claims", "value": fmt.Sprint(requests)}}
	if priority != "" {
		claim["priority"] = map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/processpriority", "code": priority}}}
	}
	if state != "" {
		pastest.Map(pastest.Entries(b)[1])["address"] = []any{map[string]any{"use": "home", "state": state}}
	}
	if npi != "" {
		pastest.Map(pastest.Entries(b)[3])["identifier"] = []any{map[string]any{"system": npiSystem, "value": npi}}
	}
	return b
}

var (
	approves = []pastest.Answer{therapy, priorNo}
	denies   = []pastest.Answer{therapy, priorYes, noChange, noFlag}
	pends    = []pastest.Answer{therapy, priorYes}
)

func submit(t *testing.T, e *env, b map[string]any) PASAnswer {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/pas/decide?item=1", strings.NewReader(pastest.JSON(b)))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var a PASAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return a
}

func TestGoldCard(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) {
		c.GoldCard = &goldcard.Config{Programs: []goldcard.Program{{Name: "Texas gold card", Basis: "illustrative",
			States: []string{"TX"}, MinApprovalRate: 0.8, LookBackDays: 365, MinDecisions: 2}}}
	})
	if a := submit(t, e, request(testNPI, "TX", "", denies...)); a.Decision != "deny" || a.Exemption != nil {
		t.Fatalf("with no record: %s", a.Decision)
	}
	for i := 0; i < 4; i++ {
		if a := submit(t, e, request(testNPI, "TX", "", approves...)); a.Decision != "approve" {
			t.Fatalf("approval %d: %s", i, a.Decision)
		}
	}
	// 4 of 5 is 80%: exempt. The engine would deny; the provider's record approves, and says on what.
	a := submit(t, e, request(testNPI, "TX", "", denies...))
	if a.Decision != "approve" || a.Exemption == nil || a.Exemption.Decisions != 5 || a.Exemption.Approved != 4 ||
		len(a.Exemption.Counted) != 5 || !strings.Contains(a.Why, "Approved under Texas gold card (illustrative)") ||
		a.Result.Outcome != "denied" {
		t.Fatalf("%s %q %+v", a.Decision, a.Why, a.Exemption)
	}
	// The law covers Texas patients; and a provider known by no NPI is no provider across requests.
	if a := submit(t, e, request(testNPI, "CA", "", denies...)); a.Decision != "deny" {
		t.Errorf("another state: %s", a.Decision)
	}
	if a := submit(t, e, request("", "TX", "", denies...)); a.Decision != "deny" {
		t.Errorf("no NPI: %s", a.Decision)
	}
	// The exempt approval does not count; the Californian denial does: 4 of 6 lapses the exemption.
	if a := submit(t, e, request(testNPI, "TX", "", denies...)); a.Decision != "deny" {
		t.Errorf("after the record fell below the rate: %s", a.Decision)
	}
	var v struct {
		Enabled   bool                `json:"enabled"`
		Standings []goldcard.Standing `json:"standings"`
	}
	json.Unmarshal(do(e.signedIn(t, "viewer"), "GET", "/api/goldcard", "").Body.Bytes(), &v)
	if !v.Enabled || len(v.Standings) != 1 || v.Standings[0].Decisions != 7 || v.Standings[0].Exempt {
		t.Errorf("%+v", v)
	}
}

func TestClinicianDenials(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) { c.ClinicianDenials = true })
	a := submit(t, e, request(testNPI, "", "", denies...))
	if a.Decision != "pend" || a.Proposed == nil || a.Proposed.Decision != "deny" ||
		!strings.Contains(a.Proposed.Why, "Deny") || strings.Contains(a.Why, "Deny") {
		t.Fatalf("%s %q %+v", a.Decision, a.Why, a.Proposed)
	}
	// Approvals are not held back.
	if a := submit(t, e, request(testNPI, "", "", approves...)); a.Decision != "approve" {
		t.Errorf("approval: %s", a.Decision)
	}
	reviewer := e.signedIn(t, "reviewer")
	path := "/api/cases/" + a.Case + "/review"
	deny := `{"decision":"deny","reason":"Repeat study within six months, no change documented."}`
	if w := do(reviewer, "POST", path, deny); w.Code != 403 {
		t.Fatalf("a reviewer who is not a clinician denied: %d %s", w.Code, w.Body)
	}
	if err := e.users.SetClinician("reviewer", testNPI, "MD, radiology"); err != nil {
		t.Fatal(err)
	}
	if w := do(reviewer, "POST", path, `{"decision":"deny","reason":"x","reviewerNpi":"1245319599"}`); w.Code != 400 {
		t.Errorf("someone else's NPI: %d", w.Code)
	}
	w := do(reviewer, "POST", path, deny)
	var c cases.Case
	json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c.Review.ReviewerNPI != testNPI || c.Review.Credential != "MD, radiology" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var me Me
	json.Unmarshal(do(reviewer, "GET", "/api/me", "").Body.Bytes(), &me)
	if !me.ClinicianDenials || me.NPI != testNPI {
		t.Errorf("%+v", me)
	}
}

func TestQuestionnaire(t *testing.T) {
	e := newEnv(t, "")
	w := do(e.signedIn(t, "viewer"), "GET", "/api/policies/lumbar_mri/questionnaire", "")
	var q struct {
		ResourceType string `json:"resourceType"`
		ID, URL      string
		Item         []map[string]any `json:"item"`
	}
	json.Unmarshal(w.Body.Bytes(), &q)
	if w.Code != 200 || q.ResourceType != "Questionnaire" || q.ID != "lumbar-mri" || q.URL != pastest.Questionnaire ||
		len(q.Item) != 5 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestReports(t *testing.T) {
	e := newEnv(t, "")
	submit(t, e, request(testNPI, "", "", approves...))
	submit(t, e, request(testNPI, "", "", denies...))
	submit(t, e, request(testNPI, "", "stat", pends...))
	viewer := e.signedIn(t, "viewer")
	var m report.Metrics
	json.Unmarshal(do(viewer, "GET", "/api/reports/metrics", "").Body.Bytes(), &m)
	if m.Standard.Requests != 2 || m.Standard.Approved != 1 || m.Standard.Denied != 1 || *m.Standard.ApprovedPercent != 50 ||
		m.Standard.RealTime != 2 || m.Expedited.Requests != 1 || m.Expedited.Pending != 1 || m.Expedited.ApprovedPercent != nil ||
		len(m.NotTracked) != 0 || m.Expedited.Late != 0 {
		t.Errorf("%+v", m)
	}
	if w := do(viewer, "GET", "/api/reports/metrics?format=csv", ""); !strings.Contains(w.Body.String(), `"Denied (count)",1,0`) {
		t.Errorf("%s", w.Body)
	}
	if w := do(viewer, "GET", "/api/reports/metrics?from=2026-13-01", ""); w.Code != 400 {
		t.Errorf("a bad date: %d", w.Code)
	}
	var l report.ListReview
	json.Unmarshal(do(viewer, "GET", "/api/reports/list?min-requests=2", "").Body.Bytes(), &l)
	if len(l.Rows) != 1 || l.Rows[0].Requests != 3 || l.Rows[0].Verdict != report.VerdictKeep || l.Criteria.MinRequests != 2 {
		t.Errorf("list review: %+v", l)
	}
	if w := do(viewer, "GET", "/api/reports/list?min-approved=120", ""); w.Code != 400 {
		t.Errorf("a percent over 100: %d", w.Code)
	}
	if w := do(viewer, "GET", "/api/reports/list?format=csv", ""); !strings.Contains(w.Body.String(), "not-enough") {
		t.Errorf("list csv: %s", w.Body)
	}
	if w := do(viewer, "GET", "/api/reports/fairness", ""); w.Code != 403 {
		t.Errorf("a viewer read demographics: %d", w.Code)
	}
	var f fairness.Report
	json.Unmarshal(do(e.signedIn(t, "reviewer"), "GET", "/api/reports/fairness", "").Body.Bytes(), &f)
	if f.Requests != 3 || len(f.Dimensions) != len(fairness.Dimensions) || !f.Dimensions[0].Groups[0].Suppressed {
		t.Errorf("fairness: %+v", f)
	}
	audited := false
	for _, r := range e.log.Records(0, 0) {
		audited = audited || r.Action == ActFairness
	}
	if !audited {
		t.Error("reading demographics for the fairness report is not in the audit log")
	}
	var p report.Pends
	json.Unmarshal(do(viewer, "GET", "/api/reports/pends", "").Body.Bytes(), &p)
	if p.ForInformation["lumbar_mri"] != 1 || len(p.Missing) == 0 || p.Missing[0].Pends != 1 || p.Missing[0].Question == "" {
		t.Errorf("%+v", p)
	}
}

func TestReplay(t *testing.T) {
	e := newEnv(t, "")
	submit(t, e, request(testNPI, "", "", approves...))
	submit(t, e, request(testNPI, "", "", denies...))
	src, err := os.ReadFile("../../policies/lumbar_mri.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	// Version 0.2.0 no longer counts conservative therapy as an indication.
	v2 := strings.ReplaceAll(string(src), "defeated by red_flag conservative_therapy_6wk surgical_planning",
		"defeated by red_flag surgical_planning")
	v2 = strings.Replace(v2, "version 0.1.0", "version 0.2.0", 1)
	body, _ := json.Marshal(map[string]string{"source": v2})
	if w := do(e.signedIn(t, "viewer"), "POST", "/api/replay", string(body)); w.Code != 403 {
		t.Errorf("a viewer replayed: %d", w.Code)
	}
	w := do(e.signedIn(t, "reviewer"), "POST", "/api/replay", string(body))
	var rep replay.Report
	json.Unmarshal(w.Body.Bytes(), &rep)
	if w.Code != 200 || rep.Version != "0.2.0" || rep.Cases != 2 || rep.Changed != 1 || rep.Unchanged != 1 ||
		rep.Transitions["approve → pend"] != 1 || len(rep.Warnings) == 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do(e.signedIn(t, "reviewer"), "POST", "/api/replay", `{"source":"policy x\n  nonsense"}`); w.Code != 400 ||
		!strings.Contains(w.Body.String(), `"line"`) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	found := false
	for _, r := range e.log.Records(0, 0) {
		found = found || r.Action == "policy.replayed"
	}
	if !found {
		t.Error("the replay is not in the audit log")
	}
}

func TestAppealsAndOverrides(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) { c.ClinicianDenials = true })
	if err := e.users.SetClinician("reviewer", testNPI, "MD, radiology"); err != nil {
		t.Fatal(err)
	}
	reviewer := e.signedIn(t, "reviewer")
	// Three proposed denials in Texas: a clinician approves one and denies two; one denial is overturned on appeal.
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, submit(t, e, request(testNPI, "TX", "", denies...)).Case)
	}
	review := func(id, decision string) {
		t.Helper()
		if w := do(reviewer, "POST", "/api/cases/"+id+"/review", `{"decision":"`+decision+`","reason":"Read the record."}`); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	review(ids[0], "approve")
	review(ids[1], "deny")
	review(ids[2], "deny")
	appeal := func(id, body string) int { return do(reviewer, "POST", "/api/cases/"+id+"/appeal", body).Code }
	if c := appeal(ids[0], `{"outcome":"overturned","reason":"x"}`); c != 409 {
		t.Errorf("an approval appealed: %d", c)
	}
	if c := appeal(ids[1], `{"outcome":"reversed","reason":"x"}`); c != 400 {
		t.Errorf("an unknown outcome: %d", c)
	}
	if c := appeal(ids[1], `{"outcome":"overturned","reason":"New imaging showed a fracture.","decided":"2026-10-12"}`); c != 200 {
		t.Fatalf("appeal: %d", c)
	}
	if c := appeal(ids[1], `{"outcome":"upheld","reason":"x"}`); c != 400 {
		t.Errorf("a second outcome: %d", c)
	}
	var o report.Overrides
	json.Unmarshal(do(reviewer, "GET", "/api/reports/overrides?by=state", "").Body.Bytes(), &o)
	if len(o.Rows) != 1 || o.Rows[0].Key != "TX" || o.Rows[0].Denials != 3 || o.Rows[0].Overridden != 1 ||
		o.Rows[0].Overturned != 1 || len(o.Rows[0].Cases) != 2 {
		t.Fatalf("%+v", o)
	}
	json.Unmarshal(do(reviewer, "GET", "/api/reports/overrides", "").Body.Bytes(), &o)
	if o.By != "clause" || o.Rows[0].Label != "Deny: repeat study within six months with no change in the condition." {
		t.Errorf("%+v", o)
	}
	if w := do(reviewer, "GET", "/api/reports/overrides?by=colour", ""); w.Code != 400 {
		t.Errorf("%d", w.Code)
	}
	var m report.Metrics
	json.Unmarshal(do(reviewer, "GET", "/api/reports/metrics", "").Body.Bytes(), &m)
	if m.Standard.Appealed != 1 || m.Standard.ApprovedAfterAppeal != 1 || *m.Standard.ApprovedAfterAppealPercent != 100 {
		t.Errorf("%+v", m.Standard)
	}
}

func TestRoutesToApproval(t *testing.T) {
	e := newEnv(t, "")
	a := submit(t, e, request("", "", "", pends...))
	if len(a.Result.ToApprove) == 0 {
		t.Errorf("%q", a.Why)
	}
	// An empty request: every indication is missing, and each alone approves; the routes restate the missing list.
	a = submit(t, e, request("", "", ""))
	if a.Decision != "pend" || strings.Contains(a.Why, "It would be approved") || len(a.Result.ToApprove) == 0 {
		t.Errorf("restated: %q", a.Why)
	}
	// A denial whose only routes contradict the record says none to the provider.
	a = submit(t, e, request("", "", "", denies...))
	if a.Decision != "deny" || strings.Contains(a.Why, "would be approved") || len(a.Result.ToApprove) == 0 {
		t.Errorf("%s %q", a.Decision, a.Why)
	}
}

type fakeModel string

func (f fakeModel) Complete(context.Context, []draft.Message) (string, error) { return string(f), nil }

// TestReadConfirmRedecide: a model's proposal changes nothing until a reviewer confirms it; confirmed, the case shows what
// the policy now decides, and the reviewer still decides.
func TestReadConfirmRedecide(t *testing.T) {
	note := "Neurology consult. The neurological examination is normal. MRI of the lumbar spine on 2026-06-01 showed a disc bulge."
	e := newEnvWith(t, "", func(c *Config) {
		c.Reader = &DocumentModel{Name: "fake", Model: fakeModel(`{"findings":[
			{"evidence":"red_flag","finding":"absent","quote":"The neurological examination is normal."},
			{"evidence":"new_or_worsening_symptoms","finding":"present","quote":"symptoms are much worse"}]}`)}
	})
	doc := pastest.Document("n1", "Neurology consult")
	pastest.Map(doc)["content"] = []any{map[string]any{"attachment": map[string]any{"contentType": "text/plain",
		"data": base64.StdEncoding.EncodeToString([]byte(note))}}}
	b := request("", "", "", therapy, priorYes, noChange)
	b["entry"] = append(pastest.Entries(b), doc)
	a := submit(t, e, b)
	if a.Decision != "pend" {
		t.Fatalf("%s %q", a.Decision, a.Why)
	}
	reviewer, viewer := e.signedIn(t, "reviewer"), e.signedIn(t, "viewer")
	var list DocumentList
	json.Unmarshal(do(viewer, "GET", "/api/cases/"+a.Case+"/documents", "").Body.Bytes(), &list)
	if !list.Reader || len(list.Documents) != 1 {
		t.Fatalf("%+v", list)
	}
	path := "/api/cases/" + a.Case
	if w := do(viewer, "POST", path+"/read", `{"document":"`+list.Documents[0].Reference+`"}`); w.Code != 403 {
		t.Errorf("a viewer had a document read: %d", w.Code)
	}
	w := do(reviewer, "POST", path+"/read", `{"document":"`+list.Documents[0].Reference+`"}`)
	var rd reader.Reading
	json.Unmarshal(w.Body.Bytes(), &rd)
	if w.Code != 200 || len(rd.Proposals) != 1 || len(rd.Refused) != 1 || rd.Model != "fake" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var d CaseDetail
	json.Unmarshal(do(viewer, "GET", path, "").Body.Bytes(), &d)
	if d.Redecided != nil {
		t.Fatal("an unconfirmed proposal changed the decision")
	}
	if w := do(reviewer, "POST", path+"/confirm", `{"reading":0,"index":0,"confirmed":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", path+"/confirm", `{"reading":0,"index":0,"confirmed":false}`); w.Code != 400 {
		t.Errorf("decided twice: %d", w.Code)
	}
	json.Unmarshal(do(viewer, "GET", path, "").Body.Bytes(), &d)
	if d.Redecided == nil || d.Redecided.Confirmed != 1 || d.Redecided.Result.Outcome != "denied" || d.Status != "pended" {
		t.Fatalf("%+v %s", d.Redecided, d.RedecideError)
	}
	found := false
	for _, it := range d.Redecided.Result.Relied {
		found = found || (it.Argument == "red_flag" && it.Citation.Quote == "The neurological examination is normal.")
	}
	if !found {
		t.Errorf("the denial does not rest on the confirmed absence: %+v", d.Redecided.Result.Relied)
	}
}

func TestFetchDocuments(t *testing.T) {
	qr := qr(pastest.Answer{LinkID: "new-or-worsening", Text: "New or worse?", Value: true}, noFlag)
	qrRes := qr["resource"].(map[string]any)
	e := newEnvWith(t, "", func(c *Config) {
		c.FetchDocuments = func(_ context.Context, request string) ([]map[string]any, error) {
			if request != "resp-9" {
				return nil, fmt.Errorf("asked about %s", request)
			}
			return []map[string]any{{"fullUrl": "urn:pas-attachment:resp-9:1", "resource": qrRes}}, nil
		}
	})
	b := request("", "", "", therapy, priorYes)
	r := httptest.NewRequest("POST", "/api/pas/decide?item=1", strings.NewReader(pastest.JSON(b)))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("X-PAS-Request-Id", "resp-9")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	var a PASAnswer
	json.Unmarshal(w.Body.Bytes(), &a)
	if a.Decision != "pend" || a.Questionnaire != pastest.Questionnaire {
		t.Fatalf("%s %q", a.Decision, a.Questionnaire)
	}
	reviewer := e.signedIn(t, "reviewer")
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/fetch", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"received": 1`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	do(reviewer, "POST", "/api/cases/"+a.Case+"/fetch", `{}`) // the same again records nothing new
	var d CaseDetail
	json.Unmarshal(do(reviewer, "GET", "/api/cases/"+a.Case, "").Body.Bytes(), &d)
	if len(d.Received) != 1 || d.Redecided == nil || d.Redecided.Result.Outcome != "approved" {
		t.Fatalf("%+v %+v %s", d.Received, d.Redecided, d.RedecideError)
	}
}

func TestGoldCardCountingToggle(t *testing.T) {
	gc := func() *goldcard.Config {
		return &goldcard.Config{Programs: []goldcard.Program{{Name: "TX", States: []string{"TX"}, MinApprovalRate: 0.9,
			LookBackDays: 365, MinDecisions: 2}}}
	}
	e := newEnvWith(t, "", func(c *Config) { c.GoldCard = gc() })
	submit(t, e, request(testNPI, "TX", "", approves...))
	submit(t, e, request(testNPI, "CA", "", approves...))
	// Counting every state, the record is 2 of 2.
	if a := submit(t, e, request(testNPI, "TX", "", denies...)); a.Decision != "approve" {
		t.Fatalf("counting all: %s", a.Decision)
	}
	path := "/api/goldcard/programs/TX/count"
	if w := do(e.signedIn(t, "reviewer"), "PUT", path, `{"count":"states"}`); w.Code != 403 {
		t.Errorf("a reviewer switched it: %d", w.Code)
	}
	admin := e.signedIn(t, "admin")
	if w := do(admin, "PUT", path, `{"count":"some"}`); w.Code != 400 {
		t.Errorf("%d", w.Code)
	}
	if w := do(admin, "PUT", path, `{"count":"states"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Texas only, the record is 1 of 1: under the minimum.
	if a := submit(t, e, request(testNPI, "TX", "", denies...)); a.Decision != "deny" {
		t.Errorf("counting Texas only: %s", a.Decision)
	}
	// A restart replays the switch from the audit log.
	cfg := gc()
	New(Config{Policies: map[string]*policy.Policy{}, Sessions: auth.NewSessions(e.users), Audit: e.log, Cases: e.cases, GoldCard: cfg})
	if cfg.Snapshot()[0].Count != goldcard.CountStates {
		t.Errorf("not replayed: %+v", cfg.Snapshot())
	}
}
