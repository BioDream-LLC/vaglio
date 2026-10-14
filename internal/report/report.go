// Package report turns the cases in the audit log into the numbers a payer has to publish, and the ones that say where
// its policies cost providers a round trip.
//
// Metrics follows the prior authorisation metrics CMS-0057-F requires impacted payers to post each year, with the counts
// CMS-0062-P proposes beside the percentages: for standard and expedited requests separately, how many were approved and
// denied, and how long a determination took. It reports only what the log knows. Vaglio does not see appeals or
// extensions of the decision timeframe, so the metrics that need them are named as not tracked rather than reported as
// zero; a zero would be a claim.
//
// Pends counts which piece of missing evidence pends requests most: each is a question a DTR questionnaire could have
// asked, or a record the provider could have been told to send, and the top of the list is where a policy or its
// questionnaire is costing providers the most.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Period is a span of days, from and to inclusive, in UTC.
type Period struct {
	From time.Time
	To   time.Time // the last day; cases created on it are included
}

// Contains is whether a case created at t falls in the period.
func (p Period) Contains(t time.Time) bool {
	return !t.Before(p.From) && t.Before(p.To.AddDate(0, 0, 1))
}

// ParsePeriod reads from and to as YYYY-MM-DD. Empty from is the start of to's year; empty to is today.
func ParsePeriod(from, to string, now time.Time) (Period, error) {
	var p Period
	var err error
	if to == "" {
		p.To = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	} else if p.To, err = time.Parse(time.DateOnly, to); err != nil {
		return p, fmt.Errorf("to: %q is not YYYY-MM-DD", to)
	}
	if from == "" {
		p.From = time.Date(p.To.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	} else if p.From, err = time.Parse(time.DateOnly, from); err != nil {
		return p, fmt.Errorf("from: %q is not YYYY-MM-DD", from)
	}
	if p.To.Before(p.From) {
		return p, fmt.Errorf("the period ends (%s) before it starts (%s)", to, from)
	}
	return p, nil
}

func created(c *cases.Case) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, c.Created)
	return t
}

// Group is the metrics for one kind of request.
type Group struct {
	Requests int `json:"requests"` // received in the period and not replaced by a resubmission
	Approved int `json:"approved"`
	Denied   int `json:"denied"`
	Pending  int `json:"pending"` // still waiting for a reviewer
	// The percentages are of requests determined, approved plus denied; nil when none was.
	ApprovedPercent *float64 `json:"approvedPercent"`
	DeniedPercent   *float64 `json:"deniedPercent"`
	// Appealed counts denials whose appeal outcome is recorded; ApprovedAfterAppeal those overturned. The percentage is
	// of appealed; nil when none was.
	Appealed                   int      `json:"appealed"`
	ApprovedAfterAppeal        int      `json:"approvedAfterAppeal"`
	ApprovedAfterAppealPercent *float64 `json:"approvedAfterAppealPercent"`
	// Extended counts requests whose decision timeframe was extended; ApprovedAfterExtension those approved after it, and
	// its percentage is of the requests determined, as CMS's metric is.
	Extended                      int      `json:"extended"`
	ApprovedAfterExtension        int      `json:"approvedAfterExtension"`
	ApprovedAfterExtensionPercent *float64 `json:"approvedAfterExtensionPercent"`
	// Late counts requests determined after they were due, or still waiting past it.
	Late        int `json:"late"`
	RealTime    int `json:"realTime"`    // determined as they were received
	AfterReview int `json:"afterReview"` // determined by a reviewer
	GoldCard    int `json:"goldCard"`    // approved because the provider is exempt
	// Time from receipt to determination, in hours, over requests determined; nil when none was.
	AverageHours *float64 `json:"averageHours"`
	MedianHours  *float64 `json:"medianHours"`
}

