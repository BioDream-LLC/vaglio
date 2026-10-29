package readings

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// reading is a random policy over n pieces of evidence quoting clauses "clause 0".."clause n-1", some of them left out
// so that each reading has evidence the other does not mention.
func reading(rng *rand.Rand, id string, n int) *policy.Policy {
	p := &policy.Policy{ID: id}
	var from, to []string
	for i := 0; i < n; i++ {
		if i > 0 && rng.Intn(6) == 0 {
			continue
		}
		name := fmt.Sprintf("%s_e%d", id, i)
		p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: policy.Evidence, Text: name,
			Clause: fmt.Sprintf("clause %d of the document", i)})
		from = append(from, name)
	}
	for i := 0; i < 2+rng.Intn(4); i++ {
		name := fmt.Sprintf("%s_c%d", id, i)
		p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: policy.Criterion, Clause: name})
		from, to = append(from, name), append(to, name)
	}
	for i, v := range []policy.Verdict{policy.Approve, policy.Deny, policy.Approve} {
		name := fmt.Sprintf("%s_d%d", id, i)
		p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: policy.Decision, Verdict: v, Clause: name})
		to = append(to, name)
	}
	for i := 0; i < n+len(to)*2; i++ {
		p.Attacks = append(p.Attacks, policy.Attack{From: from[rng.Intn(len(from))], To: to[rng.Intn(len(to))]})
	}
	return p
}

type summary struct {
	Records, Differ int
	Groups          map[string][2]int // records, example length
}

func summarise(c *Comparison) summary {
	s := summary{Records: c.Records, Differ: c.Differ, Groups: map[string][2]int{}}
	for _, g := range c.Groups {
		s.Groups[fmt.Sprint(g.A, g.B, g.ADecides, g.BDecides)] = [2]int{g.Records, len(g.Example)}
	}
	return s
}

// TestFamiliesMatchOneByOne: comparing by families of records gives the same counts, the same groups and examples
// as short as deciding every record twice.
func TestFamiliesMatchOneByOne(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	defer func(n int) { oneByOne = n }(oneByOne)
	for i := 0; i < 300; i++ {
		n := 2 + rng.Intn(11)
		a, b := reading(rng, "a", n), reading(rng, "b", n)
		oneByOne = 18
		want, err1 := Compare(a, b)
		oneByOne = 0
		got, err2 := Compare(a, b)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("pair %d: errors %v / %v", i, err1, err2)
		}
		if err1 != nil {
			continue
		}
		if !reflect.DeepEqual(summarise(want), summarise(got)) {
			t.Fatalf("pair %d:\n  one by one %+v\n  families   %+v", i, summarise(want), summarise(got))
		}
	}
}

// TestLargeReadings: two readings of 32 pieces of evidence each, 28 of them alternatives, are compared exactly and
// fast. B forgot alternative 27, so it denies the records whose only alternative is that one.
func TestLargeReadings(t *testing.T) {
	build := func(id string, skip int) *policy.Policy {
		p := &policy.Policy{ID: id}
		ev := func(name, clause string) {
			p.Arguments = append(p.Arguments, policy.Argument{Name: name, Kind: policy.Evidence, Text: name, Clause: clause})
		}
		for i := 0; i < 28; i++ {
			ev(fmt.Sprintf("alt%d", i), fmt.Sprintf("indication number %d is listed", i))
			if i != skip {
				p.Attacks = append(p.Attacks, policy.Attack{From: fmt.Sprintf("alt%d", i), To: "no_alt"})
			}
		}
		for _, n := range []string{"tried", "imaged", "contra", "repeat"} {
			ev(n, "the record shows "+n)
		}
		p.Arguments = append(p.Arguments, policy.Argument{Name: "no_alt", Kind: policy.Criterion, Clause: "none"},
			policy.Argument{Name: "unmet", Kind: policy.Criterion, Clause: "unmet"},
			policy.Argument{Name: "approve", Kind: policy.Decision, Verdict: policy.Approve, Clause: "approve"},
			policy.Argument{Name: "deny", Kind: policy.Decision, Verdict: policy.Deny, Clause: "deny"})
		p.Attacks = append(p.Attacks, policy.Attack{From: "tried", To: "unmet"}, policy.Attack{From: "imaged", To: "unmet"},
			policy.Attack{From: "no_alt", To: "approve"}, policy.Attack{From: "unmet", To: "approve"},
			policy.Attack{From: "contra", To: "approve"}, policy.Attack{From: "repeat", To: "approve"},
			policy.Attack{From: "approve", To: "deny"})
		return p
	}
	start := time.Now()
	c, err := Compare(build("a", -1), build("b", 27))
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}
	// They differ only when alt27 is the only alternative present and approval is otherwise met:
	// alt27 present, the other 27 absent, tried or imaged (3 ways), contra and repeat absent.
	if c.Records != 1<<32 || c.Differ != 3 || len(c.Groups) != 1 || len(c.Groups[0].Example) != 2 {
		t.Errorf("records %d, differ %d, groups %+v", c.Records, c.Differ, c.Groups)
	}
}

// TestFamiliesOnTheLibraryPair: the library's two readings of one LCD compare the same both ways.
func TestFamiliesOnTheLibraryPair(t *testing.T) {
	a, b := load(t, "../../library/tka.vaglio"), load(t, "../../library/readings/tka.b.vaglio")
	defer func(n int) { oneByOne = n }(oneByOne)
	want, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	oneByOne = 0
	got, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summarise(want), summarise(got)) {
		t.Errorf("one by one %+v\nfamilies %+v", summarise(want), summarise(got))
	}
}

// TestSmallComparisonsAlwaysFinish: up to 24 pieces of evidence the walk is allowed every branch it could have, so
// even with no budget of its own a tangled comparison finishes, with the same counts.
func TestSmallComparisonsAlwaysFinish(t *testing.T) {
	defer func(n, b int) { oneByOne, familyBudget = n, b }(oneByOne, familyBudget)
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 50; i++ {
		a, b := reading(rng, "a", 10), reading(rng, "b", 10)
		oneByOne, familyBudget = 18, 1<<22
		want, err := Compare(a, b)
		if err != nil {
			continue
		}
		oneByOne, familyBudget = 0, 1
		got, err := Compare(a, b)
		if err != nil {
			t.Fatalf("pair %d: %v", i, err)
		}
		if !reflect.DeepEqual(summarise(want), summarise(got)) {
			t.Fatalf("pair %d differs", i)
		}
	}
}
