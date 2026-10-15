// Package evidence finds, in a Da Vinci PAS request, the evidence a policy declares.
//
// It reads the request with Perfuse's published pas and fhir packages, so it sees a Bundle exactly as the PAS server that
// forwarded it did. Every finding carries a citation - the entry it came from and the words in it - because evidence without
// provenance is not evidence.
//
// Each piece of evidence ends up in one of three conditions, which is the distinction package decide's denial rule rests
// on:
//
//   - documented present: a questionnaire answered yes; a Condition that is active and confirmed, or provisional where
//     the policy's clause is about a suspected condition; a completed Procedure, recent enough or long enough where the
//     clause says so; a final Observation whose value meets the policy's test.
//   - documented absent: a questionnaire answered no; a Condition that is refuted; a Procedure not done, where the policy
//     says that answers the clause; a final Observation whose value fails the test.
//   - not addressed: nothing in the request speaks to it, or only uncertainly - an unanswered question, an answer that is
//     not yes or no, a questionnaire still in progress, a Condition that is resolved, entered in error, or less certain
//     than the clause requires, a Procedure outside the window or with no date, a value in another unit or given only as
//     "less than".
//
// Time is counted back from the date of the request, the Claim's created date. Nothing can be placed in time without
// one, so a windowed source finds nothing in a request that has none.
//
// Documents the engine cannot read - a DocumentReference, an attachment, answers to some other questionnaire - are listed
// as unread, so the reviewer who gets the request knows what is in it that no rule looked at.
//
// Uncertain findings are deliberately not addressed rather than read one way or the other. "Not addressed" is the condition
// that can never support a denial, so it is the safe place for anything the engine cannot read with confidence.
//
// When a request documents the same evidence both present and absent, neither is used; the evidence is reported as
// contradicted and package decide sends any outcome depending on it to a person.
package evidence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Finding is one place the request speaks to a piece of evidence.
type Finding struct {
	Argument string         `json:"argument"`
	Finding  decide.Finding `json:"finding"`
	decide.Citation
}

// Contradiction is evidence the request documents both ways.
type Contradiction struct {
	Argument string    `json:"argument"`
	Present  []Finding `json:"present"`
	Absent   []Finding `json:"absent"`
}

// Extraction is everything the request says about the policy's evidence.
type Extraction struct {
	Findings       []Finding       `json:"findings"`       // every finding, in Bundle order
	Contradictions []Contradiction `json:"contradictions"` // evidence found both ways, which is not asserted
	Ignored        []string        `json:"ignored"`        // resources that could not be read, and why
	Unread         []Unread        `json:"unread"`         // documents in the request no source reads
}

// Unread is a document in the request the engine does not read: a person may find in it what no rule could.
type Unread struct {
	Reference string `json:"reference"`
	What      string `json:"what"`
}

// Request turns an extraction into a decision request: one assertion per piece of evidence found one way only, the
// first citation for it, and the contradicted evidence named.
func (x Extraction) Request(policyID string) decide.Request {
	req := decide.Request{Policy: policyID, Evidence: []decide.Assertion{}}
	contradicted := map[string]bool{}
	for _, c := range x.Contradictions {
		contradicted[c.Argument] = true
		req.Contradicted = append(req.Contradicted, c.Argument)
	}
	seen := map[string]bool{}
	for _, f := range x.Findings {
		if contradicted[f.Argument] || seen[f.Argument] {
			continue
		}
		seen[f.Argument] = true
		req.Evidence = append(req.Evidence, decide.Assertion{Argument: f.Argument, Finding: f.Finding, Citation: f.Citation})
	}
	return req
}

// Outside prefixes the citations of findings from outside the request: a criteria service, the plan's network, the NPI
// registry.
var Outside = []string{"criteria:", "network:", "npi-registry:"}

