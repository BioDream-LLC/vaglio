package server

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/adoption"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cmswatch"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/library"
)

// Clinician approval of the policies a plan runs (internal/adoption). With RequireAdoption, a library policy decides
// only once enough of the plan's clinicians adopt its exact text; until then it decides nothing here (the PAS server
// answers as it would without Vaglio). When the program brings a newer library version, the adopted one goes on
// deciding until the new one is adopted.

// adoptState is the adopted library versions deciding in place of newer ones the program carries.
type adoptState struct {
	mu   sync.Mutex
	kept map[string]keptPolicy
}

type keptPolicy struct {
	p    *policy.Policy
	hash string
}

func (a *adoptState) get(id string) (keptPolicy, bool) {
	if a == nil {
		return keptPolicy{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k, ok := a.kept[id]
	return k, ok
}

func (a *adoptState) set(id string, k *keptPolicy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if k == nil {
		delete(a.kept, id)
	} else {
		a.kept[id] = *k
	}
}

// libHash is the SHA-256 of a library policy's text as the program carries it, "" for none. The text is in the
// program, so it never changes while it runs.
func libHash(id string) string {
	if h, ok := libHashes.Load(id); ok {
		return h.(string)
	}
	src, err := library.Source(id)
	if err != nil {
		return ""
	}
	h := adoption.Hash(src)
	libHashes.Store(id, h)
	return h
}

var libHashes sync.Map

func (cfg Config) signatures() int { return max(cfg.AdoptionSignatures, 1) }

// startAdoption puts in force, for each library policy whose version in the program is not adopted, the newest version
// that is.
func (cfg Config) startAdoption() {
	if !cfg.RequireAdoption {
		return
	}
	if cfg.Adoption == nil {
		panic("server: RequireAdoption needs Adoption")
	}
	for id, lib := range cfg.Library {
		if cfg.set.get(id) != lib {
			continue // the plan's own signed override decides
		}
		h := libHash(id)
		if cfg.Adoption.Adopted(id, h, cfg.signatures()) {
			continue
		}
		src, kh, ok := cfg.Adoption.Kept(id, h, cfg.signatures())
		if !ok {
			continue
		}
		p, err := policy.Parse(id+policy.Ext, string(src))
		if err != nil || p.ID != id {
			continue
		}
		cfg.set.put(p)
		cfg.adopt.set(id, &keptPolicy{p: p, hash: kh})
		if cfg.Versions != nil {
			_, _ = cfg.Versions.Keep(p)
		}
	}
}

// decidesHere is whether a policy in force may decide requests: always, unless adoption is required and it is a library
// text the plan's clinicians have not adopted (or have withdrawn).
func (cfg Config) decidesHere(p *policy.Policy) bool {
	if !cfg.RequireAdoption || cfg.Adoption == nil {
		return true
	}
	if lib := cfg.Library[p.ID]; lib == p {
		return cfg.Adoption.Adopted(p.ID, libHash(p.ID), cfg.signatures())
	}
	if k, ok := cfg.adopt.get(p.ID); ok && k.p == p {
		return cfg.Adoption.Adopted(p.ID, k.hash, cfg.signatures())
	}
	return true
}

// deciding is the policies in force that may decide requests here.
func (cfg Config) deciding() []*policy.Policy {
	all := cfg.set.all()
	out := all[:0:0]
	for _, p := range all {
		if cfg.decidesHere(p) {
			out = append(out, p)
		}
	}
	return out
}

// textOf is the exact text a policy in force decides with, as adopted or reviewed: the library's file, a kept adopted
// file, or the plan's own policy as formatted.
func (cfg Config) textOf(p *policy.Policy) []byte {
	if lib := cfg.Library[p.ID]; lib == p {
		if src, err := library.Source(p.ID); err == nil {
			return src
		}
	}
	if k, ok := cfg.adopt.get(p.ID); ok && k.p == p {
		if src, ok := cfg.Adoption.Text(p.ID, k.hash); ok {
			return src
		}
	}
	return []byte(policy.Format(p))
}

// AdoptionState is where a policy stands with the plan's clinicians.
type AdoptionState struct {
	Policy  string `json:"policy"`
	Title   string `json:"title"`
	Version string `json:"version"` // the version in force
	Kind    string `json:"kind"`    // library, kept (an adopted library version older than the program's), plan
	Hash    string `json:"sha256"`
	// Required: the server requires adoption, and this is a library text, so it decides only when adopted.
	Required bool              `json:"required"`
	Need     int               `json:"need"`
	Adopters []adoption.Act    `json:"adopters"`
	Deciding bool              `json:"deciding"`
	Waiting  *WaitingVersion   `json:"waiting,omitempty"` // the program's newer library version, not yet adopted
	LastLook string            `json:"lastLook,omitempty"`
	Due      string            `json:"due,omitempty"`
	Overdue  bool              `json:"overdue,omitempty"`
	DueSoon  bool              `json:"dueSoon,omitempty"` // within 30 days
	Source   *cmswatch.Finding `json:"source,omitempty"`
	History  []adoption.Act    `json:"history"`
}

// WaitingVersion is a library version the program carries that waits for the plan's clinicians.
type WaitingVersion struct {
	Version  string         `json:"version"`
	Hash     string         `json:"sha256"`
	Adopters []adoption.Act `json:"adopters"`
}

func (cfg Config) adoptionState(p *policy.Policy, now time.Time) AdoptionState {
	s := AdoptionState{Policy: p.ID, Title: p.Title, Version: p.Version, Kind: "plan", Need: cfg.signatures(),
		Adopters: []adoption.Act{}, History: []adoption.Act{}}
	lib := cfg.Library[p.ID]
	switch k, kept := cfg.adopt.get(p.ID); {
	case lib != nil && lib == p:
		s.Kind, s.Hash = "library", libHash(p.ID)
	case kept && k.p == p:
		s.Kind, s.Hash = "kept", k.hash
		h := libHash(p.ID)
		s.Waiting = &WaitingVersion{Version: lib.Version, Hash: h, Adopters: []adoption.Act{}}
		if cfg.Adoption != nil {
			s.Waiting.Adopters = append(s.Waiting.Adopters, cfg.Adoption.Adopters(p.ID, h)...)
		}
	default:
		s.Hash = adoption.Hash(cfg.textOf(p))
	}
	s.Required = cfg.RequireAdoption && s.Kind != "plan"
	s.Deciding = cfg.decidesHere(p)
	if cfg.Adoption != nil {
		s.Adopters = append(s.Adopters, cfg.Adoption.Adopters(p.ID, s.Hash)...)
		s.History = append(s.History, cfg.Adoption.History(p.ID)...)
		since := time.Time{}
		if p.Signed != nil {
			since, _ = time.Parse("2006-01-02", p.Signed.Date)
		}
		if t, _ := cfg.Adoption.LastLook(p.ID); !t.IsZero() {
			s.LastLook = t.UTC().Format(time.RFC3339)
		}
		if due := cfg.Adoption.Due(p.ID, since, cfg.ReviewEvery); !due.IsZero() {
			s.Due = due.UTC().Format(time.RFC3339)
			s.Overdue = now.After(due)
			s.DueSoon = !s.Overdue && due.Sub(now) < 30*24*time.Hour
		}
	}
	if f, ok := cfg.SourceWatch.For(p.ID); ok {
		s.Source = &f
	}
	return s
}

// AdoptionReport is every policy in force, with where it stands.
type AdoptionReport struct {
	Required    bool            `json:"required"`
	Need        int             `json:"need"`
	ReviewEvery string          `json:"reviewEvery,omitempty"`
	Policies    []AdoptionState `json:"policies"`
	// Counts: library texts deciding nothing for want of adoption, reviews overdue, sources CMS has changed.
	NotAdopted    int    `json:"notAdopted"`
	Overdue       int    `json:"overdue"`
	SourceChanged int    `json:"sourceChanged"`
	Checked       string `json:"sourcesChecked,omitempty"`
	CheckError    string `json:"sourcesError,omitempty"`
}

func (cfg Config) adoptionReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	cfg.activateDue()
	now := cfg.now()
	rep := AdoptionReport{Required: cfg.RequireAdoption, Need: cfg.signatures(), Policies: []AdoptionState{}}
	if cfg.ReviewEvery > 0 {
		rep.ReviewEvery = cfg.ReviewEvery.String()
	}
	for _, p := range cfg.set.all() {
		s := cfg.adoptionState(p, now)
		if s.Required && !s.Deciding {
			rep.NotAdopted++
		}
		if s.Overdue {
			rep.Overdue++
		}
		if s.Source != nil && (s.Source.Changed || s.Source.Missing) {
			rep.SourceChanged++
		}
		rep.Policies = append(rep.Policies, s)
	}
	sort.Slice(rep.Policies, func(i, j int) bool { return rep.Policies[i].Policy < rep.Policies[j].Policy })
	if t, e := cfg.SourceWatch.Checked(); !t.IsZero() {
		rep.Checked, rep.CheckError = t.UTC().Format(time.RFC3339), e
	}
	writeJSON(w, http.StatusOK, rep)
}

func (cfg Config) policyAdoption(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := cfg.policy(w, r); ok {
		writeJSON(w, http.StatusOK, cfg.adoptionState(p, cfg.now()))
	}
}

// clinicianOf is the signed-in clinician, or a 403.
func (cfg Config) clinicianOf(w http.ResponseWriter, s auth.Session) (adoption.Clinician, bool) {
	u, ok, err := cfg.Sessions.Account(s.User)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return adoption.Clinician{}, false
	case !ok || !u.Clinician():
		writeError(w, http.StatusForbidden, "only a clinician adopts or reviews a policy: an account with an NPI and a "+
			"credential (vaglio user clinician)")
		return adoption.Clinician{}, false
	}
	return adoption.Clinician{Account: s.User, Name: u.Name, NPI: u.NPI, Credential: u.Credential}, true
}