// Service is one service's counts.
type Service struct {
	Policy   string `json:"policy"`
	Title    string `json:"title"`
	Service  string `json:"service"`
	Requests int    `json:"requests"`
	Approved int    `json:"approved"`
	Denied   int    `json:"denied"`
	Pending  int    `json:"pending"`
}

// Metrics is a period's prior authorisation metrics.
type Metrics struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Standard  Group  `json:"standard"`
	Expedited Group  `json:"expedited"`
	// Drugs are counted apart, as CMS-0057-F's metrics exclude them and CMS-0062-P proposes their own.
	DrugStandard  Group     `json:"drugStandard"`
	DrugExpedited Group     `json:"drugExpedited"`
	Services      []Service `json:"services"`
	Superseded    int       `json:"superseded"` // requests replaced by a resubmission before anyone decided them
	NotTracked    []string  `json:"notTracked"`
}

// NotTracked is what the CMS metrics ask for that Vaglio does not see.
var NotTracked = []string{}

// ExtensionNote is said beside the extension counts.
const ExtensionNote = "Extensions counted are those recorded in Vaglio (POST /api/cases/{id}/extend); a timeframe extended elsewhere is not counted."

// AppealNote is said beside the appeal counts: Vaglio knows of an appeal only when its outcome is recorded.
const AppealNote = "Appeals counted are those whose outcome is recorded in Vaglio (POST /api/cases/{id}/appeal); an appeal decided elsewhere and not recorded is not counted."

func pct(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	v := 100 * float64(n) / float64(of)
	return &v
}

// Build computes the metrics over the cases created in the period.
func Build(cs []cases.Case, p Period) Metrics { return BuildAt(cs, p, time.Now()) }

// BuildAt is Build as of a moment: what counts as late depends on it.
func BuildAt(cs []cases.Case, p Period, now time.Time) Metrics {
	m := Metrics{From: p.From.Format(time.DateOnly), To: p.To.Format(time.DateOnly), Services: []Service{},
		NotTracked: NotTracked}
	hours := map[*Group][]float64{}
	services := map[string]*Service{}
	for i := range cs {
		c := &cs[i]
		at := created(c)
		if !p.Contains(at) {
			continue
		}
		if c.Status == cases.Superseded {
			m.Superseded++
			continue
		}
		g := &m.Standard
		switch {
		case c.Drug && c.Expedited():
			g = &m.DrugExpedited
		case c.Drug:
			g = &m.DrugStandard
		case c.Expedited():
			g = &m.Expedited
		}
		key := c.Policy + " " + c.Service
		sv := services[key]
		if sv == nil {
			sv = &Service{Policy: c.Policy, Title: c.PolicyTitle, Service: c.Service}
			services[key] = sv
		}
		g.Requests++
		sv.Requests++
		if c.Extension != nil {
			g.Extended++
		}
		final := c.Final()
		switch final {
		case "approve":
			g.Approved++
			sv.Approved++
		case "deny":
			g.Denied++
			sv.Denied++
		default:
			g.Pending++
			sv.Pending++
			if now.After(c.Due()) {
				g.Late++
			}
			continue
		}
		if final == "approve" && c.Extension != nil {
			g.ApprovedAfterExtension++
		}
		if c.Appeal != nil {
			g.Appealed++
			if c.Appeal.Outcome == "overturned" {
				g.ApprovedAfterAppeal++
			}
		}
		decided := at
		if c.Status == cases.Reviewed {
			g.AfterReview++
			decided, _ = time.Parse(time.RFC3339Nano, c.Review.Time)
		} else {
			g.RealTime++
			if c.Exemption != nil {
				g.GoldCard++
			}
		}
		if decided.After(c.Due()) {
			g.Late++
		}
		hours[g] = append(hours[g], decided.Sub(at).Hours())
	}
	for _, g := range []*Group{&m.Standard, &m.Expedited, &m.DrugStandard, &m.DrugExpedited} {
		determined := g.Approved + g.Denied
		g.ApprovedPercent, g.DeniedPercent = pct(g.Approved, determined), pct(g.Denied, determined)
		g.ApprovedAfterAppealPercent = pct(g.ApprovedAfterAppeal, g.Appealed)
		g.ApprovedAfterExtensionPercent = pct(g.ApprovedAfterExtension, determined)
		if hs := hours[g]; len(hs) > 0 {
			sort.Float64s(hs)
			sum := 0.0
			for _, h := range hs {
				sum += h
			}
			avg := sum / float64(len(hs))
			med := hs[len(hs)/2]
			if len(hs)%2 == 0 {
				med = (hs[len(hs)/2-1] + hs[len(hs)/2]) / 2
			}
			g.AverageHours, g.MedianHours = &avg, &med
		}
	}
	for _, sv := range services {
		m.Services = append(m.Services, *sv)
	}
	sort.Slice(m.Services, func(i, j int) bool {
		if m.Services[i].Requests != m.Services[j].Requests {
			return m.Services[i].Requests > m.Services[j].Requests
		}
		return m.Services[i].Service < m.Services[j].Service
	})
	return m
}

