package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/eligibility"
)

// The member's coverage, checked against the plan's member file (serve -members FILE): before a request is approved or
// denied, and as a FHIR CoverageEligibilityRequest answered at POST /api/pas/eligibility. A failed check never denies:
// the request pends for a person to look.

// memberOf is the member id and birth date a request carries: the focal Coverage's subscriberId (or its first
// identifier), else the Patient's member identifier; the birth date is the Patient's.
func memberOf(req *pas.Request) (id, birthDate string) {
	patient := pas.Map(req.Resolve(pas.Str(pas.Map(req.Claim["patient"])["reference"]))["resource"])
	birthDate = pas.Str(patient["birthDate"])
	var cov map[string]any
	for _, ins := range pas.Slice(req.Claim["insurance"]) {
		m := pas.Map(ins)
		res := pas.Map(req.Resolve(pas.Str(pas.Map(m["coverage"])["reference"]))["resource"])
		if res != nil && (cov == nil || m["focal"] == true) {
			cov = res
		}
	}
	if id = pas.Str(cov["subscriberId"]); id == "" {
		for _, x := range pas.Slice(cov["identifier"]) {
			if v := pas.Str(pas.Map(x)["value"]); v != "" {
				id = v
				break
			}
		}
	}
	if id == "" {
		for _, x := range pas.Slice(patient["identifier"]) {
			m := pas.Map(x)
			types := pas.Slice(pas.Map(m["type"])["coding"])
			member := strings.HasSuffix(pas.Str(m["system"]), "/member")
			for _, t := range types {
				if pas.Str(pas.Map(t)["code"]) == "MB" {
					member = true
				}
			}
			if member && pas.Str(m["value"]) != "" {
				id = pas.Str(m["value"])
				break
			}
		}
	}
	return id, birthDate
}

// serviceDate is the day an item is for: its servicedDate or the start of its servicedPeriod, else the Claim's
// created date, else today.
func serviceDate(req *pas.Request, seq string) string {
	for _, it := range pas.Slice(req.Claim["item"]) {
		m := pas.Map(it)
		if pas.Str(m["sequence"]) != seq && jsonNumber(m["sequence"]) != seq {
			continue
		}
		if d := pas.Str(m["servicedDate"]); len(d) >= 10 {
			return d[:10]
		}
		if d := pas.Str(pas.Map(m["servicedPeriod"])["start"]); len(d) >= 10 {
			return d[:10]
		}
	}
	if d := pas.Str(req.Claim["created"]); len(d) >= 10 {
		return d[:10]
	}
	return time.Now().UTC().Format(time.DateOnly)
}

func jsonNumber(v any) string {
	if f, ok := v.(float64); ok {
		b, _ := json.Marshal(f)
		return string(b)
	}
	return ""
}

// checkEligibility is the member file's answer for one item, or nil when there is no member file.
func (cfg Config) checkEligibility(req *pas.Request, seq, service string) *eligibility.Check {
	if cfg.Members == nil {
		return nil
	}
	id, birth := memberOf(req)
	code := service
	if i := strings.LastIndex(code, "|"); i >= 0 {
		code = code[i+1:]
	}
	c := cfg.Members.Check(id, birth, serviceDate(req, seq), code)
	return &c
}

// eligibilityWhy is what the provider is told when a request pends on its member's coverage.
func eligibilityWhy(c *eligibility.Check) string {
	return "Pended for a person to check the member's coverage: " + c.Why + "."
}

