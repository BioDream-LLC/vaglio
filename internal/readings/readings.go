// Package readings compares two readings of one coverage document: two policies drafted from the same text, each of
// which quotes it word for word and passes every check a machine can make.
//
// Where two such readings decide the same record differently, the text admits both, and that is worth a person's
// attention: it is a possible ambiguity in the document, or a mistake in one reading, and only a person who knows the
// clinical intent can say which. Nothing here says which reading is right.
//
// Evidence is matched across the readings by the clause each quotes, since both quote the same document. A record is
// every combination of the matched evidence (and of evidence only one reading has), each piece documented present or
// absent; each reading decides it, and the combinations they disagree on are grouped by the two outcomes and the
// decisions they rest on.
package readings

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// MaxEvidence bounds the evidence compared at once: 2^18 records, each decided twice.
const MaxEvidence = 18

// Item is one piece of evidence as the comparison names it: what each reading calls it, and the clause they quote.
type Item struct {
	A      string `json:"a,omitempty"` // the name in reading A; "" when only B has it
	B      string `json:"b,omitempty"`
	Text   string `json:"text"`
	Clause string `json:"clause"`
}

// Group is a set of records the two readings decide differently, for the same pair of reasons.
type Group struct {
	A        decide.Outcome `json:"a"`
	B        decide.Outcome `json:"b"`
	ADecides string         `json:"aDecides,omitempty"` // the clause of the decision A rests on
	BDecides string         `json:"bDecides,omitempty"`
	Records  int            `json:"records"`
	// Example is the simplest such record: the fewest pieces of evidence present (every other piece documented absent).
	Example []string `json:"example"`
}

// Comparison is what two readings were found to disagree on.
type Comparison struct {
	A, B    string `json:"-"`
	Matched []Item `json:"matched"`
	// OnlyA and OnlyB are evidence one reading treats as deciding and the other does not mention at all.
	OnlyA   []Item  `json:"onlyA,omitempty"`
	OnlyB   []Item  `json:"onlyB,omitempty"`
	Records int     `json:"records"`
	Differ  int     `json:"differ"`
	Groups  []Group `json:"groups,omitempty"`
}

// Compare compares two readings of one document.
func Compare(a, b *policy.Policy) (*Comparison, error) {
	ea, eb := evidence(a), evidence(b)
	pairs := match(ea, eb)
	c := &Comparison{A: a.ID, B: b.ID}
	type slot struct{ a, b string }
	var slots []slot
	usedB := map[string]bool{}
	for _, x := range ea {
		y, ok := pairs[x.Name]
		it := Item{A: x.Name, Text: x.Text, Clause: x.Clause}
		if ok {
			it.B, usedB[y] = y, true
			c.Matched = append(c.Matched, it)
		} else {
			c.OnlyA = append(c.OnlyA, it)
		}
		slots = append(slots, slot{x.Name, y})
	}
	for _, y := range eb {
		if !usedB[y.Name] {
			c.OnlyB = append(c.OnlyB, Item{B: y.Name, Text: y.Text, Clause: y.Clause})
			slots = append(slots, slot{"", y.Name})
		}
	}
	if len(slots) > MaxEvidence {
		return nil, fmt.Errorf("readings: %d pieces of evidence between them; at most %d can be compared", len(slots), MaxEvidence)
	}
	if len(c.Matched) == 0 {
		return nil, errors.New("readings: the two policies quote no clause in common; are they readings of the same document?")
	}
	text := map[string]string{}
	for _, x := range ea {
		text["a:"+x.Name] = x.Text
	}
	for _, y := range eb {
		text["b:"+y.Name] = y.Text
	}
	clause := func(p *policy.Policy, name string) string {
		if arg, ok := p.Argument(name); ok {
			return arg.Clause
		}
		return ""
	}
	groups := map[string]*Group{}
	c.Records = 1 << uint(len(slots))
	for mask := 0; mask < c.Records; mask++ {
		var pa, pb, shown []string
		for i, s := range slots {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			if s.a != "" {
				pa = append(pa, s.a)
				shown = append(shown, text["a:"+s.a])
			} else {
				shown = append(shown, text["b:"+s.b])
			}
			if s.b != "" {
				pb = append(pb, s.b)
			}
		}
		oa, da := decide.Settle(a, pa)
		ob, db := decide.Settle(b, pb)
		if oa == ob {
			continue
		}
		c.Differ++
		g := Group{A: oa, B: ob, ADecides: clause(a, da), BDecides: clause(b, db)}
		key := string(oa) + "\x00" + string(ob) + "\x00" + g.ADecides + "\x00" + g.BDecides
		if old, ok := groups[key]; ok {
			old.Records++
			if len(shown) < len(old.Example) {
				old.Example = shown
			}
			continue
		}
		g.Records, g.Example = 1, shown
		if g.Example == nil {
			g.Example = []string{}
		}
		groups[key] = &g
	}
	for _, g := range groups {
		c.Groups = append(c.Groups, *g)
	}
	sort.Slice(c.Groups, func(i, j int) bool {
		if c.Groups[i].Records != c.Groups[j].Records {
			return c.Groups[i].Records > c.Groups[j].Records
		}
		return len(c.Groups[i].Example) < len(c.Groups[j].Example)
	})
	return c, nil
}

func evidence(p *policy.Policy) []policy.Argument {
	var out []policy.Argument
	for _, a := range p.Arguments {
		if a.Kind == policy.Evidence {
			out = append(out, a)
		}
	}
	return out
}

// match pairs each piece of A's evidence with the piece of B's quoting the same clause: the same words, or, when no
// clause matches exactly, the one sharing most of its words (at least 60% of the two clauses' words together).
func match(ea, eb []policy.Argument) map[string]string {
	type cand struct {
		a, b  string
		score float64
	}
	var cands []cand
	for _, x := range ea {
		wx := words(x.Clause)
		for _, y := range eb {
			s := jaccard(wx, words(y.Clause))
			if s >= 0.6 {
				// The evidence text breaks a tie: two pieces quoting one compound clause.
				s += 0.01 * jaccard(words(x.Text), words(y.Text))
				cands = append(cands, cand{x.Name, y.Name, s})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	out, used := map[string]string{}, map[string]bool{}
	for _, c := range cands {
		if _, ok := out[c.a]; ok || used[c.b] {
			continue
		}
		out[c.a], used[c.b] = c.b, true
	}
	return out
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out[w] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}
