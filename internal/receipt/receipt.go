// Package receipt gives every decision a short receipt that anyone can check against the audit log.
//
// A receipt is the first 64 bits of the hash of the audit record that made the decision, written VG-XXXX-XXXX-XXXX-XXXX.
// The record's hash covers the decision and everything recorded with it, and chains to every record before it, so the
// receipt names one decision and only one: if that record were edited, its hash, and so the receipt, would no longer
// match, and if any record before it were edited the chain would no longer verify.
//
// Checking a receipt reveals no patient data: the decision, when, under which policy and clause, who made it (the
// engine, a reviewer or a clinician; never a name), whether a later decision replaced it, and where the record sits in
// the chain. Whoever holds a copy of the log (a regulator, say) can check it offline with vaglio receipt.
package receipt

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
)

// prefix starts every receipt.
const prefix = "VG"

// digits is how many hex digits of the record's hash a receipt keeps: 64 bits, too many to guess.
const digits = 16

// Code is the receipt for the record with this hash, or "" for a hash too short to have one.
func Code(hash string) string {
	if len(hash) < digits {
		return ""
	}
	h := strings.ToUpper(hash[:digits])
	return prefix + "-" + h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-" + h[12:16]
}

// Normalize turns what someone typed or scanned into the hash prefix a receipt stands for: case, spaces and dashes do
// not matter, and the VG is optional. It is "" for anything that is not a receipt.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "", "\u2013", "", "\u2014", "").Replace(s)
	s = strings.TrimPrefix(s, strings.ToLower(prefix))
	if len(s) != digits {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return s
}

// Decisions are the audit actions that decide a request and so carry a receipt.
var Decisions = map[string]bool{cases.ActOpened: true, cases.ActReviewed: true, cases.ActAppealed: true}

// Check is what checking a receipt tells anyone who has it.
type Check struct {
	Receipt string `json:"receipt"`
	// Decision is what was decided: approve, deny or pend, as the provider was told; for an appeal, overturned (approved
	// on appeal) or upheld.
	Decision string `json:"decision"`
	// By is who made it: "engine" (the policy, unaided), "documents" (the policy, on quote-checked findings a document
	// model read in the request's documents), "reviewer", "clinician" (a reviewer recorded as one), or
	// "appeal" (an appeal decided wherever appeals are decided).
	By            string `json:"by"`
	Credential    string `json:"credential,omitempty"` // a clinician's credential, as their account records it
	Time          string `json:"time"`
	Policy        string `json:"policy"`
	PolicyVersion string `json:"policyVersion"`
	PolicyTitle   string `json:"policyTitle"`
	// Clause is the policy's own words the engine's decision rests on, when it rests on one.
	Clause string `json:"clause,omitempty"`
	// GoldCard: approved because the provider is exempt from review.
	GoldCard bool `json:"goldCard,omitempty"`
	// Proposed: the engine would have denied, and proposed the denial to a clinician instead of issuing it.
	Proposed bool `json:"proposed,omitempty"`
	// Later is every decision recorded on the same request after this one, oldest first: a review of a pended
	// request, an appeal of a denial.
	Later []Later `json:"later,omitempty"`
	// Record is where the decision sits in the log.
	Record Position `json:"record"`
	// Chain is the log as it stands now: every record from the first to Head verifies.
	Chain Chain `json:"chain"`
}

// Later is a decision recorded on the same request after the one checked.
type Later struct {
	Receipt  string `json:"receipt"`
	Decision string `json:"decision"`
	By       string `json:"by"`
	Time     string `json:"time"`
}

// Position is a record's place in the chain.
type Position struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
	Prev string `json:"prev"`
}

// Chain is the state of the whole log.
type Chain struct {
	Verified bool   `json:"verified"`
	Records  int    `json:"records"`
	Head     string `json:"head"`
}

// ErrNotFound is a receipt no decision in the log has.
var ErrNotFound = errors.New("no decision in this log has that receipt")

// Find checks a receipt against records, which must be the whole log, verified, oldest first (an audit.Log's Records, or
// audit.Read of a copy).
func Find(records []audit.Record, code string) (*Check, error) {
	want := Normalize(code)
	if want == "" {
		return nil, errors.New("a receipt looks like VG-1A2B-3C4D-5E6F-7A8B")
	}
	var found *audit.Record
	for i := range records {
		r := &records[i]
		if Decisions[r.Action] && strings.HasPrefix(r.Hash, want) {
			if found != nil {
				// 64 bits make this all but impossible; say so rather than pick one.
				return nil, errors.New("two decisions share that receipt; check it with the full record hash")
			}
			found = r
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	c := &Check{Receipt: Code(found.Hash), Time: found.Time,
		Record: Position{Seq: found.Seq, Hash: found.Hash, Prev: found.Prev}}
	// The policy is the case's: read it from the record that opened the case.
	for i := range records {
		r := &records[i]
		if r.Case != found.Case || r.Action != cases.ActOpened {
			continue
		}
		var o cases.Opening
		if err := json.Unmarshal(r.Data, &o); err != nil {
			return nil, err
		}
		c.Policy, c.PolicyVersion, c.PolicyTitle = o.Policy, o.PolicyVersion, o.PolicyTitle
		if r == found {
			c.Decision, c.By = o.Answer.Decision, "engine"
			c.GoldCard, c.Proposed = o.Exemption != nil, o.Proposed != nil
			if o.Exemption == nil && o.Result != nil && o.Result.Proof != nil &&
				(o.Result.Outcome == decide.Approved || o.Result.Outcome == decide.Denied) {
				c.Clause = o.Result.Proof.Clause
			}
		}
		break
	}
	if c.By == "" {
		d, err := describe(*found)
		if err != nil {
			return nil, err
		}
		c.Decision, c.By, c.Credential = d.Decision, d.By, d.credential
	}
	for i := range records {
		r := records[i]
		if r.Seq <= found.Seq || r.Case != found.Case || !Decisions[r.Action] || r.Action == cases.ActOpened {
			continue
		}
		d, err := describe(r)
		if err != nil {
			return nil, err
		}
		c.Later = append(c.Later, d.Later)
	}
	c.Chain = Chain{Verified: true, Records: len(records)}
	if n := len(records); n > 0 {
		c.Chain.Head = records[n-1].Hash
	}
	return c, nil
}

type described struct {
	Later
	credential string
}

// describe reads a review or an appeal record as a decision.
func describe(r audit.Record) (described, error) {
	d := described{Later: Later{Receipt: Code(r.Hash), Time: r.Time}}
	switch r.Action {
	case cases.ActReviewed:
		var rv cases.Review
		if err := json.Unmarshal(r.Data, &rv); err != nil {
			return d, err
		}
		d.Decision, d.By = rv.Decision, "reviewer"
		if rv.ReviewerNPI != "" {
			d.By, d.credential = "clinician", rv.Credential
		}
		if rv.Automatic {
			d.By = "documents"
		}
	case cases.ActAppealed:
		var a cases.Appeal
		if err := json.Unmarshal(r.Data, &a); err != nil {
			return d, err
		}
		d.Decision, d.By = a.Outcome, "appeal"
	default:
		return d, errors.New("not a decision")
	}
	return d, nil
}
