// Package cases is every request Vaglio has decided, and what happened to it after.
//
// A case is opened by each PAS decision. One the engine approves or denies is closed as it is opened; one it pends waits
// for a reviewer, who approves or denies it with a reason, and that decision is then delivered to the PAS server. The
// state of every case is derived from the audit log and nothing else: Open replays it, and every change is an audit record
// first and a change to the case second, so the screen can never show what the log does not say.
//
// The request Bundle a case was decided on is kept beside the log, named by its SHA-256, which the log records; a Bundle
// edited after the fact no longer matches its case and is refused rather than shown.
package cases

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/biodream-llc/vaglio/internal/seal"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/eligibility"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/precedence"
	"github.com/biodream-llc/vaglio/internal/reader"
)

// Status is where a case stands.
type Status string

const (
	Decided    Status = "decided"    // the engine approved or denied it
	Pended     Status = "pended"     // waiting for a reviewer
	Reviewed   Status = "reviewed"   // a reviewer decided it
	Superseded Status = "superseded" // a later request for the same service replaced it before anyone reviewed it
)

// Answer is what the provider is told, in the PAS decision service protocol's terms.
type Answer struct {
	Decision      string   `json:"decision"` // approve, deny or pend
	Why           string   `json:"why,omitempty"`
	Requested     []string `json:"requested,omitempty"`
	Questionnaire string   `json:"questionnaire,omitempty"`
	Attachments   []string `json:"attachments,omitempty"`
}

// Opening is everything a case is opened with.
type Opening struct {
	Policy        string `json:"policy"`
	PolicyVersion string `json:"policyVersion"`
	PolicyTitle   string `json:"policyTitle"`
	Item          string `json:"item"`
	Service       string `json:"service"`           // the code that matched the policy, system|code
	Claim         string `json:"claim"`             // the Claim's identifier, system|value, or its fullUrl
	Patient       string `json:"patient"`           // the Claim's patient reference
	Provider      string `json:"provider"`          // the Claim's provider reference
	Request       string `json:"request,omitempty"` // the PAS server's id for the request, when it gave one
	Bundle        string `json:"bundle"`            // SHA-256 of the stored request Bundle
	// Priority is the Claim's priority (processpriority: stat, normal, deferred). stat is an expedited request in the
	// CMS metrics; anything else, or nothing, a standard one.
	Priority string `json:"priority,omitempty"`
	// ProviderNPI is the requesting provider's NPI, when the Claim's provider resource carries one: what gold carding
	// counts by. A provider known only by a reference in one Bundle is not one provider across requests.
	ProviderNPI string `json:"providerNpi,omitempty"`
	// PatientState is the state of the patient's address: the region overrides and appeals are counted by.
	PatientState string `json:"patientState,omitempty"`
	// Line is the member's line of business, from the request's Coverage (precedence.LineOfBusiness): which of the
	// plan's appeal deadlines its letters give.
	Line string `json:"line,omitempty"`
	// Drug is a request decided by a drug policy. CMS counts drugs apart from other items and services.
	Drug     bool                `json:"drug,omitempty"`
	Answer   Answer              `json:"answer"`
	Result   *decide.Result      `json:"result"`
	Evidence evidence.Extraction `json:"evidence"`
	// Exemption is set when the request was approved because its provider is exempt from prior authorisation review
	// (gold carding), whatever the engine's own result, which Result still holds.
	Exemption *Exemption `json:"exemption,omitempty"`
	// Proposed is set when the engine would have denied, but only a clinician may deny: the denial it proposes, which
	// the case waits on a clinician to make or not.
	Proposed *Proposal `json:"proposed,omitempty"`
	// Channel is how a request that did not come from a PAS server arrived: fax, phone, mail or portal, keyed in by a
	// person. Its decisions go to the provider by letter, since no PAS server waits for them.
	Channel string `json:"channel,omitempty"`
	// KeyedBy is the account that keyed such a request in.
	KeyedBy string `json:"keyedBy,omitempty"`
	// Precedence is why this policy decided when other policies are for the same service.
	Precedence *precedence.Choice `json:"precedence,omitempty"`
	// Eligibility is the member file's answer, when the server has one: a request whose member was not covered on the
	// day pends for a person, whatever the policy decided.
	Eligibility *eligibility.Check `json:"eligibility,omitempty"`
}

