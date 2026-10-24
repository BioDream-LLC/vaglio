// Package adoption is a plan's clinicians approving the policies it runs on. Library policies come from public
// Medicare and Medicaid documents; on a server started with -require-adoption, each decides only after the plan's
// clinicians adopt it, each adoption recorded with the clinician's name, NPI and credential, the date and the exact
// text adopted (its SHA-256). A plan's own policies carry a clinician's sign-off already; every policy, the plan's and
// the library's, is reviewed again at the plan's interval (UM accreditation expects at least yearly), and the review is
// recorded the same way.
//
// Everything is an audit record first, and the state is replayed from the log, so it can never say what the log does
// not.
package adoption

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Audit actions.
const (
	ActAdopted   = "policy.adopted"            // a clinician adopts a policy's exact text for the plan
	ActWithdrawn = "policy.adoption_withdrawn" // a clinician withdraws the plan's adoption: it decides nothing again
	ActReviewed  = "policy.reviewed"           // a periodic review: kept, or changes needed
)

// Act is one adoption, withdrawal or review, as recorded.
type Act struct {
	Action     string `json:"action"`
	Policy     string `json:"policy"`
	Version    string `json:"version"`
	Hash       string `json:"sha256"` // of the policy's text as adopted or reviewed
	By         string `json:"by"`     // the account
	Name       string `json:"name"`
	NPI        string `json:"npi"`
	Credential string `json:"credential"`
	// As is the capacity they act in: "medical director", "orthopedic surgeon" (free text, the plan's words).
	As string `json:"as,omitempty"`
	// Reference is the committee's minutes or decision it rests on, when there is one.
	Reference string `json:"reference,omitempty"`
	Note      string `json:"note,omitempty"`
	// Outcome is a review's: kept (still right as written) or change (it needs changing: a draft should follow).
	Outcome string `json:"outcome,omitempty"`
	Time    string `json:"time"`
}

// Registry is every policy's adoptions and reviews.
type Registry struct {
	mu   sync.Mutex
	log  *audit.Log
	dir  string // adopted texts kept, DATA/adopted
	acts map[string][]Act
}

// Open replays the log. dir keeps the text of every adopted version, so an adopted version goes on deciding after the
// program carries a newer one.
func Open(log *audit.Log, dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Registry{log: log, dir: dir, acts: map[string][]Act{}}
	for _, rec := range log.Records(0, 0) {
		switch rec.Action {
		case ActAdopted, ActWithdrawn, ActReviewed:
			var a Act
			if err := json.Unmarshal(rec.Data, &a); err != nil {
				return nil, fmt.Errorf("adoption: record %d: %w", rec.Seq, err)
			}
			a.Action, a.Time = rec.Action, rec.Time
			r.acts[a.Policy] = append(r.acts[a.Policy], a)
		}
	}
	return r, nil
}

// Hash is the SHA-256 a text is adopted under.
func Hash(src []byte) string {
	h := sha256.Sum256(src)
	return hex.EncodeToString(h[:])
}

// Adopted is whether the text with this hash is adopted for the plan by at least need distinct clinicians (by NPI),
// since the last withdrawal. need below 1 counts as 1.
func (r *Registry) Adopted(policy, hash string, need int) bool {
	return len(r.Adopters(policy, hash)) >= max(need, 1)
}

// Adopters are the adoptions of this text since its last withdrawal, one per clinician (the latest).
func (r *Registry) Adopters(policy, hash string) []Act {
	r.mu.Lock()
	defer r.mu.Unlock()
	by := map[string]Act{}
	var order []string
	for _, a := range r.acts[policy] {
		switch {
		case a.Action == ActWithdrawn && (a.Hash == hash || a.Hash == ""):
			by, order = map[string]Act{}, nil
		case a.Action == ActAdopted && a.Hash == hash:
			if _, seen := by[a.NPI]; !seen {
				order = append(order, a.NPI)
			}
			by[a.NPI] = a
		}
	}
	out := make([]Act, 0, len(order))
	for _, n := range order {
		out = append(out, by[n])
	}
	return out
}

// History is everything recorded about a policy, oldest first.
func (r *Registry) History(policy string) []Act {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Act(nil), r.acts[policy]...)
}

// LastLook is the last time a clinician adopted or reviewed the policy (any version), and that act.
func (r *Registry) LastLook(policy string) (time.Time, *Act) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acts := r.acts[policy]
	for i := len(acts) - 1; i >= 0; i-- {
		if a := acts[i]; a.Action == ActAdopted || a.Action == ActReviewed {
			t, _ := time.Parse(time.RFC3339Nano, a.Time)
			return t, &a
		}
	}
	return time.Time{}, nil
}

// Clinician is who acts: their account, and what it records of them.
type Clinician struct {
	Account, Name, NPI, Credential string
}

func (c Clinician) check() error {
	if c.NPI == "" || c.Credential == "" {
		return errors.New("only a clinician adopts or reviews a policy: an account with an NPI and a credential " +
			"(vaglio user clinician)")
	}
	return nil
}

