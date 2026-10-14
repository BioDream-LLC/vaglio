package goldcard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func TestValidate(t *testing.T) {
	good := Program{Name: "p", MinApprovalRate: 0.9, LookBackDays: 365, MinDecisions: 5}
	if err := (&Config{Programs: []Program{good}}).Validate(nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		change func(*Program)
		want   string
	}{
		{func(p *Program) { p.Name = " " }, "no name"},
		{func(p *Program) { p.MinApprovalRate = 90 }, "fraction"},
		{func(p *Program) { p.LookBackDays = 0 }, "lookBackDays"},
		{func(p *Program) { p.MinDecisions = 0 }, "no record"},
		{func(p *Program) { p.States = []string{"Texas"} }, "two-letter"},
		{func(p *Program) { p.Policies = []string{"nope"} }, "no policy"},
	} {
		p := good
		tc.change(&p)
		err := (&Config{Programs: []Program{p}}).Validate(map[string]*policy.Policy{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.want, err)
		}
	}
	if err := (&Config{Programs: []Program{good, good}}).Validate(nil); err == nil {
		t.Error("a name used twice")
	}
}

// history is n decided cases for one provider and policy, approved of them approved, the newest a day ago.
func history(n, approved int, now time.Time) []cases.Case {
	var out []cases.Case
	for i := 0; i < n; i++ {
		c := cases.Case{ID: fmt.Sprint(i), Status: cases.Decided, Created: now.AddDate(0, 0, -1-i).Format(time.RFC3339Nano)}
		c.ProviderNPI, c.Policy, c.Answer.Decision = "1234567893", "p", "deny"
		if i < approved {
			c.Answer.Decision = "approve"
		}
		out = append(out, c)
	}
	return out
}

func TestTheRateIsExact(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c := &Config{Programs: []Program{{Name: "p", MinApprovalRate: 0.9, LookBackDays: 30, MinDecisions: 10}}}
	if e := c.Exemption(history(10, 9, now), "1234567893", "", "p", now); e == nil || e.Approved != 9 {
		t.Errorf("9 of 10 meets 90%%: %+v", e)
	}
	if e := c.Exemption(history(10, 8, now), "1234567893", "", "p", now); e != nil {
		t.Error("8 of 10 does not")
	}
	if e := c.Exemption(history(9, 9, now), "1234567893", "", "p", now); e != nil {
		t.Error("9 decisions is under the minimum of 10")
	}
	// Decisions older than the window do not count: 21 days on, the oldest is 31 days old.
	if e := c.Exemption(history(10, 10, now), "1234567893", "", "p", now.AddDate(0, 0, 21)); e != nil {
		t.Error("a decision outside the window counted")
	}
	if e := c.Exemption(history(10, 10, now), "", "", "p", now); e != nil {
		t.Error("no NPI, no exemption")
	}
}

func TestCounting(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h := history(10, 9, now)
	for i := range h {
		h[i].PatientState = "TX"
	}
	h[0].PatientState = "CA" // one of the nine approvals was a Californian patient's
	c := &Config{Programs: []Program{{Name: "p", States: []string{"TX"}, MinApprovalRate: 0.9, LookBackDays: 30, MinDecisions: 10}}}
	if e := c.Exemption(h, "1234567893", "TX", "p", now); e == nil || e.Decisions != 10 {
		t.Fatalf("counting all: %+v", e)
	}
	if err := c.SetCounting("p", CountStates); err != nil {
		t.Fatal(err)
	}
	if e := c.Exemption(h, "1234567893", "TX", "p", now); e != nil {
		t.Errorf("counting Texas only, 9 decisions is under the minimum: %+v", e)
	}
	if err := c.SetCounting("p", "some"); err == nil {
		t.Error("an unknown mode")
	}
	if err := c.SetCounting("nope", CountAll); err == nil {
		t.Error("an unknown program")
	}
	noStates := &Config{Programs: []Program{{Name: "q", MinApprovalRate: 0.9, LookBackDays: 30, MinDecisions: 1}}}
	if err := noStates.SetCounting("q", CountStates); err == nil {
		t.Error("counting by state with no states")
	}
}
