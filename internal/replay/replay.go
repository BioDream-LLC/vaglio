// Package replay runs a new version of a policy over the requests already decided under it, and says which decisions
// would change.
//
// A policy change is a change to every future decision, and the only honest way to know what it does is to run it on
// real requests. Every case keeps the exact Bundle it was decided on (refused if it has changed since), so a replay reads
// the same evidence the original decision read, and the difference is the policy's alone.
//
// Nothing a replay finds changes a case: it is a report for the policy's authors. The changes that matter most are named:
// a request the new version would deny that a reviewer approved, or approve that a reviewer denied, is a clinician
// disagreeing with the new policy in advance.
package replay

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Row is one case whose decision would change, or could not be replayed.
type Row struct {
	Case    string `json:"case"`
	Created string `json:"created"`
	Claim   string `json:"claim"`
	Item    string `json:"item"`
	Version string `json:"version"` // of the policy that decided it
	Was     string `json:"was"`     // the engine's decision then: approve, deny or pend
	Final   string `json:"final,omitempty"`
	Now     string `json:"now,omitempty"` // the new version's: approve, deny or pend
	Reason  string `json:"reason,omitempty"`
	Note    string `json:"note,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Report is a replay's result.
type Report struct {
	Policy      string         `json:"policy"`
	Version     string         `json:"version"`
	CheckOK     bool           `json:"checkOk"` // the new version passes its exhaustive check
	Warnings    []string       `json:"warnings"`
	Cases       int            `json:"cases"` // replayed
	Unchanged   int            `json:"unchanged"`
	Changed     int            `json:"changed"`
	Errors      int            `json:"errors"`
	Transitions map[string]int `json:"transitions"` // "pend → approve": count
	Rows        []Row          `json:"rows"`        // the changed and the failed
}

// verdict is an outcome as the provider hears it.
func verdict(o decide.Outcome) string {
	switch o {
	case decide.Approved:
		return "approve"
	case decide.Denied:
		return "deny"
	}
	return "pend"
}

// covers is whether the replay applies to a case: decided under the same policy, or for a service the new one decides.
func covers(p *policy.Policy, c *cases.Case) bool {
	if c.Policy == p.ID {
		return true
	}
	system, code, ok := strings.Cut(c.Service, "|")
	return ok && p.Decides(system, code)
}

// Run replays p over every case it covers. Superseded cases are left out: nobody decided them.
func Run(store *cases.Store, p *policy.Policy) Report {
	rep := Report{Policy: p.ID, Version: p.Version, Transitions: map[string]int{}, Rows: []Row{}}
	ck := decide.Check(p)
	rep.CheckOK = ck.OK()
	rep.Warnings = policy.Lint(p)
	if rep.Warnings == nil {
		rep.Warnings = []string{}
	}
	for _, c := range store.Select(func(c *cases.Case) bool { return c.Status != cases.Superseded && covers(p, c) }) {
		rep.Cases++
		row := Row{Case: c.ID, Created: c.Created, Claim: c.Claim, Item: c.Item, Version: c.PolicyVersion, Final: c.Final()}
		if c.Result != nil {
			row.Was = verdict(c.Result.Outcome)
		}
		now, reason, err := redecide(store, p, &c)
		if err != nil {
			row.Error = err.Error()
			rep.Errors++
			rep.Rows = append(rep.Rows, row)
			continue
		}
		row.Now, row.Reason = now, reason
		if row.Now == row.Was {
			rep.Unchanged++
			continue
		}
		rep.Changed++
		rep.Transitions[row.Was+" → "+row.Now]++
		row.Note = note(row)
		rep.Rows = append(rep.Rows, row)
	}
	return rep
}

// Decide decides one stored case again under p, on the request Bundle it was decided on. A case whose service p does
// not decide is an error.
func Decide(store *cases.Store, p *policy.Policy, c *cases.Case) (*decide.Result, error) {
	if system, code, _ := strings.Cut(c.Service, "|"); !p.Decides(system, code) {
		return nil, fmt.Errorf("policy %s v%s does not decide %s", p.ID, p.Version, c.Service)
	}
	req, err := request(store, c)
	if err != nil {
		return nil, err
	}
	return decide.Decide(p, evidence.ExtractWith(p, req, c.Evidence.FromOutside()).Request(p.ID))
}

func request(store *cases.Store, c *cases.Case) (*pas.Request, error) {
	raw, err := store.Bundle(c)
	if err != nil {
		return nil, err
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		return nil, fmt.Errorf("the stored request no longer reads: %s", strings.Join(problems, "; "))
	}
	return req, nil
}

func redecide(store *cases.Store, p *policy.Policy, c *cases.Case) (string, string, error) {
	raw, err := store.Bundle(c)
	if err != nil {
		return "", "", err
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", "", err
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		return "", "", fmt.Errorf("the stored request no longer reads: %s", strings.Join(problems, "; "))
	}
	if system, code, _ := strings.Cut(c.Service, "|"); !p.Decides(system, code) {
		return "pend", notDecided, nil // no longer this policy's: the PAS server would keep its own answer
	}
	res, err := decide.Decide(p, evidence.ExtractWith(p, req, c.Evidence.FromOutside()).Request(p.ID))
	if err != nil {
		return "", "", err
	}
	return verdict(res.Outcome), string(res.Reason), nil
}

// notDecided is the reason for a case whose service the new version no longer decides.
const notDecided = "not_decided"

func note(r Row) string {
	switch {
	case r.Reason == notDecided:
		return "the new version no longer decides this service; the PAS server's own rules would answer"
	case r.Now == "deny" && r.Final == "approve" && r.Was == "pend":
		return "would deny what a reviewer approved"
	case r.Now == "approve" && r.Final == "deny" && r.Was == "pend":
		return "would approve what a reviewer denied"
	case r.Now == "deny" && r.Was == "approve":
		return "would deny what was approved"
	case r.Now == "approve" && r.Was == "deny":
		return "would approve what was denied"
	case r.Now == "pend" && r.Was != "pend":
		return "would send to review, or ask for more, what was decided at once"
	case r.Was == "pend" && r.Final == "":
		return "would decide a request still waiting for review"
	case r.Was == "pend":
		return "would decide at once what needed a reviewer"
	}
	return ""
}
