package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/precedence"
)

// maxBundle bounds a PAS request Bundle, which may carry documents inline.
const maxBundle = 16 << 20

// PASAnswer is Vaglio's side of Perfuse's PAS decision service protocol (perfuse cmd/perfuse/decider.go): decision, why
// and requested are what Perfuse reads; the rest is the proof and the evidence, for audit and for the reviewer.
type PASAnswer struct {
	Decision  string   `json:"decision"` // approve, deny or pend
	Why       string   `json:"why,omitempty"`
	Requested []string `json:"requested,omitempty"`
	// Questionnaire and Attachments, on a pend, are what to ask the provider for in the PAS server's Da Vinci CDex Task:
	// the DTR questionnaire when a missing piece of evidence is asked in it, and the LOINC attachment codes of the
	// documents the policy says would show it.
	Questionnaire string              `json:"questionnaire,omitempty"`
	Attachments   []string            `json:"attachments,omitempty"`
	Item          string              `json:"item"`
	Case          string              `json:"case"` // the case the decision is recorded as
	Result        *decide.Result      `json:"result"`
	Evidence      evidence.Extraction `json:"evidence"`
	Exemption     *cases.Exemption    `json:"exemption,omitempty"` // approved because the provider is gold carded
	Proposed      *cases.Proposal     `json:"proposed,omitempty"`  // a denial left to a clinician
	// Precedence is why this policy decided when others were for the same service: they were outranked, not in effect,
	// or for another line of business or state.
	Precedence *precedence.Choice `json:"precedence,omitempty"`
	// Receipt checks this decision against the audit log, by anyone holding it, at /api/receipts/{receipt}.
	Receipt string `json:"receipt,omitempty"`
}

// decidePAS answers POST /api/pas/decide?item=<sequence> with a PAS request Bundle as the body. Every decision is a case
// in the audit log before it is answered: one that cannot be recorded is not given, and the PAS server pends the item.
func (cfg Config) decidePAS(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBundle))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		writeError(w, http.StatusBadRequest, "the request could not be read: "+err.Error())
		return
	}
	a, status, msg := cfg.decideBundle(raw, r.URL.Query().Get("item"), r.Header.Get("X-PAS-Request-Id"), "", "")
	if status != http.StatusOK {
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// decideBundle decides one item of a PAS request Bundle and records it as a case: the one path every request takes,
// from a PAS server or keyed in from a fax (channel names it then). It returns the answer, or an HTTP status and why.
func (cfg Config) decideBundle(raw []byte, seq, requestID, channel, keyedBy string) (PASAnswer, int, string) {
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		return PASAnswer{}, http.StatusBadRequest, "malformed request: " + err.Error()
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		return PASAnswer{}, http.StatusBadRequest, strings.Join(problems, "; ")
	}
	if seq == "" {
		if all := req.Services(); len(all) == 1 {
			seq = all[0].Sequence
		} else {
			return PASAnswer{}, http.StatusBadRequest, "the request has several items; say which with ?item=<sequence>"
		}
	}
	svc, ok := req.Service(seq)
	if !ok {
		return PASAnswer{}, http.StatusBadRequest, fmt.Sprintf("the Claim has no item %q", seq)
	}
	choice, service := cfg.policyFor(svc, req)
	p := choice.Policy
	if p == nil {
		// 404 is the protocol's "decides nothing": the PAS server keeps whatever it would otherwise have answered.
		msg := "no policy decides this service"
		if len(choice.Unsettled) > 0 {
			msg = "no policy decides this service for this request as it stands: " + unsettledWhy(precedence.Choice{
				Policy: &policy.Policy{Title: "the plan's policy"}, Unsettled: choice.Unsettled})
		}
		return PASAnswer{}, http.StatusNotFound, msg
	}
	of, notes := cfg.Outside.Findings(context.Background(), p, req, raw, service)
	x := evidence.ExtractWith(p, req, of)
	x.Ignored = append(x.Ignored, notes...)
	res, err := decide.Decide(p, x.Request(p.ID))
	if err != nil {
		// The extraction produced a request the engine refuses, which is Vaglio's defect, not the provider's.
		return PASAnswer{}, http.StatusInternalServerError, err.Error()
	}
	a := answerFor(p, res, x)
	a.Item, a.Result, a.Evidence = seq, res, x
	o := cases.Opening{
		Policy: p.ID, PolicyVersion: p.Version, PolicyTitle: p.Title, Item: seq, Service: service,
		Claim: claimKey(req), Patient: pas.Str(pas.Map(req.Claim["patient"])["reference"]),
		Provider: pas.Str(pas.Map(req.Claim["provider"])["reference"]), Request: requestID,
		Priority: priority(req), ProviderNPI: providerNPI(req), PatientState: patientState(req), Drug: p.Drug,
		Result: res, Evidence: x, Channel: channel, KeyedBy: keyedBy,
	}
	if res.Outcome != decide.Approved && cfg.GoldCard != nil {
		// The record is the log as it stands when the request arrives. Two requests arriving together are each judged on
		// it, without the other, which is what either would have been judged on a moment earlier.
		if e := cfg.GoldCard.Exemption(cfg.Cases.Select(nil), o.ProviderNPI, o.PatientState, p.ID,
			time.Now().UTC()); e != nil {
			o.Exemption = e
			a.Decision, a.Why, a.Requested = "approve", goldcard.Why(e, p.Title), nil
		}
	}
	if o.Exemption == nil && res.Outcome == decide.Denied && cfg.ClinicianDenials {
		o.Proposed = &cases.Proposal{Decision: "deny", Why: a.Why}
		a.Decision, a.Requested = "pend", nil
		a.Why = "Pended for review by a licensed clinician: a denial on medical necessity is made by a clinician, " +
			"never by this system alone."
	}
	if len(choice.Unsettled) > 0 && a.Decision == "deny" {
		a.Decision, a.Why, a.Requested = "pend", unsettledWhy(choice), nil
	}
	if len(choice.SetAside)+len(choice.Unsettled) > 0 {
		o.Precedence = &choice
	}
	a.Exemption, a.Proposed, a.Precedence = o.Exemption, o.Proposed, o.Precedence
	if a.Decision != "pend" {
		a.Questionnaire, a.Attachments = "", nil
	}
	o.Answer = cases.Answer{Decision: a.Decision, Why: a.Why, Requested: a.Requested, Questionnaire: a.Questionnaire,
		Attachments: a.Attachments}
	c, err := cfg.Cases.Open(o, raw)
	if err != nil {
		return PASAnswer{}, http.StatusInternalServerError, "the decision could not be recorded, so it is not given: " + err.Error()
	}
	a.Case = c.ID
	if rs := receiptsFor(cfg.Audit.ForCase(c.ID)); len(rs) > 0 {
		a.Receipt = rs[0].Receipt
	}
	cfg.readSoon(c)
	return a, http.StatusOK, ""
}

