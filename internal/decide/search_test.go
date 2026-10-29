package decide

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// oneByOneCheck is Check by solving every combination, whatever the size.
func oneByOneCheck(p *policy.Policy) *Report {
	ev := p.EvidenceNames()
	r := &Report{Policy: p.ID, Combinations: 1 << uint(len(ev)), Outcomes: map[string]int{}, Reached: map[string]int{}}
	for mask := 0; mask < r.Combinations; mask++ {
		present := map[string]*Citation{}
		for i, name := range ev {
			if mask&(1<<uint(i)) != 0 {
				present[name] = &Citation{}
			}
		}
		s := solve(p, present)
		r.Outcomes[string(s.outcome)+"/"+string(s.reason)]++
		for _, a := range p.Arguments {
			if a.Kind == policy.Decision {
				if id, ok := s.f.Lookup(a.Name); ok && s.lab.Label[id] == 1 { // af.In
					r.Reached[a.Name]++
				}
			}
		}
		if s.outcome == PolicyDefect {
			r.Defects = append(r.Defects, nil)
		}
	}
	return r
}

func sameCounts(t *testing.T, name string, a, b *Report) {
	t.Helper()
	if !reflect.DeepEqual(a.Outcomes, b.Outcomes) || !reflect.DeepEqual(a.Reached, b.Reached) ||
		(len(a.Defects) > 0) != (len(b.Defects) > 0) {
		t.Errorf("%s: the search and the one-by-one walk differ\n  search   %v %v defects %d\n  one by one %v %v defects %d",
			name, a.Outcomes, a.Reached, len(a.Defects), b.Outcomes, b.Reached, len(b.Defects))
	}
}

// randomPolicy is a policy of n evidence, some criteria (with cycles among them, for undecided outcomes) and two to
// four decisions, attacked at random.
func randomPolicy(rng *rand.Rand, n int) *policy.Policy {
	p := &policy.Policy{ID: "r"}
	var names []string
	add := func(kind policy.Kind, v policy.Verdict) string {
		name := fmt.Sprintf("a%d", len(names))
		p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: kind, Verdict: v})
		names = append(names, name)
		return name
	}
	var ev, crit, dec []string
	for i := 0; i < n; i++ {
		ev = append(ev, add(policy.Evidence, ""))
	}
	for i := 0; i < 2+rng.Intn(5); i++ {
		crit = append(crit, add(policy.Criterion, ""))
	}
	for i := 0; i < 2+rng.Intn(3); i++ {
		v := policy.Approve
		if i%2 == 1 {
			v = policy.Deny
		}
		dec = append(dec, add(policy.Decision, v))
	}
	from := append(append([]string{}, ev...), crit...)
	to := append(append([]string{}, crit...), dec...)
	for i := 0; i < n+len(to)*2; i++ {
		p.Attacks = append(p.Attacks, policy.Attack{From: from[rng.Intn(len(from))], To: to[rng.Intn(len(to))]})
	}
	return p
}

// TestSearchMatchesOneByOne: on every library policy and on random ones, the search counts exactly what solving
// every combination counts.
func TestSearchMatchesOneByOne(t *testing.T) {
	files, _ := filepath.Glob("../../library/*.vaglio")
	for _, f := range files {
		p, err := policy.LoadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r := &Report{Policy: p.ID, Outcomes: map[string]int{}, Reached: map[string]int{}}
		checkSearch(p, r)
		sameCounts(t, f, r, oneByOneCheck(p))
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		p := randomPolicy(rng, 1+rng.Intn(12))
		r := &Report{Policy: p.ID, Outcomes: map[string]int{}, Reached: map[string]int{}}
		checkSearch(p, r)
		sameCounts(t, fmt.Sprintf("random %d", i), r, oneByOneCheck(p))
	}
}

// TestMinimalSetsMatch: the size-ordered search finds the same minimal sets as the mask walk.
func TestMinimalSetsMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	norm := func(ss [][]string) []string {
		var out []string
		for _, s := range ss {
			c := append([]string(nil), s...)
			sort.Strings(c)
			out = append(out, fmt.Sprint(c))
		}
		sort.Strings(out)
		return out
	}
	for i := 0; i < 300; i++ {
		p := randomPolicy(rng, 1+rng.Intn(10))
		ev := p.EvidenceNames()
		present := map[string]*Citation{}
		var open []string
		for _, n := range ev {
			if rng.Intn(3) == 0 {
				present[n] = &Citation{}
			} else {
				open = append(open, n)
			}
		}
		was := solve(p, present).outcome
		old, _ := overturning(p, present, open, was)
		got, complete := minimalSets(p, present, open, func(o Outcome) bool { return o != was })
		if !complete || !reflect.DeepEqual(norm(old), norm(got)) {
			t.Fatalf("random %d: %v against %v", i, got, old)
		}
	}
}

