// Package goldcard exempts providers from prior authorisation review on their record, as state gold card laws require.
//
// A gold card law exempts a provider from prior authorisation for a service when enough of their recent requests for it
// were approved: Texas, Georgia, Kentucky and others each set a rate (80 to 100 percent), a look-back window (six or
// twelve months) and a minimum number of requests. Studies of the first programmes found few providers ever qualified,
// because the payer computed the record out of sight. Here the record is the audit log, every exemption names the cases
// it was computed from, and anyone with the log can recompute it.
//
// A provider is known by NPI. One known only by a reference inside a single Bundle cannot be counted across requests, so
// is never exempt: an exemption granted to the wrong provider would be care approved on someone else's record.
//
// Only decided requests count: approved or denied, by the engine or a reviewer. Requests approved by an exemption do not
// count toward the next one, or an exemption would renew itself forever on its own approvals; it lasts while the
// provider's reviewed record in the window still qualifies.
package goldcard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Program is one exemption rule, a state's law or a payer's own programme.
type Program struct {
	Name  string `json:"name"`
	Basis string `json:"basis,omitempty"` // what the answer cites, such as "Texas Insurance Code 4201.653"
	// States limits it to patients whose address is in one of these (two-letter codes). Empty: every request.
	States []string `json:"states,omitempty"`
	// Policies limits it to these policies' services. Empty: every policy.
	Policies        []string `json:"policies,omitempty"`
	MinApprovalRate float64  `json:"minApprovalRate"` // 0.9 is 90 percent
	LookBackDays    int      `json:"lookBackDays"`
	MinDecisions    int      `json:"minDecisions"`
	// Count says which of the provider's decisions make the record, for a program with States: "all" (the default) counts
	// every decision for the policy, wherever the patient lives; "states" counts only those for patients in the
	// program's states, as a law that covers only its own state's plans may require. An administrator can switch it in
	// the console; the switch is in the audit log and outlasts a restart.
	Count string `json:"count,omitempty"`
}

// Counting modes.
const (
	CountAll    = "all"
	CountStates = "states"
)

// counting is the program's mode, the default filled in.
func (p Program) counting() string {
	if p.Count == "" {
		return CountAll
	}
	return p.Count
}

// Config is the programs in force, tried in order: the first that exempts a request is the one cited.
type Config struct {
	Programs []Program `json:"programs"`
	mu       sync.RWMutex
}

// ActCounting is the audit action that records a change of a program's counting.
const ActCounting = "goldcard.counting"

// SetCounting changes which decisions a program counts.
func (c *Config) SetCounting(program, mode string) error {
	if mode != CountAll && mode != CountStates {
		return fmt.Errorf("counting is %q or %q, not %q", CountAll, CountStates, mode)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Programs {
		if c.Programs[i].Name == program {
			if mode == CountStates && len(c.Programs[i].States) == 0 {
				return fmt.Errorf("program %s names no states, so it has no states to count by", program)
			}
			c.Programs[i].Count = mode
			return nil
		}
	}
	return fmt.Errorf("no program %q", program)
}

// Snapshot is a copy of the programs as they now stand.
func (c *Config) Snapshot() []Program {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Program(nil), c.Programs...)
}

var stateRe = regexp.MustCompile(`^[A-Z]{2}$`)

// Load reads and validates a configuration. Unknown fields are refused: a misspelt limit is an exemption granted on
// rules nobody wrote.
func Load(path string, policies map[string]*policy.Policy) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(policies); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate checks every program's limits.
func (c *Config) Validate(policies map[string]*policy.Policy) error {
	if len(c.Programs) == 0 {
		return errors.New("no programs")
	}
	names := map[string]bool{}
	for i, p := range c.Programs {
		where := fmt.Sprintf("program %d (%s)", i+1, p.Name)
		switch {
		case strings.TrimSpace(p.Name) == "":
			return fmt.Errorf("program %d has no name", i+1)
		case names[p.Name]:
			return fmt.Errorf("%s: the name is used twice", where)
		case p.MinApprovalRate <= 0 || p.MinApprovalRate > 1:
			return fmt.Errorf("%s: minApprovalRate is a fraction above 0 and at most 1, such as 0.9", where)
		case p.LookBackDays < 1 || p.LookBackDays > 3660:
			return fmt.Errorf("%s: lookBackDays is 1 to 3660", where)
		case p.MinDecisions < 1:
			return fmt.Errorf("%s: minDecisions is at least 1; an exemption on no record is not one", where)
		case p.Count != "" && p.Count != CountAll && p.Count != CountStates:
			return fmt.Errorf("%s: count is %q or %q", where, CountAll, CountStates)
		case p.Count == CountStates && len(p.States) == 0:
			return fmt.Errorf("%s: count %q needs states", where, CountStates)
		}
		names[p.Name] = true
		for _, s := range p.States {
			if !stateRe.MatchString(s) {
				return fmt.Errorf("%s: %q is not a two-letter state code", where, s)
			}
		}
		for _, id := range p.Policies {
			if policies != nil && policies[id] == nil {
				return fmt.Errorf("%s: no policy %q is loaded", where, id)
			}
		}
	}
	return nil
}

