package af

import (
	"math/rand"
	"testing"
)

// TestGroundedAgainstDefinition checks the worklist against the definition rather than against another worklist.
//
// The grounded extension is the intersection of all complete extensions, and complete extensions are found here by
// trying every subset. That shares no code and no idea with Grounded, which is the point: two
// implementations of the same algorithm can agree on every input and still both be wrong.
func TestGroundedAgainstDefinition(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 600; trial++ {
		n := 1 + rng.Intn(9)
		density := rng.Float64() * 0.45
		f := New()
		for i := 0; i < n; i++ {
			if _, err := f.Add(string(rune('a' + i))); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if rng.Float64() < density {
					f.Attack(ID(i), ID(j))
				}
			}
		}
		want := groundedByDefinition(f)
		got := Grounded(f)
		for i := 0; i < n; i++ {
			if got.Label[i] != want[i] {
				t.Fatalf("trial %d, argument %s: got %v, definition says %v", trial, f.Name(ID(i)), got.Label[i], want[i])
			}
		}
		checkDefenders(t, f, got)
	}
}

// checkDefenders verifies the property proofs depend on: every attacker of an In argument has a defender accepted in
// a strictly earlier round.
func checkDefenders(t *testing.T, f *Framework, lab Labelling) {
	t.Helper()
	for i := 0; i < f.Len(); i++ {
		if lab.Label[i] != In {
			continue
		}
		for _, b := range f.Attackers(ID(i)) {
			d, ok := Defender(f, lab, b)
			if !ok {
				t.Fatalf("%s is In but its attacker %s has no defender", f.Name(ID(i)), f.Name(b))
			}
			if lab.Round[d] >= lab.Round[i] {
				t.Fatalf("defender %s (round %d) is not earlier than %s (round %d)",
					f.Name(d), lab.Round[d], f.Name(ID(i)), lab.Round[i])
			}
		}
	}
}

func groundedByDefinition(f *Framework) []Label {
	n := f.Len()
	attacks := func(set uint, target int) bool {
		for _, a := range f.Attackers(ID(target)) {
			if set&(1<<uint(a)) != 0 {
				return true
			}
		}
		return false
	}
	defends := func(set uint, x int) bool {
		for _, b := range f.Attackers(ID(x)) {
			if !attacks(set, int(b)) {
				return false
			}
		}
		return true
	}
	all := uint(1)<<uint(n) - 1
	inter, any := all, false
	for s := uint(0); s <= all; s++ {
		ok := true
		for x := 0; x < n && ok; x++ {
			member := s&(1<<uint(x)) != 0
			if member && attacks(s, x) { // conflict-free
				ok = false
			}
			if member != defends(s, x) { // admissible, and contains everything it defends
				ok = false
			}
		}
		if ok {
			inter &= s
			any = true
		}
	}
	if !any {
		panic("every framework has a complete extension")
	}
	out := make([]Label, n)
	for x := 0; x < n; x++ {
		switch {
		case inter&(1<<uint(x)) != 0:
			out[x] = In
		case attacks(inter, x):
			out[x] = Out
		}
	}
	return out
}

func TestSelfAttackIsUndecided(t *testing.T) {
	f := New()
	a, _ := f.Add("a")
	b, _ := f.Add("b")
	f.Attack(a, a)
	f.Attack(a, b)
	lab := Grounded(f)
	if lab.Label[a] != Undec || lab.Label[b] != Undec {
		t.Fatalf("got a=%v b=%v, want both undec", lab.Label[a], lab.Label[b])
	}
}

func TestDuplicateNameRefused(t *testing.T) {
	f := New()
	if _, err := f.Add("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add("x"); err == nil {
		t.Fatal("a second declaration of x was accepted")
	}
}