// TestLargePolicy: 34 pieces of evidence, 30 of them alternatives (any one of them meets the need), is checked
// exactly and fast, and decided with the absence test working: a denial with alternatives not addressed pends.
func TestLargePolicy(t *testing.T) {
	p := &policy.Policy{ID: "big"}
	ev := func(name string) {
		p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: policy.Evidence})
	}
	for i := 0; i < 30; i++ {
		ev(fmt.Sprintf("alt%d", i))
	}
	for _, n := range []string{"tried", "imaged", "contra", "smoker"} {
		ev(n)
	}
	p.Arguments = append(p.Arguments,
		policy.Argument{Name: "no_alt", Kind: policy.Criterion},
		policy.Argument{Name: "unmet", Kind: policy.Criterion},
		policy.Argument{Name: "ok", Kind: policy.Criterion},
		policy.Argument{Name: "approve", Kind: policy.Decision, Verdict: policy.Approve},
		policy.Argument{Name: "deny", Kind: policy.Decision, Verdict: policy.Deny})
	for i := 0; i < 30; i++ {
		p.Attacks = append(p.Attacks, policy.Attack{From: fmt.Sprintf("alt%d", i), To: "no_alt"})
	}
	p.Attacks = append(p.Attacks,
		policy.Attack{From: "no_alt", To: "ok"}, policy.Attack{From: "unmet", To: "ok"}, policy.Attack{From: "contra", To: "ok"},
		policy.Attack{From: "tried", To: "unmet"}, policy.Attack{From: "imaged", To: "unmet"},
		policy.Attack{From: "no_alt", To: "approve"}, policy.Attack{From: "unmet", To: "approve"}, policy.Attack{From: "contra", To: "approve"},
		policy.Attack{From: "ok", To: "deny"})
	start := time.Now()
	r := Check(p)
	if r.Incomplete != "" || r.Combinations != 1<<34 {
		t.Fatalf("%+v", r)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("checking took %v", took)
	}
	total := 0
	for _, c := range r.Outcomes {
		total += c
	}
	if total != 1<<34 {
		t.Errorf("the outcomes count %d combinations, not 2^34", total)
	}
	// Approved exactly when some alternative, not contra, and (tried or imaged): (2^30-1) * 1 * 3 ways * 2 (smoker).
	if want := (1<<30 - 1) * 3 * 2; r.Outcomes["approved/decided"] != want {
		t.Errorf("approved on %d combinations, want %d", r.Outcomes["approved/decided"], want)
	}
	// Nothing documented about the alternatives: a denial would rest on their absence, so it pends for them.
	ref := Citation{Reference: "DocumentReference/1", Quote: "x"}
	req := Request{Evidence: []Assertion{{Argument: "tried", Finding: Present, Citation: ref}, {Argument: "contra", Finding: Absent, Citation: ref}}}
	res, err := Decide(p, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Pended || res.Reason != NeedsInformation || len(res.Missing) < 30 || res.Cautious != "" {
		t.Errorf("%s/%s, %d missing, cautious %q", res.Outcome, res.Reason, len(res.Missing), res.Cautious)
	}
}

