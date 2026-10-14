package policy

import "fmt"

// Lint lists what is legal in a policy but probably a mistake. Validate refuses what cannot work; Check in package decide
// refuses what decides badly; these are for the author to look at, and a policy with warnings still loads.
func Lint(p *Policy) []string {
	var out []string
	attacks, attacked := map[string]int{}, map[string]int{}
	seen := map[Attack]bool{}
	for _, at := range p.Attacks {
		if seen[at] {
			out = append(out, fmt.Sprintf("%s defeats %s twice", at.From, at.To))
		}
		seen[at] = true
		attacks[at.From]++
		attacked[at.To]++
	}
	linkIDs := map[string]string{}
	for _, a := range p.Arguments {
		switch {
		case a.Kind == Evidence && attacks[a.Name] == 0:
			out = append(out, fmt.Sprintf("evidence %s defeats nothing, so it can never change a decision", a.Name))
		case a.Kind == Criterion && attacks[a.Name] == 0:
			out = append(out, fmt.Sprintf("criterion %s defeats nothing, so it can never change a decision", a.Name))
		case a.Kind == Criterion && attacked[a.Name] == 0:
			out = append(out, fmt.Sprintf("criterion %s is defeated by nothing, so it always holds", a.Name))
		}
		if a.Kind == Evidence && len(a.Find) == 0 {
			out = append(out, fmt.Sprintf("evidence %s has no find, so it is never found in a PAS request: only a person "+
				"entering it by hand can assert it", a.Name))
		}
		for _, s := range a.Find {
			if s.Question == "" {
				continue
			}
			if other, dup := linkIDs[s.Question]; dup && other != a.Name {
				out = append(out, fmt.Sprintf("question %s is read as both %s and %s", s.Question, other, a.Name))
			}
			linkIDs[s.Question] = a.Name
		}
	}
	if p.Status == Draft {
		out = append(out, "status draft: nobody has signed this policy off, and the server will not decide requests with it")
	}
	return out
}
