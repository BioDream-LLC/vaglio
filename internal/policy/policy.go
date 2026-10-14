// Package policy is a medical policy encoded as arguments and attacks.
//
// The encoding is flat: one argument per named statement, attacks between names. There are
// three kinds of argument:
//
//   - evidence: a fact about the patient. It is present in a decision only when the request asserts it, with the record
//     it came from. Evidence the policy declares and the request does not assert is absent, and the proof says so.
//   - criterion: an intermediate conclusion of the policy, such as "the indication is not established". Always present.
//   - decision: approve or deny. Always present, and never an attacker: a decision is a conclusion, not a reason.
//
// People write policies in the language in lang.go, which compiles to this model; JSON is the same model as the engine
// reads it and the API serves it. Validate checks a policy is well formed, Lint what is legal but probably a mistake, and
// Check in package decide whether it decides well on every combination of evidence.
package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Kind is what an argument stands for.
type Kind string

const (
	Evidence  Kind = "evidence"
	Criterion Kind = "criterion"
	Decision  Kind = "decision"
)

// Draft is the status of a policy nobody has signed off.
const Draft = "draft"

// Verdict is what a decision argument decides.
type Verdict string

const (
	Approve Verdict = "approve"
	Deny    Verdict = "deny"
)

// Argument is one named statement of the policy.
type Argument struct {
	Name    string   `json:"name"`
	Kind    Kind     `json:"kind"`
	Text    string   `json:"text"`              // what it says, in words a reviewer reads
	Clause  string   `json:"clause"`            // the policy text it encodes, verbatim
	Verdict Verdict  `json:"verdict,omitempty"` // decisions only
	Find    []Source `json:"find,omitempty"`    // evidence only: where in a FHIR request it is documented
	// Documents are the LOINC attachment codes of the documents that would show this evidence (evidence only). A request
	// pended for it asks for them, and the PAS server asks the provider with a Da Vinci CDex Task.
	Documents []string `json:"documents,omitempty"`
}

// Source is one place a piece of evidence can be documented in a FHIR request. Exactly one field is set.
type Source struct {
	// Question is the linkId of a QuestionnaireResponse item - in practice the Da Vinci DTR questionnaire the payer asked
	// for - answered yes (documented present) or no (documented absent).
	Question string `json:"question,omitempty"`
	// Condition is a Condition coded with one of these codes: active and confirmed (or another verification status the
	// source accepts) is documented present, refuted is documented absent, and anything else is not addressed at all.
	Condition *Codes `json:"condition,omitempty"`
	// Procedure is a Procedure coded with one of these codes, completed (and, as the source asks, recent enough or lasting
	// long enough): documented present.
	Procedure *Procedure `json:"procedure,omitempty"`
	// Observation is a final Observation coded with one of these codes, whose value the source's test reads as present
	// or absent.
	Observation *Observation `json:"observation,omitempty"`
	// Medication is a medication the patient was given: completed MedicationDispenses and MedicationStatements coded with
	// one of these codes. A prescription alone (a MedicationRequest) is not read: it says a drug was ordered, not taken.
	Medication *Medication `json:"medication,omitempty"`
}

// Medication finds evidence in medications dispensed or taken - for step therapy, that a first drug was tried.
//
// Days count back from the request, as for procedures. MinDays is the days of the drug supplied (a MedicationDispense's
// daysSupply, summed over every matching dispense in the window) or taken (a MedicationStatement's effectivePeriod): a
// trial of "at least 90 days" is 90 days of the drug, however many fills it took.
type Medication struct {
	System     string   `json:"system"`
	Codes      []string `json:"codes"`
	WithinDays int      `json:"withinDays,omitempty"`
	MinDays    int      `json:"minDays,omitempty"`
	// NotTakenIsAbsent: a MedicationStatement with status not-taken documents the evidence absent. Only where "not
	// taken" answers the clause: a statement that a drug was never taken says nothing of a contraindication to it.
	NotTakenIsAbsent bool `json:"notTakenIsAbsent,omitempty"`
}

// SignOff is the clinician who approved a policy for use, and when.
type SignOff struct {
	By   string `json:"by"`   // name and credential, as they sign: "Dr A. Example, MD"
	Date string `json:"date"` // YYYY-MM-DD
}

