package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Prior authorization list review. Health plans have committed to require prior authorization for fewer services; the
// services a plan approves nearly every time it is asked are where review adds burden and changes little. This report
// says, per service the policies decide, how often requests were approved in the end, how often a denial did not stand,
// and how much review it took, and sorts the services into three plain verdicts by thresholds the plan sets and the
// report states. It recommends; it changes no policy and no list.

// ListCriteria are the thresholds of the list review.
type ListCriteria struct {
	// MinRequests is the fewest decided requests a service needs for a verdict; fewer is "not enough requests".
	MinRequests int `json:"minRequests"`
	// MinApprovedPercent: approved at least this often in the end (by the engine, a reviewer or on appeal), a service
	// is a candidate to stop reviewing.
	MinApprovedPercent float64 `json:"minApprovedPercent"`
	// MinReversedPercent: a candidate with denials also had at least this share of them reversed (by a clinician or on
	// appeal). Denials that mostly stand mean review is catching something, however rare they are.
	MinReversedPercent float64 `json:"minReversedPercent"`
}

// DefaultListCriteria: 30 requests, 95% approved, and any denials reversed at least half the time.
var DefaultListCriteria = ListCriteria{MinRequests: 30, MinApprovedPercent: 95, MinReversedPercent: 50}

// Verdicts of the list review.
const (
	VerdictRemove = "consider-removing" // approved nearly always: review changes little
	VerdictKeep   = "keep"              // denials are made and stand, or approval is not near-certain
	VerdictFew    = "not-enough"        // too few requests to say
)

// ListRow is one service.
type ListRow struct {
	Service string `json:"service"` // system|code
	Code    string `json:"code"`
	Policy  string `json:"policy"`
	Title   string `json:"title"`
	// Requests decided in the period (a superseded request is counted once, as the request that replaced it).
	Requests int `json:"requests"`
	// Approved in the end: by the engine, a reviewer, a gold card exemption, or on appeal.
	Approved        int     `json:"approved"`
	ApprovedPercent float64 `json:"approvedPercent"`
	// EngineApproved at once, with no person involved.
	EngineApproved int `json:"engineApproved"`
	// Pended for a reviewer (any reason), and Exempt approved by gold card.
	Pended int `json:"pended"`
	Exempt int `json:"exempt"`
	// Denials stood or not: Denied in the end, and Reversed, denials (the engine's, proposed or issued) a clinician or an
	// appeal turned into approvals.
	Denied          int      `json:"denied"`
	EngineDenials   int      `json:"engineDenials"`
	Reversed        int      `json:"reversed"`
	ReversedPercent *float64 `json:"reversedPercent"` // nil: no denials to reverse
	// Waiting are requests still undecided.
	Waiting int `json:"waiting"`
	// MedianHours from request to final decision, over requests a person decided (engine decisions take seconds).
	MedianReviewHours *float64 `json:"medianReviewHours"`
	Verdict           string   `json:"verdict"`
	// Why is the verdict in words, with its numbers.
	Why string `json:"why"`
}

// ListReview is the list review for a period.
type ListReview struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Criteria ListCriteria `json:"criteria"`
	Rows     []ListRow    `json:"rows"`
	// Note is what the review does not know.
	Note string `json:"note"`
}

// ListNote is said under the list review.
const ListNote = "Counts are of requests Vaglio decided. Services the plan requires prior authorization for but no " +
	"policy here decides are not seen. An approval rate says how often review changed the answer, not whether care was " +
	"needed: a service may stay on the list for reasons this report cannot see (cost, safety, fraud). It recommends; " +
	"it changes no policy and no list."