type adoptBody struct {
	As        string `json:"as"`
	Reference string `json:"reference"`
	Note      string `json:"note"`
	Outcome   string `json:"outcome"`
	// SHA256 is the text the clinician read, as the console showed it: refused if the text has changed since.
	SHA256 string `json:"sha256"`
}

// adoptPolicy records a clinician adopting the program's library text of a policy (the one waiting, when an older
// adopted version decides), and puts it in force once enough clinicians have.
func (cfg Config) adoptPolicy(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if cfg.Adoption == nil {
		writeError(w, http.StatusNotFound, "policy adoption is off on this server")
		return
	}
	var body adoptBody
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.clinicianOf(w, s)
	if !ok {
		return
	}
	id := r.PathValue("id")
	lib := cfg.Library[id]
	if lib == nil {
		writeError(w, http.StatusConflict, id+" is the plan's own policy: its clinician's sign-off is its adoption; "+
			"review it instead")
		return
	}
	src, err := library.Source(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.SHA256 != "" && body.SHA256 != adoption.Hash(src) {
		writeError(w, http.StatusConflict, "the text has changed since you read it: read it again")
		return
	}
	cfg.edit.Lock()
	defer cfg.edit.Unlock()
	cur := cfg.set.get(id)
	if cur != lib {
		if _, kept := cfg.adopt.get(id); !kept {
			writeError(w, http.StatusConflict, "the plan's own override of "+id+" decides; adopting the library's text "+
				"would change nothing (remove the override first)")
			return
		}
	}
	if _, err := cfg.Adoption.Adopt(c, id, lib.Version, src, body.As, body.Reference, body.Note); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cur != lib && cfg.Adoption.Adopted(id, adoption.Hash(src), cfg.signatures()) {
		// The program's version is adopted now: it replaces the older adopted one.
		cfg.set.put(lib)
		cfg.adopt.set(id, nil)
		if cfg.Versions != nil {
			_, _ = cfg.Versions.Keep(lib)
		}
	}
	writeJSON(w, http.StatusOK, cfg.adoptionState(cfg.set.get(id), cfg.now()))
}

// withdrawAdoption records a clinician withdrawing the plan's adoption of the text in force: it decides nothing here.
func (cfg Config) withdrawAdoption(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if cfg.Adoption == nil {
		writeError(w, http.StatusNotFound, "policy adoption is off on this server")
		return
	}
	var body adoptBody
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.clinicianOf(w, s)
	if !ok {
		return
	}
	p, ok := cfg.policy(w, r)
	if !ok {
		return
	}
	st := cfg.adoptionState(p, cfg.now())
	if st.Kind == "plan" {
		writeError(w, http.StatusConflict, p.ID+" is the plan's own policy: withdraw it by retiring it (an ends date) or "+
			"removing it from the policy directory")
		return
	}
	if len(st.Adopters) == 0 {
		writeError(w, http.StatusConflict, "nobody has adopted this version")
		return
	}
	if _, err := cfg.Adoption.Withdraw(c, p.ID, p.Version, st.Hash, body.Note); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.adoptionState(p, cfg.now()))
}

