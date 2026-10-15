package server

import (
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/precedence"
)

// withCoverage gives a request a Coverage whose type is this Source of Payment Typology code.
func withCoverage(b map[string]any, sopt string) map[string]any {
	b["entry"] = append(pastest.Entries(b), map[string]any{"fullUrl": pastest.Base + "/Coverage/cov1", "resource": map[string]any{
		"resourceType": "Coverage", "id": "cov1", "status": "active",
		"type": map[string]any{"coding": []any{map[string]any{"system": precedence.SOPTSystem, "code": sopt}}}}})
	pastest.Map(pastest.Entries(b)[0])["insurance"] = []any{map[string]any{"sequence": 1, "focal": true,
		"coverage": map[string]any{"reference": "Coverage/cov1"}}}
	return b
}

// lumbarAs is the lumbar MRI policy under another id, authority and scope, deciding every request (it approves what
// the original denies, so which one decided shows).
func lumbarAs(t *testing.T, id, version, authority string, lines []string, effective string) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.ID, q.Version, q.Title, q.Authority, q.Lines, q.Effective = id, version, id, authority, lines, effective
	q.Arguments = nil
	for _, a := range p.Arguments {
		if a.Kind == policy.Decision && a.Verdict == policy.Deny {
			continue // no denial: with its denials gone it approves whenever nothing outstanding stands against it
		}
		q.Arguments = append(q.Arguments, a)
	}
	q.Attacks = nil
	for _, at := range p.Attacks {
		if _, ok := q.Argument(at.To); ok {
			q.Attacks = append(q.Attacks, at)
		}
	}
	return &q
}

func TestPrecedence(t *testing.T) {
	day := "2026-10-15"
	ncd := lumbarAs(t, "lumbar_ncd", "1", policy.NCD, []string{"medicare-advantage"}, "")
	e := newEnvWith(t, "", func(c *Config) {
		c.Policies["lumbar_ncd"] = ncd
		c.Today = func() string { return day }
	})
	// A Medicare Advantage member: the NCD decides, and the plan's policy is set aside as outranked.
	a := submit(t, e, withCoverage(request("", "WA", "", denies...), "111"))
	if a.Result.Policy != "lumbar_ncd" || a.Precedence == nil || !strings.Contains(a.Precedence.SetAside[0].Why, "outranked") {
		t.Fatalf("MA member: %+v %+v", a.Result.Policy, a.Precedence)
	}
	// A commercial member: the plan's policy decides, and denies.
	a = submit(t, e, withCoverage(request("", "WA", "", denies...), "51"))
	if a.Result.Policy != "lumbar_mri" || a.Decision != "deny" {
		t.Fatalf("commercial member: %s %s", a.Result.Policy, a.Decision)
	}
	// Nothing says the line: the plan's policy decides, but may not deny under the NCD's possible reach.
	a = submit(t, e, request("", "WA", "", denies...))
	if a.Result.Policy != "lumbar_mri" || a.Decision != "pend" || !strings.Contains(a.Why, "may fall under lumbar_ncd") {
		t.Fatalf("line unknown: %s %s %q", a.Result.Policy, a.Decision, a.Why)
	}
	c, _ := e.cases.Get(a.Case)
	if c.Precedence == nil || len(c.Precedence.Unsettled) != 1 {
		t.Fatalf("the case does not record why: %+v", c.Precedence)
	}
	// ...and approves as it would have.
	if a := submit(t, e, request("", "WA", "", approves...)); a.Decision != "approve" {
		t.Fatalf("line unknown, approvable: %s", a.Decision)
	}

	// A scheduled version takes over on its effective date, and the log records it.
	next := lumbarAs(t, "lumbar_mri", "0.2.0", "", nil, "2026-11-01")
	e = newEnvWith(t, "", func(c *Config) {
		c.Scheduled = map[string]*policy.Policy{"lumbar_mri": next}
		c.Today = func() string { return day }
	})
	if a := submit(t, e, request("", "WA", "", denies...)); a.Result.PolicyVersion != "0.1.0" || a.Decision != "deny" {
		t.Fatalf("before the day: %s %s", a.Result.PolicyVersion, a.Decision)
	}
	day = "2026-11-01"
	if a := submit(t, e, request("", "WA", "", denies...)); a.Result.PolicyVersion != "0.2.0" || a.Decision == "deny" {
		t.Fatalf("on the day: %s %s", a.Result.PolicyVersion, a.Decision)
	}
	found := false
	for _, r := range e.log.Records(0, 0) {
		found = found || r.Action == ActInForce
	}
	if !found {
		t.Fatal("putting the scheduled version in force is not in the audit log")
	}
}
