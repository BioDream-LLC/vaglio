package decide

import (
	"fmt"
	"sort"
	"strings"

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
	// Incomplete, for a policy past 16 pieces of evidence, is why its check could not finish (search.go): the
	// policy then fails its check.
	Incomplete string `json:"incomplete,omitempty"`
}

// OK is whether the policy can be put in front of a request.
func (r *Report) OK() bool {
	return len(r.Defects) == 0 && len(r.Unreachable) == 0 && r.Incomplete == ""
}

// Check solves the policy on every combination of evidence present: a policy is not trusted on the strength of the
// cases its author thought of. It fails a policy that contradicts itself or rejects everything on some evidence, or
// that declares a decision that never holds.
func Check(p *policy.Policy) *Report {
	ev := p.EvidenceNames()
	total := 1 << uint(len(ev)) // policy.Validate bounds len(ev)
	r := &Report{Policy: p.ID, Combinations: total, Outcomes: map[string]int{}, Reached: map[string]int{}}
	if len(ev) > oneByOne {
		checkSearch(p, r)
		return unreachable(p, r)
	}
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
	return unreachable(p, r)
}

// unreachable fills in the decisions no combination reaches.
func unreachable(p *policy.Policy, r *Report) *Report {
	if r.Incomplete != "" {
		return r
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

// Map is every combination of up to MaxMapEvidence pieces of a policy's evidence and its outcome on a fully documented
// record: what the console's policy map draws. Combination m has evidence i present when bit i of m is set, in the
// order of Evidence. A policy with more evidence is mapped a slice at a time: Held is the evidence not on the grid,
// each held present (HeldPresent) or absent throughout.
type Map struct {
	Policy   string   `json:"policy"`
	Evidence []string `json:"evidence"`
	// Outcomes holds one letter per combination, in order: a approved, d denied, p pended, x policy defect.
	Outcomes string `json:"outcomes"`
	// Decisions, per combination, the index in Clauses of the decision the outcome rests on, -1 for none.
	Decisions   []int    `json:"decisions"`
	Clauses     []string `json:"clauses"` // decision names, as Decisions indexes them
	Held        []string `json:"held"`
	HeldPresent []string `json:"heldPresent"`
}

// MaxMapEvidence bounds the grid: 2^16 combinations is 64 KB of letters.
const MaxMapEvidence = 16

// DecisionMap decides every combination of a policy's evidence, or for a policy with more than MaxMapEvidence, every
// combination of the default slice (MapAxes), the rest held absent.
func DecisionMap(p *policy.Policy) (*Map, error) {
	return DecisionMapSlice(p, MapAxes(p), nil)
}

// MapAxes is the evidence a map shows by default: all of it when it fits, else MaxMapEvidence pieces taken in turn
// from each family of interchangeable evidence (pieces that defeat the same arguments), so that a long list of
// alternatives does not fill the grid on its own.
func MapAxes(p *policy.Policy) []string {
	ev := p.EvidenceNames()
	if len(ev) <= MaxMapEvidence {
		return ev
	}
	targets := map[string][]string{}
	for _, x := range p.Attacks {
		targets[x.From] = append(targets[x.From], x.To)
	}
	var keys []string
	family := map[string][]string{}
	for _, n := range ev {
		t := append([]string(nil), targets[n]...)
		sort.Strings(t)
		k := strings.Join(t, "\x00")
		if family[k] == nil {
			keys = append(keys, k)
		}
		family[k] = append(family[k], n)
	}
	chosen := map[string]bool{}
	for len(chosen) < MaxMapEvidence {
		for _, k := range keys {
			if len(chosen) < MaxMapEvidence && len(family[k]) > 0 {
				chosen[family[k][0]] = true
				family[k] = family[k][1:]
			}
		}
	}
	var axes []string
	for _, n := range ev {
		if chosen[n] {
			axes = append(axes, n)
		}
	}
	return axes
}

// DecisionMapSlice decides every combination of the evidence named in axes (at most MaxMapEvidence, in the policy's
// order), every other piece held present when named in present and absent otherwise.
func DecisionMapSlice(p *policy.Policy, axes, present []string) (*Map, error) {
	isEv := map[string]bool{}
	for _, n := range p.EvidenceNames() {
		isEv[n] = true
	}
	onGrid := map[string]bool{}
	for _, n := range axes {
		if !isEv[n] {
			return nil, fmt.Errorf("decide: %q is not evidence of policy %s", n, p.ID)
		}
		onGrid[n] = true
	}
	if len(onGrid) > MaxMapEvidence {
		return nil, fmt.Errorf("decide: at most %d pieces of evidence fit on the map at once, not %d", MaxMapEvidence, len(onGrid))
	}
	held := map[string]bool{}
	for _, n := range present {
		if !isEv[n] || onGrid[n] {
			return nil, fmt.Errorf("decide: %q is not evidence of policy %s held off the map", n, p.ID)
		}
		held[n] = true
	}
	m := &Map{Policy: p.ID, Clauses: []string{}, Held: []string{}, HeldPresent: []string{}}
	for _, n := range p.EvidenceNames() {
		switch {
		case onGrid[n]:
			m.Evidence = append(m.Evidence, n)
		case held[n]:
			m.Held = append(m.Held, n)
			m.HeldPresent = append(m.HeldPresent, n)
		default:
			m.Held = append(m.Held, n)
		}
	}
	ev := m.Evidence
	total := 1 << uint(len(ev))
	m.Decisions = make([]int, total)
	index := map[string]int{}
	letters := make([]byte, total)
	for mask := 0; mask < total; mask++ {
		with := map[string]*Citation{}
		for n := range held {
			with[n] = &Citation{}
		}
		for i, name := range ev {
			if mask&(1<<uint(i)) != 0 {
				with[name] = &Citation{}
			}
		}
		s := solve(p, with)
		switch s.outcome {
		case Approved:
			letters[mask] = 'a'
		case Denied:
			letters[mask] = 'd'
		case Pended:
			letters[mask] = 'p'
		default:
			letters[mask] = 'x'
		}
		m.Decisions[mask] = -1
		if s.decision != "" {
			i, ok := index[s.decision]
			if !ok {
				i = len(m.Clauses)
				index[s.decision] = i
				m.Clauses = append(m.Clauses, s.decision)
			}
			m.Decisions[mask] = i
		}
	}
	m.Outcomes = string(letters)
	return m, nil
}