// claimKey identifies the request across resubmissions: the Claim's first identifier, or its fullUrl.
func claimKey(req *pas.Request) string {
	for _, id := range pas.Slice(req.Claim["identifier"]) {
		m := pas.Map(id)
		if v := pas.Str(m["value"]); v != "" {
			return pas.Str(m["system"]) + "|" + v
		}
	}
	return req.ClaimURL
}

// priority is the Claim's priority code (processpriority: stat, normal, deferred).
func priority(req *pas.Request) string {
	for _, c := range pas.Slice(pas.Map(req.Claim["priority"])["coding"]) {
		if code := pas.Str(pas.Map(c)["code"]); code != "" {
			return code
		}
	}
	return ""
}

// npiSystem is the NPI's identifier system.
const npiSystem = "http://hl7.org/fhir/sid/us-npi"

// providerNPI is the NPI of the Claim's provider, when the resource it references is in the Bundle and carries one.
func providerNPI(req *pas.Request) string {
	res := pas.Map(req.Resolve(pas.Str(pas.Map(req.Claim["provider"])["reference"]))["resource"])
	for _, id := range pas.Slice(res["identifier"]) {
		m := pas.Map(id)
		if pas.Str(m["system"]) == npiSystem && validNPI(pas.Str(m["value"])) {
			return pas.Str(m["value"])
		}
	}
	return ""
}

// patientState is the state of the patient's address: the home address when one is marked, else the first with a
// state. A state gold card law covers the patient's plan, and the plan's state is not in a PAS request; the address is
// the closest the request comes, and a deployment covering one state needs no state rule at all.
func patientState(req *pas.Request) string {
	res := pas.Map(req.Resolve(pas.Str(pas.Map(req.Claim["patient"])["reference"]))["resource"])
	first := ""
	for _, a := range pas.Slice(res["address"]) {
		m := pas.Map(a)
		st := strings.ToUpper(strings.TrimSpace(pas.Str(m["state"])))
		if st == "" || pas.Str(m["use"]) == "old" {
			continue
		}
		if pas.Str(m["use"]) == "home" {
			return st
		}
		if first == "" {
			first = st
		}
	}
	return first
}

// policyFor chooses the policy deciding a service for this request, and the code it matched (system|code): among the
// policies in force for the first of its codes any policy decides, the one package precedence picks. Choice.Policy is
// nil when none applies.
func (cfg Config) policyFor(svc pas.Service, req *pas.Request) (precedence.Choice, string) {
	cfg.activateDue()
	rq := precedence.Request{Day: cfg.today(), Line: precedence.LineOfBusiness(req), State: patientState(req)}
	all := cfg.set.all()
	for _, cc := range svc.Codes {
		for _, c := range pas.Slice(cc["coding"]) {
			cm := pas.Map(c)
			sys, code := pas.Str(cm["system"]), pas.Str(cm["code"])
			var cands []*policy.Policy
			for _, p := range all {
				if p.Decides(sys, code) {
					cands = append(cands, p)
				}
			}
			if len(cands) > 0 {
				return precedence.Choose(cands, rq), sys + "|" + code
			}
		}
	}
	return precedence.Choice{}, ""
}

