package server

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/ncpdp"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func postScript(t *testing.T, e *env, body string) *ncpdp.Message {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/pas/ncpdp", strings.NewReader(body))
	r.Host = "127.0.0.1:8740"
	r.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	m, err := ncpdp.ReadMessage(strings.NewReader(w.Body.String()))
	if err != nil || w.Code != 200 {
		t.Fatalf("%d %v\n%s", w.Code, err, w.Body)
	}
	return m
}

func paRequest(t *testing.T, caseID string, answers map[string]string) string {
	raw, _ := os.ReadFile("../../testdata/ncpdp/painitiation.xml")
	var qs strings.Builder
	for id, v := range answers {
		qs.WriteString("<Question><QuestionID>" + id + "</QuestionID><QuestionResponse><SelectedChoice><ChoiceID>" + v +
			"</ChoiceID></SelectedChoice></QuestionResponse></Question>")
	}
	s := strings.Replace(string(raw), "<PAInitiationRequest>", "<PARequest><PACaseID>"+caseID+"</PACaseID><QuestionSet>"+qs.String()+"</QuestionSet>", 1)
	s = strings.Replace(s, "<MessageID>MSG-1001</MessageID>", "<MessageID>MSG-1002</MessageID>", 1)
	return strings.Replace(s, "</PAInitiationRequest>", "</PARequest>", 1)
}

func TestNCPDPePA(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) {
		p, err := policy.LoadFile("../../policies/adalimumab_ra" + policy.Ext)
		if err != nil {
			t.Fatal(err)
		}
		c.Policies[p.ID] = p
	})
	raw, _ := os.ReadFile("../../testdata/ncpdp/painitiation.xml")
	m := postScript(t, e, string(raw))
	if m.Transaction != "PAInitiationResponse" || m.Body.Path("Response", "ResponseStatus", "Open") == nil {
		t.Fatalf("initiation: %+v", m.Body)
	}
	caseID := m.Body.Value("PACaseID")
	var ids []string
	for _, q := range m.Body.All("QuestionID") {
		ids = append(ids, strings.TrimSpace(q.Text))
	}
	if caseID == "" || !strings.Contains(strings.Join(ids, " "), "methotrexate-trial") || m.Root.Value("Header", "RelatesToMessageID") != "MSG-1001" {
		t.Fatalf("case %q questions %v", caseID, ids)
	}

	// Answered: the diagnosis came as M06.9 in MedicationPrescribed, the trial as a yes.
	m = postScript(t, e, paRequest(t, caseID, map[string]string{"methotrexate-trial": "yes"}))
	if m.Transaction != "PAResponse" || m.Body.Path("Response", "ResponseStatus", "Approved") == nil || m.Body.Value("PACaseID") != caseID {
		t.Fatalf("approval: %s", m.Body.Find("PANote").Text)
	}
	// The trial answered no: denied, with the reason.
	m = postScript(t, e, paRequest(t, caseID+"X", map[string]string{"methotrexate-trial": "no", "methotrexate-contraindicated": "no"}))
	if m.Body.Path("Response", "ResponseStatus", "Denied") == nil || !strings.Contains(m.Body.Find("PANote").Text, "methotrexate") {
		t.Fatalf("denial: %+v", m.Body.Find("ResponseStatus"))
	}
	// Not answered: asked again for what a question can answer.
	m = postScript(t, e, paRequest(t, caseID+"Y", nil))
	if m.Body.Path("Response", "ResponseStatus", "Open") == nil || m.Body.Find("QuestionID") == nil {
		t.Fatalf("pend: %+v", m.Body.Find("ResponseStatus"))
	}
	var keyed int
	for _, c := range e.cases.List("") {
		if c.Channel == "ncpdp" && c.Request != "" {
			keyed++
		}
	}
	if keyed != 3 {
		t.Errorf("%d ncpdp cases", keyed)
	}
	// A drug no policy decides: Closed, prior authorization not required.
	m = postScript(t, e, strings.Replace(string(raw), "<Code>327361</Code>", "<Code>999999</Code>", 1))
	if m.Body.Path("Response", "ResponseStatus", "Closed", "ReasonCode").Text != "CC" {
		t.Errorf("not required: %+v", m.Body)
	}
	if m := postScript(t, e, strings.ReplaceAll(string(raw), "PAInitiationRequest", "PACancelRequest")); m.Transaction != "Error" {
		t.Errorf("cancel: %s", m.Transaction)
	}
}