// Codes is a set of codes from one code system. Codes match exactly: a policy names every code it means.
type Codes struct {
	System string   `json:"system"`
	Codes  []string `json:"codes"`
	// Present lists the Condition verification statuses that document the evidence present. Empty means confirmed only.
	// A clause about a *suspected* condition accepts provisional and differential too; one about a diagnosis does not.
	Present []string `json:"present,omitempty"`
	// OnsetDays (conditions only): the condition began at least this many days before the request - "pain for at least
	// three months". A condition with no onset date, or one whose onset cannot be placed that far back, is not evidence of
	// how long it has lasted, so it documents nothing either way.
	OnsetDays int `json:"onsetDays,omitempty"`
}

// PresentStatuses is the verification statuses that document a condition present.
func (c *Codes) PresentStatuses() []string {
	if len(c.Present) == 0 {
		return []string{"confirmed"}
	}
	return c.Present
}

// Procedure finds evidence in completed procedures.
//
// Days are counted back from the date of the request (the Claim's created date): a request without one cannot place
// anything in time, so a windowed source finds nothing in it. A date known only to the month or year counts only if the
// whole of that month or year is inside the window.
type Procedure struct {
	System string   `json:"system"`
	Codes  []string `json:"codes"`
	// WithinDays: performed (or, for a course, ended) at most this many days before the request.
	WithinDays int `json:"withinDays,omitempty"`
	// MinDays: a course of treatment whose performedPeriod lasted at least this many days. A shorter course, or one with
	// no period, documents nothing either way: it is not evidence that no long enough course happened.
	MinDays int `json:"minDays,omitempty"`
	// NotDoneIsAbsent: a Procedure with status not-done documents the evidence absent. Only where "this was not done"
	// answers the clause - a therapy not given - and not where it does not: an MRI not done says nothing of another MRI.
	NotDoneIsAbsent bool `json:"notDoneIsAbsent,omitempty"`
	// Times: the evidence is a count - at least this many completed procedures, on different days (in the window, if
	// there is one). Fewer document nothing either way: a request holding one block is not evidence that there was no
	// second. The finding cites every procedure counted. Times of 0 or 1 is a single procedure.
	Times int `json:"times,omitempty"`
	// ApartDays (with Times): a procedure counts only if it is at least this many days after the last one counted - "the
	// second diagnostic block at least two weeks after the first".
	ApartDays int `json:"apartDays,omitempty"`
}

// Observation finds evidence in final, amended or corrected observations. Exactly one value test is set.
type Observation struct {
	System     string   `json:"system"`
	Codes      []string `json:"codes"`
	WithinDays int      `json:"withinDays,omitempty"` // effective at most this many days before the request
	// Boolean: valueBoolean true is present, false absent.
	Boolean bool `json:"boolean,omitempty"`
	// AtLeast: a valueQuantity at or above it is present, below it absent. AtMost is the reverse. A value with a
	// comparator (<, >) or in another unit is not read: Vaglio does not convert units.
	AtLeast *Threshold `json:"atLeast,omitempty"`
	AtMost  *Threshold `json:"atMost,omitempty"`
	// Values: a valueCodeableConcept with one of Present's codes is present, one of Absent's absent.
	Values *ValueCodes `json:"values,omitempty"`
}

// Threshold is a quantity in one UCUM unit.
type Threshold struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// ValueCodes reads a coded observation value.
type ValueCodes struct {
	System  string   `json:"system"`
	Present []string `json:"present,omitempty"`
	Absent  []string `json:"absent,omitempty"`
}

// Service is a code a policy decides, as a prior authorization request names it.
type Service struct {
	System string `json:"system"`
	Code   string `json:"code"`
}

// Attack is one argument defeating another.
type Attack struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Policy is a complete encoded policy.
type Policy struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Version  string    `json:"version"`
	Source   string    `json:"source"`   // the document the clauses are quoted from
	Services []Service `json:"services"` // what it decides; a request for any other service is not this policy's
	// Questionnaire is the canonical URL of the questionnaire whose answers question sources read. A linkId means
	// something only within its questionnaire, so answers to any other are not read.
	Questionnaire string `json:"questionnaire,omitempty"`
	// Status "draft" marks a policy nobody has signed off: drafted by a machine, or still being written. It can be
	// checked and tried, but the server will not decide requests with it.
	Status string `json:"status,omitempty"`
	// Drug marks a policy for drugs (a medication, by HCPCS J-code or RxNorm). CMS reports drugs apart from other items
	// and services, and proposes shorter decision timeframes for them.
	Drug bool `json:"drug,omitempty"`
	// Signed is the clinical sign-off a policy carries once someone qualified has read it against its source. A draft has
	// none; vaglio signoff writes it and clears the draft status.
	Signed    *SignOff   `json:"signed,omitempty"`
	Arguments []Argument `json:"arguments"`
	Attacks   []Attack   `json:"attacks"`
}

