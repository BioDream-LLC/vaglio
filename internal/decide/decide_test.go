package decide

import (
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
)

func lumbar(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile("../../policies/lumbar_mri.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ev(name string, f Finding) Assertion {
	return Assertion{Argument: name, Finding: f, Citation: Citation{Reference: "DocumentReference/test", Quote: "test quote"}}
}

func argNames(items []Item) string {
	var s []string
	for _, i := range items {
		s = append(s, i.Argument)
	}
	return strings.Join(s, ",")
}

func TestDecisions(t *testing.T) {
	p := lumbar(t)
	cases := []struct {
		name        string
		evidence    []Assertion
		outcome     Outcome
		reason      Reason
		decision    string
		missing     string
		assumptions string
		relied      string
	}{
		{
			name:    "an empty request is a question, not a denial",
			outcome: Pended, reason: NeedsInformation, decision: "deny_not_indicated",
			missing: "red_flag,conservative_therapy_6wk,surgical_planning",
		},
		{
			name:     "failed conservative therapy approves, assuming no recent study",
			evidence: []Assertion{ev("conservative_therapy_6wk", Present)},
			outcome:  Approved, reason: Decided, decision: "approve",
			assumptions: "recent_same_imaging",
		},
		{
			name: "every indication documented absent is a denial, and says which absences it rests on",
			evidence: []Assertion{
				ev("red_flag", Absent), ev("conservative_therapy_6wk", Absent), ev("surgical_planning", Absent),
			},
			outcome: Denied, reason: Decided, decision: "deny_not_indicated",
			relied: "red_flag,conservative_therapy_6wk,surgical_planning",
		},
		{
			name: "a recent study pends while a change in condition is unaddressed",
			evidence: []Assertion{
				ev("conservative_therapy_6wk", Present), ev("recent_same_imaging", Present),
			},
			outcome: Pended, reason: NeedsInformation, decision: "deny_duplicate",
			missing: "red_flag,new_or_worsening_symptoms",
		},
		{
			name: "a recent study with no change documented is a denial",
			evidence: []Assertion{
				ev("conservative_therapy_6wk", Present), ev("recent_same_imaging", Present),
				ev("new_or_worsening_symptoms", Absent), ev("red_flag", Absent),
			},
			outcome: Denied, reason: Decided, decision: "deny_duplicate",
			relied: "red_flag,new_or_worsening_symptoms",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Decide(p, Request{Policy: p.ID, Evidence: c.evidence})
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome != c.outcome || r.Reason != c.reason || r.Decision != c.decision {
				t.Fatalf("got %s/%s on %s, want %s/%s on %s", r.Outcome, r.Reason, r.Decision, c.outcome, c.reason, c.decision)
			}
			if got := argNames(r.Missing); got != c.missing {
				t.Errorf("missing: got %q, want %q", got, c.missing)
			}
			if got := argNames(r.Assumptions); got != c.assumptions {
				t.Errorf("assumptions: got %q, want %q", got, c.assumptions)
			}
			if got := argNames(r.Relied); got != c.relied {
				t.Errorf("relied: got %q, want %q", got, c.relied)
			}
			if r.Proof == nil || r.Proof.Argument != c.decision {
				t.Errorf("proof does not conclude %s", c.decision)
			}
		})
	}
}

