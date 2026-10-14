package server

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/editor"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// policySet is the policies in force. Signing a draft off replaces one while requests are being decided, so every read
// goes through it; a request decided while a policy is replaced is decided wholly by the old one or wholly by the new.
type policySet struct {
	mu sync.RWMutex
	m  map[string]*policy.Policy
}

func newPolicySet(m map[string]*policy.Policy) *policySet {
	out := &policySet{m: map[string]*policy.Policy{}}
	for id, p := range m {
		out.m[id] = p
	}
	return out
}

func (s *policySet) get(id string) *policy.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[id]
}

// all is every policy, by id.
func (s *policySet) all() []*policy.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*policy.Policy, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *policySet) snapshot() map[string]*policy.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*policy.Policy, len(s.m))
	for id, p := range s.m {
		out[id] = p
	}
	return out
}

func (s *policySet) put(p *policy.Policy) {
	s.mu.Lock()
	s.m[p.ID] = p
	s.mu.Unlock()
}

// Audit actions for policies.
const (
	ActDraftSaved   = "policy.draft_saved"
	ActDraftRemoved = "policy.draft_removed"
	ActSigned       = "policy.signed"
)

// SourceCheck is what the editor shows beside a source as it is written: where it fails to parse, or its check and
// warnings.
type SourceCheck struct {
	Error    string         `json:"error,omitempty"`
	Line     int            `json:"line,omitempty"`
	Col      int            `json:"col,omitempty"`
	Policy   *policy.Policy `json:"policy,omitempty"`
	Check    *CheckReport   `json:"check,omitempty"`
	Ready    bool           `json:"ready"`              // could be signed off as it stands
	NotReady string         `json:"notReady,omitempty"` // why not
	// InForce is the version of this id now in force, if any: signing off replaces it.
	InForce string `json:"inForce,omitempty"`
}

func (cfg Config) checkSource(src string) SourceCheck {
	var out SourceCheck
	p, err := policy.Parse("draft"+policy.Ext, src)
	if err != nil {
		out.Error = err.Error()
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			out.Line, out.Col = pe.Line, pe.Col
		}
		return out
	}
	rep := decide.Check(p)
	warnings := policy.Lint(p)
	if warnings == nil {
		warnings = []string{}
	}
	out.Policy, out.Check = p, &CheckReport{Report: rep, OK: rep.OK(), Warnings: warnings}
	if live := cfg.set.get(p.ID); live != nil {
		out.InForce = live.Version
	}
	if err := editor.Ready(p); err != nil {
		out.NotReady = err.Error()
	} else if err := editor.Fits(p, cfg.set.snapshot()); err != nil {
		out.NotReady = err.Error()
	} else {
		out.Ready = true
	}
	return out
}

// checkSourceHandler checks a source sent by the editor. Nothing is saved.
func (cfg Config) checkSourceHandler(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var body struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, cfg.checkSource(body.Source))
}

func (cfg Config) editing(w http.ResponseWriter) bool {
	if cfg.PolicyDir == "" {
		writeError(w, http.StatusNotFound, "policy editing is off: start the server with -edit-policies to write drafts "+
			"into its policy directory")
		return false
	}
	return true
}

// DraftView is a draft with its check. The draft's own parse error is the check's, so it is said once.
type DraftView struct {
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Saved  time.Time `json:"saved"`
	SourceCheck
}

func view(cfg Config, d editor.Draft) DraftView {
	return DraftView{ID: d.ID, Source: d.Source, Saved: d.Saved, SourceCheck: cfg.checkSource(d.Source)}
}

