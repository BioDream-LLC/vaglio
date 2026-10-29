// Package decide turns a policy and the evidence in a request into a decision with its proof.
//
// The rule that makes this product what it is lives here, in one place: **a denial may not rest on absence of
// evidence, only on evidence of absence.**
//
// Every piece of evidence a policy declares is, in a request, one of three things: documented present, documented
// absent (the record says it is not so - a normal neurological exam, say - and cites where), or unaddressed. A denial is
// issued only if no combination of unaddressed evidence would overturn it. Otherwise the request is pended and exactly
// that evidence is asked for. An unaddressed question goes to the provider; it is never a reason to refuse care.
//
// The same test run on an approval lists what the approval assumed, and both outcomes list the documented absences they
// relied on, with their citations, so a reviewer sees what was shown, what was attested absent, and what was assumed.
package decide

import (
	"fmt"
	"math/bits"

	"github.com/biodream-llc/vaglio/internal/af"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Citation is where in the patient's record a piece of evidence is found.
type Citation struct {
	Reference string `json:"reference"` // a FHIR reference, e.g. DocumentReference/123
	Quote     string `json:"quote"`     // the words in that record that establish it
}

// Finding is what the record says about one piece of evidence.
type Finding string

const (
	Present Finding = "present"
	Absent  Finding = "absent" // documented absent: evidence of absence, with a citation like any other evidence
)

// Assertion is one piece of evidence a request puts forward.
type Assertion struct {
	Argument string  `json:"argument"`
	Finding  Finding `json:"finding"`
	Citation
}

// Request is a prior authorisation request reduced to what a policy can reason over.
type Request struct {
	Policy   string      `json:"policy"`
	Evidence []Assertion `json:"evidence"`
	// Contradicted is evidence the record documents both present and absent. It is treated as not addressed, and an
	// outcome that depends on it goes to a person: a contradiction is not the provider's question to answer by default,
	// nor the engine's to settle by picking a side.
	Contradicted []string `json:"contradicted,omitempty"`
}

// Outcome is the decision as a payer would report it.
type Outcome string

const (
	Approved     Outcome = "approved"
	Denied       Outcome = "denied"
	Pended       Outcome = "pended"        // a human, or more information, is needed
	PolicyDefect Outcome = "policy_defect" // the policy itself is wrong; no decision is issued on it
)

// Reason separates outcomes that look alike to a provider but go to different people:
// a pend for information goes to the provider, an indeterminate one to a clinician, a defect to the policy's authors.
type Reason string

const (
	Decided          Reason = "decided"
	NeedsInformation Reason = "needs_information" // a denial could be overturned by evidence nobody has addressed
	Indeterminate    Reason = "indeterminate"     // the policy cannot settle it on this evidence: clinical review
	Unsatisfied      Reason = "unsatisfied"       // every decision is rejected: the policy has a gap
	Conflicted       Reason = "conflicted"        // approve and deny both accepted: the policy contradicts itself
	// ContradictoryRecord: the outcome depends on evidence the record both asserts and denies. Clinical review.
	ContradictoryRecord Reason = "contradictory_record"
)

// Item is a piece of declared evidence named in a result, with its citation when the record addressed it.
type Item struct {
	Argument string    `json:"argument"`
	Text     string    `json:"text"`
	Clause   string    `json:"clause"`
	Citation *Citation `json:"citation,omitempty"`
}

// Node is one accepted argument in a proof, with everything argued against it and what defeated each.
type Node struct {
	Argument string    `json:"argument"`
	Kind     string    `json:"kind"`
	Text     string    `json:"text"`
	Clause   string    `json:"clause"`
	Citation *Citation `json:"citation,omitempty"` // evidence only
	Defeats  []Defeat  `json:"defeats,omitempty"`
}

// Defeat is an attacker of an accepted argument and the accepted argument that defeated it.
type Defeat struct {
	Attacker string `json:"attacker"`
	Text     string `json:"text"`
	By       *Node  `json:"by"`
}

// Result is a decision and everything needed to check it.
type Result struct {
	Policy        string  `json:"policy"`
	PolicyVersion string  `json:"policyVersion"`
	Outcome       Outcome `json:"outcome"`
	Reason        Reason  `json:"reason"`
	Decision      string  `json:"decision,omitempty"` // the decision argument the outcome rests on
	Proof         *Node   `json:"proof,omitempty"`
	Missing       []Item  `json:"missing,omitempty"`      // NeedsInformation: what to ask the provider about
	Assumptions   []Item  `json:"assumptions,omitempty"`  // Approved: unaddressed evidence the approval assumes absent
	Relied        []Item  `json:"relied,omitempty"`       // documented absences the outcome depends on
	Contradicted  []Item  `json:"contradicted,omitempty"` // ContradictoryRecord: the contradicted evidence it depends on
	// ToApprove, for an outcome that is not an approval, is each smallest set of evidence not documented present whose
	// documentation would make the policy approve: what a provider could send, or a reviewer look for. Empty, for a
	// denial, means no further evidence would approve it under this policy - only a record that is wrong could.
	ToApprove [][]Change        `json:"toApprove,omitempty"`
	Labels    map[string]string `json:"labels"` // every argument's status, for audit
	// Cautious, when set, is why the search for what could change this outcome did not finish (a policy past 16
	// pieces of evidence, search.go): every piece not documented was then treated as able to change it, so a denial
	// pends, asking for all of them.
	Cautious string `json:"cautious,omitempty"`
}

// Change is one piece of evidence a route to approval needs documented present.
type Change struct {
	Item
	// ContradictsRecord: the record documents it absent (Item.Citation says where), so this route needs that record to
	// be wrong, not merely more documentation.
	ContradictsRecord bool `json:"contradictsRecord,omitempty"`
}

// Decide applies a policy to a request.
func Decide(p *policy.Policy, req Request) (*Result, error) {
	if req.Policy != "" && req.Policy != p.ID {
		return nil, fmt.Errorf("decide: request is for policy %q, not %q", req.Policy, p.ID)
	}
	present := map[string]*Citation{}
	absent := map[string]*Citation{}
	for _, e := range req.Evidence {
		a, ok := p.Argument(e.Argument)
		switch {
		case !ok:
			return nil, fmt.Errorf("decide: evidence %q is not declared by policy %s", e.Argument, p.ID)
		case a.Kind != policy.Evidence:
			return nil, fmt.Errorf("decide: %q is a %s, and only evidence can be asserted", e.Argument, a.Kind)
		case e.Finding != Present && e.Finding != Absent:
			return nil, fmt.Errorf("decide: evidence %q must be found %q or %q, not %q", e.Argument, Present, Absent, e.Finding)
		case e.Reference == "" || e.Quote == "":
			return nil, fmt.Errorf("decide: evidence %q has no reference or no quote; evidence without provenance is not evidence", e.Argument)
		case present[e.Argument] != nil || absent[e.Argument] != nil:
			return nil, fmt.Errorf("decide: evidence %q asserted twice", e.Argument)
		}
		c := e.Citation
		if contains(req.Contradicted, e.Argument) {
			return nil, fmt.Errorf("decide: evidence %q is both asserted and listed as contradicted", e.Argument)
		}
		if e.Finding == Present {
			present[e.Argument] = &c
		} else {
			absent[e.Argument] = &c
		}
	}

	for _, n := range req.Contradicted {
		if a, ok := p.Argument(n); !ok || a.Kind != policy.Evidence {
			return nil, fmt.Errorf("decide: contradicted %q is not evidence declared by policy %s", n, p.ID)
		}
	}

	s := solve(p, present)
	res := &Result{Policy: p.ID, PolicyVersion: p.Version, Outcome: s.outcome, Reason: s.reason, Labels: s.labels()}
	defer func() {
		if res.Outcome != Approved && res.Outcome != PolicyDefect {
			res.ToApprove = toApprove(p, present, absent)
		}
	}()
	if s.decision == "" {
		return res, nil
	}
	res.Decision = s.decision
	res.Proof = s.node(s.decision)

	// Everything not documented present could, as far as the policy is concerned, be present. Find the smallest sets of
	// it that would change the outcome.
	var open []string
	for _, n := range p.EvidenceNames() {
		if present[n] == nil {
			open = append(open, n)
		}
	}
	minimal, complete := overturning(p, present, open, s.outcome)
	if !complete {
		// The search ran out of budget: any piece not documented might change the outcome, so each is treated as if
		// it could. A denial then pends for all of them; nothing is denied on a search that did not finish.
		res.Cautious = "the search for what could change this outcome did not finish; every piece of evidence not " +
			"documented is treated as able to change it"
		minimal = nil
		for _, n := range open {
			minimal = append(minimal, []string{n})
		}
	}

	var unaddressed, relied []string
	for _, set := range minimal {
		pure := true
		for _, n := range set {
			if absent[n] != nil {
				pure = false
			}
		}
		for _, n := range set {
			if absent[n] != nil {
				relied = appendOnce(relied, n)
			} else if pure {
				unaddressed = appendOnce(unaddressed, n)
			}
		}
	}
	res.Relied = items(p, relied, absent)
	switch s.outcome {
	case Denied:
		if len(unaddressed) > 0 {
			res.Outcome, res.Reason, res.Missing = Pended, NeedsInformation, items(p, unaddressed, nil)
		}
	case Approved:
		res.Assumptions = items(p, unaddressed, nil)
	}

	// Unaddressed evidence the outcome depends on, which the record in fact speaks to both ways.
	var depends []string
	for _, n := range unaddressed {
		if contains(req.Contradicted, n) {
			depends = append(depends, n)
		}
	}
	if len(depends) > 0 {
		res.Outcome, res.Reason, res.Contradicted = Pended, ContradictoryRecord, items(p, depends, nil)
		// The provider was not asked about what is contradicted (it is a person's to settle), so it is not "missing";
		// whatever else is unaddressed still is.
		var missing []Item
		for _, it := range res.Missing {
			if !contains(depends, it.Argument) {
				missing = append(missing, it)
			}
		}
		res.Missing = missing
	}
	return res, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// overturning returns the minimal subsets of open evidence whose addition changes the outcome, smallest first.
// policy.Validate bounds the evidence count, which bounds this enumeration.
func overturning(p *policy.Policy, present map[string]*Citation, open []string, was Outcome) ([][]string, bool) {
	if len(open) > oneByOne {
		return minimalSets(p, present, open, func(o Outcome) bool { return o != was })
	}
	n := len(open)
	var found []uint32
	for size := 1; size <= n; size++ {
		for m := uint32(1); m < 1<<uint(n); m++ {
			if bits.OnesCount32(m) != size {
				continue
			}
			covered := false
			for _, f := range found {
				if m&f == f {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			with := map[string]*Citation{}
			for k, v := range present {
				with[k] = v
			}
			for i, name := range open {
				if m&(1<<uint(i)) != 0 {
					with[name] = &Citation{}
				}
			}
			if solve(p, with).outcome != was {
				found = append(found, m)
			}
		}
	}
	out := make([][]string, len(found))
	for i, m := range found {
		for j, name := range open {
			if m&(1<<uint(j)) != 0 {
				out[i] = append(out[i], name)
			}
		}
	}
	return out, true
}

// maxRoutes bounds how many routes to approval a result lists: past a handful, a list is no longer advice.
const maxRoutes = 5

// toApprove finds the smallest sets of evidence not documented present that would make the policy approve, routes
// needing only undocumented evidence before routes that need the record to be wrong. Evidence documented present is never
// taken away: the question is what more would approve, not what if the record were otherwise.
func toApprove(p *policy.Policy, present, absent map[string]*Citation) [][]Change {
	var open []string
	for _, n := range p.EvidenceNames() {
		if present[n] == nil {
			open = append(open, n)
		}
	}
	n := len(open)
	var routes [][]string
	if n > oneByOne {
		routes, _ = minimalSets(p, present, open, func(o Outcome) bool { return o == Approved }) // advice: what it found
	} else {
		var found []uint32
		for size := 1; size <= n; size++ {
			for m := uint32(1); m < 1<<uint(n); m++ {
				if bits.OnesCount32(m) != size {
					continue
				}
				covered := false
				for _, f := range found {
					if m&f == f {
						covered = true
						break
					}
				}
				if covered {
					continue
				}
				with := map[string]*Citation{}
				for k, v := range present {
					with[k] = v
				}
				for i, name := range open {
					if m&(1<<uint(i)) != 0 {
						with[name] = &Citation{}
					}
				}
				if solve(p, with).outcome == Approved {
					found = append(found, m)
				}
			}
		}
		for _, m := range found {
			var r []string
			for i, name := range open {
				if m&(1<<uint(i)) != 0 {
					r = append(r, name)
				}
			}
			routes = append(routes, r)
		}
	}
	var clean, contradicting [][]Change
	for _, names := range routes {
		var route []Change
		wrong := false
		for _, name := range names {
			a, _ := p.Argument(name)
			c := Change{Item: Item{Argument: a.Name, Text: a.Text, Clause: a.Clause, Citation: absent[name]}}
			if absent[name] != nil {
				c.ContradictsRecord, wrong = true, true
			}
			route = append(route, c)
		}
		if wrong {
			contradicting = append(contradicting, route)
		} else {
			clean = append(clean, route)
		}
	}
	out := append(clean, contradicting...)
	if len(out) > maxRoutes {
		out = out[:maxRoutes]
	}
	return out
}

// solved is one grounded solve of a policy over the evidence documented present.
type solved struct {
	p        *policy.Policy
	f        *af.Framework
	lab      af.Labelling
	cites    map[string]*Citation
	outcome  Outcome
	reason   Reason
	decision string
}

func solve(p *policy.Policy, present map[string]*Citation) *solved {
	f := af.New()
	for _, a := range p.Arguments {
		if a.Kind == policy.Evidence && present[a.Name] == nil {
			continue
		}
		f.Add(a.Name) // names were validated unique when the policy was loaded
	}
	for _, at := range p.Attacks {
		from, ok1 := f.Lookup(at.From)
		to, ok2 := f.Lookup(at.To)
		if ok1 && ok2 {
			f.Attack(from, to)
		}
	}
	s := &solved{p: p, f: f, lab: af.Grounded(f), cites: present}

	var approve, deny []string
	undecided := false
	for _, a := range p.Arguments {
		if a.Kind != policy.Decision {
			continue
		}
		id, _ := f.Lookup(a.Name)
		switch s.lab.Label[id] {
		case af.In:
			if a.Verdict == policy.Approve {
				approve = append(approve, a.Name)
			} else {
				deny = append(deny, a.Name)
			}
		case af.Undec:
			undecided = true
		}
	}
	switch {
	case len(approve) > 0 && len(deny) > 0:
		s.outcome, s.reason = PolicyDefect, Conflicted
	case len(approve) > 0:
		s.outcome, s.reason, s.decision = Approved, Decided, approve[0]
	case len(deny) > 0:
		s.outcome, s.reason, s.decision = Denied, Decided, deny[0]
	case undecided:
		s.outcome, s.reason = Pended, Indeterminate
	default:
		s.outcome, s.reason = PolicyDefect, Unsatisfied
	}
	return s
}

func (s *solved) labels() map[string]string {
	out := map[string]string{}
	for _, a := range s.p.Arguments {
		if id, ok := s.f.Lookup(a.Name); ok {
			out[a.Name] = s.lab.Label[id].String()
		} else {
			out[a.Name] = "absent"
		}
	}
	return out
}

// node builds the proof of an In argument. It terminates because each defender was accepted in an earlier round.
func (s *solved) node(name string) *Node {
	a, _ := s.p.Argument(name)
	n := &Node{Argument: a.Name, Kind: string(a.Kind), Text: a.Text, Clause: a.Clause, Citation: s.cites[name]}
	id, _ := s.f.Lookup(name)
	for _, attacker := range s.f.Attackers(id) {
		src, _ := s.p.Argument(s.f.Name(attacker))
		def, ok := af.Defender(s.f, s.lab, attacker)
		if !ok {
			continue // cannot happen for an In argument; af's tests hold Grounded to that
		}
		n.Defeats = append(n.Defeats, Defeat{Attacker: src.Name, Text: src.Text, By: s.node(s.f.Name(def))})
	}
	return n
}

func items(p *policy.Policy, names []string, cites map[string]*Citation) []Item {
	var out []Item
	for _, n := range names {
		a, _ := p.Argument(n)
		out = append(out, Item{Argument: a.Name, Text: a.Text, Clause: a.Clause, Citation: cites[n]})
	}
	return out
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
