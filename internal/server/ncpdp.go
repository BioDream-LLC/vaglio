package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/dtr"
	"github.com/biodream-llc/vaglio/internal/intake"
	"github.com/biodream-llc/vaglio/internal/ncpdp"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// NCPDP SCRIPT electronic prior authorization, answered as the payer at POST /api/pas/ncpdp (behind the PAS token,
// from the plan's ePA intermediary). A PAInitiationRequest is answered with the drug policy's questions (Open), or
// Closed CC when no policy here decides the drug. A PARequest is decided on the same path as every other request,
// its answers and diagnoses read as a keyed-in request's are: Approved, Denied with the reason, Open again with the
// questions still unanswered, or InProcess when it waits for a person. Other transactions get a SCRIPT Error.

const ndcSystem = "http://hl7.org/fhir/sid/ndc"

func scriptID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// drugPolicy is the policy in force that decides a SCRIPT request's drug, and the code it matched.
func (cfg Config) drugPolicy(d ncpdp.Drug) (*policy.Policy, string, string) {
	cfg.activateDue()
	for _, c := range []struct{ sys, code string }{{policy.Systems["rxnorm"], d.RxNorm}, {ndcSystem, d.NDC}} {
		if c.code == "" {
			continue
		}
		for _, p := range cfg.deciding() {
			if p.Decides(c.sys, c.code) && p.InEffect(cfg.today()) {
				return p, c.sys, c.code
			}
		}
	}
	return nil, "", ""
}

func (cfg Config) ncpdpEPA(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	sent := now.Format("2006-01-02T15:04:05.0Z")
	reply := func(status int, body []byte) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	m, err := ncpdp.ReadMessage(http.MaxBytesReader(w, r.Body, maxBundle))
	if err != nil {
		reply(http.StatusBadRequest, ncpdp.Error(nil, "900", err.Error(), scriptID(), sent))
		return
	}
	switch m.Transaction {
	case "PAInitiationRequest", "PARequest":
	default:
		reply(http.StatusOK, ncpdp.Error(m, "900", m.Transaction+" is not answered here: only PAInitiationRequest and PARequest",
			scriptID(), sent))
		return
	}
	q := ncpdp.ReadRequest(m)
	resp := ncpdp.Response{Transaction: strings.TrimSuffix(m.Transaction, "Request") + "Response", PAReferenceID: q.PAReferenceID,
		PACaseID: q.PACaseID}
	if q.PAReferenceID == "" {
		reply(http.StatusOK, ncpdp.Error(m, "900", "the request has no PAReferenceID", scriptID(), sent))
		return
	}
	if resp.PACaseID == "" {
		resp.PACaseID = "VG" + scriptID()[:14]
	}
	p, sys, code := cfg.drugPolicy(q.Drug)
	if p == nil {
		resp.Status, resp.ReasonCode = "Closed", "CC" // prior authorization not required
		resp.Note = "No prior authorization policy here covers this drug."
		reply(http.StatusOK, ncpdp.Write(m, resp, scriptID(), sent))
		return
	}
	questions := func(only map[string]bool) []ncpdp.Question {
		qs, err := dtr.For(p)
		if err != nil {
			return nil
		}
		var out []ncpdp.Question
		for _, it := range qs.Item {
			if it.Type == "boolean" && (only == nil || only[it.LinkID]) {
				out = append(out, ncpdp.Question{ID: it.LinkID, Text: it.Text})
			}
		}
		return out
	}
	resp.QuestionSetID, resp.Title = p.ID+"@"+p.Version, p.Title
	if m.Transaction == "PAInitiationRequest" {
		resp.Status, resp.Questions = "Open", questions(nil)
		if len(resp.Questions) == 0 {
			resp.Note = "Send the PA request with the patient's records: this policy asks no questions."
		}
		reply(http.StatusOK, ncpdp.Write(m, resp, scriptID(), sent))
		return
	}

	// The PA request: decided as any request is, through a request Bundle made from the message.
	answers := map[string]string{}
	for id, v := range q.Answers {
		answers[id] = v
	}
	f := intake.Form{Channel: "ncpdp", Reference: q.PAReferenceID,
		Patient:  intake.Patient{Family: q.Patient.Family, Given: q.Patient.Given, BirthDate: scriptDate(q.Patient.BirthDate), Gender: q.Patient.Gender, MemberID: q.MemberID},
		Provider: intake.Provider{Name: nonEmptyOr(q.Prescriber, "The prescriber"), NPI: q.PrescriberNPI},
		Service:  intake.Service{System: sys, Code: code}, Questionnaire: p.Questionnaire, Answers: answers,
		KeyedBy: "ncpdp:" + m.From}
	if p.Questionnaire == "" {
		f.Answers = nil
	}
	if err := f.Check(); err != nil {
		reply(http.StatusOK, ncpdp.Error(m, "900", "the request cannot be read: "+err.Error(), scriptID(), sent))
		return
	}
	b, _ := f.Bundle(now)
	addDiagnoses(b, q.Diagnoses)
	raw, _ := json.Marshal(b)
	a, status, msg := cfg.decideBundle(raw, "1", resp.PACaseID, "ncpdp", "ncpdp:"+m.From)
	if status != http.StatusOK {
		reply(http.StatusOK, ncpdp.Error(m, "900", msg, scriptID(), sent))
		return
	}
	cfg.StandbySync.Confirm(r.Context())
	switch a.Decision {
	case "approve":
		resp.Status, resp.Note = "Approved", a.Why
		resp.EffectiveDate = now.Format(time.DateOnly)
		resp.ExpirationDate = now.AddDate(1, 0, 0).Format(time.DateOnly)
	case "deny":
		resp.Status, resp.ReasonCode, resp.Note = "Denied", "BY", a.Why // BY: other, with the reason in the note
	default:
		// Pended: ask again for what a question can answer; otherwise it waits for a person.
		missing := map[string]bool{}
		if a.Result != nil {
			for _, it := range a.Result.Missing {
				missing[it.Argument] = true
			}
		}
		var again map[string]bool
		for _, arg := range p.Arguments {
			if !missing[arg.Name] {
				continue
			}
			for _, s := range arg.Find {
				if s.Question != "" {
					if again == nil {
						again = map[string]bool{}
					}
					again[s.Question] = true
				}
			}
		}
		if a.Result != nil && a.Result.Outcome == decide.Pended && again != nil && a.Proposed == nil {
			resp.Status, resp.Questions = "Open", questions(again)
			resp.Note = a.Why
		} else {
			resp.Status, resp.Questions, resp.Note = "InProcess", nil, a.Why
		}
	}
	reply(http.StatusOK, ncpdp.Write(m, resp, scriptID(), sent))
}

