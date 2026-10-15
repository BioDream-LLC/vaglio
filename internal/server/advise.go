package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
)

// Advice is what the policy would decide on an order before any request is made: for a coverage requirements (CRD)
// service to show a clinician while the order is still a draft. It is advice, never a decision: nothing is recorded as a
// case, and only the request that follows is decided.
type Advice struct {
	// Decides is false when no policy here decides the service: the CRD service answers as it would without Vaglio.
	Decides       bool   `json:"decides"`
	Policy        string `json:"policy,omitempty"`
	PolicyTitle   string `json:"policyTitle,omitempty"`
	PolicyVersion string `json:"policyVersion,omitempty"`
	// Answer is what a request sent now, with this record, would be told: approve, pend or deny.
	Answer string `json:"answer,omitempty"`
	// Summary is one sentence for a clinician's card.
	Summary string `json:"summary,omitempty"`
	// Missing is the evidence nobody has addressed that the decision needs; Questionnaire asks for it.
	Missing       []string `json:"missing,omitempty"`
	Questionnaire string   `json:"questionnaire,omitempty"`
	// ToApprove is each smallest set of evidence whose documentation would approve it.
	ToApprove [][]string `json:"toApprove,omitempty"`
	// Clause is the policy's words the answer rests on, when it rests on one.
	Clause string `json:"clause,omitempty"`
}

// adviseRequest is an order and the record a CRD service has for it (its CDS Hooks prefetch: Patient, Conditions,
// Procedures, Observations, medications, and any QuestionnaireResponse already answered).
type adviseRequest struct {
	Service   struct{ System, Code string } `json:"service"`
	Resources []map[string]any              `json:"resources"`
}

// crdAdvise answers POST /api/pas/advise with the bearer token the PAS server uses. A CRD service that calls it, and
// gets an error or no answer in time, answers as it would have without Vaglio.
func (cfg Config) crdAdvise(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBundle))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	var in adviseRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "not JSON: "+err.Error())
		return
	}
	if in.Service.System == "" || in.Service.Code == "" {
		writeError(w, http.StatusBadRequest, "service.system and service.code are required")
		return
	}
	b, err := adviseBundle(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		writeError(w, http.StatusBadRequest, strings.Join(problems, "; "))
		return
	}
	svc, _ := req.Service("1")
	choice, _ := cfg.policyFor(svc, req)
	p := choice.Policy
	if p == nil {
		writeJSON(w, http.StatusOK, Advice{Decides: false})
		return
	}
	x := evidence.Extract(p, req)
	res, err := decide.Decide(p, x.Request(p.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a := answerFor(p, res, x)
	out := Advice{Decides: true, Policy: p.ID, PolicyTitle: p.Title, PolicyVersion: p.Version, Answer: a.Decision,
		Questionnaire: a.Questionnaire}
	for _, it := range res.Missing {
		out.Missing = append(out.Missing, it.Text)
	}
	for _, route := range res.ToApprove {
		var r []string
		for _, ch := range route {
			r = append(r, ch.Text)
		}
		out.ToApprove = append(out.ToApprove, r)
	}
	if res.Proof != nil && a.Decision != "pend" {
		out.Clause = res.Proof.Clause
	}
	switch a.Decision {
	case "approve":
		out.Summary = "As the record stands, a prior authorization request for this would be approved."
	case "deny":
		out.Summary = "As the record stands, a request for this would not be approved; see what would change it."
	default:
		if len(out.Missing) > 0 {
			out.Summary = fmt.Sprintf("A request sent now would wait for information the record does not address yet (%d %s).",
				len(out.Missing), map[bool]string{true: "item", false: "items"}[len(out.Missing) == 1])
		} else {
			out.Summary = "A request for this would go to a clinician for review."
		}
	}
	// Advice reads a patient's record, so it is logged; what it said is not a decision and opens no case.
	if _, err := cfg.Audit.Append("pas", "crd.advised", "", map[string]string{"policy": p.ID, "version": p.Version,
		"answer": out.Answer}); err != nil {
		writeError(w, http.StatusInternalServerError, "the advice could not be recorded, so it is not given: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// adviseBundle makes the order and its record into the PAS request shape the engine reads: a Claim for the one service,
// followed by the record's resources.
func adviseBundle(in adviseRequest) (map[string]any, error) {
	const base = "https://advise.vaglio.invalid/fhir/" // relative references resolve against it
	patient := ""
	entries := []any{}
	for _, res := range in.Resources {
		t, _ := res["resourceType"].(string)
		id, _ := res["id"].(string)
		if t == "" {
			return nil, errors.New("every resource needs its resourceType")
		}
		if id == "" {
			id = newJobID()
			res["id"] = id
		}
		if t == "Patient" && patient == "" {
			patient = "Patient/" + id
		}
		entries = append(entries, map[string]any{"fullUrl": base + t + "/" + id, "resource": res})
	}
	if patient == "" {
		patient = "Patient/advised"
		entries = append(entries, map[string]any{"fullUrl": base + "Patient/advised",
			"resource": map[string]any{"resourceType": "Patient", "id": "advised"}})
	}
	org := func(id string) map[string]any {
		return map[string]any{"fullUrl": base + "Organization/" + id,
			"resource": map[string]any{"resourceType": "Organization", "id": id}}
	}
	claim := map[string]any{"fullUrl": base + "Claim/advised", "resource": map[string]any{
		"resourceType": "Claim", "id": "advised", "status": "active", "use": "preauthorization",
		"created":  time.Now().UTC().Format("2006-01-02"),
		"patient":  map[string]any{"reference": patient},
		"insurer":  map[string]any{"reference": "Organization/advise-plan"},
		"provider": map[string]any{"reference": "Organization/advise-provider"},
		"item": []any{map[string]any{"sequence": float64(1), "productOrService": map[string]any{
			"coding": []any{map[string]any{"system": in.Service.System, "code": in.Service.Code}}}}}}}
	all := append([]any{claim}, entries...)
	all = append(all, org("advise-plan"), org("advise-provider"))
	return map[string]any{"resourceType": "Bundle", "type": "collection", "entry": all}, nil
}
