// Package fairness asks whether the engine's mistakes fall harder on some patients than on others.
//
// Approval rates are not the measure: coverage criteria treat patients differently on purpose (a test indicated at one
// age and not another), so groups are approved at different rates for good reasons. The measure is error, following the
// approach in "Fairness Evaluation of Automated Prior Authorization" (Anterior, 2026): of the engine's denials, how many a
// clinician or an appeal reversed, compared across groups; and how often a request was pended to ask for more, a burden
// a group should not carry more than another for the same service.
//
// Vaglio's engine never reads demographics: evidence comes only from the sources a policy names, and no source reads the
// patient's sex, age, race or ethnicity. A difference here comes from the records providers sent, from reviewers, or
// from appeals, which is where to look. Demographics are read from the stored requests when the report is made, never
// copied into cases or the log, and a group of fewer than MinCell requests is reported without its counts.
package fairness

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/precedence"
)

// MinCell is the smallest group whose counts are shown: smaller ones could identify a patient (CMS suppresses cells
// under 11 in the data it publishes).
const MinCell = 11

// Dimensions a report groups by.
var Dimensions = []string{"sex", "age", "race", "ethnicity", "state", "line"}

// Patient is what one request says about its patient.
type Patient struct {
	Sex       string
	Age       string // a band
	Race      string
	Ethnicity string
	State     string
	Line      string
}

const unknown = "not recorded"

// Group is one value of a dimension.
type Group struct {
	Value    string `json:"value"`
	Requests int    `json:"requests"`
	// Suppressed: fewer than MinCell requests; no counts are shown.
	Suppressed bool `json:"suppressed,omitempty"`
	// EngineDenials and Reversed: denials the engine issued or proposed, and those a clinician or an appeal reversed.
	EngineDenials   int      `json:"engineDenials,omitempty"`
	Reversed        int      `json:"reversed,omitempty"`
	ReversedPercent *float64 `json:"reversedPercent,omitempty"`
	// Pended to ask the provider for more, and its share of requests.
	PendedForInformation int      `json:"pendedForInformation,omitempty"`
	PendedPercent        *float64 `json:"pendedPercent,omitempty"`
	// ApprovedPercent is shown for context only: it differs between groups for legitimate reasons.
	ApprovedPercent *float64 `json:"approvedPercent,omitempty"`
	// Flags say where this group differs from the rest beyond what chance explains (two-sided Fisher exact test,
	// p < Alpha), in words.
	Flags []string `json:"flags,omitempty"`
}

// Alpha is the significance level of a flag. With many groups some differences are flagged by chance: a flag is a
// place to read cases, not a finding.
const Alpha = 0.01

// Dimension is a report's groups for one dimension.
type Dimension struct {
	Name   string  `json:"name"`
	Groups []Group `json:"groups"`
}

// Report is a period's fairness report.
type Report struct {
	From       string      `json:"from"`
	To         string      `json:"to"`
	Policy     string      `json:"policy,omitempty"` // empty: every policy
	Requests   int         `json:"requests"`
	Unreadable int         `json:"unreadable,omitempty"` // requests whose stored Bundle could not be read
	Dimensions []Dimension `json:"dimensions"`
	Note       string      `json:"note"`
}

// Note is said under every report.
const Note = "The engine reads no demographics, so a difference here comes from the records providers sent, reviewers or " +
	"appeals. Approval rates are shown for context only: criteria treat patients differently on purpose. Flags mark " +
	"error rates that differ from the rest of the requests beyond what chance explains at p < 0.01; with many groups " +
	"some are flagged by chance, so read the cases before concluding anything. Groups of fewer than 11 requests are not " +
	"shown. Income and disability are not in a prior authorization request and are not reported."