// CSV is the metrics as rows of metric, standard, expedited (drugs last, apart, as CMS counts them): the shape of CMS's reporting template, ready to publish.
func (m Metrics) CSV() string {
	var b strings.Builder
	num := func(v *float64, digits int) string {
		if v == nil {
			return "n/a"
		}
		return fmt.Sprintf("%.*f", digits, *v)
	}
	row := func(name, s, e string) { fmt.Fprintf(&b, "%q,%s,%s\n", name, s, e) }
	row("Prior authorization metrics "+m.From+" to "+m.To, "standard", "expedited")
	row("Requests", fmt.Sprint(m.Standard.Requests), fmt.Sprint(m.Expedited.Requests))
	row("Approved (count)", fmt.Sprint(m.Standard.Approved), fmt.Sprint(m.Expedited.Approved))
	row("Approved (percent of determined)", num(m.Standard.ApprovedPercent, 1), num(m.Expedited.ApprovedPercent, 1))
	row("Denied (count)", fmt.Sprint(m.Standard.Denied), fmt.Sprint(m.Expedited.Denied))
	row("Denied (percent of determined)", num(m.Standard.DeniedPercent, 1), num(m.Expedited.DeniedPercent, 1))
	row("Awaiting determination", fmt.Sprint(m.Standard.Pending), fmt.Sprint(m.Expedited.Pending))
	row("Average hours to determination", num(m.Standard.AverageHours, 2), num(m.Expedited.AverageHours, 2))
	row("Median hours to determination", num(m.Standard.MedianHours, 2), num(m.Expedited.MedianHours, 2))
	row("Determined as received", fmt.Sprint(m.Standard.RealTime), fmt.Sprint(m.Expedited.RealTime))
	row("Approved under a gold card exemption", fmt.Sprint(m.Standard.GoldCard), fmt.Sprint(m.Expedited.GoldCard))
	row("Denials appealed (outcome recorded)", fmt.Sprint(m.Standard.Appealed), fmt.Sprint(m.Expedited.Appealed))
	row("Approved after appeal (count)", fmt.Sprint(m.Standard.ApprovedAfterAppeal), fmt.Sprint(m.Expedited.ApprovedAfterAppeal))
	row("Approved after appeal (percent of appealed)", num(m.Standard.ApprovedAfterAppealPercent, 1),
		num(m.Expedited.ApprovedAfterAppealPercent, 1))
	row("Timeframe extended", fmt.Sprint(m.Standard.Extended), fmt.Sprint(m.Expedited.Extended))
	row("Approved after an extension (count)", fmt.Sprint(m.Standard.ApprovedAfterExtension), fmt.Sprint(m.Expedited.ApprovedAfterExtension))
	row("Approved after an extension (percent of determined)", num(m.Standard.ApprovedAfterExtensionPercent, 1),
		num(m.Expedited.ApprovedAfterExtensionPercent, 1))
	row("Determined late, or waiting past the deadline", fmt.Sprint(m.Standard.Late), fmt.Sprint(m.Expedited.Late))
	for _, n := range m.NotTracked {
		row(n, "not tracked", "not tracked")
	}
	row("Drugs: requests", fmt.Sprint(m.DrugStandard.Requests), fmt.Sprint(m.DrugExpedited.Requests))
	row("Drugs: approved (count)", fmt.Sprint(m.DrugStandard.Approved), fmt.Sprint(m.DrugExpedited.Approved))
	row("Drugs: denied (count)", fmt.Sprint(m.DrugStandard.Denied), fmt.Sprint(m.DrugExpedited.Denied))
	row("Drugs: average hours to determination", num(m.DrugStandard.AverageHours, 2), num(m.DrugExpedited.AverageHours, 2))
	return b.String()
}

