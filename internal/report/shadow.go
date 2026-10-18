package report

import (
	"encoding/csv"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
)

// Shadow is a shadow pilot's report: Vaglio's decisions beside the plan's, over the cases with the plan's outcome.
type Shadow struct {
	Cases        int `json:"cases"`        // decided in shadow
	WithOutcome  int `json:"withOutcome"`  // the plan's decision known
	BothDecided  int `json:"bothDecided"`  // Vaglio approved or denied, and so did the plan
	Agreed       int `json:"agreed"`       // of BothDecided
	VaglioPended int `json:"vaglioPended"` // with an outcome, Vaglio asked for evidence instead
	// Grid counts Vaglio's verdict (approve, deny, pend) by the plan's (approve, deny).
	Grid map[string]map[string]int `json:"grid"`
	// Agreement is Agreed/BothDecided, with its Wilson 95% interval; Kappa is Cohen's kappa over the same pairs: the
	// agreement beyond what the two rates would give by chance (1 is perfect, 0 chance).
	Agreement float64    `json:"agreement"`
	Interval  [2]float64 `json:"interval"`
	Kappa     float64    `json:"kappa"`
	// Rulings tallies clinicians' rulings on disagreements, by what they found the record supports.
	Rulings    map[string]int `json:"rulings"`
	Unruled    int            `json:"unruled"`
	ByPolicy   []ShadowGroup  `json:"byPolicy"`
	ByWeek     []ShadowGroup  `json:"byWeek"`
	Disagree   []ShadowRow    `json:"disagreements"`
	Overturned int            `json:"overturned"` // plan denials overturned on appeal that Vaglio would have approved
	Note       string         `json:"note"`
}

// ShadowGroup is agreement within a policy or a week.
type ShadowGroup struct {
	Key         string  `json:"key"`
	Title       string  `json:"title,omitempty"`
	BothDecided int     `json:"bothDecided"`
	Agreed      int     `json:"agreed"`
	Pended      int     `json:"pended"`
	Agreement   float64 `json:"agreement"`
}

// ShadowRow is one disagreement.
type ShadowRow struct {
	Case    string        `json:"case"`
	Created string        `json:"created"`
	Claim   string        `json:"claim"`
	Item    string        `json:"item"`
	Policy  string        `json:"policy"`
	Vaglio  string        `json:"vaglio"`
	Plan    string        `json:"plan"`
	By      string        `json:"by,omitempty"`
	Appeal  string        `json:"appeal,omitempty"`
	Why     string        `json:"why,omitempty"`
	Ruling  *cases.Ruling `json:"ruling,omitempty"`
}