// ReadPatient reads the patient's demographics and the member's line of business from a stored request Bundle; state is
// the case's patient state.
func ReadPatient(raw []byte, state string) (Patient, error) {
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		return Patient{}, err
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		return Patient{}, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	pt := pas.Map(req.Resolve(pas.Str(pas.Map(req.Claim["patient"])["reference"]))["resource"])
	p := Patient{Sex: unknown, Age: unknown, Race: unknown, Ethnicity: unknown, State: state, Line: precedence.LineOfBusiness(req)}
	if p.State == "" {
		p.State = unknown
	}
	if p.Line == "" {
		p.Line = unknown
	}
	switch g := pas.Str(pt["gender"]); g {
	case "female", "male", "other":
		p.Sex = g
	}
	on, _ := time.Parse(time.DateOnly, firstN(pas.Str(req.Claim["created"]), 10))
	if born, err := time.Parse(time.DateOnly, firstN(pas.Str(pt["birthDate"]), 10)); err == nil && !on.IsZero() {
		p.Age = ageBand(born, on)
	}
	for _, e := range pas.Slice(pt["extension"]) {
		m := pas.Map(e)
		switch pas.Str(m["url"]) {
		case "http://hl7.org/fhir/us/core/StructureDefinition/us-core-race":
			p.Race = ombCategory(m, raceNames)
		case "http://hl7.org/fhir/us/core/StructureDefinition/us-core-ethnicity":
			p.Ethnicity = ombCategory(m, ethnicityNames)
		}
	}
	return p, nil
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func ageBand(born, on time.Time) string {
	age := on.Year() - born.Year()
	if on.YearDay() < born.YearDay() {
		age--
	}
	switch {
	case age < 0:
		return unknown
	case age < 18:
		return "0-17"
	case age < 45:
		return "18-44"
	case age < 65:
		return "45-64"
	case age < 75:
		return "65-74"
	}
	return "75 and over"
}

// The OMB categories US Core codes race and ethnicity in (CDC race and ethnicity code set).
var raceNames = map[string]string{
	"1002-5": "American Indian or Alaska Native", "2028-9": "Asian", "2054-5": "Black or African American",
	"2076-8": "Native Hawaiian or Other Pacific Islander", "2106-3": "White",
}
var ethnicityNames = map[string]string{"2135-2": "Hispanic or Latino", "2186-5": "Not Hispanic or Latino"}

// ombCategory reads a US Core race or ethnicity extension: its one OMB category, "more than one" for several, or not
// recorded (asked but declined counts as not recorded).
func ombCategory(ext map[string]any, names map[string]string) string {
	var found []string
	for _, s := range pas.Slice(ext["extension"]) {
		m := pas.Map(s)
		if pas.Str(m["url"]) != "ombCategory" {
			continue
		}
		if n, ok := names[pas.Str(pas.Map(m["valueCoding"])["code"])]; ok {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return unknown
	case 1:
		return found[0]
	}
	return "more than one"
}

func (p Patient) value(dim string) string {
	switch dim {
	case "sex":
		return p.Sex
	case "age":
		return p.Age
	case "race":
		return p.Race
	case "ethnicity":
		return p.Ethnicity
	case "state":
		return p.State
	}
	return p.Line
}

type tally struct{ requests, approved, decided, denials, reversed, pended int }

// Build makes the report over the cases created in the period (from, to inclusive) for one policy, or every policy when
// policy is "". patient reads a case's patient from its stored request.
func Build(cs []cases.Case, from, to time.Time, policy string, patient func(*cases.Case) (Patient, error)) Report {
	out := Report{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Policy: policy, Note: Note}
	type row struct {
		pt Patient
		t  tally
	}
	var rows []row
	for i := range cs {
		c := &cs[i]
		t0, _ := time.Parse(time.RFC3339Nano, c.Created)
		if c.Result == nil || c.Status == cases.Superseded || t0.Before(from) || !t0.Before(to.AddDate(0, 0, 1)) ||
			(policy != "" && c.Policy != policy) {
			continue
		}
		pt, err := patient(c)
		if err != nil {
			out.Unreadable++
			continue
		}
		var t tally
		t.requests = 1
		final := c.Final()
		if c.Appeal != nil && c.Appeal.Outcome == "overturned" {
			final = "approve"
		}
		if final != "" {
			t.decided = 1
		}
		if final == "approve" {
			t.approved = 1
		}
		if c.Result.Outcome == decide.Denied || c.Proposed != nil {
			t.denials = 1
			if final == "approve" {
				t.reversed = 1
			}
		}
		if c.Result.Outcome == decide.Pended && len(c.Result.Missing) > 0 {
			t.pended = 1
		}
		rows = append(rows, row{pt, t})
	}
	out.Requests = len(rows)
	var total tally
	for _, r := range rows {
		total = add(total, r.t)
	}
	for _, dim := range Dimensions {
		byValue := map[string]tally{}
		for _, r := range rows {
			v := r.pt.value(dim)
			byValue[v] = add(byValue[v], r.t)
		}
		d := Dimension{Name: dim, Groups: []Group{}}
		for v, t := range byValue {
			g := Group{Value: v, Requests: t.requests}
			if t.requests < MinCell {
				g.Suppressed = true
				d.Groups = append(d.Groups, g)
				continue
			}
			g.EngineDenials, g.Reversed, g.PendedForInformation = t.denials, t.reversed, t.pended
			g.ReversedPercent, g.PendedPercent, g.ApprovedPercent = percent(t.reversed, t.denials),
				percent(t.pended, t.requests), percent(t.approved, t.decided)
			rest := sub(total, t)
			if t.denials > 0 && rest.denials > 0 {
				if p := Fisher(t.reversed, t.denials-t.reversed, rest.reversed, rest.denials-rest.reversed); p < Alpha {
					g.Flags = append(g.Flags, fmt.Sprintf("its denials were reversed %s of the time, against %s for the rest (p = %.3g)",
						fmtPct(t.reversed, t.denials), fmtPct(rest.reversed, rest.denials), p))
				}
			}
			if rest.requests > 0 {
				if p := Fisher(t.pended, t.requests-t.pended, rest.pended, rest.requests-rest.pended); p < Alpha {
					g.Flags = append(g.Flags, fmt.Sprintf("pended to ask for more %s of the time, against %s for the rest (p = %.3g)",
						fmtPct(t.pended, t.requests), fmtPct(rest.pended, rest.requests), p))
				}
			}
			d.Groups = append(d.Groups, g)
		}
		sort.Slice(d.Groups, func(i, j int) bool {
			if d.Groups[i].Requests != d.Groups[j].Requests {
				return d.Groups[i].Requests > d.Groups[j].Requests
			}
			return d.Groups[i].Value < d.Groups[j].Value
		})
		out.Dimensions = append(out.Dimensions, d)
	}
	return out
}

func add(a, b tally) tally {
	return tally{a.requests + b.requests, a.approved + b.approved, a.decided + b.decided, a.denials + b.denials,
		a.reversed + b.reversed, a.pended + b.pended}
}

func sub(a, b tally) tally {
	return tally{a.requests - b.requests, a.approved - b.approved, a.decided - b.decided, a.denials - b.denials,
		a.reversed - b.reversed, a.pended - b.pended}
}

func percent(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	v := 100 * float64(n) / float64(of)
	return &v
}

func fmtPct(n, of int) string {
	return fmt.Sprintf("%.0f%% (%d of %d)", 100*float64(n)/float64(of), n, of)
}

// Fisher is the two-sided p-value of Fisher's exact test on the 2x2 table [[a b] [c d]]: the probability, with the
// margins fixed, of a table at most as likely as this one.
func Fisher(a, b, c, d int) float64 {
	r1, c1, n := a+b, a+c, a+b+c+d
	if n == 0 {
		return 1
	}
	lp := func(x int) float64 { // log P(top-left = x)
		return lchoose(r1, x) + lchoose(n-r1, c1-x) - lchoose(n, c1)
	}
	observed := lp(a)
	lo, hi := max(0, c1-(n-r1)), min(r1, c1)
	p := 0.0
	for x := lo; x <= hi; x++ {
		if l := lp(x); l <= observed+1e-7 {
			p += math.Exp(l)
		}
	}
	return math.Min(1, p)
}

func lchoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}