func (cfg Config) listDrafts(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if !cfg.editing(w) {
		return
	}
	drafts, err := editor.Dir{Path: cfg.PolicyDir}.Drafts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []DraftView{}
	for _, d := range drafts {
		out = append(out, view(cfg, d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (cfg Config) getDraft(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if !cfg.editing(w) {
		return
	}
	d, ok, err := editor.Dir{Path: cfg.PolicyDir}.Draft(r.PathValue("id"))
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		writeError(w, http.StatusNotFound, "no such draft")
	default:
		writeJSON(w, http.StatusOK, view(cfg, d))
	}
}

// saveDraft writes a draft (author). It is recorded in the audit log by its hash, so who changed a policy's text, and
// when, is known for every draft that was ever signed.
func (cfg Config) saveDraft(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	var body struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	dir := editor.Dir{Path: cfg.PolicyDir}
	if _, err := cfg.Audit.Append("user:"+s.User, ActDraftSaved, "", map[string]string{"policy": id,
		"sha256": editor.Hash([]byte(body.Source))}); err != nil {
		writeError(w, http.StatusInternalServerError, "the save could not be recorded, so it is not made: "+err.Error())
		return
	}
	d, err := dir.SaveDraft(id, []byte(body.Source))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view(cfg, d))
}

func (cfg Config) removeDraft(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	id := r.PathValue("id")
	dir := editor.Dir{Path: cfg.PolicyDir}
	if _, ok, _ := dir.Draft(id); !ok {
		writeError(w, http.StatusNotFound, "no such draft")
		return
	}
	if _, err := cfg.Audit.Append("user:"+s.User, ActDraftRemoved, "", map[string]string{"policy": id}); err != nil {
		writeError(w, http.StatusInternalServerError, "the removal could not be recorded, so it is not made: "+err.Error())
		return
	}
	if err := dir.RemoveDraft(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signDraft signs a draft off and puts it in force at once. Only a clinician's account may: the sign-off records their
// name and credential as the person who read every rule against its source. The signature is the account's, never typed.
func (cfg Config) signDraft(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	u, ok, err := cfg.Sessions.Account(s.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || !u.Clinician() {
		writeError(w, http.StatusForbidden, "only a clinician signs a policy off: an account with an NPI and a credential "+
			"(vaglio user clinician)")
		return
	}
	id := r.PathValue("id")
	dir := editor.Dir{Path: cfg.PolicyDir}
	d, found, err := dir.Draft(id)
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "no such draft")
		return
	}
	if c := cfg.checkSource(d.Source); !c.Ready {
		msg := c.NotReady
		if c.Error != "" {
			msg = c.Error
		}
		writeError(w, http.StatusConflict, "not signed: "+msg)
		return
	}
	by := u.Name + ", " + u.Credential
	date := time.Now().UTC().Format(time.DateOnly)
	cfg.edit.Lock() // one sign-off at a time, so two cannot both pass Fits against the same policies
	defer cfg.edit.Unlock()
	signed, _, err := editor.SignOff(id+policy.Ext, []byte(d.Source), by, date)
	if err != nil {
		writeError(w, http.StatusConflict, "not signed: "+err.Error())
		return
	}
	prev := ""
	if old := cfg.set.get(id); old != nil {
		prev = old.Version
	}
	// Recorded first: a policy in force that the log does not account for is the one thing this must never produce.
	if _, err := cfg.Audit.Append("user:"+s.User, ActSigned, "", map[string]string{"policy": id, "by": by, "date": date,
		"npi": u.NPI, "sha256": editor.Hash(signed), "replaces": prev}); err != nil {
		writeError(w, http.StatusInternalServerError, "the sign-off could not be recorded, so it is not made: "+err.Error())
		return
	}
	p, _, err := dir.Promote(id, by, date, cfg.set.snapshot())
	if err != nil {
		writeError(w, http.StatusConflict, "not signed: "+err.Error())
		return
	}
	cfg.set.put(p)
	if cfg.Versions != nil {
		if _, err := cfg.Versions.Keep(p); err != nil {
			// In force and in the log already; the time machine is the poorer, and says so.
			log.Printf("versions: %s v%s is in force but could not be kept: %v", p.ID, p.Version, err)
		}
	}
	writeJSON(w, http.StatusOK, p)
}

// newDraftFrom starts a draft from a policy in force, as its next version: the editor's "edit this policy".
func (cfg Config) draftFromLive(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	id := r.PathValue("id")
	p := cfg.set.get(id)
	if p == nil {
		writeError(w, http.StatusNotFound, "no such policy")
		return
	}
	dir := editor.Dir{Path: cfg.PolicyDir}
	if _, exists, _ := dir.Draft(id); exists {
		writeError(w, http.StatusConflict, "a draft of "+id+" exists already; open it, or remove it first")
		return
	}
	q := *p
	q.Status, q.Signed = policy.Draft, nil
	src := "# Draft of the next version of " + id + ", started from version " + p.Version + ". Change the version before\n" +
		"# signing it off: a changed policy is a new version.\n" + policy.Format(&q)
	if _, err := cfg.Audit.Append("user:"+s.User, ActDraftSaved, "", map[string]string{"policy": id,
		"sha256": editor.Hash([]byte(src)), "from": p.Version}); err != nil {
		writeError(w, http.StatusInternalServerError, "the draft could not be recorded, so it is not made: "+err.Error())
		return
	}
	d, err := dir.SaveDraft(id, []byte(src))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view(cfg, d))
}

// scopedCase hides a case from a session that may not see its policy: as not found, so a narrowed reviewer learns
// nothing of other teams' cases, not even that they exist.
func (cfg Config) scopedCase(r *http.Request, s auth.Session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/cases/") {
		return true
	}
	id := r.PathValue("id")
	if id == "" {
		return true
	}
	c, ok := cfg.Cases.Get(id)
	return !ok || s.Sees(c.Policy)
}