// Cause is one piece of evidence and how many pended requests lacked it.
type Cause struct {
	Policy   string `json:"policy"`
	Argument string `json:"argument"`
	Text     string `json:"text"`
	Question string `json:"question,omitempty"` // the questionnaire linkId that asks for it, when one does
	Pends    int    `json:"pends"`
	// Percent of the policy's requests pended for information that lacked it.
	Percent float64 `json:"percent"`
}

// Pends is why requests were pended in a period.
type Pends struct {
	From string `json:"from"`
	To   string `json:"to"`
	// ForInformation counts requests pended because evidence was missing, per policy.
	ForInformation map[string]int `json:"forInformation"`
	Missing        []Cause        `json:"missing"`
	Contradicted   []Cause        `json:"contradicted"` // evidence the record both asserted and denied
}

// BuildPends counts the evidence missing from, or contradicted in, the requests the engine pended in the period.
// policies, when given, names the question that asks for each piece of evidence.
func BuildPends(cs []cases.Case, p Period, policies map[string]*policy.Policy) Pends {
	out := Pends{From: p.From.Format(time.DateOnly), To: p.To.Format(time.DateOnly), ForInformation: map[string]int{},
		Missing: []Cause{}, Contradicted: []Cause{}}
	missing, contradicted := map[string]*Cause{}, map[string]*Cause{}
	count := func(into map[string]*Cause, c *cases.Case, it decide.Item) {
		key := c.Policy + "/" + it.Argument
		if into[key] == nil {
			into[key] = &Cause{Policy: c.Policy, Argument: it.Argument, Text: it.Text, Question: question(policies, c.Policy, it.Argument)}
		}
		into[key].Pends++
	}
	contradictedPends := map[string]int{}
	for i := range cs {
		c := &cs[i]
		if c.Result == nil || !p.Contains(created(c)) || c.Result.Outcome != decide.Pended {
			continue
		}
		switch c.Result.Reason {
		case decide.NeedsInformation:
			out.ForInformation[c.Policy]++
			for _, it := range c.Result.Missing {
				count(missing, c, it)
			}
		case decide.ContradictoryRecord:
			contradictedPends[c.Policy]++
			for _, it := range c.Result.Contradicted {
				count(contradicted, c, it)
			}
		}
	}
	finish := func(from map[string]*Cause, of map[string]int) []Cause {
		list := []Cause{}
		for _, c := range from {
			if n := of[c.Policy]; n > 0 {
				c.Percent = 100 * float64(c.Pends) / float64(n)
			}
			list = append(list, *c)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Pends != list[j].Pends {
				return list[i].Pends > list[j].Pends
			}
			return list[i].Policy+list[i].Argument < list[j].Policy+list[j].Argument
		})
		return list
	}
	out.Missing = finish(missing, out.ForInformation)
	out.Contradicted = finish(contradicted, contradictedPends)
	return out
}