// Exemption is a gold card approval: the program, and the provider's record it rests on.
type Exemption struct {
	Program      string   `json:"program"`
	Basis        string   `json:"basis,omitempty"` // the law or payer program, as the answer cites it
	NPI          string   `json:"npi"`
	Policy       string   `json:"policy"`
	LookBackDays int      `json:"lookBackDays"`
	Decisions    int      `json:"decisions"` // decided requests in the window
	Approved     int      `json:"approved"`
	MinRate      float64  `json:"minRate"`
	MinDecisions int      `json:"minDecisions"`
	Counted      []string `json:"counted"` // the cases counted, so the rate can be recomputed from the log
}

// Proposal is a denial the engine proposes to a clinician rather than issues.
type Proposal struct {
	Decision string `json:"decision"` // deny
	Why      string `json:"why"`      // what the denial would have told the provider
}

// Appeal is the outcome of an appeal of a denial, recorded once the appeal is decided wherever it was decided.
type Appeal struct {
	By      string `json:"by"`
	Time    string `json:"time"`
	Outcome string `json:"outcome"` // upheld (the denial stands) or overturned (approved on appeal)
	Reason  string `json:"reason"`
	// Decided is when the appeal was decided, YYYY-MM-DD, as the appeal record says; empty means when it was recorded.
	Decided string `json:"decided,omitempty"`
	// Reference is the appeal's id in the system that decided it, when it came from an appeals feed: a second delivery
	// of the same outcome is recognised by it and not recorded twice.
	Reference string `json:"reference,omitempty"`
}

// Extension is a decision timeframe extended, as Medicare Advantage (42 CFR 422.568(b)(2), 422.572(b)) and Medicaid
// rules allow: by up to 14 days, for one of the reasons the rules name, and once.
type Extension struct {
	By     string `json:"by"`
	Time   string `json:"time"`
	Days   int    `json:"days"`
	Reason string `json:"reason"` // ExtendRequested, ExtendEvidence or ExtendExceptional
	Note   string `json:"note"`   // what the notice to the enrollee says the delay is for
	Due    string `json:"due"`    // the new deadline, RFC 3339
}

// The reasons a timeframe may be extended, as 42 CFR 422.568(b)(2)(i) words them.
const (
	ExtendRequested   = "requested"   // the enrollee (or the provider for them) asked for it
	ExtendEvidence    = "evidence"    // more medical evidence, from a noncontract provider, that may change a denial
	ExtendExceptional = "exceptional" // extraordinary, exigent or other non-routine circumstances, in the enrollee's interest
)

// MaxExtensionDays is the longest extension the rules allow.
const MaxExtensionDays = 14