// FromOutside is the extraction's findings from outside the request, to decide a stored case again on what those
// sources said then, without asking them again.
func (x Extraction) FromOutside() []Finding {
	var out []Finding
	for _, f := range x.Findings {
		for _, pre := range Outside {
			if strings.HasPrefix(f.Reference, pre) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// Extract reads every QuestionnaireResponse and Condition in the request against the policy's sources.
func Extract(p *policy.Policy, req *pas.Request) Extraction { return ExtractWith(p, req, nil) }

// ExtractWith is Extract with findings from outside the request (a criteria service, the provider registry) beside the
// request's own, so a contradiction between them is found like any other.
func ExtractWith(p *policy.Policy, req *pas.Request, outside []Finding) Extraction {
	x := Extraction{Findings: []Finding{}, Contradictions: []Contradiction{}, Ignored: []string{}, Unread: []Unread{}}
	on, dated := day(pas.Str(req.Claim["created"]))
	asOf := &on
	if !dated || on.precision < 3 {
		asOf = nil
		if windowed(p) {
			x.Ignored = append(x.Ignored, "the Claim has no created date, so nothing can be placed in time and no source with a "+
				"window of days finds anything")
		}
	}
	for _, info := range pas.Slice(req.Claim["supportingInfo"]) {
		if a := pas.Map(pas.Map(info)["valueAttachment"]); len(a) > 0 {
			x.Unread = append(x.Unread, Unread{Reference: req.ClaimURL + "#supportingInfo",
				What: describe("an attachment", pas.Str(a["title"]), pas.Str(a["contentType"]))})
		}
	}
	var meds []taken
	counted := map[*policy.Procedure][]occurrence{}
	for _, e := range req.Entries {
		res := pas.Map(e["resource"])
		kind := pas.Str(res["resourceType"])
		switch kind {
		case "QuestionnaireResponse", "Condition", "Procedure", "Observation", "DocumentReference", "MedicationDispense",
			"MedicationStatement":
		case "Binary":
			x.Unread = append(x.Unread, Unread{Reference: reference(e), What: describe("a document", "", pas.Str(res["contentType"]))})
			continue
		default:
			continue
		}
		ref := reference(e)
		raw, err := json.Marshal(res)
		if err != nil {
			x.Ignored = append(x.Ignored, ref+": "+err.Error())
			continue
		}
		r, err := fhir.UnmarshalResource(raw)
		if err != nil {
			x.Ignored = append(x.Ignored, ref+": "+err.Error())
			continue
		}
		switch r := r.(type) {
		case *fhir.QuestionnaireResponse:
			if canonical, _, _ := strings.Cut(r.Questionnaire, "|"); canonical != p.Questionnaire {
				x.Unread = append(x.Unread, Unread{Reference: ref, What: describe("answers to another questionnaire", r.Questionnaire, "")})
				continue
			}
			x.Findings = append(x.Findings, questionnaire(p, r, ref, &x.Ignored)...)
		case *fhir.Condition:
			x.Findings = append(x.Findings, condition(p, r, ref, asOf)...)
		case *fhir.Procedure:
			x.Findings = append(x.Findings, procedure(p, r, ref, asOf, &counted)...)
		case *fhir.Observation:
			x.Findings = append(x.Findings, observation(p, r, ref, asOf)...)
		case *fhir.MedicationDispense:
			meds = append(meds, taken{ref: ref, code: medicationCode(req, r.Medication), dispense: r})
		case *fhir.MedicationStatement:
			meds = append(meds, taken{ref: ref, code: medicationCode(req, r.Medication), statement: r})
		case *fhir.DocumentReference:
			what := r.Description
			if what == "" && r.Type != nil {
				what = r.Type.Text
				if what == "" && len(r.Type.Coding) > 0 {
					what = r.Type.Coding[0].Display
				}
			}
			x.Unread = append(x.Unread, Unread{Reference: ref, What: describe("a document", what, "")})
		}
	}

	x.Findings = append(x.Findings, medications(p, meds, asOf, &x.Ignored)...)
	x.Findings = append(x.Findings, counts(p, counted)...)
	x.Findings = append(x.Findings, outside...)

	byArg := map[string]*Contradiction{}
	var order []string
	for _, f := range x.Findings {
		c := byArg[f.Argument]
		if c == nil {
			c = &Contradiction{Argument: f.Argument, Present: []Finding{}, Absent: []Finding{}}
			byArg[f.Argument] = c
			order = append(order, f.Argument)
		}
		if f.Finding == decide.Present {
			c.Present = append(c.Present, f)
		} else {
			c.Absent = append(c.Absent, f)
		}
	}
	for _, a := range order {
		if c := byArg[a]; len(c.Present) > 0 && len(c.Absent) > 0 {
			x.Contradictions = append(x.Contradictions, *c)
		}
	}
	return x
}

// reference is how a finding cites the entry it came from: its fullUrl, or type/id when it has none.
func reference(e map[string]any) string {
	if u := pas.Str(e["fullUrl"]); u != "" {
		return u
	}
	res := pas.Map(e["resource"])
	return pas.Str(res["resourceType"]) + "/" + pas.Str(res["id"])
}

// questionnaire reads yes/no answers. Only a completed or amended response counts: one in progress has not been given.
func questionnaire(p *policy.Policy, qr *fhir.QuestionnaireResponse, ref string, ignored *[]string) []Finding {
	if qr.Status != "completed" && qr.Status != "amended" {
		*ignored = append(*ignored, fmt.Sprintf("%s: status %q, so its answers have not been given", ref, qr.Status))
		return nil
	}
	questions := map[string]string{} // linkId -> argument
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.Question != "" {
				questions[s.Question] = a.Name
			}
		}
	}
	var out []Finding
	var walk func(items []fhir.QRItem)
	walk = func(items []fhir.QRItem) {
		for _, it := range items {
			if arg, ok := questions[it.LinkID]; ok {
				if f, ok := answer(arg, it, ref); ok {
					out = append(out, f)
				} else if len(it.Answer) > 0 {
					*ignored = append(*ignored, fmt.Sprintf("%s: item %q is not answered yes or no", ref, it.LinkID))
				}
			}
			walk(it.Item)
			for _, a := range it.Answer {
				walk(a.Item)
			}
		}
	}
	walk(qr.Item)
	return out
}

// answer reads one item answered with a single boolean. Several answers, or any other type, is not a yes or a no.
func answer(arg string, it fhir.QRItem, ref string) (Finding, bool) {
	if len(it.Answer) != 1 || it.Answer[0].ValueBoolean == nil {
		return Finding{}, false
	}
	question := it.Text
	if question == "" {
		question = it.LinkID
	}
	f := Finding{Argument: arg, Finding: decide.Absent, Citation: decide.Citation{Reference: ref + "#" + it.LinkID,
		Quote: question + " - No"}}
	if *it.Answer[0].ValueBoolean {
		f.Finding, f.Quote = decide.Present, question+" - Yes"
	}
	return f, true
}

// condition matches a Condition's codes against the policy's condition sources.
func condition(p *policy.Policy, c *fhir.Condition, ref string, asOf *date) []Finding {
	if c.Code == nil {
		return nil
	}
	clinical, verification := status(c.ClinicalStatus), status(c.VerificationStatus)
	active := clinical == "active" || clinical == "recurrence" || clinical == "relapse"
	var out []Finding
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.Condition == nil {
				continue
			}
			coding, ok := matches(c.Code, s.Condition)
			if !ok {
				continue
			}
			var finding decide.Finding
			switch {
			case verification == "refuted":
				finding = decide.Absent
			case active && containsString(s.Condition.PresentStatuses(), verification):
				finding = decide.Present
			default:
				continue // resolved, entered in error, unstated, or less certain than the clause needs: not addressed
			}
			quote := conditionQuote(c.Code, coding, clinical, verification)
			if n := s.Condition.OnsetDays; n > 0 && finding == decide.Present {
				// How long it has lasted is the evidence: an onset that cannot be placed at least n days back says nothing
				// either way, and neither does a refuted condition's onset (it is absent, however long ago it was not).
				onset, ok := conditionOnset(c)
				if !ok || asOf == nil || onset.last().After(asOf.t.AddDate(0, 0, -n)) {
					continue
				}
				quote += ", onset " + onset.raw
			}
			out = append(out, Finding{Argument: a.Name, Finding: finding, Citation: decide.Citation{Reference: ref, Quote: quote}})
		}
	}
	return out
}