// TestNoDenialRestsOnUnaddressedEvidence is the product's guarantee, checked by brute force over every request the
// example policy can receive: each evidence argument present, documented absent, or unaddressed (3^5 requests). For
// each denial issued, no combination of the unaddressed evidence may change the outcome. This deliberately does not
// reuse the minimal-set search Decide uses.
func TestNoDenialRestsOnUnaddressedEvidence(t *testing.T) {
	p := lumbar(t)
	names := p.EvidenceNames()
	total := 1
	for range names {
		total *= 3
	}
	denials := 0
	for code := 0; code < total; code++ {
		var req Request
		var open []string
		present := map[string]*Citation{}
		c := code
		for _, n := range names {
			switch c % 3 {
			case 0:
				open = append(open, n)
			case 1:
				req.Evidence = append(req.Evidence, ev(n, Present))
				present[n] = &Citation{}
			case 2:
				req.Evidence = append(req.Evidence, ev(n, Absent))
			}
			c /= 3
		}
		r, err := Decide(p, req)
		if err != nil {
			t.Fatal(err)
		}
		if r.Outcome != Denied {
			continue
		}
		denials++
		for m := 1; m < 1<<uint(len(open)); m++ {
			with := map[string]*Citation{}
			for k, v := range present {
				with[k] = v
			}
			for i, n := range open {
				if m&(1<<uint(i)) != 0 {
					with[n] = &Citation{}
				}
			}
			if got := solve(p, with).outcome; got != Denied {
				t.Fatalf("request %d was denied, but adding unaddressed evidence gives %s", code, got)
			}
		}
	}
	if denials == 0 {
		t.Fatal("no request was ever denied; the test proved nothing")
	}
}