// BuildListReview reviews the services over the cases created in the period.
func BuildListReview(cs []cases.Case, p Period, policies map[string]*policy.Policy, crit ListCriteria) ListReview {
	if crit.MinRequests <= 0 {
		crit.MinRequests = DefaultListCriteria.MinRequests
	}
	out := ListReview{From: p.From.Format(time.DateOnly), To: p.To.Format(time.DateOnly), Criteria: crit, Rows: []ListRow{},
		Note: ListNote}
	rows := map[string]*ListRow{}
	hours := map[string][]float64{}
	for i := range cs {
		c := &cs[i]
		if c.Result == nil || c.Status == cases.Superseded || !p.Contains(created(c)) {
			continue
		}
		key := c.Service
		r := rows[key]
		if r == nil {
			_, code, _ := strings.Cut(c.Service, "|")
			r = &ListRow{Service: c.Service, Code: code, Policy: c.Policy, Title: c.PolicyTitle}
			if pp := policies[c.Policy]; pp != nil {
				r.Title = pp.Title
			}
			rows[key] = r
		}
		r.Requests++
		final := c.Final()
		if c.Appeal != nil && c.Appeal.Outcome == "overturned" {
			final = "approve"
		}
		engineDenied := c.Result.Outcome == decide.Denied || c.Proposed != nil
		if engineDenied {
			r.EngineDenials++
		}
		switch {
		case c.Exemption != nil:
			r.Exempt++
		case c.Answer.Decision == "approve" && c.Result.Outcome == decide.Approved:
			r.EngineApproved++
		case c.Answer.Decision == "pend":
			r.Pended++
		}
		switch final {
		case "approve":
			r.Approved++
			if engineDenied {
				r.Reversed++
			}
		case "deny":
			r.Denied++
		default:
			r.Waiting++
		}
		if c.Review != nil {
			t0, e1 := time.Parse(time.RFC3339Nano, c.Created)
			t1, e2 := time.Parse(time.RFC3339Nano, c.Review.Time)
			if e1 == nil && e2 == nil && t1.After(t0) {
				hours[key] = append(hours[key], t1.Sub(t0).Hours())
			}
		}
	}
	for key, r := range rows {
		decided := r.Requests - r.Waiting
		if decided > 0 {
			r.ApprovedPercent = 100 * float64(r.Approved) / float64(decided)
		}
		r.ReversedPercent = pct(r.Reversed, r.EngineDenials)
		if h := hours[key]; len(h) > 0 {
			sort.Float64s(h)
			m := h[len(h)/2]
			if len(h)%2 == 0 {
				m = (h[len(h)/2-1] + h[len(h)/2]) / 2
			}
			r.MedianReviewHours = &m
		}
		r.Verdict, r.Why = verdict(*r, decided, crit)
		out.Rows = append(out.Rows, *r)
	}
	rank := map[string]int{VerdictRemove: 0, VerdictKeep: 1, VerdictFew: 2}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if rank[a.Verdict] != rank[b.Verdict] {
			return rank[a.Verdict] < rank[b.Verdict]
		}
		if a.ApprovedPercent != b.ApprovedPercent {
			return a.ApprovedPercent > b.ApprovedPercent
		}
		return a.Service < b.Service
	})
	return out
}

func verdict(r ListRow, decided int, c ListCriteria) (string, string) {
	if decided < c.MinRequests {
		return VerdictFew, fmt.Sprintf("%d decided requests; a verdict needs %d", decided, c.MinRequests)
	}
	approved := fmt.Sprintf("approved %d of %d (%.1f%%)", r.Approved, decided, r.ApprovedPercent)
	if r.ApprovedPercent < c.MinApprovedPercent {
		return VerdictKeep, fmt.Sprintf("%s, under %.0f%%: review changes the answer often enough to keep", approved, c.MinApprovedPercent)
	}
	if r.EngineDenials == 0 {
		return VerdictRemove, approved + ", never denied: review changed no answer"
	}
	if *r.ReversedPercent >= c.MinReversedPercent {
		return VerdictRemove, fmt.Sprintf("%s, and %d of %d denials (%.0f%%, at least %.0f%%) did not stand: review "+
			"mostly delays approvals", approved, r.Reversed, r.EngineDenials, *r.ReversedPercent, c.MinReversedPercent)
	}
	return VerdictKeep, fmt.Sprintf("%s, but %d of %d denials stood (%.0f%% reversed, under %.0f%%): review is rare to "+
		"change the answer, and right when it does", approved, r.EngineDenials-r.Reversed, r.EngineDenials,
		*r.ReversedPercent, c.MinReversedPercent)
}

// CSV is the list review as rows, one per service.
func (l ListReview) CSV() string {
	var b strings.Builder
	b.WriteString("service,policy,requests,approved,approved_percent,engine_approved,pended,exempt,denied,engine_denials," +
		"reversed,reversed_percent,median_review_hours,verdict,why\n")
	f := func(p *float64) string {
		if p == nil {
			return ""
		}
		return fmt.Sprintf("%.1f", *p)
	}
	for _, r := range l.Rows {
		fmt.Fprintf(&b, "%s,%s,%d,%d,%.1f,%d,%d,%d,%d,%d,%d,%s,%s,%s,%s\n", csvField(r.Service), csvField(r.Policy),
			r.Requests, r.Approved, r.ApprovedPercent, r.EngineApproved, r.Pended, r.Exempt, r.Denied, r.EngineDenials,
			r.Reversed, f(r.ReversedPercent), f(r.MedianReviewHours), r.Verdict, csvField(r.Why))
	}
	return b.String()
}

// csvField quotes a field when it needs it, and neutralizes one a spreadsheet would run as a formula.
func csvField(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		s = "'" + s
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
