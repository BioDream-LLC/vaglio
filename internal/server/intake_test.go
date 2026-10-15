package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/intake"
)

func TestIntake(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) {
		c.Faxes, c.FaxToken = &intake.Inbox{Dir: t.TempDir()}, "fax-secret"
	})
	clerk := "clerk"
	pw, err := e.users.Set(clerk, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	e.passwords[clerk] = pw
	if err := e.users.SetScopes(clerk, []string{"intake"}); err != nil {
		t.Fatal(err)
	}
	viewer, intakeUser := e.signedIn(t, "viewer"), e.signedIn(t, clerk)

	// The fax service delivers a fax; without its token it is refused.
	post := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/feeds/fax?from=%2B15550100&pages=2", strings.NewReader("%PDF-1.4 a fax"))
		r.Host = "127.0.0.1:8740"
		r.Header.Set("Content-Type", "application/pdf")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		return w
	}
	if w := post("wrong"); w.Code != 401 {
		t.Fatalf("a fax without the token: %d", w.Code)
	}
	w := post("fax-secret")
	var got struct{ Fax string }
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.Fax == "" {
		t.Fatalf("fax: %d %s", w.Code, w.Body)
	}

	// A viewer without the intake scope sees no inbox; the clerk does.
	if w := do(viewer, "GET", "/api/fax", ""); w.Code != 403 {
		t.Errorf("a viewer read the fax inbox: %d", w.Code)
	}
	var faxes []intake.Fax
	json.Unmarshal(do(intakeUser, "GET", "/api/fax", "").Body.Bytes(), &faxes)
	if len(faxes) != 1 || faxes[0].From != "+15550100" || faxes[0].Pages != 2 {
		t.Fatalf("inbox: %+v", faxes)
	}

	// The form for the service names the policy and its questions.
	var form []IntakePolicy
	json.Unmarshal(do(intakeUser, "GET", "/api/intake/form?system=cpt&code=72148", "").Body.Bytes(), &form)
	if len(form) != 1 || form[0].ID != "lumbar_mri" || len(form[0].Questions) == 0 {
		t.Fatalf("form: %+v", form)
	}

	// Keyed in with the answers the fax shows: six weeks of therapy, no recent MRI. Approved, like the PAS request.
	body := IntakeBody{Form: intake.Form{Channel: "fax", Reference: "F-1",
		Patient:  intake.Patient{Family: "Example", BirthDate: "1960-01-01", MemberID: "M1", State: "WA"},
		Provider: intake.Provider{Name: "Example Clinic", NPI: testNPI}, Service: intake.Service{System: "cpt", Code: "72148"},
		Questionnaire: form[0].Questionnaire,
		Answers:       map[string]string{"conservative-therapy": "yes", "prior-imaging-6mo": "no"}},
		Faxes: []string{got.Fax}}
	raw, _ := json.Marshal(body)
	w = do(intakeUser, "POST", "/api/intake", string(raw))
	var a PASAnswer
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != 200 || a.Decision != "approve" || a.Case == "" || a.Receipt == "" {
		t.Fatalf("keyed in: %d %s", w.Code, w.Body)
	}
	c, _ := e.cases.Get(a.Case)
	if c.Channel != "fax" || c.KeyedBy != clerk || c.ProviderNPI != testNPI || c.Request != "" {
		t.Fatalf("case: %+v", c.Opening)
	}
	// The fax is in the case's request, and closed in the inbox.
	bundle, _ := e.cases.Bundle(c)
	if !strings.Contains(string(bundle), `"DocumentReference"`) {
		t.Fatal("the fax is not in the request")
	}
	json.Unmarshal(do(intakeUser, "GET", "/api/fax", "").Body.Bytes(), &faxes)
	if faxes[0].Case != a.Case {
		t.Fatalf("the fax is not marked keyed in: %+v", faxes[0])
	}
	// The opening record names who keyed it in.
	found := false
	for _, r := range e.log.ForCase(a.Case) {
		found = found || (r.Action == "case.opened" && r.Actor == "user:"+clerk)
	}
	if !found {
		t.Fatal("the opening is not attributed to the clerk")
	}
	// The same fax cannot be keyed in twice.
	if w := do(intakeUser, "POST", "/api/intake", string(raw)); w.Code != 409 {
		t.Errorf("a fax keyed in twice: %d", w.Code)
	}
	// Questions left "not shown" pend and ask, as any request with gaps does.
	body.Faxes, body.Reference, body.Answers = nil, "F-2", map[string]string{"conservative-therapy": ""}
	raw, _ = json.Marshal(body)
	json.Unmarshal(do(intakeUser, "POST", "/api/intake", string(raw)).Body.Bytes(), &a)
	if a.Decision != "pend" {
		t.Fatalf("gaps: %s", a.Decision)
	}
	// A bad form says what is wrong.
	if w := do(intakeUser, "POST", "/api/intake", `{"channel":"pigeon"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "channel") {
		t.Errorf("bad form: %d %s", w.Code, w.Body)
	}
	// A second fax is set aside, with a reason.
	post2 := httptest.NewRequest("POST", "/api/feeds/fax", strings.NewReader("%PDF-1.4 junk"))
	post2.Host, post2.Header = "127.0.0.1:8740", map[string][]string{"Content-Type": {"application/pdf"}, "Authorization": {"Bearer fax-secret"}}
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, post2)
	json.Unmarshal(w.Body.Bytes(), &got)
	if w := do(intakeUser, "POST", "/api/fax/"+got.Fax+"/set-aside", `{"reason":""}`); w.Code != 400 {
		t.Errorf("set aside without a reason: %d", w.Code)
	}
	if w := do(intakeUser, "POST", "/api/fax/"+got.Fax+"/set-aside", `{"reason":"a misdial"}`); w.Code != 200 {
		t.Errorf("set aside: %d %s", w.Code, w.Body)
	}
}
