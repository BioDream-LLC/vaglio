package decide

// Searching large policies. A policy of up to 16 pieces of evidence is checked by solving it on every combination of
// its evidence, one by one (Check, overturning and toApprove in their first form). Past that, one by one is too slow
// (2^24 solves is about a minute), so these search instead, with the same results:
//
//   - Check walks the evidence one piece at a time, present or absent, and after each step works out which arguments
//     are already settled whatever the rest turns out to be (an argument is in when every attacker is out, out when an
//     attacker is in: the rule grounded semantics labels by, applied only to what is already certain). Once every
//     decision is settled, the outcome is the same for every way the rest could go, and all those combinations are
//     counted at once. A list of alternatives (any one of ten findings) settles as soon as one is present, so most of
//     the 2^n combinations are never visited. The counts are exact; only which defective combinations are shown as
//     examples can differ from the one-by-one walk.
//   - The searches for what would change an outcome (overturning, toApprove) try sets of evidence in order of size, as
//     before, but by listing the sets of each size directly, and within a budget of solves. Past the budget,
//     overturning says it could not finish, and the decision is then taken cautiously (Decide).
//
// A policy whose check would need more than checkBudget settled branches is too tangled to check exactly, and fails
// its check saying so: the limit is how intricate the policy is, not how much evidence it names.