// coverageEligibility answers a FHIR CoverageEligibilityRequest (purpose validation, benefits or auth-requirements)
// with a CoverageEligibilityResponse: whether the coverage is in force on the day asked, and, per item, whether this
// plan requires prior authorization for it (a policy here decides it) and whether the benefit leaves it out.
func (cfg Config) coverageEligibility(w http.ResponseWriter, r *http.Request) {
	if cfg.Members == nil {
		writeError(w, http.StatusNotFound, "no member file is configured (serve -members FILE)")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBundle))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read")
		return
	}
	var cer map[string]any
	if json.Unmarshal(raw, &cer) != nil || pas.Str(cer["resourceType"]) != "CoverageEligibilityRequest" {
		writeError(w, http.StatusBadRequest, "send a FHIR CoverageEligibilityRequest")
		return
	}
	// Resolve contained resources (#id) and the patient and coverage it names, as a request Bundle would hold them.
	contained := map[string]map[string]any{}
	for _, c := range pas.Slice(cer["contained"]) {
		m := pas.Map(c)
		contained[pas.Str(m["resourceType"])+"/"+pas.Str(m["id"])] = m
		contained["#"+pas.Str(m["id"])] = m
	}
	find := func(ref string) map[string]any { return contained[ref] }
	patient := find(pas.Str(pas.Map(cer["patient"])["reference"]))
	var cov map[string]any
	for _, ins := range pas.Slice(cer["insurance"]) {
		if c := find(pas.Str(pas.Map(pas.Map(ins)["coverage"])["reference"])); c != nil {
			cov = c
		}
	}
	member := pas.Str(cov["subscriberId"])
	if member == "" {
		for _, x := range pas.Slice(cov["identifier"]) {
			if member = pas.Str(pas.Map(x)["value"]); member != "" {
				break
			}
		}
	}
	day := pas.Str(cer["servicedDate"])
	if day == "" {
		day = pas.Str(pas.Map(cer["servicedPeriod"])["start"])
	}
	if len(day) >= 10 {
		day = day[:10]
	} else {
		day = time.Now().UTC().Format(time.DateOnly)
	}
	birth := pas.Str(patient["birthDate"])
	head := cfg.Members.Check(member, birth, day, "")
	resp := map[string]any{
		"resourceType": "CoverageEligibilityResponse", "status": "active", "purpose": cer["purpose"],
		"patient": cer["patient"], "created": time.Now().UTC().Format(time.RFC3339), "request": map[string]any{
			"identifier": firstIdentifier(cer)}, "insurer": cer["insurer"],
		"servicedDate": day,
		"outcome":      "complete",
		"disposition":  head.Why,
	}
	if head.Status == eligibility.NotFound || head.Status == eligibility.Unknown {
		resp["outcome"] = "error"
		resp["error"] = []any{map[string]any{"code": map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/adjudication-error", "code": "a001"}}, "text": head.Why}}}
		writeFHIR(w, resp)
		return
	}
	insurance := map[string]any{"coverage": pas.Map(pas.Slice(cer["insurance"])[0])["coverage"],
		"inforce": head.Status == eligibility.Active}
	if head.Coverage != nil {
		period := map[string]any{"start": head.Coverage.Start}
		if head.Coverage.End != "" {
			period["end"] = head.Coverage.End
		}
		insurance["benefitPeriod"] = period
	}
	var items []any
	for _, it := range pas.Slice(cer["item"]) {
		m := pas.Map(it)
		out := map[string]any{"productOrService": m["productOrService"]}
		for _, c := range pas.Slice(pas.Map(m["productOrService"])["coding"]) {
			cm := pas.Map(c)
			code := pas.Str(cm["code"])
			ch := cfg.Members.Check(member, birth, day, code)
			out["excluded"] = ch.Status == eligibility.Excluded
			_, required := cfg.policyForCode(pas.Str(cm["system"]), code)
			out["authorizationRequired"] = required
			if required {
				out["authorizationSupporting"] = []any{map[string]any{"text": "The plan's policy decides it; send a PAS request."}}
			}
			break
		}
		items = append(items, out)
	}
	if len(items) > 0 {
		insurance["item"] = items
	}
	resp["insurance"] = []any{insurance}
	writeFHIR(w, resp)
}

func firstIdentifier(r map[string]any) any {
	if ids := pas.Slice(r["identifier"]); len(ids) > 0 {
		return ids[0]
	}
	return map[string]any{"value": pas.Str(r["id"])}
}

func writeFHIR(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(v)
}

// policyForCode is whether a policy in force here decides a code, without a request to weigh precedence on.
func (cfg Config) policyForCode(system, code string) (string, bool) {
	cfg.activateDue()
	for _, p := range cfg.set.all() {
		if p.Decides(system, code) {
			return p.ID, true
		}
	}
	return "", false
}