func (p Program) applies(state, policyID string) bool {
	if len(p.Policies) > 0 && !contains(p.Policies, policyID) {
		return false
	}
	// A state rule needs a state: a patient whose address is unknown is not known to be covered by the law.
	return len(p.States) == 0 || contains(p.States, strings.ToUpper(state))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Standing is a provider's record for one policy under one program.
type Standing struct {
	Program   string  `json:"program"`
	NPI       string  `json:"npi"`
	Policy    string  `json:"policy"`
	Decisions int     `json:"decisions"`
	Approved  int     `json:"approved"`
	Rate      float64 `json:"rate"`
	Exempt    bool    `json:"exempt"`
	counted   []string
}

// evaluate is a provider's record for a policy in the program's window ending at now.
func (p Program) evaluate(history []cases.Case, npi, policyID string, now time.Time) Standing {
	s := Standing{Program: p.Name, NPI: npi, Policy: policyID}
	since := now.AddDate(0, 0, -p.LookBackDays)
	for i := range history {
		c := &history[i]
		if c.ProviderNPI != npi || c.Policy != policyID || c.Exemption != nil {
			continue
		}
		if p.counting() == CountStates && !contains(p.States, strings.ToUpper(c.PatientState)) {
			continue // a patient outside the program's states, or of no known state
		}
		at, err := time.Parse(time.RFC3339Nano, c.Created)
		if err != nil || at.Before(since) || at.After(now) {
			continue
		}
		switch c.Final() {
		case "approve":
			s.Approved++
		case "deny":
		default:
			continue // still waiting, or replaced by a resubmission
		}
		s.Decisions++
		s.counted = append(s.counted, c.ID)
	}
	if s.Decisions > 0 {
		s.Rate = float64(s.Approved) / float64(s.Decisions)
	}
	// Compared without division, so 9 of 10 meets 0.9 exactly.
	s.Exempt = s.Decisions >= p.MinDecisions && float64(s.Approved) >= p.MinApprovalRate*float64(s.Decisions)-1e-9
	return s
}

// Exemption is the first program exempting the provider from review of a request for the policy's service, or nil.
func (c *Config) Exemption(history []cases.Case, npi, state, policyID string, now time.Time) *cases.Exemption {
	if c == nil || npi == "" {
		return nil
	}
	for _, p := range c.Snapshot() {
		if !p.applies(state, policyID) {
			continue
		}
		if s := p.evaluate(history, npi, policyID, now); s.Exempt {
			return &cases.Exemption{Program: p.Name, Basis: p.Basis, NPI: npi, Policy: policyID,
				LookBackDays: p.LookBackDays, Decisions: s.Decisions, Approved: s.Approved, MinRate: p.MinApprovalRate,
				MinDecisions: p.MinDecisions, Counted: s.counted}
		}
	}
	return nil
}

// Standings is every provider's record under every program, for the policies each covers: who is exempt now, and who
// is close.
func (c *Config) Standings(history []cases.Case, now time.Time) []Standing {
	if c == nil {
		return []Standing{}
	}
	type key struct{ npi, policy string }
	seen := map[key]bool{}
	for i := range history {
		if h := &history[i]; h.ProviderNPI != "" {
			seen[key{h.ProviderNPI, h.Policy}] = true
		}
	}
	out := []Standing{}
	for _, p := range c.Snapshot() {
		for k := range seen {
			if len(p.Policies) > 0 && !contains(p.Policies, k.policy) {
				continue
			}
			if s := p.evaluate(history, k.npi, k.policy, now); s.Decisions > 0 {
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Program != b.Program {
			return a.Program < b.Program
		}
		if a.Exempt != b.Exempt {
			return a.Exempt
		}
		if a.Rate != b.Rate {
			return a.Rate > b.Rate
		}
		return a.NPI+a.Policy < b.NPI+b.Policy
	})
	return out
}

// Why is what the provider is told about an exemption.
func Why(e *cases.Exemption, title string) string {
	basis := e.Program
	if e.Basis != "" {
		basis = e.Program + " (" + e.Basis + ")"
	}
	return fmt.Sprintf("Approved under %s: the requesting provider (NPI %s) had %d of %d decided requests under %s approved "+
		"(%.0f%%; %.0f%% of at least %d is required) in the %d days before this request, so prior authorization review "+
		"is not required.", basis, e.NPI, e.Approved, e.Decisions, title,
		100*float64(e.Approved)/float64(e.Decisions), 100*e.MinRate, e.MinDecisions, e.LookBackDays)
}