// reviewPolicy records a clinician's periodic review of the text in force, for any policy the plan runs.
func (cfg Config) reviewPolicy(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if cfg.Adoption == nil {
		writeError(w, http.StatusNotFound, "policy adoption is off on this server")
		return
	}
	var body adoptBody
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.clinicianOf(w, s)
	if !ok {
		return
	}
	p, ok := cfg.policy(w, r)
	if !ok {
		return
	}
	src := cfg.textOf(p)
	if body.SHA256 != "" && body.SHA256 != adoption.Hash(src) {
		writeError(w, http.StatusConflict, "the text has changed since you read it: read it again")
		return
	}
	if cfg.RequireAdoption && cfg.Library[p.ID] != nil && !cfg.decidesHere(p) {
		writeError(w, http.StatusConflict, "this version is not adopted: adopt it (or not) rather than review it")
		return
	}
	if _, err := cfg.Adoption.Review(c, p.ID, p.Version, src, body.Outcome, body.As, body.Reference, body.Note); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.adoptionState(p, cfg.now()))
}

// Packet is everything a clinician reads before adopting or reviewing a policy: its text, the source document beside
// it with each clause found in it, the exhaustive check, possible ambiguities, the codes, and what changed since the
// version last adopted.
type Packet struct {
	Policy   string           `json:"policy"`
	Version  string           `json:"version"`
	SHA256   string           `json:"sha256"`
	Text     string           `json:"text"`
	Basis    string           `json:"basis,omitempty"`
	Document string           `json:"document,omitempty"` // the source document's copy, when the library carries one
	Clauses  []ClauseFound    `json:"clauses"`
	Check    *decide.Report   `json:"check"`
	Warnings []string         `json:"warnings"`
	Services []policy.Service `json:"services"`
	// Changes are the lines added and removed since the version last adopted, when there is one and it differs.
	Since   string        `json:"since,omitempty"`
	Added   []string      `json:"added,omitempty"`
	Removed []string      `json:"removed,omitempty"`
	State   AdoptionState `json:"state"`
}

