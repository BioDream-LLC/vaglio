package report

import (
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
)

// kase is a case for service code, created now, with the engine's outcome and what the provider was told.
func kase(code string, outcome decide.Outcome, answer string) cases.Case {
	return cases.Case{Created: time.Now().UTC().Format(time.RFC3339Nano), Status: cases.Decided,
		Opening: cases.Opening{Policy: "p" + code, PolicyTitle: "Policy " + code, Service: "cpt|" + code,
			Result: &decide.Result{Outcome: outcome}, Answer: cases.Answer{Decision: answer}}}
}

func reviewed(c cases.Case, decision string, after time.Duration) cases.Case {
	t0, _ := time.Parse(time.RFC3339Nano, c.Created)
	c.Status, c.Review = cases.Reviewed, &cases.Review{Decision: decision, Time: t0.Add(after).Format(time.RFC3339Nano)}
	return c
}

func TestListReview(t *testing.T) {
	var cs []cases.Case
	// 100: approved 98 at once, 2 pended and approved by a reviewer: never denied.
	for i := 0; i < 98; i++ {
		cs = append(cs, kase("100", decide.Approved, "approve"))
	}
	for i := 0; i < 2; i++ {
		cs = append(cs, reviewed(kase("100", decide.Pended, "pend"), "approve", 4*time.Hour))
	}
	// 200: 97 approved, 3 denials of which 2 were overturned on appeal: approved 99%, denials mostly do not stand.
	for i := 0; i < 97; i++ {
		cs = append(cs, kase("200", decide.Approved, "approve"))
	}
	for i := 0; i < 3; i++ {
		c := kase("200", decide.Denied, "deny")
		if i < 2 {
			c.Appeal = &cases.Appeal{Outcome: "overturned"}
		}
		cs = append(cs, c)
	}
	// 300: 96 approved, 4 denials that all stood: approved 96%, yet review is right when it denies.
	for i := 0; i < 96; i++ {
		cs = append(cs, kase("300", decide.Approved, "approve"))
	}
	for i := 0; i < 4; i++ {
		cs = append(cs, kase("300", decide.Denied, "deny"))
	}
	// 400: 60 approved, 40 denied: keep.
	for i := 0; i < 100; i++ {
		if i < 60 {
			cs = append(cs, kase("400", decide.Approved, "approve"))
		} else {
			cs = append(cs, kase("400", decide.Denied, "deny"))
		}
	}
	// 500: 5 requests: not enough.
	for i := 0; i < 5; i++ {
		cs = append(cs, kase("500", decide.Approved, "approve"))
	}
	p, _ := ParsePeriod("", "", time.Now())
	l := BuildListReview(cs, p, nil, DefaultListCriteria)
	got := map[string]ListRow{}
	for _, r := range l.Rows {
		got[r.Code] = r
	}
	want := map[string]string{"100": VerdictRemove, "200": VerdictRemove, "300": VerdictKeep, "400": VerdictKeep, "500": VerdictFew}
	for code, v := range want {
		if got[code].Verdict != v {
			t.Errorf("%s: %s (%s), want %s", code, got[code].Verdict, got[code].Why, v)
		}
	}
	if r := got["100"]; r.EngineApproved != 98 || r.Pended != 2 || r.MedianReviewHours == nil || *r.MedianReviewHours != 4 ||
		r.ReversedPercent != nil || !strings.Contains(r.Why, "never denied") {
		t.Errorf("100: %+v", r)
	}
	if r := got["200"]; r.Approved != 99 || r.Reversed != 2 || r.Denied != 1 {
		t.Errorf("200: %+v", r)
	}
	if l.Rows[0].Verdict != VerdictRemove || l.Rows[len(l.Rows)-1].Verdict != VerdictFew {
		t.Error("the candidates come first, the unknowns last")
	}
	csv := l.CSV()
	if !strings.HasPrefix(csv, "service,policy,requests") || strings.Count(csv, "\n") != 6 || !strings.Contains(csv, "consider-removing") {
		t.Errorf("csv:\n%s", csv)
	}
	if csvField("=1+1") != "'=1+1" || csvField(`a,"b"`) != `"a,""b"""` {
		t.Error("csv fields")
	}
}