func question(policies map[string]*policy.Policy, id, arg string) string {
	p := policies[id]
	if p == nil {
		return ""
	}
	a, ok := p.Argument(arg)
	if !ok {
		return ""
	}
	for _, s := range a.Find {
		if s.Question != "" {
			return s.Question
		}
	}
	return ""
}

// Overrides is how often the engine's denials did not stand, grouped one way.
type Overrides struct {
	From string        `json:"from"`
	To   string        `json:"to"`
	By   string        `json:"by"` // clause, provider or state
	Rows []OverrideRow `json:"rows"`
}

// OverrideRow is one clause, provider or state.
type OverrideRow struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Policy string `json:"policy,omitempty"` // by clause: the policy whose decision it is
	// Denials counts the engine's denials, issued or (where only clinicians deny) proposed.
	Denials int `json:"denials"`
	// Overridden counts proposed denials a clinician approved instead.
	Overridden int `json:"overridden"`
	// Appealed counts denials whose appeal outcome is recorded; Overturned those approved on appeal.
	Appealed   int `json:"appealed"`
	Overturned int `json:"overturned"`
	// ReversalPercent is overridden plus overturned, as a percent of denials.
	ReversalPercent float64  `json:"reversalPercent"`
	Cases           []string `json:"cases"` // the reversed ones, to read
}

// ByKinds are the ways overrides group.
var ByKinds = []string{"clause", "provider", "state"}

// BuildOverrides counts, over the cases created in the period, the engine's denials and how often a clinician or an
// appeal reversed them. A clause most often reversed is the first place to look for a policy that is wrong.
func BuildOverrides(cs []cases.Case, p Period, by string, policies map[string]*policy.Policy) (Overrides, error) {
	if !contains(ByKinds, by) {
		return Overrides{}, fmt.Errorf("by is clause, provider or state, not %q", by)
	}
	out := Overrides{From: p.From.Format(time.DateOnly), To: p.To.Format(time.DateOnly), By: by, Rows: []OverrideRow{}}
	rows := map[string]*OverrideRow{}
	for i := range cs {
		c := &cs[i]
		if c.Result == nil || c.Status == cases.Superseded || !p.Contains(created(c)) {
			continue
		}
		engineDenied := c.Result.Outcome == decide.Denied || c.Proposed != nil
		if !engineDenied {
			continue
		}
		var key, label, pol string
		switch by {
		case "clause":
			key, pol = c.Policy+"/"+c.Result.Decision, c.Policy
			label = c.Result.Decision
			if pp := policies[c.Policy]; pp != nil {
				if a, ok := pp.Argument(c.Result.Decision); ok {
					label = a.Text
				}
			}
		case "provider":
			key, label = c.ProviderNPI, "NPI "+c.ProviderNPI
			if key == "" {
				key, label = "", "no NPI in the request"
			}
		case "state":
			key, label = c.PatientState, c.PatientState
			if key == "" {
				label = "no state in the patient's address"
			}
		}
		r := rows[key]
		if r == nil {
			r = &OverrideRow{Key: key, Label: label, Policy: pol, Cases: []string{}}
			rows[key] = r
		}
		r.Denials++
		reversed := false
		if c.Proposed != nil && c.Final() == "approve" {
			r.Overridden++
			reversed = true
		}
		if c.Appeal != nil {
			r.Appealed++
			if c.Appeal.Outcome == "overturned" {
				r.Overturned++
				reversed = true
			}
		}
		if reversed {
			r.Cases = append(r.Cases, c.ID)
		}
	}
	for _, r := range rows {
		r.ReversalPercent = 100 * float64(r.Overridden+r.Overturned) / float64(r.Denials)
		out.Rows = append(out.Rows, *r)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Overridden+a.Overturned != b.Overridden+b.Overturned {
			return a.Overridden+a.Overturned > b.Overridden+b.Overturned
		}
		if a.Denials != b.Denials {
			return a.Denials > b.Denials
		}
		return a.Key < b.Key
	})
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