// conditionOnset is when a condition began: onsetDateTime, or the start of onsetPeriod.
func conditionOnset(c *fhir.Condition) (date, bool) {
	if c.OnsetDateTime != "" {
		return day(c.OnsetDateTime)
	}
	if c.OnsetPeriod != nil {
		return day(c.OnsetPeriod.Start)
	}
	return date{}, false
}

// occurrence is one completed procedure a counting source may count.
type occurrence struct {
	when  date
	ref   string
	quote string
}

// counts reads the counting sources (times N): the evidence is present when at least N procedures, on different days and
// as far apart as the source asks, were found. Days are counted from the earliest, so the count is the most the record
// supports. Fewer document nothing.
func counts(p *policy.Policy, counted map[*policy.Procedure][]occurrence) []Finding {
	var out []Finding
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			src := s.Procedure
			if src == nil || src.Times < 2 {
				continue
			}
			occ := append([]occurrence(nil), counted[src]...)
			sort.SliceStable(occ, func(i, j int) bool { return occ[i].when.first().Before(occ[j].when.first()) })
			var kept []occurrence
			for _, o := range occ {
				if len(kept) > 0 {
					prev := kept[len(kept)-1].when
					// Different days, provably: the first day this one can be is after the last day the previous can be,
					// by at least the gap asked for.
					gap := src.ApartDays
					if gap < 1 {
						gap = 1
					}
					if o.when.first().Before(prev.last().AddDate(0, 0, gap)) {
						continue
					}
				}
				kept = append(kept, o)
			}
			if len(kept) < src.Times {
				continue
			}
			refs, quotes := make([]string, len(kept)), make([]string, len(kept))
			for i, o := range kept {
				refs[i], quotes[i] = o.ref, o.quote
			}
			out = append(out, Finding{Argument: a.Name, Finding: decide.Present, Citation: decide.Citation{
				Reference: strings.Join(refs, ", "),
				Quote:     fmt.Sprintf("%d in all: %s", len(kept), strings.Join(quotes, "; "))}})
		}
	}
	return out
}