func TestRequestsRefused(t *testing.T) {
	p := lumbar(t)
	bad := map[string]Assertion{
		"no quote":        {Argument: "red_flag", Finding: Present, Citation: Citation{Reference: "DocumentReference/1"}},
		"undeclared":      ev("made_up", Present),
		"criterion":       ev("duplicate_study", Present),
		"no finding":      {Argument: "red_flag", Citation: Citation{Reference: "x", Quote: "y"}},
		"unknown finding": {Argument: "red_flag", Finding: "maybe", Citation: Citation{Reference: "x", Quote: "y"}},
	}
	for name, a := range bad {
		if _, err := Decide(p, Request{Evidence: []Assertion{a}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Decide(p, Request{Evidence: []Assertion{ev("red_flag", Present), ev("red_flag", Absent)}}); err == nil {
		t.Error("the same evidence both present and absent was accepted")
	}
	if _, err := Decide(p, Request{Policy: "other"}); err == nil {
		t.Error("a request for another policy was accepted")
	}
}

func TestCheckPassesExample(t *testing.T) {
	r := Check(lumbar(t))
	if !r.OK() {
		t.Fatalf("example policy fails its own check: %+v", r)
	}
	if r.Combinations != 32 {
		t.Fatalf("checked %d combinations, want 32", r.Combinations)
	}
}

// TestCheckFindsConflict removes one attack - a red flag no longer overrides the duplicate-study denial - and expects
// Check to find the evidence on which the policy then both approves and denies.
func TestCheckFindsConflict(t *testing.T) {
	p := lumbar(t)
	var kept []policy.Attack
	for _, a := range p.Attacks {
		if !(a.From == "red_flag" && a.To == "deny_duplicate") {
			kept = append(kept, a)
		}
	}
	p.Attacks = kept
	r := Check(p)
	if r.OK() || len(r.Defects) == 0 {
		t.Fatal("check passed a policy that approves and denies the same request")
	}
	found := false
	for _, set := range r.Defects {
		s := strings.Join(set, ",")
		if strings.Contains(s, "red_flag") && strings.Contains(s, "recent_same_imaging") {
			found = true
		}
	}
	if !found {
		t.Fatalf("defects %v do not include red flag with a recent study", r.Defects)
	}
}

func TestContradictedEvidence(t *testing.T) {
	p := lumbar(t)
	// A recent study, failed therapy, no change in symptoms - and a record that both asserts and denies a red flag. The
	// denial depends on the red flag, so it goes to a person.
	req := Request{Evidence: []Assertion{
		ev("conservative_therapy_6wk", Present), ev("recent_same_imaging", Present), ev("new_or_worsening_symptoms", Absent),
	}, Contradicted: []string{"red_flag"}}
	r, err := Decide(p, req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != Pended || r.Reason != ContradictoryRecord || argNames(r.Contradicted) != "red_flag" {
		t.Fatalf("got %s/%s contradicted %q", r.Outcome, r.Reason, argNames(r.Contradicted))
	}
	if len(r.Missing) != 0 {
		t.Errorf("contradicted evidence is also listed as missing: %q", argNames(r.Missing))
	}

	// A contradiction the outcome does not depend on changes nothing: with a red flag documented present, surgical
	// planning cannot affect the approval.
	r, err = Decide(p, Request{Evidence: []Assertion{ev("red_flag", Present)}, Contradicted: []string{"surgical_planning"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != Approved || len(r.Contradicted) != 0 {
		t.Fatalf("got %s/%s contradicted %q", r.Outcome, r.Reason, argNames(r.Contradicted))
	}

	for name, bad := range map[string]Request{
		"asserted and contradicted": {Evidence: []Assertion{ev("red_flag", Present)}, Contradicted: []string{"red_flag"}},
		"not evidence":              {Contradicted: []string{"duplicate_study"}},
		"undeclared":                {Contradicted: []string{"made_up"}},
	} {
		if _, err := Decide(p, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestToApprove: a pended or denied result says each smallest route to approval; an approval says none.
func TestToApprove(t *testing.T) {
	p := lumbar(t)
	cite := Citation{Reference: "QuestionnaireResponse/q", Quote: "answered"}
	res, err := Decide(p, Request{Policy: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Pended || len(res.ToApprove) == 0 {
		t.Fatalf("%s %d routes", res.Outcome, len(res.ToApprove))
	}
	for _, route := range res.ToApprove {
		if len(route) != 1 {
			t.Errorf("an empty request's smallest routes are one indication each: %+v", route)
		}
	}
	// Denied as a duplicate study: no red flag, no change. Approval now needs the record to be wrong about one of them.
	res, _ = Decide(p, Request{Policy: p.ID, Evidence: []Assertion{
		{Argument: "conservative_therapy_6wk", Finding: Present, Citation: cite},
		{Argument: "recent_same_imaging", Finding: Present, Citation: cite},
		{Argument: "new_or_worsening_symptoms", Finding: Absent, Citation: cite},
		{Argument: "red_flag", Finding: Absent, Citation: cite},
	}})
	if res.Outcome != Denied || len(res.ToApprove) == 0 {
		t.Fatalf("%s %+v", res.Outcome, res.ToApprove)
	}
	for _, route := range res.ToApprove {
		for _, c := range route {
			if !c.ContradictsRecord || c.Citation == nil {
				t.Errorf("every route contradicts a documented absence: %+v", c)
			}
		}
	}
	res, _ = Decide(p, Request{Policy: p.ID, Evidence: []Assertion{
		{Argument: "conservative_therapy_6wk", Finding: Present, Citation: cite},
		{Argument: "recent_same_imaging", Finding: Absent, Citation: cite}}})
	if res.Outcome != Approved || res.ToApprove != nil {
		t.Errorf("%s %+v", res.Outcome, res.ToApprove)
	}
}

func TestDecisionMapAgreesWithDecide(t *testing.T) {
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecisionMap(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Outcomes) != 1<<len(m.Evidence) || len(m.Decisions) != len(m.Outcomes) {
		t.Fatalf("sizes: %d %d", len(m.Outcomes), len(m.Decisions))
	}
	for mask := range m.Outcomes {
		var req Request
		for i, n := range m.Evidence {
			f := Absent
			if mask&(1<<i) != 0 {
				f = Present
			}
			req.Evidence = append(req.Evidence, Assertion{Argument: n, Finding: f, Citation: Citation{Reference: "r", Quote: "q"}})
		}
		res, err := Decide(p, req)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Outcomes[mask]; got != res.Outcome[0] {
			t.Errorf("mask %d: map %c, decide %s", mask, got, res.Outcome)
		}
	}
}
