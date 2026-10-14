// Package af is a Dung abstract argumentation framework under grounded semantics.
//
// Grounded only, for two reasons. It is polynomial and unique, so a decision has exactly one
// answer and a bounded cost. And it is the most sceptical of the standard semantics: the grounded extension is contained
// in every complete extension, so anything it accepts, every reasonable reading of the policy accepts too. A prior
// authorisation engine that may only deny what it can prove wants exactly that.
//
// Every argument that becomes In or Out is given the round in which it did. Rounds are what make a proof possible: an
// argument that went In at round r has every attacker Out by round r-1, and each of those was put Out by an argument In
// at an earlier round still. So following defenders always reaches unattacked arguments, and always terminates.
package af

import "fmt"

// ID names an argument inside one Framework. It is an index, not a stable identifier across frameworks.
type ID int

// Framework is a set of named arguments and the attacks between them.
type Framework struct {
	names     []string
	index     map[string]ID
	attackers [][]ID
}

// New returns an empty framework.
func New() *Framework {
	return &Framework{index: map[string]ID{}}
}

// Add declares an argument. A name may be declared once.
func (f *Framework) Add(name string) (ID, error) {
	if _, dup := f.index[name]; dup {
		return 0, fmt.Errorf("af: argument %q declared twice", name)
	}
	id := ID(len(f.names))
	f.names = append(f.names, name)
	f.index[name] = id
	f.attackers = append(f.attackers, nil)
	return id, nil
}

// Attack records that from attacks to. Repeating an attack has no further effect.
func (f *Framework) Attack(from, to ID) {
	for _, a := range f.attackers[to] {
		if a == from {
			return
		}
	}
	f.attackers[to] = append(f.attackers[to], from)
}

// Lookup returns the argument with the given name.
func (f *Framework) Lookup(name string) (ID, bool) {
	id, ok := f.index[name]
	return id, ok
}

// Len is the number of arguments.
func (f *Framework) Len() int { return len(f.names) }

// Name returns the name an argument was declared with.
func (f *Framework) Name(id ID) string { return f.names[id] }

// Attackers returns the arguments attacking id. The slice must not be modified.
func (f *Framework) Attackers(id ID) []ID { return f.attackers[id] }

// Label is an argument's status in a labelling.
type Label int8

const (
	Undec Label = iota // neither accepted nor rejected: the framework cannot settle it
	In                 // accepted
	Out                // rejected, because an accepted argument attacks it
)

func (l Label) String() string {
	switch l {
	case In:
		return "in"
	case Out:
		return "out"
	default:
		return "undec"
	}
}

// Labelling is the grounded labelling of a framework.
type Labelling struct {
	Label []Label
	// Round is the round in which an In or Out argument was settled, starting at 0. It is -1 for Undec.
	Round []int
}

// Grounded computes the grounded labelling.
//
// Each round first accepts every undecided argument whose attackers are all Out, then rejects every undecided argument
// attacked by something In. It stops at the first round that changes nothing.
func Grounded(f *Framework) Labelling {
	n := f.Len()
	lab := Labelling{Label: make([]Label, n), Round: make([]int, n)}
	for i := range lab.Round {
		lab.Round[i] = -1
	}
	for round := 0; ; round++ {
		changed := false
		var accepted []ID
		for i := 0; i < n; i++ {
			if lab.Label[i] != Undec {
				continue
			}
			ok := true
			for _, a := range f.attackers[i] {
				if lab.Label[a] != Out {
					ok = false
					break
				}
			}
			if ok {
				accepted = append(accepted, ID(i))
			}
		}
		for _, id := range accepted {
			lab.Label[id], lab.Round[id] = In, round
			changed = true
		}
		for i := 0; i < n; i++ {
			if lab.Label[i] != Undec {
				continue
			}
			for _, a := range f.attackers[i] {
				if lab.Label[a] == In {
					lab.Label[i], lab.Round[i] = Out, round
					changed = true
					break
				}
			}
		}
		if !changed {
			return lab
		}
	}
}

// Defender returns the argument that put attacker Out, for an attacker of an In argument: the In argument attacking it
// that was accepted earliest. Its round is always strictly less than that of the argument being defended, which is the
// property a proof relies on to terminate.
func Defender(f *Framework, lab Labelling, attacker ID) (ID, bool) {
	best, found := ID(-1), false
	for _, a := range f.attackers[attacker] {
		if lab.Label[a] == In && (!found || lab.Round[a] < lab.Round[best]) {
			best, found = a, true
		}
	}
	return best, found
}
