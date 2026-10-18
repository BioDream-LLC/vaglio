package cases

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// A shadow pilot (serve -shadow) decides the plan's live requests beside the plan's own process and answers none of
// them. What the plan decided arrives later from the plan's systems (PlanDecided), and a clinician rules on each
// disagreement (Rule): which decision the record supports.

const (
	ActPlanDecided = "shadow.plan_decided"
	ActRuled       = "shadow.ruled"
)

// PlanOutcome is what the plan's own process decided for a request Vaglio decided in shadow.
type PlanOutcome struct {
	Time      string `json:"time"`
	Decision  string `json:"decision"`            // approve or deny
	By        string `json:"by,omitempty"`        // who decided: a reviewer's role, a vendor, a program
	Decided   string `json:"decided,omitempty"`   // YYYY-MM-DD
	Appeal    string `json:"appeal,omitempty"`    // overturned or upheld, when the plan's decision was appealed
	Reference string `json:"reference,omitempty"` // the plan's id for the decision
}

// Ruling is a clinician's word on a disagreement: which decision the record supports.
type Ruling struct {
	Time       string `json:"time"`
	Supports   string `json:"supports"` // vaglio, plan, both or neither
	Note       string `json:"note"`
	By         string `json:"by"`
	Credential string `json:"credential,omitempty"`
	NPI        string `json:"npi,omitempty"`
}

// Rulings a clinician may give.
var Rulings = []string{"vaglio", "plan", "both", "neither"}

// ErrSameOutcome is the plan's outcome delivered again: nothing is recorded, and nothing is wrong.
var ErrSameOutcome = errors.New("this outcome is recorded already")

// Verdict is what Vaglio would have decided: the reviewed decision if a reviewer took up its pend, a denial it
// proposed to a clinician, or its answer.
func (c *Case) Verdict() string {
	switch {
	case c.Status == Reviewed && c.Review != nil:
		return c.Review.Decision
	case c.Proposed != nil && c.Status == Pended:
		return "deny"
	}
	return c.Answer.Decision
}

// Disagrees is whether Vaglio and the plan both decided, differently.
func (c *Case) Disagrees() bool {
	if c.PlanOutcome == nil {
		return false
	}
	v := c.Verdict()
	return (v == "approve" || v == "deny") && v != c.PlanOutcome.Decision
}

// PlanDecided records the plan's own decision on a shadow case.
func (s *Store) PlanDecided(id string, o PlanOutcome) (*Case, error) {
	o.Decision = strings.ToLower(strings.TrimSpace(o.Decision))
	switch o.Decision {
	case "approved", "a1":
		o.Decision = "approve"
	case "denied", "a3":
		o.Decision = "deny"
	}
	if o.Decision != "approve" && o.Decision != "deny" {
		return nil, fmt.Errorf("the plan's decision is approve or deny, not %q", o.Decision)
	}
	if o.Appeal = strings.ToLower(strings.TrimSpace(o.Appeal)); o.Appeal != "" && o.Appeal != "overturned" && o.Appeal != "upheld" {
		return nil, fmt.Errorf("an appeal is overturned or upheld, not %q", o.Appeal)
	}
	if o.Appeal != "" && o.Decision != "deny" {
		return nil, errors.New("only a denial is appealed")
	}
	if o.Decided != "" {
		if _, err := time.Parse(time.DateOnly, o.Decided); err != nil {
			return nil, fmt.Errorf("decided %q is not YYYY-MM-DD", o.Decided)
		}
	}
	o.By, o.Reference = strings.TrimSpace(o.By), strings.TrimSpace(o.Reference)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if p := c.PlanOutcome; p != nil {
		if p.Decision == o.Decision && p.Appeal == o.Appeal && p.By == o.By && p.Decided == o.Decided {
			out := *c
			return &out, ErrSameOutcome
		}
		if p.Appeal != "" || o.Decision != p.Decision {
			return nil, errors.New("the plan's decision on this case is recorded already, differently")
		}
		// The same decision again, now with its appeal or more detail: recorded as the newer word.
	}
	if _, err := s.write("shadow-feed", ActPlanDecided, id, o); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// Rule records a clinician's ruling on a disagreement. The caller has checked the user is a clinician.
func (s *Store) Rule(id, user string, r Ruling) (*Case, error) {
	r.Supports = strings.ToLower(strings.TrimSpace(r.Supports))
	ok := false
	for _, k := range Rulings {
		ok = ok || k == r.Supports
	}
	if !ok {
		return nil, fmt.Errorf("a ruling supports vaglio, plan, both or neither, not %q", r.Supports)
	}
	if r.Note = strings.TrimSpace(r.Note); r.Note == "" {
		return nil, errors.New("a ruling needs its reason: what in the record supports it")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, found := s.cases[id]
	if !found {
		return nil, os.ErrNotExist
	}
	if !c.Disagrees() {
		return nil, errors.New("Vaglio and the plan do not disagree on this case: there is nothing to rule on")
	}
	r.By = user
	if _, err := s.write("user:"+user, ActRuled, id, r); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// applyShadow applies a shadow record; ok is false for any other action.
func (s *Store) applyShadow(action, id, t string, data json.RawMessage) (bool, error) {
	switch action {
	case ActPlanDecided:
		c, ok := s.cases[id]
		if !ok {
			return true, fmt.Errorf("plan outcome of unknown case %s", id)
		}
		var o PlanOutcome
		if err := json.Unmarshal(data, &o); err != nil {
			return true, err
		}
		o.Time = t
		c.PlanOutcome, c.Updated = &o, t
		return true, nil
	case ActRuled:
		c, ok := s.cases[id]
		if !ok {
			return true, fmt.Errorf("ruling on unknown case %s", id)
		}
		var r Ruling
		if err := json.Unmarshal(data, &r); err != nil {
			return true, err
		}
		r.Time = t
		c.Ruling, c.Updated = &r, t
		return true, nil
	}
	return false, nil
}
