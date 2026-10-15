// Package precedence settles which policy decides a request when more than one could: a policy in effect on the day,
// for the request's line of business and state, of the highest authority. A national coverage determination outranks a
// local one or a state's criteria, which outrank the plan's own policy.
//
// Two policies of one rank may never both apply to one request; Conflict finds them before they are put in force, so
// the choice is never a tie. A request that does not say what a higher policy's scope turns on (the member's line of
// business, the patient's state) is not decided against that policy's reach: Choose reports the higher policy as
// unsettled, and a caller lets the lower one approve but not deny.
package precedence

import (
	"fmt"

	"github.com/biodream-llc/perfuse/pas"
	"slices"
	"sort"
	"strings"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// Request is what a request says about its scope. An empty field is one the request does not say.
type Request struct {
	Day   string // YYYY-MM-DD: the day it is decided
	Line  string // the member's line of business
	State string // the patient's state
}

// Aside is a policy for the service that does not decide this request, and why.
type Aside struct {
	Policy  string `json:"policy"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Why     string `json:"why"`
}

// Choice is the policy that decides a request, and the others for its service.
type Choice struct {
	Policy *policy.Policy `json:"-"`
	// Why says, in words, why it is the one: it is the only policy for the service, or it outranks the others.
	Why      string  `json:"why"`
	SetAside []Aside `json:"setAside,omitempty"`
	// Unsettled are higher policies the request may fall under: their scope turns on what the request does not say.
	Unsettled []Aside `json:"unsettled,omitempty"`
}

type fit int

const (
	no fit = iota
	unknown
	yes
)

// applies says whether p reaches the request, and when it does not, why.
func applies(p *policy.Policy, r Request) (fit, string) {
	if p.Effective != "" && r.Day < p.Effective {
		return no, "in effect from " + p.Effective
	}
	if p.Ends != "" && r.Day > p.Ends {
		return no, "ended " + p.Ends
	}
	f := yes
	var why []string
	if len(p.Lines) > 0 {
		switch {
		case r.Line == "":
			f, why = unknown, append(why, "applies to "+strings.Join(p.Lines, ", ")+", and the request does not say the member's line of business")
		case !slices.Contains(p.Lines, r.Line):
			return no, "applies to " + strings.Join(p.Lines, ", ") + ", not " + r.Line
		}
	}
	if len(p.States) > 0 {
		switch {
		case r.State == "":
			f, why = unknown, append(why, "applies in "+strings.Join(p.States, ", ")+", and the request does not say the patient's state")
		case !slices.Contains(p.States, r.State):
			return no, "applies in " + strings.Join(p.States, ", ") + ", not " + r.State
		}
	}
	return f, strings.Join(why, "; ")
}

func aside(p *policy.Policy, why string) Aside {
	return Aside{Policy: p.ID, Version: p.Version, Title: p.Title, Why: why}
}

// Choose picks the policy among candidates (every policy deciding the service) that decides r: the highest-ranked that
// applies. Choice.Policy is nil when none does.
func Choose(candidates []*policy.Policy, r Request) Choice {
	cs := slices.Clone(candidates)
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Rank() != cs[j].Rank() {
			return cs[i].Rank() > cs[j].Rank()
		}
		return cs[i].ID < cs[j].ID
	})
	var c Choice
	for _, p := range cs {
		f, why := applies(p, r)
		switch {
		case c.Policy != nil && f != no:
			c.SetAside = append(c.SetAside, aside(p, fmt.Sprintf("outranked by %s, the %s", c.Policy.Title, c.Policy.AuthorityName())))
		case f == no:
			c.SetAside = append(c.SetAside, aside(p, why))
		case f == unknown:
			c.Unsettled = append(c.Unsettled, aside(p, why))
		default:
			c.Policy = p
		}
	}
	switch {
	case c.Policy == nil:
	case len(cs) == 1:
		c.Why = "the only policy for this service"
	case len(c.Unsettled) > 0:
		c.Why = fmt.Sprintf("the %s for this service that applies to the request, below %d it may also fall under",
			c.Policy.AuthorityName(), len(c.Unsettled))
	default:
		c.Why = fmt.Sprintf("the %s for this service that applies to the request, and the highest-ranked one", c.Policy.AuthorityName())
	}
	return c
}

// Conflict says why a and b may not both be in force, or "": they decide a service in common, at one rank, and some
// request could fall under both (their days, lines and states overlap). Two versions of one policy never conflict: the
// later replaces the earlier.
func Conflict(a, b *policy.Policy) string {
	if a.ID == b.ID || a.Rank() != b.Rank() {
		return ""
	}
	var shared policy.Service
	found := false
	for _, x := range a.Services {
		if b.Decides(x.System, x.Code) {
			shared, found = x, true
			break
		}
	}
	if !found || !daysOverlap(a, b) || !overlap(a.Lines, b.Lines) || !overlap(a.States, b.States) {
		return ""
	}
	return fmt.Sprintf("%s %s is decided by policy %s as well, at the same rank (%s), for the same days, lines of business "+
		"and states: a request would fall under both. Give one a higher authority, or separate them by effective and "+
		"ends dates, lines or states", shared.System, shared.Code, b.ID, a.AuthorityName())
}

func daysOverlap(a, b *policy.Policy) bool {
	// An empty start is the beginning of time, an empty end the end of it.
	startsBefore := func(start, end string) bool { return start == "" || end == "" || start <= end }
	return startsBefore(a.Effective, b.Ends) && startsBefore(b.Effective, a.Ends)
}

// overlap is whether two scopes share a value; an empty list is every value.
func overlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// Conflicts checks a set of policies against each other, and is the first conflict, or "".
func Conflicts(ps []*policy.Policy) string {
	for i, a := range ps {
		for _, b := range ps[i+1:] {
			if why := Conflict(a, b); why != "" {
				return a.ID + ": " + why
			}
		}
	}
	return ""
}

// SOPTSystem is the Source of Payment Typology (PHDSC), the code system US Coverage.type uses for the payer's line.
const SOPTSystem = "https://nahdo.org/sopt"

// LineOfBusiness is the member's line of business, from the Coverage the Claim's focal insurance names: its type coded
// in the Source of Payment Typology (11x Medicare managed care, that is Medicare Advantage; other 1x Medicare; 2x
// Medicaid; 5x and 6x private insurance and Blue Cross Blue Shield, commercial). Nothing, when the request does not say.
func LineOfBusiness(req *pas.Request) string {
	var cov map[string]any
	for _, ins := range pas.Slice(req.Claim["insurance"]) {
		m := pas.Map(ins)
		res := pas.Map(req.Resolve(pas.Str(pas.Map(m["coverage"])["reference"]))["resource"])
		if res != nil && (cov == nil || m["focal"] == true) {
			cov = res
		}
	}
	for _, c := range pas.Slice(pas.Map(cov["type"])["coding"]) {
		cm := pas.Map(c)
		sys, code := pas.Str(cm["system"]), pas.Str(cm["code"])
		if sys != SOPTSystem && sys != "urn:oid:2.16.840.1.113883.3.221.5" {
			continue
		}
		switch {
		case strings.HasPrefix(code, "11"):
			return "medicare-advantage"
		case strings.HasPrefix(code, "1"):
			return "medicare"
		case strings.HasPrefix(code, "2"):
			return "medicaid"
		case strings.HasPrefix(code, "5"), strings.HasPrefix(code, "6"):
			return "commercial"
		}
	}
	return ""
}