// TestCautiousWhenTheSearchCannotFinish: approval needs all 34 pieces at once, so the only set that overturns a
// denial is all of them, past any search budget. The check still finishes; the decision does not deny on an
// unfinished search: it pends, for every piece not documented, and says why.
func TestCautiousWhenTheSearchCannotFinish(t *testing.T) {
	p := &policy.Policy{ID: "all"}
	for i := 0; i < 34; i++ {
		p.Arguments = append(p.Arguments, policy.Argument{Name: fmt.Sprintf("e%d", i), Kind: policy.Evidence})
	}
	p.Arguments = append(p.Arguments, policy.Argument{Name: "approve", Kind: policy.Decision, Verdict: policy.Approve},
		policy.Argument{Name: "deny", Kind: policy.Decision, Verdict: policy.Deny})
	for i := 0; i < 34; i++ {
		u := fmt.Sprintf("u%d", i)
		p.Arguments = append(p.Arguments, policy.Argument{Name: u, Kind: policy.Criterion})
		p.Attacks = append(p.Attacks, policy.Attack{From: fmt.Sprintf("e%d", i), To: u}, policy.Attack{From: u, To: "approve"})
	}
	p.Attacks = append(p.Attacks, policy.Attack{From: "approve", To: "deny"})
	if r := Check(p); !r.OK() || r.Outcomes["approved/decided"] != 1 {
		t.Fatalf("%+v", r)
	}
	res, err := Decide(p, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Pended || res.Cautious == "" || len(res.Missing) != 34 {
		t.Errorf("%s/%s cautious %q, %d missing", res.Outcome, res.Reason, res.Cautious, len(res.Missing))
	}
}

// TestLargePolicyInTheLanguage: a policy of 24 pieces of evidence, written in the policy language, loads (past the
// old limit of 16), passes its check, and decides in well under a second, the absence test included.
func TestLargePolicyInTheLanguage(t *testing.T) {
	var b strings.Builder
	b.WriteString("policy wide\n  title \"Wide\"\n  version 1\n  source \"Test 1\"\n  questionnaire https://payer.example/Q/w\n  service cpt 99999\n\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "evidence dx%d\n  text \"Indication %d\"\n  clause \"Any of these.\"\n  find question dx%d\n\n", i, i, i)
	}
	for _, n := range []string{"conservative", "imaging", "contra", "repeat"} {
		fmt.Fprintf(&b, "evidence %s\n  text \"%s\"\n  clause \"Any of these.\"\n  find question %s\n\n", n, n, n)
	}
	b.WriteString("criterion no_indication\n  text \"No indication\"\n  clause \"Any of these.\"\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "  defeated by dx%d\n", i)
	}
	b.WriteString("\ncriterion no_workup\n  text \"No workup\"\n  clause \"Any of these.\"\n  defeated by conservative\n  defeated by imaging\n\n")
	b.WriteString("decision approve approves\n  text \"Approve\"\n  clause \"Any of these.\"\n  defeated by no_indication\n  defeated by no_workup\n  defeated by contra\n  defeated by repeat\n\n")
	b.WriteString("decision deny denies\n  text \"Deny\"\n  clause \"Any of these.\"\n  defeated by met\n\n")
	b.WriteString("criterion met\n  text \"Met\"\n  clause \"Any of these.\"\n  defeated by no_indication\n  defeated by no_workup\n  defeated by contra\n  defeated by repeat\n")
	p, err := policy.Parse("wide.vaglio", b.String())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if r := Check(p); !r.OK() || r.Combinations != 1<<24 {
		t.Fatalf("%+v", r)
	}
	ref := Citation{Reference: "DocumentReference/1", Quote: "x"}
	res, err := Decide(p, Request{Evidence: []Assertion{{Argument: "dx3", Finding: Present, Citation: ref},
		{Argument: "imaging", Finding: Present, Citation: ref}, {Argument: "contra", Finding: Absent, Citation: ref},
		{Argument: "repeat", Finding: Absent, Citation: ref}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Approved || res.Cautious != "" {
		t.Errorf("%s/%s %q", res.Outcome, res.Reason, res.Cautious)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("check and decision took %v", took)
	}
}

// TestMapSlices: a policy past 16 pieces is mapped a slice at a time. The default slice takes pieces from each family
// (here the four workup pieces plus alternatives, not alternatives only), and every cell agrees with Settle on the
// whole record it stands for.
func TestMapSlices(t *testing.T) {
	p := &policy.Policy{ID: "big"}
	for i := 0; i < 30; i++ {
		p.Arguments = append(p.Arguments, policy.Argument{Name: fmt.Sprintf("alt%d", i), Kind: policy.Evidence})
		p.Attacks = append(p.Attacks, policy.Attack{From: fmt.Sprintf("alt%d", i), To: "no_alt"})
	}
	for _, n := range []string{"tried", "imaged", "contra"} {
		p.Arguments = append(p.Arguments, policy.Argument{Name: n, Kind: policy.Evidence})
	}
	p.Arguments = append(p.Arguments, policy.Argument{Name: "no_alt", Kind: policy.Criterion},
		policy.Argument{Name: "unmet", Kind: policy.Criterion},
		policy.Argument{Name: "approve", Kind: policy.Decision, Verdict: policy.Approve},
		policy.Argument{Name: "deny", Kind: policy.Decision, Verdict: policy.Deny})
	p.Attacks = append(p.Attacks, policy.Attack{From: "tried", To: "unmet"}, policy.Attack{From: "imaged", To: "unmet"},
		policy.Attack{From: "no_alt", To: "approve"}, policy.Attack{From: "unmet", To: "approve"},
		policy.Attack{From: "contra", To: "approve"}, policy.Attack{From: "approve", To: "deny"})
	axes := MapAxes(p)
	if len(axes) != MaxMapEvidence || !contains(axes, "tried") || !contains(axes, "imaged") || !contains(axes, "contra") {
		t.Fatalf("default slice %v", axes)
	}
	m, err := DecisionMapSlice(p, axes, []string{"alt29"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Held) != 33-16 || len(m.HeldPresent) != 1 || len(m.Outcomes) != 1<<16 {
		t.Fatalf("held %d, present %v, %d cells", len(m.Held), m.HeldPresent, len(m.Outcomes))
	}
	for _, mask := range []int{0, 1, 1 << 13, 1<<16 - 1, 12345} {
		present := []string{"alt29"}
		for i, n := range m.Evidence {
			if mask&(1<<i) != 0 {
				present = append(present, n)
			}
		}
		if o, _ := Settle(p, present); string(o[0]) != m.Outcomes[mask:mask+1] {
			t.Errorf("cell %d: map %c, settle %s", mask, m.Outcomes[mask], o)
		}
	}
	if _, err := DecisionMapSlice(p, append(axes, "alt29"), nil); err == nil {
		t.Error("17 pieces on the grid accepted")
	}
	if _, err := DecisionMapSlice(p, axes, []string{"tried"}); err == nil {
		t.Error("a piece both on the grid and held accepted")
	}
}