// Timeframes, from receipt of the request: CMS-0057-F's 7 calendar days for a standard request and 72 hours for an
// expedited one. A drug policy's timeframes are CMS-0062-P's proposed 72 and 24 hours, and a Part B drug's 72 hours may
// not be extended (42 CFR 422.568(b)(3)), so drug cases are never extended here.
func (c *Case) Timeframe() time.Duration {
	switch {
	case c.Drug && c.Expedited():
		return 24 * time.Hour
	case c.Drug:
		return 72 * time.Hour
	case c.Expedited():
		return 72 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// Due is when the decision is due: the timeframe from the request's receipt, or the extension's deadline.
func (c *Case) Due() time.Time {
	if c.Extension != nil {
		if t, err := time.Parse(time.RFC3339Nano, c.Extension.Due); err == nil {
			return t
		}
	}
	t, _ := time.Parse(time.RFC3339Nano, c.Created)
	return t.Add(c.Timeframe())
}

// Confirmation is a reviewer's verdict on one proposal: confirmed, it is evidence; rejected, it is nothing.
type Confirmation struct {
	reader.Proposal
	Reading   int    `json:"reading"`
	Index     int    `json:"index"`
	Confirmed bool   `json:"confirmed"`
	By        string `json:"by"`
	Time      string `json:"time"`
}

// Received is one fetch of documents sent for the request after it was decided.
type Received struct {
	Time      string   `json:"time"`
	Bundle    string   `json:"bundle"` // SHA-256 of a collection Bundle of what arrived
	Resources []string `json:"resources"`
}

// Final is what the case finally decided: approve or deny, or "" while it waits or after it was superseded.
func (c *Case) Final() string {
	switch {
	case c.Status == Reviewed && c.Review != nil:
		return c.Review.Decision
	case c.Status == Decided:
		return c.Answer.Decision
	}
	return ""
}

// Expedited is whether the request asked for an expedited decision.
func (c *Case) Expedited() bool { return c.Priority == "stat" }

// Review is a reviewer's decision on a pended case.
type Review struct {
	By          string `json:"by"`
	Time        string `json:"time"`
	Decision    string `json:"decision"` // approve or deny
	Reason      string `json:"reason"`
	ReviewerNPI string `json:"reviewerNpi,omitempty"`
	Credential  string `json:"credential,omitempty"` // the reviewer's, when their account records one
	// Automatic: no person decided. The policy approved the request on what a document model read in its documents,
	// every finding quoted word for word from them (serve -approve-from-documents). Only approvals are ever automatic.
	Automatic bool `json:"automatic,omitempty"`
	// Sampled: an automatic approval set aside for a reviewer to check after the fact, for quality.
	Sampled bool `json:"sampled,omitempty"`
}

// QA is a reviewer's check of an automatic approval, after the fact: whether they agree the documents show what the
// approval rests on. A disagreement does not undo the approval (the provider was told); it is what decides whether
// automatic approval stays on.
type QA struct {
	By    string `json:"by"`
	Time  string `json:"time"`
	Agree bool   `json:"agree"`
	Note  string `json:"note,omitempty"`
}

// Delivery is the last attempt to send a review to the PAS server.
type Delivery struct {
	Time  string `json:"time"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Final: the PAS server refused it (it has no such request, or someone there decided it first), so sending it again
	// would get the same answer. A person has to look; the case says so.
	Final bool `json:"final,omitempty"`
}

// Case is one decision request and its history.
type Case struct {
	ID      string `json:"id"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Status  Status `json:"status"`
	Opening
	Review   *Review   `json:"review,omitempty"`
	Delivery *Delivery `json:"delivery,omitempty"`
	Appeal   *Appeal   `json:"appeal,omitempty"`
	// Extension is the decision timeframe extended, while the case waited.
	Extension *Extension `json:"extension,omitempty"`
	// Readings are a model's proposals from the documents, each confirmed or rejected by a reviewer in Confirmations.
	Readings      []reader.Reading `json:"readings,omitempty"`
	Confirmations []Confirmation   `json:"confirmations,omitempty"`
	// Received is documents the provider sent after the request, fetched from the PAS server, each kept as a Bundle of
	// the resources received, by its SHA-256 like the request.
	Received     []Received `json:"received,omitempty"`
	SupersededBy string     `json:"supersededBy,omitempty"`
	QA           *QA        `json:"qa,omitempty"`
	// Work on the case (work.go): who has it, whether a physician must decide it, peer-to-peer calls, an appeal taken in.
	Assigned    *Assignment    `json:"assigned,omitempty"`
	Escalated   *Escalation    `json:"escalated,omitempty"`
	PeerToPeer  []PeerToPeer   `json:"peerToPeer,omitempty"`
	AppealTaken *AppealRequest `json:"appealTaken,omitempty"`
	// Notices are the letters mailed to the member about this case (internal/notice), and the ones that could not be.
	Notices []Notice `json:"notices,omitempty"`
	// PlanOutcome and Ruling are a shadow pilot's (shadow.go): what the plan's own process decided, and a clinician's
	// ruling where the two disagree.
	PlanOutcome *PlanOutcome `json:"planOutcome,omitempty"`
	Ruling      *Ruling      `json:"ruling,omitempty"`
}

// Notice is one letter to the member: sent, or why not.
type Notice struct {
	Time    string `json:"time"`
	Event   string `json:"event"`             // denied, approved, extended, appeal
	For     int64  `json:"for"`               // the audit record it is the notice of
	Service string `json:"service"`           // folder, lob
	ID      string `json:"id,omitempty"`      // the service's id for the mail piece, or the file written
	Lang    string `json:"lang,omitempty"`    // the letter's language
	Error   string `json:"error,omitempty"`   // why it was not sent
	Receipt string `json:"receipt,omitempty"` // the receipt the letter carries
}

// Audit actions this package writes and replays.
const (
	ActOpened     = "case.opened"
	ActReviewed   = "case.reviewed"
	ActSuperseded = "case.superseded"
	ActDelivered  = "case.delivered"
	ActViewed     = "case.viewed"
	ActAppealed   = "case.appealed"
	ActRead       = "case.read"
	ActConfirmed  = "case.confirmed"
	ActReceived   = "case.received"
	ActExtended   = "case.extended"
	ActQA         = "case.qa"
	ActNoticed    = "case.noticed"
)

// ErrAlreadyRecorded is an appeal delivered again by its feed: nothing is recorded, and nothing is wrong.
var ErrAlreadyRecorded = errors.New("this appeal's outcome is recorded already")

// ErrNotPending is an extension asked for a case that is not waiting for a decision.
var ErrNotPending = errors.New("only a case waiting for a decision can have its timeframe extended")

// Store is the cases, kept in step with the audit log.
type Store struct {
	mu      sync.Mutex
	log     *audit.Log
	bundles string
	cases   map[string]*Case
	order   []string
}

// Open replays the audit log into cases. bundles is the directory request Bundles are kept in.
func Open(log *audit.Log, bundles string) (*Store, error) {
	if err := os.MkdirAll(bundles, 0o700); err != nil {
		return nil, err
	}
	s := &Store{log: log, bundles: bundles, cases: map[string]*Case{}}
	for _, r := range log.Records(0, 0) {
		if err := s.apply(r); err != nil {
			return nil, fmt.Errorf("cases: replaying audit record %d: %w", r.Seq, err)
		}
	}
	return s, nil
}

func (s *Store) apply(r audit.Record) error {
	if ok, err := s.applyShadow(r.Action, r.Case, r.Time, r.Data); ok {
		return err
	}
	switch r.Action {
	case ActOpened:
		var o Opening
		if err := json.Unmarshal(r.Data, &o); err != nil {
			return err
		}
		c := &Case{ID: r.Case, Created: r.Time, Updated: r.Time, Status: Decided, Opening: o}
		if o.Answer.Decision == "pend" {
			c.Status = Pended
		}
		s.cases[c.ID] = c
		s.order = append(s.order, c.ID)
	case ActReviewed:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("review of unknown case %s", r.Case)
		}
		var rv Review
		if err := json.Unmarshal(r.Data, &rv); err != nil {
			return err
		}
		rv.Time = r.Time
		c.Review, c.Status, c.Updated, c.Delivery = &rv, Reviewed, r.Time, nil
	case ActSuperseded:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("supersession of unknown case %s", r.Case)
		}
		var d struct {
			By string `json:"by"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			return err
		}
		c.Status, c.SupersededBy, c.Updated = Superseded, d.By, r.Time
	case ActAppealed:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("appeal of unknown case %s", r.Case)
		}
		var a Appeal
		if err := json.Unmarshal(r.Data, &a); err != nil {
			return err
		}
		a.Time = r.Time
		c.Appeal, c.Updated = &a, r.Time
	case ActExtended:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("extension of unknown case %s", r.Case)
		}
		var x Extension
		if err := json.Unmarshal(r.Data, &x); err != nil {
			return err
		}
		x.Time = r.Time
		c.Extension, c.Updated = &x, r.Time
	case ActRead:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("reading of unknown case %s", r.Case)
		}
		var rd reader.Reading
		if err := json.Unmarshal(r.Data, &rd); err != nil {
			return err
		}
		c.Readings, c.Updated = append(c.Readings, rd), r.Time
	case ActConfirmed:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("confirmation on unknown case %s", r.Case)
		}
		var cf Confirmation
		if err := json.Unmarshal(r.Data, &cf); err != nil {
			return err
		}
		cf.Time = r.Time
		c.Confirmations, c.Updated = append(c.Confirmations, cf), r.Time
	case ActReceived:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("documents for unknown case %s", r.Case)
		}
		var rc Received
		if err := json.Unmarshal(r.Data, &rc); err != nil {
			return err
		}
		rc.Time = r.Time
		c.Received, c.Updated = append(c.Received, rc), r.Time
	case ActQA:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("quality check of unknown case %s", r.Case)
		}
		var q QA
		if err := json.Unmarshal(r.Data, &q); err != nil {
			return err
		}
		q.Time = r.Time
		c.QA, c.Updated = &q, r.Time
	case ActAssigned, ActEscalated, ActPeerToPeer, ActAppealTaken:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("%s on unknown case %s", r.Action, r.Case)
		}
		if _, err := s.applyWork(r.Action, c, r.Time, func(v any) error { return json.Unmarshal(r.Data, v) }); err != nil {
			return err
		}
		c.Updated = r.Time
	case ActNoticed:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("notice of unknown case %s", r.Case)
		}
		var n Notice
		if err := json.Unmarshal(r.Data, &n); err != nil {
			return err
		}
		n.Time = r.Time
		c.Notices = append(c.Notices, n)
	case ActDelivered:
		c, ok := s.cases[r.Case]
		if !ok {
			return fmt.Errorf("delivery of unknown case %s", r.Case)
		}
		var d Delivery
		if err := json.Unmarshal(r.Data, &d); err != nil {
			return err
		}
		d.Time = r.Time
		c.Delivery, c.Updated = &d, r.Time
	}
	return nil
}

// write appends a record and applies it: the log first, the case second.
func (s *Store) write(actor, action, caseID string, data any) (audit.Record, error) {
	r, err := s.log.Append(actor, action, caseID, data)
	if err != nil {
		return r, err
	}
	return r, s.apply(r)
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Open records a decision as a new case, keeping the Bundle it was made on. Earlier pended cases for the same Claim and
// item are superseded: the request was sent again, so the earlier one is no longer what anyone is waiting on.
func (s *Store) Open(o Opening, bundle []byte) (*Case, error) {
	sum := sha256.Sum256(bundle)
	o.Bundle = hex.EncodeToString(sum[:])
	path := filepath.Join(s.bundles, o.Bundle+".json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		// On disk before the record that names it: a case is never durable without its request.
		tmp := path + "." + newID() + ".new" // two copies of one request may arrive together
		if err := seal.WriteFileSync(tmp, "bundle", bundle, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			// The file is named by its content, so a copy another request put there first is this one. Windows refuses
			// to replace a file someone has open, where Unix would replace it silently.
			if _, serr := os.Stat(path); serr != nil {
				os.Remove(tmp)
				return nil, err
			}
			os.Remove(tmp)
		}
	}
	c, wait, err := func() (*Case, func() error, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := newID()
		actor := "pas"
		if o.KeyedBy != "" {
			actor = "user:" + o.KeyedBy
		}
		wait, err := s.writeLater(actor, ActOpened, id, o)
		if err != nil {
			return nil, nil, err
		}
		if o.Claim != "" {
			for _, old := range s.order {
				c := s.cases[old]
				if old != id && c.Status == Pended && c.Claim == o.Claim && c.Item == o.Item {
					if wait, err = s.writeLater("system", ActSuperseded, old, map[string]string{"by": id}); err != nil {
						return nil, nil, err
					}
				}
			}
		}
		c := *s.cases[id]
		return &c, wait, nil
	}()
	if err != nil {
		return nil, err
	}
	// The store's lock is released before the sync, so requests arriving together share one: the last record's sync
	// covers the ones before it. The decision is answered only after.
	if err := wait(); err != nil {
		return nil, err
	}
	return c, nil
}

// writeLater is write, returning before the record is synced with the wait for it.
func (s *Store) writeLater(actor, action, caseID string, data any) (func() error, error) {
	r, wait, err := s.log.AppendLater(actor, action, caseID, data)
	if err != nil {
		return nil, err
	}
	return wait, s.apply(r)
}

// ErrNotPended is a review of a case nobody is waiting on.
var ErrNotPended = errors.New("the case is not waiting for a reviewer")

// Review records a reviewer's decision on a pended case.
func (s *Store) Review(id, user string, rv Review) (*Case, error) {
	if rv.Decision != "approve" && rv.Decision != "deny" {
		return nil, fmt.Errorf("a review approves or denies, not %q", rv.Decision)
	}
	rv.Reason = strings.TrimSpace(rv.Reason)
	if rv.Reason == "" {
		// CMS-0057 requires a specific reason for a denial; an approval's reason is what the next reviewer reads.
		return nil, errors.New("a review needs its reason")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if c.Status != Pended {
		return nil, ErrNotPended
	}
	rv.By = user
	if _, err := s.write("user:"+user, ActReviewed, id, rv); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// AutomaticReviewer is the name an automatic approval is recorded under.
const AutomaticReviewer = "documents"

// AutoApprove records an automatic approval of a pended case: the policy's, on quote-checked findings from its
// documents. Only an approval: a denial or a pend always waits for a person.
func (s *Store) AutoApprove(id, reason string, sampled bool) (*Case, error) {
	rv := Review{Decision: "approve", Reason: strings.TrimSpace(reason), Automatic: true, Sampled: sampled,
		By: AutomaticReviewer}
	if rv.Reason == "" {
		return nil, errors.New("an automatic approval says what it rests on")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if c.Status != Pended {
		return nil, ErrNotPended
	}
	if _, err := s.write("system:"+AutomaticReviewer, ActReviewed, id, rv); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// CheckQA records a reviewer's after-the-fact check of an automatic approval. Each is checked once.
func (s *Store) CheckQA(id, user string, agree bool, note string) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if c.Review == nil || !c.Review.Automatic {
		return nil, errors.New("only an automatic approval is checked this way")
	}
	if c.QA != nil {
		return nil, errors.New("this approval has been checked")
	}
	note = strings.TrimSpace(note)
	if !agree && note == "" {
		return nil, errors.New("a disagreement says what the documents do not show")
	}
	if _, err := s.write("user:"+user, ActQA, id, QA{By: user, Agree: agree, Note: note}); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// ErrNotDenied is an appeal of a case that was not denied.
var ErrNotDenied = errors.New("only a denial is appealed")

// Appealed records the outcome of an appeal of a denied case. An appeal is decided once: a second outcome is refused,
// since the first is what the provider was told.
func (s *Store) Appealed(id, user string, a Appeal) (*Case, error) {
	if a.Outcome != "upheld" && a.Outcome != "overturned" {
		return nil, fmt.Errorf("an appeal is upheld or overturned, not %q", a.Outcome)
	}
	a.Reason = strings.TrimSpace(a.Reason)
	if a.Reason == "" {
		return nil, errors.New("an appeal's outcome needs its reason")
	}
	if a.Decided != "" {
		if _, err := time.Parse(time.DateOnly, a.Decided); err != nil {
			return nil, fmt.Errorf("decided %q is not YYYY-MM-DD", a.Decided)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if c.Final() != "deny" {
		return nil, ErrNotDenied
	}
	if t := c.AppealTaken; t != nil && a.Reference == "" && strings.EqualFold(user, t.Denier) {
		// 42 CFR 422.590(h)(2): an appeal is decided by someone who took no part in the denial.
		return nil, errors.New("you made this denial: its appeal is decided by someone else")
	}
	if c.Appeal != nil {
		if a.Reference != "" && c.Appeal.Reference == a.Reference && c.Appeal.Outcome == a.Outcome {
			out := *c
			return &out, ErrAlreadyRecorded
		}
		return nil, errors.New("this case's appeal is already recorded")
	}
	a.By = user
	if _, err := s.write("user:"+user, ActAppealed, id, a); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// Extend records a decision timeframe extended. It must be for a reason the rules allow, by at most maxDays (at most
// MaxExtensionDays), before the decision was due, on a case still waiting, never a drug case, and only once.
func (s *Store) Extend(id, user string, x Extension, maxDays int, now time.Time) (*Case, error) {
	switch x.Reason {
	case ExtendRequested, ExtendEvidence, ExtendExceptional:
	default:
		return nil, fmt.Errorf("an extension is for one of the reasons the rules allow (%s, %s, %s), not %q",
			ExtendRequested, ExtendEvidence, ExtendExceptional, x.Reason)
	}
	if maxDays <= 0 || maxDays > MaxExtensionDays {
		maxDays = MaxExtensionDays
	}
	if x.Days < 1 || x.Days > maxDays {
		return nil, fmt.Errorf("an extension is 1 to %d days", maxDays)
	}
	x.Note = strings.TrimSpace(x.Note)
	if x.Note == "" {
		return nil, errors.New("an extension says why: the enrollee is told the reason for the delay")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	switch {
	case c.Status != Pended:
		return nil, ErrNotPending
	case c.Drug:
		return nil, errors.New("a drug request's timeframe is not extended: a Part B drug's 72 hours may not be")
	case c.Extension != nil:
		return nil, errors.New("this case's timeframe has been extended once already")
	case !now.Before(c.Due()):
		// After the deadline the request is already late; an extension then would hide that, not excuse it.
		return nil, errors.New("the decision was due " + c.Due().UTC().Format(time.RFC3339) + "; a timeframe is extended before it runs out")
	}
	x.By, x.Due = user, c.Due().Add(time.Duration(x.Days)*24*time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.write("user:"+user, ActExtended, id, x); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// Read records a model's reading of one of a case's documents, asked for by user ("" for the automatic reader).
func (s *Store) Read(id, user string, rd reader.Reading) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cases[id]; !ok {
		return nil, os.ErrNotExist
	}
	actor := "user:" + user
	if user == "" {
		actor = "system:reader"
	}
	if _, err := s.write(actor, ActRead, id, rd); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// Confirm records a reviewer's verdict on a proposal. A proposal is decided once.
func (s *Store) Confirm(id, user string, reading, index int, confirmed bool) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if reading < 0 || reading >= len(c.Readings) || index < 0 || index >= len(c.Readings[reading].Proposals) {
		return nil, errors.New("no such proposal")
	}
	for _, cf := range c.Confirmations {
		if cf.Reading == reading && cf.Index == index {
			return nil, errors.New("that proposal has been decided")
		}
	}
	cf := Confirmation{Proposal: c.Readings[reading].Proposals[index], Reading: reading, Index: index, Confirmed: confirmed,
		By: user}
	if _, err := s.write("user:"+user, ActConfirmed, id, cf); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// Receive keeps documents sent for a case after it was decided, as a collection Bundle beside the request.
func (s *Store) Receive(id string, entries []map[string]any) (*Case, error) {
	if len(entries) == 0 {
		return nil, errors.New("nothing was received")
	}
	var refs []string
	for _, e := range entries {
		refs = append(refs, fmt.Sprint(e["fullUrl"]))
	}
	b, err := json.Marshal(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:])
	if err := seal.WriteFile(filepath.Join(s.bundles, name+".json"), "bundle", b, 0o600); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	for _, r := range c.Received {
		if r.Bundle == name {
			out := *c
			return &out, nil // the same documents again: nothing new
		}
	}
	if _, err := s.write("system", ActReceived, id, Received{Bundle: name, Resources: refs}); err != nil {
		return nil, err
	}
	out := *s.cases[id]
	return &out, nil
}

// Entries is the request a case was decided on with every document received since, as one list of Bundle entries.
// Each stored Bundle is checked against the hash the log recorded for it.
func (s *Store) Entries(c *Case) (map[string]any, error) {
	raw, err := s.Bundle(c)
	if err != nil {
		return nil, err
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	for _, r := range c.Received {
		more, err := seal.ReadFile(filepath.Join(s.bundles, r.Bundle+".json"), "bundle")
		if err != nil {
			return nil, err
		}
		if sum := sha256.Sum256(more); hex.EncodeToString(sum[:]) != r.Bundle {
			return nil, ErrBundleChanged
		}
		var rb map[string]any
		if err := json.Unmarshal(more, &rb); err != nil {
			return nil, err
		}
		entries, _ := b["entry"].([]any)
		add, _ := rb["entry"].([]any)
		b["entry"] = append(entries, add...)
	}
	return b, nil
}

// Delivered records an attempt to send a case's review to the PAS server. final says a failure is not worth retrying.
func (s *Store) Delivered(id string, err error, final bool) error {
	d := Delivery{OK: err == nil}
	if err != nil {
		d.Error, d.Final = err.Error(), final
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, werr := s.write("system", ActDelivered, id, d)
	return werr
}

// Noticed records a letter mailed to the member, or why it could not be.
func (s *Store) Noticed(id string, n Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cases[id]; !ok {
		return fmt.Errorf("no case %s", id)
	}
	_, err := s.write("notices", ActNoticed, id, n)
	return err
}

// Viewed records that a user read a case: a case holds patient data, and reading it is audited as changing it is.
func (s *Store) Viewed(id, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cases[id]; !ok {
		return os.ErrNotExist
	}
	_, err := s.log.Append("user:"+user, ActViewed, id, nil)
	return err
}

// Get returns a case.
func (s *Store) Get(id string) (*Case, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, false
	}
	out := *c
	return &out, true
}

// List returns cases, the pended ones first (oldest waiting first), then the rest newest first. status filters when set.
func (s *Store) List(status Status) []Case {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Case{}
	for _, id := range s.order {
		if c := s.cases[id]; status == "" || c.Status == status {
			out = append(out, *c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Status == Pended, out[j].Status == Pended
		if pi != pj {
			return pi
		}
		if pi {
			return out[i].Created < out[j].Created
		}
		return out[i].Created > out[j].Created
	})
	return out
}

// Select returns the cases f accepts, oldest first.
func (s *Store) Select(f func(*Case) bool) []Case {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Case
	for _, id := range s.order {
		if c := s.cases[id]; f == nil || f(c) {
			out = append(out, *c)
		}
	}
	return out
}

// Undelivered returns reviewed cases whose review has not reached the PAS server.
func (s *Store) Undelivered() []Case {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Case
	for _, id := range s.order {
		c := s.cases[id]
		if c.Status == Reviewed && c.Request != "" && (c.Delivery == nil || (!c.Delivery.OK && !c.Delivery.Final)) {
			out = append(out, *c)
		}
	}
	return out
}

// ErrBundleChanged is a stored Bundle that no longer matches the hash its case recorded.
var ErrBundleChanged = errors.New("the stored request does not match the one the case was decided on")

// Bundle returns the request a case was decided on, refusing one that has changed since.
func (s *Store) Bundle(c *Case) ([]byte, error) {
	raw, err := seal.ReadFile(filepath.Join(s.bundles, c.Bundle+".json"), "bundle")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != c.Bundle {
		return nil, ErrBundleChanged
	}
	return raw, nil
}

// History is every audit record about a case.
func (s *Store) History(id string) []audit.Record { return s.log.ForCase(id) }