// MaxEvidence bounds the evidence a policy may declare. Both the policy check and every decision's absence test
// enumerate combinations of evidence, so this is what keeps them exact rather than sampled. 2^16 solves of a policy
// this size take well under a second.
const MaxEvidence = 16

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	dateRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	loincRe = regexp.MustCompile(`^\d{1,7}-\d$`)
)

// Load reads a policy and validates it. Unknown fields are refused: a misspelt field is a rule silently not applied.
func Load(r io.Reader) (*Policy, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Ext is the file extension of a policy written in the language; any other file is read as JSON.
const Ext = ".vaglio"

// LoadFile reads a policy from a file: written in the language when it ends in .vaglio, JSON otherwise.
func LoadFile(path string) (*Policy, error) {
	if strings.HasSuffix(path, Ext) {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return Parse(path, string(src))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p, err := Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Validate checks the policy is well formed. It does not check that it decides well; that is Check in package decide,
// which tries every combination of evidence.
func (p *Policy) Validate() error {
	if !nameRe.MatchString(p.ID) {
		return fmt.Errorf("policy: id %q is not a usable name", p.ID)
	}
	if p.Status != "" && p.Status != Draft {
		return fmt.Errorf("policy %s: status is %q or nothing, not %q", p.ID, Draft, p.Status)
	}
	if s := p.Signed; s != nil {
		if strings.TrimSpace(s.By) == "" || !dateRe.MatchString(s.Date) {
			return fmt.Errorf("policy %s: a sign-off names who signed and the date, YYYY-MM-DD", p.ID)
		}
		if p.Status == Draft {
			return fmt.Errorf("policy %s: a draft cannot carry a sign-off", p.ID)
		}
	}
	byName := map[string]Argument{}
	verdicts := map[Verdict]int{}
	for _, a := range p.Arguments {
		if !nameRe.MatchString(a.Name) {
			return fmt.Errorf("policy %s: %q is not a usable name (lower case letters, digits and _)", p.ID, a.Name)
		}
		if _, dup := byName[a.Name]; dup {
			return fmt.Errorf("policy %s: %q is declared twice", p.ID, a.Name)
		}
		if a.Text == "" || a.Clause == "" {
			return fmt.Errorf("policy %s: %q needs both text and the clause it encodes", p.ID, a.Name)
		}
		if (len(a.Find) > 0 || len(a.Documents) > 0) && a.Kind != Evidence {
			return fmt.Errorf("policy %s: %q is %s; only evidence is found in a record", p.ID, a.Name, a.Kind)
		}
		for _, d := range a.Documents {
			if !loincRe.MatchString(d) {
				return fmt.Errorf("policy %s: %q: document %q is not a LOINC code (digits, a hyphen, a check digit)", p.ID, a.Name, d)
			}
		}
		for _, src := range a.Find {
			if err := src.validate(); err != nil {
				return fmt.Errorf("policy %s: %q: %w", p.ID, a.Name, err)
			}
		}
		switch a.Kind {
		case Evidence, Criterion:
			if a.Verdict != "" {
				return fmt.Errorf("policy %s: %q is %s and cannot carry a verdict", p.ID, a.Name, a.Kind)
			}
		case Decision:
			if a.Verdict != Approve && a.Verdict != Deny {
				return fmt.Errorf("policy %s: decision %q must have verdict approve or deny", p.ID, a.Name)
			}
			verdicts[a.Verdict]++
		default:
			return fmt.Errorf("policy %s: %q has unknown kind %q", p.ID, a.Name, a.Kind)
		}
		byName[a.Name] = a
	}
	questions := false
	for _, a := range p.Arguments {
		for _, src := range a.Find {
			questions = questions || src.Question != ""
		}
	}
	if questions && p.Questionnaire == "" {
		return fmt.Errorf("policy %s: question sources need the policy's questionnaire, the one their linkIds belong to", p.ID)
	}
	for _, sv := range p.Services {
		if sv.System == "" || sv.Code == "" {
			return fmt.Errorf("policy %s: a service needs both a system and a code", p.ID)
		}
	}
	if verdicts[Approve] == 0 {
		return fmt.Errorf("policy %s: no decision approves anything", p.ID)
	}
	if n := len(p.EvidenceNames()); n > MaxEvidence {
		return fmt.Errorf("policy %s: %d evidence arguments; at most %d can be checked exhaustively", p.ID, n, MaxEvidence)
	}
	for _, at := range p.Attacks {
		from, ok := byName[at.From]
		if !ok {
			return fmt.Errorf("policy %s: attack from undeclared %q", p.ID, at.From)
		}
		if _, ok := byName[at.To]; !ok {
			return fmt.Errorf("policy %s: attack on undeclared %q", p.ID, at.To)
		}
		if from.Kind == Decision {
			return fmt.Errorf("policy %s: decision %q attacks %q; a decision is a conclusion, not a reason", p.ID, at.From, at.To)
		}
	}
	return nil
}

func (s Source) validate() error {
	set := 0
	for _, b := range []bool{s.Question != "", s.Condition != nil, s.Procedure != nil, s.Observation != nil, s.Medication != nil} {
		if b {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("a source is exactly one of a question, a condition, a procedure, an observation or a medication")
	}
	switch {
	case s.Question != "":
		return nil
	case s.Condition != nil:
		if err := codes(s.Condition.System, s.Condition.Codes); err != nil {
			return fmt.Errorf("a condition source %w", err)
		}
		if s.Condition.OnsetDays < 0 {
			return fmt.Errorf("a condition source counts days from 0 up")
		}
		for _, v := range s.Condition.Present {
			switch v {
			case "confirmed", "provisional", "differential", "unconfirmed":
			default:
				// refuted documents absence and entered-in-error documents nothing; neither can mean present.
				return fmt.Errorf("verification status %q cannot document a condition present", v)
			}
		}
	case s.Procedure != nil:
		pr := s.Procedure
		if err := codes(pr.System, pr.Codes); err != nil {
			return fmt.Errorf("a procedure source %w", err)
		}
		if pr.WithinDays < 0 || pr.MinDays < 0 || pr.Times < 0 || pr.ApartDays < 0 {
			return fmt.Errorf("a procedure source counts days from 0 up")
		}
		if pr.ApartDays > 0 && pr.Times < 2 {
			return fmt.Errorf("apart counts the days between procedures, so it needs times 2 or more")
		}
		if pr.Times > 1 && pr.MinDays > 0 {
			return fmt.Errorf("a procedure source counts procedures (times) or a course's length (lasting), not both")
		}
	case s.Medication != nil:
		m := s.Medication
		if err := codes(m.System, m.Codes); err != nil {
			return fmt.Errorf("a medication source %w", err)
		}
		if m.WithinDays < 0 || m.MinDays < 0 {
			return fmt.Errorf("a medication source counts days from 0 up")
		}
	case s.Observation != nil:
		o := s.Observation
		if err := codes(o.System, o.Codes); err != nil {
			return fmt.Errorf("an observation source %w", err)
		}
		if o.WithinDays < 0 {
			return fmt.Errorf("an observation source counts days from 0 up")
		}
		tests := 0
		for _, b := range []bool{o.Boolean, o.AtLeast != nil, o.AtMost != nil, o.Values != nil} {
			if b {
				tests++
			}
		}
		if tests != 1 {
			return fmt.Errorf("an observation source reads its value exactly one way: yes-no, at least, at most, or coded values")
		}
		for _, t := range []*Threshold{o.AtLeast, o.AtMost} {
			if t != nil && t.Unit == "" {
				return fmt.Errorf("an observation threshold needs its unit")
			}
		}
		if v := o.Values; v != nil {
			if v.System == "" || len(v.Present)+len(v.Absent) == 0 {
				return fmt.Errorf("coded observation values need a system and at least one code")
			}
			for _, c := range v.Present {
				for _, d := range v.Absent {
					if c == d {
						return fmt.Errorf("value code %q cannot document both present and absent", c)
					}
				}
			}
		}
	}
	return nil
}

func codes(system string, list []string) error {
	if system == "" || len(list) == 0 {
		return fmt.Errorf("needs a system and at least one code")
	}
	for _, c := range list {
		if c == "" {
			return fmt.Errorf("has an empty code")
		}
	}
	return nil
}

// Decides reports whether the policy decides a service with the given code.
func (p *Policy) Decides(system, code string) bool {
	for _, s := range p.Services {
		if s.System == system && s.Code == code {
			return true
		}
	}
	return false
}

// Argument returns the argument with the given name.
func (p *Policy) Argument(name string) (Argument, bool) {
	for _, a := range p.Arguments {
		if a.Name == name {
			return a, true
		}
	}
	return Argument{}, false
}

// EvidenceNames returns every evidence argument, in declaration order.
func (p *Policy) EvidenceNames() []string {
	var out []string
	for _, a := range p.Arguments {
		if a.Kind == Evidence {
			out = append(out, a.Name)
		}
	}
	return out
}