// BuildShadow builds the report over the cases not superseded by a resubmission.
func BuildShadow(all []cases.Case) Shadow {
	r := Shadow{Grid: map[string]map[string]int{}, Rulings: map[string]int{}}
	for _, v := range []string{"approve", "deny", "pend"} {
		r.Grid[v] = map[string]int{"approve": 0, "deny": 0}
	}
	type acc struct {
		title             string
		both, agree, pend int
	}
	pol, week := map[string]*acc{}, map[string]*acc{}
	// For kappa: the two raters' approval counts over the pairs both decided.
	vApprove, pApprove := 0, 0
	for i := range all {
		c := &all[i]
		if c.SupersededBy != "" {
			continue
		}
		r.Cases++
		o := c.PlanOutcome
		if o == nil {
			continue
		}
		r.WithOutcome++
		v := c.Verdict()
		if v != "approve" && v != "deny" {
			v = "pend"
		}
		r.Grid[v][o.Decision]++
		wk := weekOf(c.Created)
		for _, m := range []struct {
			to  map[string]*acc
			key string
		}{{pol, c.Policy}, {week, wk}} {
			a := m.to[m.key]
			if a == nil {
				a = &acc{title: c.PolicyTitle}
				m.to[m.key] = a
			}
			switch {
			case v == "pend":
				a.pend++
			case v == o.Decision:
				a.both++
				a.agree++
			default:
				a.both++
			}
		}
		if v == "pend" {
			r.VaglioPended++
			continue
		}
		r.BothDecided++
		if v == "approve" {
			vApprove++
		}
		if o.Decision == "approve" {
			pApprove++
		}
		if v == o.Decision {
			r.Agreed++
			continue
		}
		if v == "approve" && o.Appeal == "overturned" {
			r.Overturned++
		}
		row := ShadowRow{Case: c.ID, Created: c.Created, Claim: c.Claim, Item: c.Item, Policy: c.Policy, Vaglio: v,
			Plan: o.Decision, By: o.By, Appeal: o.Appeal, Why: c.Answer.Why, Ruling: c.Ruling}
		if c.Ruling != nil {
			r.Rulings[c.Ruling.Supports]++
		} else {
			r.Unruled++
		}
		r.Disagree = append(r.Disagree, row)
	}
	if n := float64(r.BothDecided); n > 0 {
		r.Agreement = float64(r.Agreed) / n
		r.Interval = wilson(r.Agreed, r.BothDecided)
		pe := (float64(vApprove)/n)*(float64(pApprove)/n) + (1-float64(vApprove)/n)*(1-float64(pApprove)/n)
		if pe < 1 {
			r.Kappa = round3((r.Agreement - pe) / (1 - pe))
		} else {
			r.Kappa = 1 // both raters gave one answer only, and the same one
		}
		r.Agreement = round3(r.Agreement)
	}
	groups := func(m map[string]*acc, byKey bool) []ShadowGroup {
		var out []ShadowGroup
		for k, a := range m {
			g := ShadowGroup{Key: k, BothDecided: a.both, Agreed: a.agree, Pended: a.pend}
			if !byKey {
				g.Title = a.title
			}
			if a.both > 0 {
				g.Agreement = round3(float64(a.agree) / float64(a.both))
			}
			out = append(out, g)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out
	}
	r.ByPolicy, r.ByWeek = groups(pol, false), groups(week, true)
	// Disagreements to rule on first, those Vaglio would have approved first among them: the denials it found no ground for.
	sort.SliceStable(r.Disagree, func(i, j int) bool {
		a, b := r.Disagree[i], r.Disagree[j]
		if (a.Ruling == nil) != (b.Ruling == nil) {
			return a.Ruling == nil
		}
		if (a.Vaglio == "approve") != (b.Vaglio == "approve") {
			return a.Vaglio == "approve"
		}
		return a.Created < b.Created
	})
	r.Note = "Agreement and kappa count only requests both decided. A pend is not a disagreement: it is Vaglio asking for " +
		"evidence the record did not have. A clinician's ruling says which decision the record supports; neither " +
		"the plan's decision nor Vaglio's is taken as the truth."
	return r
}

// CSV is the disagreements, one row each.
func (s Shadow) CSV() string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	w.Write([]string{"case", "created", "claim", "item", "policy", "vaglio", "plan", "by", "appeal", "ruling", "ruled by", "note", "why"})
	for _, d := range s.Disagree {
		ruling, by, note := "", "", ""
		if d.Ruling != nil {
			ruling, by, note = d.Ruling.Supports, d.Ruling.By, d.Ruling.Note
		}
		w.Write(safe([]string{d.Case, d.Created, d.Claim, d.Item, d.Policy, d.Vaglio, d.Plan, d.By, d.Appeal, ruling, by, note, d.Why}))
	}
	w.Flush()
	return b.String()
}

// safe keeps cells from being read as formulas by a spreadsheet.
func safe(cells []string) []string {
	for i, c := range cells {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			cells[i] = "'" + c
		}
	}
	return cells
}

func weekOf(t string) string {
	d, err := time.Parse(time.RFC3339Nano, t)
	if err != nil {
		if len(t) >= 10 {
			return t[:10]
		}
		return t
	}
	d = d.UTC()
	d = d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // back to Monday
	return d.Format(time.DateOnly)
}

// wilson is the Wilson score 95% interval for k of n.
func wilson(k, n int) [2]float64 {
	const z = 1.959964
	p, nf := float64(k)/float64(n), float64(n)
	den := 1 + z*z/nf
	mid := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return [2]float64{round3(math.Max(0, mid-half)), round3(math.Min(1, mid+half))}
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