// procedure reads a Procedure against the policy's procedure sources. One a counting source (times N) matches is put in
// counted, for counts to weigh with the others.
func procedure(p *policy.Policy, pr *fhir.Procedure, ref string, asOf *date, counted *map[*policy.Procedure][]occurrence) []Finding {
	if pr.Code == nil {
		return nil
	}
	var out []Finding
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			src := s.Procedure
			if src == nil {
				continue
			}
			coding, ok := matches(pr.Code, &policy.Codes{System: src.System, Codes: src.Codes})
			if !ok {
				continue
			}
			name := codeName(pr.Code, coding)
			switch pr.Status {
			case "not-done":
				if src.NotDoneIsAbsent {
					out = append(out, Finding{Argument: a.Name, Finding: decide.Absent,
						Citation: decide.Citation{Reference: ref, Quote: name + ", not done"}})
				}
				continue
			case "completed":
			default:
				continue // in progress, on hold, stopped, entered in error, unknown: nothing either way
			}
			var when, from, to date
			var hasWhen, hasCourse bool
			if pr.PerformedPeriod != nil {
				from, hasCourse = day(pr.PerformedPeriod.Start)
				var hasEnd bool
				to, hasEnd = day(pr.PerformedPeriod.End)
				hasCourse = hasCourse && hasEnd
				when, hasWhen = to, hasEnd
				if !hasEnd {
					when, hasWhen = day(pr.PerformedPeriod.Start)
				}
			} else {
				when, hasWhen = day(pr.PerformedDateTime)
			}
			quote := name + ", completed"
			if src.MinDays > 0 {
				if !hasCourse || from.last().AddDate(0, 0, src.MinDays).After(to.first()) {
					continue // no period, or not provably long enough: not evidence that no long enough course happened
				}
				quote += fmt.Sprintf(", %s to %s", pr.PerformedPeriod.Start, pr.PerformedPeriod.End)
			} else if hasWhen {
				quote += ", " + when.raw
			}
			if src.WithinDays > 0 && (asOf == nil || !hasWhen || !when.within(*asOf, src.WithinDays)) {
				continue
			}
			if src.Times > 1 {
				if hasWhen { // an undated procedure cannot be told apart from another
					(*counted)[src] = append((*counted)[src], occurrence{when: when, ref: ref, quote: quote})
				}
				continue
			}
			out = append(out, Finding{Argument: a.Name, Finding: decide.Present, Citation: decide.Citation{Reference: ref, Quote: quote}})
		}
	}
	return out
}