import (
	"fmt"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// oneByOne is the most evidence checked by solving every combination in turn.
const oneByOne = 16

// checkBudget bounds the branches Check settles for a large policy.
const checkBudget = 1 << 22

// searchBudget bounds the solves one search for overturning or approving sets may make.
const searchBudget = 50_000

const (
	unknown int8 = iota
	in
	out
)

// graph is a policy's arguments and attacks as indices, for the searches.
type graph struct {
	p         *policy.Policy
	attackers [][]int
	isEv      []bool
	evIndex   []int // evidence, in EvidenceNames order, as argument indices
	decisions []int // decision arguments, in argument order
	approve   []bool
}

func newGraph(p *policy.Policy) *graph {
	g := &graph{p: p, attackers: make([][]int, len(p.Arguments)), isEv: make([]bool, len(p.Arguments)),
		approve: make([]bool, len(p.Arguments))}
	at := map[string]int{}
	for i, a := range p.Arguments {
		at[a.Name] = i
		switch a.Kind {
		case policy.Evidence:
			g.isEv[i] = true
		case policy.Decision:
			g.decisions = append(g.decisions, i)
			g.approve[i] = a.Verdict == policy.Approve
		}
	}
	for _, x := range p.Attacks {
		from, ok1 := at[x.From]
		to, ok2 := at[x.To]
		if ok1 && ok2 {
			g.attackers[to] = append(g.attackers[to], from)
		}
	}
	for _, n := range p.EvidenceNames() {
		g.evIndex = append(g.evIndex, at[n])
	}
	return g
}

// settle labels what is certain given each evidence argument's state (unknown, in for present, out for absent):
// in when every attacker is out, out when some attacker is in. With every evidence state known it is the grounded
// labelling, unknown standing for undecided.
func (g *graph) settle(state []int8, lab []int8) {
	for i := range lab {
		lab[i] = unknown
		if g.isEv[i] && state[i] == out {
			lab[i] = out // absent: not an argument at all, so it attacks nothing
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range lab {
			if lab[i] != unknown {
				continue
			}
			attackedIn, allOut := false, true
			for _, a := range g.attackers[i] {
				switch lab[a] {
				case in:
					attackedIn = true
				case unknown:
					allOut = false
				}
				if attackedIn {
					break
				}
			}
			switch {
			case attackedIn:
				lab[i], changed = out, true // absent or defeated: either way it attacks nothing
			case allOut && !(g.isEv[i] && state[i] == unknown):
				lab[i], changed = in, true
			}
		}
	}
}

// outcomeOf is solve's outcome from the decisions' labels, unknown standing for undecided.
func (g *graph) outcomeOf(lab []int8) (Outcome, Reason) {
	var approve, deny, undecided bool
	for _, d := range g.decisions {
		switch lab[d] {
		case in:
			if g.approve[d] {
				approve = true
			} else {
				deny = true
			}
		case unknown:
			undecided = true
		}
	}
	switch {
	case approve && deny:
		return PolicyDefect, Conflicted
	case approve:
		return Approved, Decided
	case deny:
		return Denied, Decided
	case undecided:
		return Pended, Indeterminate
	}
	return PolicyDefect, Unsatisfied
}

// checkSearch is Check for a policy past oneByOne pieces of evidence.
func checkSearch(p *policy.Policy, r *Report) {
	g := newGraph(p)
	targets := make([][]int, len(p.Arguments)) // what each argument attacks
	for to, as := range g.attackers {
		for _, from := range as {
			targets[from] = append(targets[from], to)
		}
	}
	state := make([]int8, len(p.Arguments))
	branches := 0
	var walk func()
	walk = func() {
		if r.Incomplete != "" {
			return
		}
		branches++
		if branches > checkBudget {
			r.Incomplete = "the policy is too intricate to check every combination of its evidence exactly"
			return
		}
		lab := make([]int8, len(p.Arguments))
		g.settle(state, lab)
		settled := true
		for _, d := range g.decisions {
			if lab[d] == unknown {
				settled = false
				break
			}
		}
		// The next piece to try both ways: one not yet given whose attacks still reach something unsettled. A piece
		// whose every target is settled cannot change anything, however it turns out.
		next, open := -1, 0
		for _, i := range g.evIndex {
			if state[i] != unknown {
				continue
			}
			open++
			if next >= 0 || settled {
				continue
			}
			for _, t := range targets[i] {
				if lab[t] == unknown {
					next = i
					break
				}
			}
		}
		if next >= 0 {
			state[next] = in
			walk()
			state[next] = out
			walk()
			state[next] = unknown
			return
		}
		// Nothing left that matters: the same outcome for each of the 2^open ways the rest could go.
		if !settled {
			// Undecided decisions with nothing left to try: give the rest as absent, so settle labels by grounded
			// semantics proper (what is still unknown is undecided).
			for _, i := range g.evIndex {
				if state[i] == unknown {
					state[i] = out
					defer func(i int) { state[i] = unknown }(i)
				}
			}
			g.settle(state, lab)
		}
		ways := 1 << uint(open)
		o, why := g.outcomeOf(lab)
		r.Outcomes[string(o)+"/"+string(why)] += ways
		for _, d := range g.decisions {
			if lab[d] == in {
				r.Reached[p.Arguments[d].Name] += ways
			}
		}
		if o == PolicyDefect && len(r.Defects) < 10 {
			set := []string{}
			for _, i := range g.evIndex {
				if state[i] == in {
					set = append(set, p.Arguments[i].Name)
				}
			}
			r.Defects = append(r.Defects, set)
		}
	}
	walk()
}

// minimalSets is the minimal sets of open evidence for which want holds once they are added to present, smallest
// first, found within searchBudget solves; complete says whether the search finished. A piece that is a set on its
// own is left out of the larger sets (any set holding it is not minimal), which keeps a long list of alternatives,
// each enough alone, from multiplying the search.
func minimalSets(p *policy.Policy, present map[string]*Citation, open []string, want func(Outcome) bool) (sets [][]string, complete bool) {
	var found [][]int // indices into open, ascending
	covered := func(idx []int) bool {
		for _, f := range found {
			j := 0
			for _, x := range idx {
				if j < len(f) && f[j] == x {
					j++
				}
			}
			if j == len(f) {
				return true
			}
		}
		return false
	}
	solves := 0
	alone := map[int]bool{}
	matters := relevant(p, present, open)
	for size := 1; size <= len(open); size++ {
		var universe []int
		for i := range open {
			if matters[i] && !alone[i] {
				universe = append(universe, i)
			}
		}
		if size > len(universe) {
			break
		}
		// every combination of size from universe, in order
		pick := make([]int, size)
		for i := range pick {
			pick[i] = i
		}
		idx := make([]int, size)
		for {
			for i, k := range pick {
				idx[i] = universe[k]
			}
			if !covered(idx) {
				solves++
				if solves > searchBudget {
					return toNames(open, found), false
				}
				with := make(map[string]*Citation, len(present)+size)
				for k, v := range present {
					with[k] = v
				}
				for _, i := range idx {
					with[open[i]] = &Citation{}
				}
				if want(solve(p, with).outcome) {
					found = append(found, append([]int(nil), idx...))
					if size == 1 {
						alone[idx[0]] = true
					}
				}
			}
			i := size - 1
			for i >= 0 && pick[i] == len(universe)-size+i {
				i--
			}
			if i < 0 {
				break
			}
			pick[i]++
			for j := i + 1; j < size; j++ {
				pick[j] = pick[j-1] + 1
			}
		}
	}
	return toNames(open, found), true
}

func toNames(open []string, found [][]int) [][]string {
	var sets [][]string
	for _, f := range found {
		var s []string
		for _, i := range f {
			s = append(s, open[i])
		}
		sets = append(sets, s)
	}
	return sets
}

// relevant marks the pieces of open evidence that could change any decision: with what is present given and the
// open pieces unknown, a piece matters only if its attacks reach an undecided decision through arguments that are
// not already settled. Settled arguments keep their labels whichever open pieces are added, so a piece that reaches
// a decision only through them changes nothing, alone or with others.
func relevant(p *policy.Policy, present map[string]*Citation, open []string) []bool {
	g := newGraph(p)
	state := make([]int8, len(p.Arguments))
	at := map[string]int{}
	for i, a := range p.Arguments {
		at[a.Name] = i
		if a.Kind == policy.Evidence && present[a.Name] != nil {
			state[i] = in
		}
	}
	lab := make([]int8, len(p.Arguments))
	g.settle(state, lab)
	// Backwards from the unsettled decisions, through unsettled arguments.
	reach := make([]bool, len(p.Arguments))
	var stack []int
	for _, d := range g.decisions {
		if lab[d] == unknown {
			reach[d] = true
			stack = append(stack, d)
		}
	}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, a := range g.attackers[x] {
			if !reach[a] && lab[a] == unknown {
				reach[a] = true
				stack = append(stack, a)
			}
		}
	}
	out := make([]bool, len(open))
	for k, n := range open {
		out[k] = reach[at[n]]
	}
	return out
}

// Family is a set of records several policies all decide the same way: every combination of the slots in Present
// documented present, the slots in Absent documented absent, and the Free other slots either way (2^Free records).
type Family struct {
	Present, Absent []int
	Free            int
	Outcomes        []Outcome // per policy, as Settle gives it
	Decisions       []string  // per policy, the decision the outcome rests on ("" for none)
}

// Families walks every combination of shared slots of evidence across several policies, as Check walks one policy's:
// slots[i][k] names the evidence of policy k that slot i stands for ("" when policy k has none there). It branches
// only on slots that can still change a decision not yet settled in some policy, and calls each once per family of
// records decided alike, so a list of alternatives is not multiplied out. It gives up, with an error, past budget
// branches. Within one family, every policy's outcome is what Settle gives on any of its records.
func Families(ps []*policy.Policy, slots [][]string, budget int, each func(Family)) error {
	type one struct {
		g       *graph
		at      []int   // per slot, the argument index in this policy, -1 for none
		targets [][]int // per argument, what it attacks
		state   []int8
	}
	var all []*one
	for k, p := range ps {
		o := &one{g: newGraph(p), at: make([]int, len(slots)), targets: make([][]int, len(p.Arguments)),
			state: make([]int8, len(p.Arguments))}
		idx := map[string]int{}
		for i, a := range p.Arguments {
			idx[a.Name] = i
		}
		for i, s := range slots {
			o.at[i] = -1
			if s[k] != "" {
				j, ok := idx[s[k]]
				if !ok || !o.g.isEv[j] {
					return fmt.Errorf("decide: %q is not evidence of policy %s", s[k], p.ID)
				}
				o.at[i] = j
			}
		}
		// Evidence of this policy in no slot is documented absent throughout.
		inSlot := map[int]bool{}
		for _, j := range o.at {
			inSlot[j] = true
		}
		for _, j := range o.g.evIndex {
			if !inSlot[j] {
				o.state[j] = out
			}
		}
		for to, as := range o.g.attackers {
			for _, from := range as {
				o.targets[from] = append(o.targets[from], to)
			}
		}
		all = append(all, o)
	}
	given := make([]int8, len(slots))
	branches := 0
	var failed error
	var walk func()
	walk = func() {
		if failed != nil {
			return
		}
		branches++
		if branches > budget {
			failed = fmt.Errorf("decide: too intricate to compare every combination of %d pieces of evidence exactly", len(slots))
			return
		}
		labs := make([][]int8, len(all))
		settled := make([]bool, len(all))
		for k, o := range all {
			labs[k] = make([]int8, len(o.state))
			o.g.settle(o.state, labs[k])
			settled[k] = true
			for _, d := range o.g.decisions {
				if labs[k][d] == unknown {
					settled[k] = false
					break
				}
			}
		}
		next, free := -1, 0
		for i := range slots {
			if given[i] != unknown {
				continue
			}
			free++
			if next >= 0 {
				continue
			}
			for k, o := range all {
				j := o.at[i]
				if settled[k] || j < 0 {
					continue
				}
				for _, t := range o.targets[j] {
					if labs[k][t] == unknown {
						next = i
						break
					}
				}
				if next >= 0 {
					break
				}
			}
		}
		set := func(i int, v int8) {
			given[i] = v
			for _, o := range all {
				if o.at[i] >= 0 {
					o.state[o.at[i]] = v
				}
			}
		}
		if next >= 0 {
			set(next, in)
			walk()
			set(next, out)
			walk()
			set(next, unknown)
			return
		}
		// Nothing left changes any decision: label each unsettled policy with the free slots absent, which is its
		// grounded labelling on every record of the family.
		var freed []int
		for i := range slots {
			if given[i] == unknown {
				freed = append(freed, i)
				set(i, out)
			}
		}
		f := Family{Free: free}
		for i := range slots {
			if given[i] == in {
				f.Present = append(f.Present, i)
			}
		}
		for k, o := range all {
			if !settled[k] {
				o.g.settle(o.state, labs[k])
			}
			got, _ := o.g.outcomeOf(labs[k])
			dec := ""
			if got == Approved || got == Denied {
				for _, d := range o.g.decisions {
					if labs[k][d] == in && o.g.approve[d] == (got == Approved) {
						dec = ps[k].Arguments[d].Name
						break
					}
				}
			}
			f.Outcomes = append(f.Outcomes, got)
			f.Decisions = append(f.Decisions, dec)
		}
		for _, i := range freed {
			set(i, unknown)
		}
		for i := range slots {
			if given[i] == out {
				f.Absent = append(f.Absent, i)
			}
		}
		each(f)
	}
	walk()
	return failed
}