// Adopt records a clinician adopting a policy's exact text, and keeps the text.
func (r *Registry) Adopt(c Clinician, policy, version string, src []byte, as, reference, note string) (Act, error) {
	if err := c.check(); err != nil {
		return Act{}, err
	}
	a := Act{Action: ActAdopted, Policy: policy, Version: version, Hash: Hash(src), By: c.Account, Name: c.Name, NPI: c.NPI,
		Credential: c.Credential, As: strings.TrimSpace(as), Reference: strings.TrimSpace(reference), Note: strings.TrimSpace(note)}
	for _, prev := range r.Adopters(policy, a.Hash) {
		if prev.NPI == c.NPI {
			return Act{}, fmt.Errorf("%s has adopted this version already", c.Name)
		}
	}
	if err := r.keep(policy, a.Hash, src); err != nil {
		return Act{}, err
	}
	return r.write(c.Account, a)
}

// Withdraw records a clinician withdrawing the plan's adoption of a policy's text: it decides nothing until adopted
// again. hash "" withdraws every version.
func (r *Registry) Withdraw(c Clinician, policy, version, hash, note string) (Act, error) {
	if err := c.check(); err != nil {
		return Act{}, err
	}
	if strings.TrimSpace(note) == "" {
		return Act{}, errors.New("say why the adoption is withdrawn: the next reviewer reads it")
	}
	return r.write(c.Account, Act{Action: ActWithdrawn, Policy: policy, Version: version, Hash: hash, By: c.Account,
		Name: c.Name, NPI: c.NPI, Credential: c.Credential, Note: strings.TrimSpace(note)})
}

// Review records a periodic review of the text in force: kept as it is, or in need of change.
func (r *Registry) Review(c Clinician, policy, version string, src []byte, outcome, as, reference, note string) (Act, error) {
	if err := c.check(); err != nil {
		return Act{}, err
	}
	if outcome != "kept" && outcome != "change" {
		return Act{}, errors.New("a review keeps the policy as it is (kept) or says it needs changing (change)")
	}
	if outcome == "change" && strings.TrimSpace(note) == "" {
		return Act{}, errors.New("say what needs changing: the policy's authors read it")
	}
	return r.write(c.Account, Act{Action: ActReviewed, Policy: policy, Version: version, Hash: Hash(src), By: c.Account,
		Name: c.Name, NPI: c.NPI, Credential: c.Credential, As: strings.TrimSpace(as), Reference: strings.TrimSpace(reference),
		Note: strings.TrimSpace(note), Outcome: outcome})
}

func (r *Registry) write(account string, a Act) (Act, error) {
	rec, err := r.log.Append("user:"+account, a.Action, "", a)
	if err != nil {
		return Act{}, err
	}
	a.Time = rec.Time
	r.mu.Lock()
	r.acts[a.Policy] = append(r.acts[a.Policy], a)
	r.mu.Unlock()
	return a, nil
}

// keep writes an adopted text into the directory, named by its policy and hash.
func (r *Registry) keep(policy, hash string, src []byte) error {
	file := r.file(policy, hash)
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, src, 0o600); err != nil {
		return err
	}
	return fsx.Replace(tmp, file)
}

func (r *Registry) file(policy, hash string) string {
	return filepath.Join(r.dir, policy+"."+hash[:16]+".vaglio")
}

// Kept is the text of the policy's most recently adopted version that is still adopted by need clinicians, other than
// the one with skip's hash: what decides while a newer version waits for adoption. ok is false when there is none, or
// its kept text no longer matches what was adopted.
func (r *Registry) Kept(policy, skip string, need int) (src []byte, hash string, ok bool) {
	r.mu.Lock()
	var hashes []string
	seen := map[string]bool{}
	acts := r.acts[policy]
	for i := len(acts) - 1; i >= 0; i-- {
		if a := acts[i]; a.Action == ActAdopted && a.Hash != skip && !seen[a.Hash] {
			seen[a.Hash] = true
			hashes = append(hashes, a.Hash)
		}
	}
	r.mu.Unlock()
	for _, h := range hashes {
		if !r.Adopted(policy, h, need) {
			continue
		}
		src, err := os.ReadFile(r.file(policy, h))
		if err != nil || Hash(src) != h {
			continue
		}
		return src, h, true
	}
	return nil, "", false
}

// Text is the kept text adopted under hash, if it is kept and still matches.
func (r *Registry) Text(policy, hash string) ([]byte, bool) {
	if len(hash) < 16 {
		return nil, false
	}
	src, err := os.ReadFile(r.file(policy, hash))
	if err != nil || Hash(src) != hash {
		return nil, false
	}
	return src, true
}

// Due is a policy's next review: a year (or every) after its last adoption or review, or after since when nobody has
// looked yet (the plan's own sign-off date, say). Zero when every is 0.
func (r *Registry) Due(policy string, since time.Time, every time.Duration) time.Time {
	if every <= 0 {
		return time.Time{}
	}
	last, _ := r.LastLook(policy)
	if last.Before(since) {
		last = since
	}
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(every)
}

// Policies is every policy id with anything recorded, sorted.
func (r *Registry) Policies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.acts))
	for id := range r.acts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