// observation reads a final Observation's value against the policy's observation sources.
func observation(p *policy.Policy, o *fhir.Observation, ref string, asOf *date) []Finding {
	if o.Code == nil || (o.Status != "final" && o.Status != "amended" && o.Status != "corrected") || o.DataAbsentReason != nil {
		return nil // preliminary, cancelled, entered in error, or with no value: nothing to read
	}
	when, hasWhen := day(o.EffectiveDateTime)
	if !hasWhen && o.EffectivePeriod != nil {
		if when, hasWhen = day(o.EffectivePeriod.End); !hasWhen {
			when, hasWhen = day(o.EffectivePeriod.Start)
		}
	}
	var out []Finding
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			src := s.Observation
			if src == nil {
				continue
			}
			coding, ok := matches(o.Code, &policy.Codes{System: src.System, Codes: src.Codes})
			if !ok {
				continue
			}
			if src.WithinDays > 0 && (asOf == nil || !hasWhen || !when.within(*asOf, src.WithinDays)) {
				continue
			}
			finding, value, ok := read(o, src)
			if !ok {
				continue
			}
			quote := codeName(o.Code, coding) + ": " + value
			if hasWhen {
				quote += ", " + when.raw
			}
			out = append(out, Finding{Argument: a.Name, Finding: finding, Citation: decide.Citation{Reference: ref, Quote: quote}})
		}
	}
	return out
}

// read applies a source's value test. It reads only what it can read exactly.
func read(o *fhir.Observation, src *policy.Observation) (decide.Finding, string, bool) {
	switch {
	case src.Boolean:
		if o.ValueBoolean == nil {
			return "", "", false
		}
		if *o.ValueBoolean {
			return decide.Present, "yes", true
		}
		return decide.Absent, "no", true
	case src.AtLeast != nil || src.AtMost != nil:
		q := o.ValueQuantity
		t := src.AtLeast
		if t == nil {
			t = src.AtMost
		}
		if q == nil || q.Value == nil || q.Comparator != "" || unit(q) != t.Unit {
			return "", "", false // "<5", another unit, or no number: not read
		}
		v := *q.Value
		value := strconv.FormatFloat(v, 'f', -1, 64) + " " + t.Unit
		meets := v >= t.Value
		if src.AtMost != nil {
			meets = v <= t.Value
		}
		if meets {
			return decide.Present, value, true
		}
		return decide.Absent, value, true
	case src.Values != nil:
		if o.ValueCodeableConcept == nil {
			return "", "", false
		}
		if c, ok := matches(o.ValueCodeableConcept, &policy.Codes{System: src.Values.System, Codes: src.Values.Present}); ok {
			return decide.Present, codeName(o.ValueCodeableConcept, c), true
		}
		if c, ok := matches(o.ValueCodeableConcept, &policy.Codes{System: src.Values.System, Codes: src.Values.Absent}); ok {
			return decide.Absent, codeName(o.ValueCodeableConcept, c), true
		}
	}
	return "", "", false
}