// scriptDate is a SCRIPT date (YYYY-MM-DD, or YYYYMMDD) as YYYY-MM-DD.
func scriptDate(s string) string {
	if len(s) == 8 && !strings.Contains(s, "-") {
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func nonEmptyOr(s, or string) string {
	if strings.TrimSpace(s) == "" {
		return or
	}
	return s
}

// addDiagnoses adds the prescriber's ICD-10-CM diagnoses to a request Bundle as confirmed, active Conditions: the
// diagnosis a prescription is written for, as the prescriber states it.
func addDiagnoses(b map[string]any, codes []string) {
	entries, _ := b["entry"].([]any)
	patient := ""
	for _, e := range entries {
		res, _ := e.(map[string]any)["resource"].(map[string]any)
		if res["resourceType"] == "Claim" {
			patient, _ = res["patient"].(map[string]any)["reference"].(string)
		}
	}
	for i, c := range codes {
		code := strings.ToUpper(strings.TrimSpace(c))
		if len(code) > 3 && !strings.Contains(code, ".") {
			code = code[:3] + "." + code[3:]
		}
		id := "dx-" + scriptID()[:8] + "-" + string(rune('a'+i))
		entries = append(entries, map[string]any{"fullUrl": intake.Base + "/Condition/" + id, "resource": map[string]any{
			"resourceType": "Condition", "id": id, "subject": map[string]any{"reference": patient},
			"clinicalStatus": map[string]any{"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": "active"}}},
			"verificationStatus": map[string]any{"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/condition-ver-status", "code": "confirmed"}}},
			"code": map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": code}}}}})
	}
	b["entry"] = entries
}