// ClauseFound is one of the policy's quoted clauses and where it is in the source document (-1 when it is not there
// word for word).
type ClauseFound struct {
	Argument string `json:"argument"`
	Clause   string `json:"clause"`
	At       int    `json:"at"`
}

func (cfg Config) adoptionPacket(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	p, ok := cfg.policy(w, r)
	if !ok {
		return
	}
	// The text to read: the program's library version when it is the one waiting, else the one in force.
	text, version := cfg.textOf(p), p.Version
	target := p
	if k, kept := cfg.adopt.get(p.ID); kept && k.p == p {
		if src, err := library.Source(p.ID); err == nil {
			text, version, target = src, cfg.Library[p.ID].Version, cfg.Library[p.ID]
		}
	}
	pk := Packet{Policy: p.ID, Version: version, SHA256: adoption.Hash(text), Text: string(text), Services: target.Services,
		Clauses: []ClauseFound{}, Warnings: []string{}, State: cfg.adoptionState(p, cfg.now())}
	if es, err := library.Entries(); err == nil {
		pk.Basis = es[p.ID].Basis
	}
	if doc, err := library.SourceDocument(p.ID); err == nil {
		pk.Document = string(doc)
	}
	norm := strings.Join(strings.Fields(pk.Document), " ")
	for _, a := range target.Arguments {
		if a.Clause == "" {
			continue
		}
		at := -1
		if norm != "" {
			at = strings.Index(norm, strings.Join(strings.Fields(a.Clause), " "))
		}
		pk.Clauses = append(pk.Clauses, ClauseFound{Argument: a.Name, Clause: a.Clause, At: at})
	}
	rep := decide.Check(target)
	pk.Check = rep
	pk.Warnings = append(pk.Warnings, policy.Lint(target)...)
	if cfg.Adoption != nil {
		if old, h, ok := cfg.Adoption.Kept(p.ID, pk.SHA256, 1); ok && h != pk.SHA256 {
			pk.Since = h
			pk.Added, pk.Removed = lineDiff(string(old), pk.Text)
		}
	}
	writeJSON(w, http.StatusOK, pk)
}

// lineDiff is the lines only in b (added) and only in a (removed), each in its order, comment lines included.
func lineDiff(a, b string) (added, removed []string) {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[strings.TrimRight(l, " \t")]++
		}
		return m
	}
	ca, cb := count(a), count(b)
	for _, l := range strings.Split(b, "\n") {
		l = strings.TrimRight(l, " \t")
		if ca[l] > 0 {
			ca[l]--
		} else if l != "" {
			added = append(added, l)
		}
	}
	for _, l := range strings.Split(a, "\n") {
		l = strings.TrimRight(l, " \t")
		if cb[l] > 0 {
			cb[l]--
		} else if l != "" {
			removed = append(removed, l)
		}
	}
	return added, removed
}