// unsettledWhy is the answer when a policy above the one deciding may apply and the request does not say: the lower
// policy may approve, but a denial under it could be the wrong policy's denial.
func unsettledWhy(c precedence.Choice) string {
	var parts []string
	for _, u := range c.Unsettled {
		parts = append(parts, fmt.Sprintf("%s (%s)", u.Title, u.Why))
	}
	return "Pended: the request may fall under " + strings.Join(parts, "; ") + ", which would outrank " + c.Policy.Title +
		". Send the member's coverage (its type) and the patient's address, and it will be decided under the right policy."
}

// answerFor says a result the way a PAS response says it to a provider. A denial names the policy, the clause and what it
// rests on, because CMS-0057 requires a specific reason for every denial; a pend says what would let it be decided.
func answerFor(p *policy.Policy, res *decide.Result, x evidence.Extraction) PASAnswer {
	a := answerOf(p, res)
	if a.Decision == "pend" {
		a.Questionnaire, a.Attachments = asks(p, res.Missing)
	}
	if a.Decision != "approve" && !restates(res) {
		a.Why += routes(res)
	}
	if a.Decision == "pend" && len(x.Unread) > 0 {
		a.Why += fmt.Sprintf(" A reviewer will read the %d document(s) in the request that no rule reads.", len(x.Unread))
	}
	return a
}

func answerOf(p *policy.Policy, res *decide.Result) PASAnswer {
	clause := func() string {
		a, _ := p.Argument(res.Decision)
		return fmt.Sprintf("%s (%s v%s: %q)", phrase(a.Text), p.Title, p.Version, a.Clause)
	}
	switch {
	case res.Outcome == decide.Approved:
		return PASAnswer{Decision: "approve", Why: clause() + "."}
	case res.Outcome == decide.Denied:
		why := clause() + "."
		if len(res.Relied) > 0 {
			var docs []string
			for _, it := range res.Relied {
				docs = append(docs, fmt.Sprintf("%s (%s)", phrase(it.Text), it.Citation.Reference))
			}
			why += " Documented as absent: " + strings.Join(docs, "; ") + "."
		}
		return PASAnswer{Decision: "deny", Why: why}
	case res.Reason == decide.NeedsInformation:
		var need []string
		for _, it := range res.Missing {
			need = append(need, phrase(it.Text))
		}
		return PASAnswer{Decision: "pend", Why: "A decision needs information the request does not address.", Requested: need}
	case res.Reason == decide.ContradictoryRecord:
		var about []string
		for _, it := range res.Contradicted {
			about = append(about, phrase(it.Text))
		}
		return PASAnswer{Decision: "pend", Why: "Pended for clinical review: the record is contradictory about: " +
			strings.Join(about, "; ") + "."}
	case res.Reason == decide.Indeterminate:
		return PASAnswer{Decision: "pend", Why: "Pended for clinical review."}
	default:
		// A policy defect is the policy authors' to fix; the provider is told only that a person will decide.
		return PASAnswer{Decision: "pend", Why: "Pended for a reviewer."}
	}
}

// asks is what to ask a provider for, for the missing evidence: the questionnaire when one of them is a question in it,
// and the documents the policy names for them.
func asks(p *policy.Policy, missing []decide.Item) (string, []string) {
	questionnaire := ""
	var docs []string
	seen := map[string]bool{}
	for _, it := range missing {
		a, ok := p.Argument(it.Argument)
		if !ok {
			continue
		}
		for _, s := range a.Find {
			if s.Question != "" && p.Questionnaire != "" {
				questionnaire = p.Questionnaire
			}
		}
		for _, d := range a.Documents {
			if !seen[d] {
				seen[d] = true
				docs = append(docs, d)
			}
		}
	}
	return questionnaire, docs
}

// restates is whether the routes to approval are only the missing evidence, one piece each: the provider is already told
// it as the information needed, and saying it twice is noise.
func restates(res *decide.Result) bool {
	if res.Reason != decide.NeedsInformation {
		return false
	}
	missing := map[string]bool{}
	for _, it := range res.Missing {
		missing[it.Argument] = true
	}
	for _, r := range res.ToApprove {
		if len(r) != 1 || !missing[r[0].Argument] {
			return false
		}
	}
	return true
}

// routes says what would approve the request, in the provider's terms: only routes that need more documentation, never
// one that needs the record to be wrong, which is not the provider's to fix by sending something.
func routes(res *decide.Result) string {
	var ways []string
	for _, route := range res.ToApprove {
		var need []string
		for _, c := range route {
			if c.ContradictsRecord {
				need = nil
				break
			}
			need = append(need, phrase(c.Text))
		}
		if len(need) > 0 {
			ways = append(ways, strings.Join(need, " and "))
		}
	}
	if len(ways) == 0 {
		return ""
	}
	return " It would be approved with documentation of: " + strings.Join(ways, "; or ") + "."
}

// phrase is a policy's text as part of a sentence: without its closing full stop, which the sentence supplies.
func phrase(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".")
}
