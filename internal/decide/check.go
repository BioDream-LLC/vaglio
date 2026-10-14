package decide

import (
	"sort"

	"github.com/biodream-llc/vaglio/internal/af"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Report is what Check found over every combination of evidence a policy can be given.
type Report struct {
	Policy       string         `json:"policy"`
	Combinations int            `json:"combinations"`
	Outcomes     map[string]int `json:"outcomes"`    // "outcome/reason" -> count, before the absence test
	Reached      map[string]int `json:"reached"`     // decision argument -> on how many combinations it holds
	Unreachable  []string       `json:"unreachable"` // decisions no combination of evidence ever reaches
	Defects      [][]string     `json:"defects"`     // up to 10 evidence sets on which the policy is defective
}

// OK is whether the policy can be put in front of a request.
func (r *Report) OK() bool { return len(r.Defects) == 0 && len(r.Unreachable) == 0 }

// Check solves the policy on every combination of evidence present, as cbc's validation pass does for a casebook: a
// policy is not trusted on the strength of the cases its author thought of. It fails a policy that contradicts itself
// or rejects everything on some evidence, or that declares a decision that never holds.
func Check(p *policy.Policy) *Report {
	ev := p.EvidenceNames()
	total := 1 << uint(len(ev)) // policy.Validate bounds len(ev)
	r := &Report{Policy: p.ID, Combinations: total, Outcomes: map[string]int{}, Reached: map[string]int{}}
	for mask := 0; mask < total; mask++ {
		present := map[string]*Citation{}
		var set []string
		for i, name := range ev {
			if mask&(1<<uint(i)) != 0 {
				present[name] = &Citation{}
				set = append(set, name)
			}
		}
		s := solve(p, present)
		r.Outcomes[string(s.outcome)+"/"+string(s.reason)]++
		// A decision is reached when it holds, whether or not it is the one the outcome names: with two denials holding,
		// the outcome cites the first, but the second is no less a reason, and is not dead code.
		for _, a := range p.Arguments {
			if a.Kind == policy.Decision {
				if id, ok := s.f.Lookup(a.Name); ok && s.lab.Label[id] == af.In {
					r.Reached[a.Name]++
				}
			}
		}
		if s.outcome == PolicyDefect && len(r.Defects) < 10 {
			if set == nil {
				set = []string{}
			}
			r.Defects = append(r.Defects, set)
		}
	}
	for _, a := range p.Arguments {
		if a.Kind == policy.Decision && r.Reached[a.Name] == 0 {
			r.Unreachable = append(r.Unreachable, a.Name)
		}
	}
	sort.Strings(r.Unreachable)
	return r
}

// Settle is the outcome on a fully documented record: the evidence named present, everything else documented absent,
// and the decision argument the outcome rests on ("" when none does).
func Settle(p *policy.Policy, present []string) (Outcome, string) {
	m := map[string]*Citation{}
	for _, n := range present {
		m[n] = &Citation{}
	}
	s := solve(p, m)
	return s.outcome, s.decision
}
