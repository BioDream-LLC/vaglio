package draft

// Drafting twice: models draft unevenly (the same model on the same document gives a different policy about one run
// in eight), and a single draft that passes every gate gives no sign of it. Twice drafts the document two times,
// with the same model or a second one, and compares the two readings over every combination of evidence
// (internal/readings). Where they agree, nothing is hidden by the choice of one run; where they disagree, the author
// is shown exactly which records they decide differently and on which clauses, before anyone signs the draft off.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/vaglio/internal/readings"
)

// Twin is two drafts of one document and how they compare.
type Twin struct {
	First, Second *Result
	// Compared is each policy id both drafts hold, with the comparison of the two readings of it (nil when it could
	// not be compared: Problems says why).
	Compared map[string]*readings.Comparison
	// Problems are what keeps the two from being called the same: a policy only one draft holds, a comparison that
	// could not be made, a reading that decides records differently.
	Problems []string
}

// Agree is whether the two drafts decide every record the same way, policy by policy.
func (t *Twin) Agree() bool { return len(t.Problems) == 0 }

// Twice drafts req two times, the first with first and the second with second (which may be the same model: each
// draft starts afresh), and compares them. Both must pass every gate; an error from either is returned with what
// was drafted so far.
func Twice(ctx context.Context, first, second Model, req Request) (*Twin, error) {
	t := &Twin{Compared: map[string]*readings.Comparison{}}
	var err error
	if t.First, err = Draft(ctx, first, req); err != nil {
		return t, fmt.Errorf("the first draft: %w", err)
	}
	if t.Second, err = Draft(ctx, second, req); err != nil {
		return t, fmt.Errorf("the second draft: %w", err)
	}
	byID := func(r *Result) map[string]int {
		m := map[string]int{}
		for i, p := range r.Policies {
			m[p.ID] = i
		}
		return m
	}
	a, b := byID(t.First), byID(t.Second)
	var ids []string
	for id := range a {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j, ok := b[id]
		if !ok {
			t.Problems = append(t.Problems, fmt.Sprintf("%s: only the first draft has this policy (the second split the document differently)", id))
			continue
		}
		c, err := readings.Compare(t.First.Policies[a[id]], t.Second.Policies[j])
		if err != nil {
			t.Problems = append(t.Problems, fmt.Sprintf("%s: the two drafts could not be compared: %v", id, err))
			continue
		}
		t.Compared[id] = c
		if c.Differ > 0 {
			t.Problems = append(t.Problems, fmt.Sprintf("%s: the drafts decide %d of %d records differently", id, c.Differ, c.Records))
		}
		for _, it := range c.OnlyA {
			t.Problems = append(t.Problems, fmt.Sprintf("%s: only the first draft decides on %q", id, it.Text))
		}
		for _, it := range c.OnlyB {
			t.Problems = append(t.Problems, fmt.Sprintf("%s: only the second draft decides on %q", id, it.Text))
		}
	}
	var onlyB []string
	for id := range b {
		if _, ok := a[id]; !ok {
			onlyB = append(onlyB, id)
		}
	}
	sort.Strings(onlyB)
	for _, id := range onlyB {
		t.Problems = append(t.Problems, fmt.Sprintf("%s: only the second draft has this policy (the first split the document differently)", id))
	}
	return t, nil
}

// Report is the comparison in plain words, for the author: one line when they agree, otherwise each disagreement with
// the clauses each draft rests on and the simplest record that shows it.
func (t *Twin) Report() string {
	var b strings.Builder
	if t.Agree() {
		n := 0
		for _, c := range t.Compared {
			n += c.Records
		}
		fmt.Fprintf(&b, "Drafted twice: the two drafts decide all %d records the same way.\n", n)
		return b.String()
	}
	b.WriteString("Drafted twice, and the two drafts DISAGREE. Read the document on these points before signing either off:\n")
	for _, p := range t.Problems {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	ids := make([]string, 0, len(t.Compared))
	for id := range t.Compared {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, g := range t.Compared[id].Groups {
			fmt.Fprintf(&b, "\n  %s, %d records: the first draft %s, the second %s\n", id, g.Records, g.A, g.B)
			if g.ADecides != "" {
				fmt.Fprintf(&b, "    the first rests on  %q\n", g.ADecides)
			}
			if g.BDecides != "" {
				fmt.Fprintf(&b, "    the second rests on %q\n", g.BDecides)
			}
			ex := "nothing (every piece of evidence documented absent)"
			if len(g.Example) > 0 {
				ex = strings.Join(g.Example, "; ")
			}
			fmt.Fprintf(&b, "    for example, documented present: %s\n", ex)
		}
	}
	return b.String()
}

// Commented is the report as comment lines, to head the policy file that is written.
func (t *Twin) Commented() string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(t.Report(), "\n"), "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	return b.String() + "#\n"
}
