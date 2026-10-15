package precedence

import (
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
)

func pol(id, authority, effective, ends string, lines, states []string) *policy.Policy {
	return &policy.Policy{ID: id, Title: id, Version: "1", Authority: authority, Effective: effective, Ends: ends,
		Lines: lines, States: states, Services: []policy.Service{{System: "http://www.ama-assn.org/go/cpt", Code: "27447"}}}
}

func TestChoose(t *testing.T) {
	ncd := pol("ncd", policy.NCD, "", "", []string{"medicare-advantage"}, nil)
	lcd := pol("lcd", policy.LCD, "", "", []string{"medicare-advantage"}, []string{"WA", "OR"})
	plan := pol("plan", "", "", "", nil, nil)
	next := pol("next", policy.NCD, "2027-01-01", "", nil, nil)
	all := []*policy.Policy{plan, lcd, ncd, next}

	c := Choose(all, Request{Day: "2026-10-15", Line: "medicare-advantage", State: "WA"})
	if c.Policy != ncd || len(c.Unsettled) != 0 {
		t.Fatalf("an MA member in WA: %+v", c)
	}
	why := map[string]string{}
	for _, a := range c.SetAside {
		why[a.Policy] = a.Why
	}
	if !strings.Contains(why["lcd"], "outranked by ncd") || !strings.Contains(why["plan"], "outranked") ||
		!strings.Contains(why["next"], "in effect from 2027-01-01") {
		t.Fatalf("set aside: %v", why)
	}
	if c := Choose(all, Request{Day: "2026-10-15", Line: "commercial", State: "WA"}); c.Policy != plan {
		t.Fatalf("a commercial member gets the plan's policy: %+v", c)
	}
	// The NCD in force from 2027 outranks everything, for every line.
	if c := Choose(all, Request{Day: "2027-01-01", Line: "commercial"}); c.Policy != next {
		t.Fatalf("on its effective day: %+v", c)
	}
	// Nothing says the member's line: the plan's policy decides, and the two Medicare documents are unsettled.
	c = Choose(all, Request{Day: "2026-10-15", State: "WA"})
	if c.Policy != plan || len(c.Unsettled) != 2 {
		t.Fatalf("line unknown: %+v", c)
	}
	if c := Choose([]*policy.Policy{plan}, Request{Day: "2026-10-15"}); c.Why != "the only policy for this service" {
		t.Fatal(c.Why)
	}
	ended := pol("old", "", "", "2026-01-31", nil, nil)
	if c := Choose([]*policy.Policy{ended}, Request{Day: "2026-10-15"}); c.Policy != nil || !strings.Contains(c.SetAside[0].Why, "ended") {
		t.Fatalf("an ended policy decides nothing: %+v", c)
	}
}

func TestConflict(t *testing.T) {
	a := pol("a", "", "", "", nil, nil)
	b := pol("b", "", "", "", nil, nil)
	if Conflict(a, b) == "" {
		t.Fatal("two plan policies for one service, always, conflict")
	}
	if Conflict(a, pol("b", policy.NCD, "", "", nil, nil)) != "" {
		t.Fatal("different ranks do not")
	}
	if Conflict(pol("a", "", "", "2026-12-31", nil, nil), pol("b", "", "2027-01-01", "", nil, nil)) != "" {
		t.Fatal("one ending the day before the other starts does not")
	}
	if Conflict(pol("a", "", "", "2027-01-01", nil, nil), pol("b", "", "2027-01-01", "", nil, nil)) == "" {
		t.Fatal("sharing one day does")
	}
	if Conflict(pol("a", "", "", "", []string{"medicaid"}, nil), pol("b", "", "", "", []string{"commercial"}, nil)) != "" {
		t.Fatal("different lines do not")
	}
	if Conflict(pol("a", "", "", "", nil, []string{"WA"}), pol("b", "", "", "", nil, []string{"WA", "OR"})) == "" {
		t.Fatal("a shared state does")
	}
	if Conflict(a, pol("a", "", "", "", nil, nil)) != "" {
		t.Fatal("two versions of one policy do not")
	}
	other := pol("c", "", "", "", nil, nil)
	other.Services = []policy.Service{{System: "http://www.ama-assn.org/go/cpt", Code: "72148"}}
	if Conflict(a, other) != "" {
		t.Fatal("different services do not")
	}
}