// unit is a quantity's UCUM code, or its human unit when it gives no code.
func unit(q *fhir.Quantity) string {
	if q.Code != "" && (q.System == "" || q.System == "http://unitsofmeasure.org") {
		return q.Code
	}
	return q.Unit
}

func codeName(cc *fhir.CodeableConcept, c fhir.Coding) string {
	name := cc.Text
	if name == "" {
		name = c.Display
	}
	if name == "" {
		return c.Code
	}
	return name + " (" + c.Code + ")"
}

func describe(kind, what, contentType string) string {
	s := kind
	if what != "" {
		s += ": " + what
	}
	if contentType != "" {
		s += " (" + contentType + ")"
	}
	return s
}

// windowed is whether any of the policy's sources counts days.
func windowed(p *policy.Policy) bool {
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if (s.Procedure != nil && s.Procedure.WithinDays > 0) || (s.Observation != nil && s.Observation.WithinDays > 0) ||
				(s.Medication != nil && s.Medication.WithinDays > 0) || (s.Condition != nil && s.Condition.OnsetDays > 0) {
				return true
			}
		}
	}
	return false
}

// date is a FHIR date or dateTime to the day: a year, a month or a day (precision 1, 2 or 3). Times are dropped; a window
// of days does not turn on hours.
type date struct {
	t         time.Time // the first day it can be
	precision int
	raw       string
}

func day(s string) (date, bool) {
	if len(s) > 10 {
		s = s[:10] // the date part of a dateTime
	}
	for precision, layout := range []string{"2006", "2006-01", "2006-01-02"} {
		if len(s) == len(layout) {
			if t, err := time.Parse(layout, s); err == nil {
				return date{t: t, precision: precision + 1, raw: s}, true
			}
		}
	}
	return date{}, false
}

func (d date) first() time.Time { return d.t }

// last is the last day it can be.
func (d date) last() time.Time {
	switch d.precision {
	case 1:
		return d.t.AddDate(1, 0, -1)
	case 2:
		return d.t.AddDate(0, 1, -1)
	}
	return d.t
}

// within is whether every day it can be falls in the n days up to and including asOf.
func (d date) within(asOf date, n int) bool {
	return !d.first().Before(asOf.t.AddDate(0, 0, -n)) && !d.last().After(asOf.t)
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func status(cc *fhir.CodeableConcept) string {
	if cc == nil {
		return ""
	}
	for _, c := range cc.Coding {
		if c.Code != "" {
			return c.Code
		}
	}
	return ""
}

func matches(cc *fhir.CodeableConcept, want *policy.Codes) (fhir.Coding, bool) {
	for _, c := range cc.Coding {
		if c.System != want.System {
			continue
		}
		for _, code := range want.Codes {
			if c.Code == code {
				return c, true
			}
		}
	}
	return fhir.Coding{}, false
}

func conditionQuote(cc *fhir.CodeableConcept, c fhir.Coding, clinical, verification string) string {
	name := cc.Text
	if name == "" {
		name = c.Display
	}
	parts := []string{}
	if name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, "("+c.Code+")")
	q := strings.Join(parts, " ") + ", verification status " + verification
	if clinical != "" {
		q += ", clinical status " + clinical
	}
	return q
}

// taken is a MedicationDispense or MedicationStatement, with its medication's code resolved.
type taken struct {
	ref       string
	code      *fhir.CodeableConcept
	dispense  *fhir.MedicationDispense
	statement *fhir.MedicationStatement
}

// medicationCode is a medication's code: the concept itself, or the code of the Medication resource it references in
// the Bundle.
func medicationCode(req *pas.Request, m *fhir.CodeableReference) *fhir.CodeableConcept {
	if m == nil {
		return nil
	}
	if m.Concept != nil {
		return m.Concept
	}
	if m.Reference == nil {
		return nil
	}
	res := pas.Map(req.Resolve(m.Reference.Reference)["resource"])
	if pas.Str(res["resourceType"]) != "Medication" {
		return nil
	}
	raw, _ := json.Marshal(res)
	r, err := fhir.UnmarshalResource(raw)
	if med, ok := r.(*fhir.Medication); err == nil && ok {
		return med.Code
	}
	return nil
}

// medications reads the medication sources. For a source with lasting N days, every matching dispense and statement in
// the window adds its days, and the evidence is present when they reach N: a 90-day trial is often three 30-day fills,
// and each fill alone says nothing either way. The finding cites every record counted.
func medications(p *policy.Policy, meds []taken, asOf *date, ignored *[]string) []Finding {
	var out []Finding
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			src := s.Medication
			if src == nil {
				continue
			}
			var days int
			var refs, quotes []string
			for _, m := range meds {
				coding, ok := matches(m.code, &policy.Codes{System: src.System, Codes: src.Codes})
				if !ok {
					continue
				}
				name := codeName(m.code, coding)
				var when date
				var hasWhen bool
				var n int
				var quote string
				switch {
				case m.dispense != nil:
					d := m.dispense
					if d.Status != "completed" {
						continue // preparation, in progress, cancelled, declined: nothing was handed over
					}
					when, hasWhen = day(d.WhenHandedOver)
					if q := d.DaysSupply; q != nil && q.Value != nil && q.Comparator == "" && inDays(q) {
						n = int(*d.DaysSupply.Value)
					}
					quote = fmt.Sprintf("%s dispensed %s", name, d.WhenHandedOver)
					if n > 0 {
						quote += fmt.Sprintf(", %d days' supply", n)
					}
				default:
					st := m.statement
					switch st.Status {
					case "not-taken":
						if src.NotTakenIsAbsent {
							out = append(out, Finding{Argument: a.Name, Finding: decide.Absent,
								Citation: decide.Citation{Reference: m.ref, Quote: name + ", not taken"}})
						}
						continue
					case "active", "completed":
					default:
						continue // intended, stopped, on hold, entered in error, unknown
					}
					if st.EffectivePeriod != nil {
						from, ok1 := day(st.EffectivePeriod.Start)
						to, ok2 := day(st.EffectivePeriod.End)
						if ok1 && ok2 {
							// Whole days provably taken: from the last day the start can be to the first day the end can be.
							n = int(to.first().Sub(from.last()).Hours()/24) + 1
							when, hasWhen = to, true
						} else if ok1 {
							when, hasWhen = from, true
						}
						quote = fmt.Sprintf("%s taken %s to %s", name, st.EffectivePeriod.Start, st.EffectivePeriod.End)
					} else {
						when, hasWhen = day(st.EffectiveStart)
						quote = fmt.Sprintf("%s taken %s", name, st.EffectiveStart)
					}
				}
				if src.WithinDays > 0 && (asOf == nil || !hasWhen || !when.within(*asOf, src.WithinDays)) {
					continue
				}
				if src.MinDays == 0 {
					out = append(out, Finding{Argument: a.Name, Finding: decide.Present, Citation: decide.Citation{Reference: m.ref, Quote: quote}})
					continue
				}
				if n <= 0 {
					*ignored = append(*ignored, fmt.Sprintf("%s: %s, but how many days it covers is not recorded", m.ref, name))
					continue
				}
				days += n
				refs, quotes = append(refs, m.ref), append(quotes, quote)
			}
			if src.MinDays > 0 && days >= src.MinDays {
				out = append(out, Finding{Argument: a.Name, Finding: decide.Present, Citation: decide.Citation{
					Reference: strings.Join(refs, ", "),
					Quote:     fmt.Sprintf("%d days in all: %s", days, strings.Join(quotes, "; "))}})
			}
		}
	}
	return out
}

// inDays is whether a quantity is in days: UCUM d, or a unit spelled as days. A supply with no unit is read as days, as
// daysSupply is defined to be.
func inDays(q *fhir.Quantity) bool {
	switch strings.ToLower(strings.TrimSpace(q.Unit)) {
	case "", "d", "day", "days":
		return q.Code == "" || q.Code == "d"
	}
	return q.Code == "d"
}
