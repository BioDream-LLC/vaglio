// Package server is Vaglio's HTTP interface: the JSON API and the embedded web console.
//
// Two kinds of caller, authenticated two ways:
//
//   - The PAS server (Perfuse) calls /api/pas/ with a bearer token, when one is configured. It is a machine on the same
//     host or a private network, and it never signs in.
//   - People use the console and its API with a session: they sign in with a name and a generated password, and get an
//     HttpOnly, SameSite=Strict cookie. What they may do is their role's (package auth). A state-changing request must
//     also be JSON from this origin, so no other site can make a signed-in browser change anything.
//
// Every request is refused unless its Host header is one the server expects, which stops a page elsewhere reaching it
// through DNS rebinding.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/reader"
	"github.com/biodream-llc/vaglio/internal/versions"
)

// maxBody bounds a console request. Requests carry citations, not documents.
const maxBody = 1 << 20

// cookieName is the session cookie.
const cookieName = "vaglio_session"

// Config is what the server serves and to whom.
type Config struct {
	Policies     map[string]*policy.Policy
	Assets       fs.FS    // the built web console; nil serves the API only
	AllowedHosts []string // Host header values accepted; empty accepts any (tests only)
	Token        string   // when set, required as "Authorization: Bearer <token>" on /api/pas/, the PAS server's endpoint
	Sessions     *auth.Sessions
	Audit        *audit.Log
	AuditPath    string // the log's file, for verifying it from the start
	Cases        *cases.Store
	Secure       bool // served over TLS: the session cookie is marked Secure
	// Deliver, when set, sends a reviewed case to the PAS server. It is called after the review is in the audit log and
	// must not block; failures are its to record and retry.
	Deliver func(cases.Case)
	// GoldCard, when set, approves requests from providers its programs exempt from review.
	GoldCard *goldcard.Config
	// ClinicianDenials: the engine never denies on its own. A denial it would issue is proposed to a reviewer instead,
	// and only an account recorded as a clinician (an NPI and a credential) may deny - as California's SB 1120 and laws
	// like it require of medical necessity denials.
	ClinicianDenials bool
	// Reader, when set, reads attached documents for a reviewer, as proposals the reviewer confirms.
	Reader *DocumentModel
	// Extractors are local programs that make text of PDFs and images for the Reader.
	Extractors reader.Extractors
	// FetchDocuments, when set, fetches what a provider sent for a request since it was answered (by the PAS server's id).
	FetchDocuments func(ctx context.Context, request string) ([]map[string]any, error)
	// PolicyDir, when set, turns on the console's policy editor: drafts are saved in its drafts/ subdirectory, and a
	// draft a clinician signs off is written into it and put in force at once.
	PolicyDir string
	// SSO, when set, signs people in through the organization's identity provider (OpenID Connect).
	SSO SSO
	// ExtensionDays is the longest extension of a decision timeframe a reviewer may record (at most 14, the default).
	ExtensionDays int
	// AppealsToken, when set, turns on /api/feeds/appeals for the payer's appeals system, which must send it as a bearer
	// token.
	AppealsToken string
	// Versions, when set, keeps every policy version put in force (at start and at sign-off), for the replay time
	// machine.
	Versions *versions.Store

	set          *policySet
	edit         *sync.Mutex
	receiptLimit *limiter
}

// New returns the server's handler.
func New(cfg Config) http.Handler {
	if cfg.Sessions == nil || cfg.Audit == nil || cfg.Cases == nil {
		panic("server: Sessions, Audit and Cases are required")
	}
	cfg.set, cfg.edit, cfg.receiptLimit = newPolicySet(cfg.Policies), &sync.Mutex{}, newLimiter(30)
	if cfg.Versions != nil {
		for _, p := range cfg.Policies {
			if _, err := cfg.Versions.Keep(p); err != nil {
				panic("server: keeping policy versions: " + err.Error())
			}
		}
	}
	cfg.Policies = nil // read through cfg.set from here on, so a signed-off policy is seen everywhere
	if cfg.GoldCard != nil {
		// Counting switches made in the console, in order: the last one per program stands. One for a program no longer
		// in the file, or no longer valid for it, is ignored; the file is the starting point.
		for _, rec := range cfg.Audit.Records(0, 0) {
			if rec.Action != goldcard.ActCounting {
				continue
			}
			var d struct{ Program, Count string }
			if json.Unmarshal(rec.Data, &d) == nil {
				_ = cfg.GoldCard.SetCounting(d.Program, d.Count)
			}
		}
	}
	mux := http.NewServeMux()
	route := func(pattern string, need auth.Role, h func(http.ResponseWriter, *http.Request, auth.Session)) {
		mux.HandleFunc(pattern, cfg.signedIn(need, "", h))
	}
	// scoped: the role, or the scope beside it, for a job off the role ladder.
	scoped := func(pattern string, need auth.Role, scope string, h func(http.ResponseWriter, *http.Request, auth.Session)) {
		mux.HandleFunc(pattern, cfg.signedIn(need, scope, h))
	}
	mux.HandleFunc("POST /api/login", cfg.login)
	mux.HandleFunc("POST /api/logout", cfg.logout)
	mux.HandleFunc("GET /api/sso", cfg.ssoInfo)
	mux.HandleFunc("GET /api/sso/start", cfg.ssoStart)
	mux.HandleFunc("GET /api/sso/callback", cfg.ssoCallback)
	mux.HandleFunc("GET /api/receipts/{code}", cfg.checkReceipt)
	route("GET /api/me", auth.Viewer, cfg.me)
	route("GET /api/policies", auth.Viewer, cfg.listPolicies)
	route("GET /api/policies/{id}", auth.Viewer, cfg.getPolicy)
	route("GET /api/policies/{id}/source", auth.Viewer, cfg.policySource)
	route("GET /api/policies/{id}/check", auth.Viewer, cfg.checkPolicy)
	route("GET /api/policies/{id}/map", auth.Viewer, cfg.policyMap)
	route("POST /api/decide", auth.Viewer, cfg.decide)
	route("GET /api/cases", auth.Viewer, cfg.listCases)
	route("GET /api/cases/{id}", auth.Viewer, cfg.getCase)
	route("GET /api/cases/{id}/bundle", auth.Viewer, cfg.caseBundle)
	route("GET /api/cases/{id}/letter", auth.Viewer, cfg.caseLetter)
	route("POST /api/cases/{id}/review", auth.Reviewer, cfg.reviewCase)
	route("GET /api/policies/{id}/questionnaire", auth.Viewer, cfg.policyQuestionnaire)
	route("GET /api/reports/metrics", auth.Viewer, cfg.metricsReport)
	route("GET /api/reports/pends", auth.Viewer, cfg.pendsReport)
	route("GET /api/reports/overrides", auth.Viewer, cfg.overridesReport)
	scoped("POST /api/cases/{id}/appeal", auth.Reviewer, auth.ScopeAppeals, cfg.appealCase)
	route("GET /api/cases/{id}/documents", auth.Viewer, cfg.caseDocuments)
	route("GET /api/cases/{id}/documents/original", auth.Viewer, cfg.documentOriginal)
	route("POST /api/cases/{id}/read", auth.Reviewer, cfg.readDocument)
	route("POST /api/cases/{id}/confirm", auth.Reviewer, cfg.confirmProposal)
	route("POST /api/cases/{id}/fetch", auth.Reviewer, cfg.fetchDocuments)
	route("GET /api/goldcard", auth.Viewer, cfg.goldCard)
	route("PUT /api/goldcard/programs/{name}/count", auth.Admin, cfg.goldCardCounting)
	route("POST /api/replay", auth.Reviewer, cfg.replay)
	route("POST /api/replay/case", auth.Reviewer, cfg.replayCase)
	route("GET /api/policies/{id}/versions", auth.Viewer, cfg.policyVersions)
	route("GET /api/policies/{id}/versions/{sha}", auth.Viewer, cfg.policyVersion)
	scoped("GET /api/audit", auth.Admin, auth.ScopeAudit, cfg.listAudit)
	scoped("GET /api/audit/verify", auth.Admin, auth.ScopeAudit, cfg.verifyAudit)
	route("POST /api/policies/check", auth.Viewer, cfg.checkSourceHandler)
	route("POST /api/cases/{id}/extend", auth.Reviewer, cfg.extendCase)
	mux.HandleFunc("POST /api/feeds/appeals", cfg.appealsFeed)
	// Reading drafts is open to reviewers as well as authors: a clinician signs a draft off, and must read it to do so.
	// Writing one stays with authors.
	scoped("GET /api/drafts", auth.Reviewer, auth.ScopeAuthor, cfg.listDrafts)
	scoped("GET /api/drafts/{id}", auth.Reviewer, auth.ScopeAuthor, cfg.getDraft)
	scoped("PUT /api/drafts/{id}", auth.Admin, auth.ScopeAuthor, cfg.saveDraft)
	scoped("DELETE /api/drafts/{id}", auth.Admin, auth.ScopeAuthor, cfg.removeDraft)
	scoped("POST /api/policies/{id}/draft", auth.Admin, auth.ScopeAuthor, cfg.draftFromLive)
	route("POST /api/drafts/{id}/signoff", auth.Reviewer, cfg.signDraft)
	mux.HandleFunc("POST /api/pas/decide", cfg.decidePAS)

	// Without these, a wrong method falls through to the /api/ catch-all and reads as "no such endpoint".
	for path, allow := range map[string]string{
		"/api/pas/decide": "POST", "/api/decide": "POST", "/api/login": "POST", "/api/logout": "POST",
		"/api/policies": "GET", "/api/cases": "GET", "/api/audit": "GET", "/api/me": "GET",
		"/api/replay": "POST", "/api/goldcard": "GET",
	} {
		mux.HandleFunc(path, methodNotAllowed(allow))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	if cfg.Assets != nil {
		mux.Handle("/", spa(cfg.Assets))
	}
	return cfg.guard(mux)
}

func (cfg Config) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, h := range cfg.AllowedHosts {
		allowed[h] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) > 0 && !allowed[r.Host] {
			writeError(w, http.StatusForbidden, "unexpected Host header")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/feeds/") {
			// A machine's feed, by its own bearer token, checked by its handler: not a browser's, so no origin check.
		} else if strings.HasPrefix(r.URL.Path, "/api/pas/") {
			if cfg.Token != "" {
				got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.Token)) != 1 {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeError(w, http.StatusUnauthorized, "a bearer token is required")
					return
				}
			}
		} else if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			if msg := sameOrigin(r); msg != "" {
				writeError(w, http.StatusForbidden, msg)
				return
			}
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store") // patient data stays out of caches
		}
		if cfg.Secure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a state-changing console request another site could have made: it must be JSON (which a form
// cannot send, and a cross-site script cannot without the preflight this server never grants), and an Origin, when the
// browser sends one, must be this server.
func sameOrigin(r *http.Request) string {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return "a request that changes anything must be application/json"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return "cross-origin request refused"
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return "cross-origin request refused"
	}
	return ""
}

// signedIn wraps a handler that needs a session with at least role need.
func (cfg Config) signedIn(need auth.Role, scope string, h func(http.ResponseWriter, *http.Request, auth.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		sess, ok := cfg.Sessions.Get(c.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "the session has ended; sign in again")
			return
		}
		if !sess.Role.Can(need) && (scope == "" || !sess.Has(scope)) {
			msg := "this needs the " + string(need) + " role"
			if scope != "" {
				msg += ", or the " + scope + " scope"
			}
			writeError(w, http.StatusForbidden, msg)
			return
		}
		if !cfg.scopedCase(r, sess) {
			writeError(w, http.StatusNotFound, "no such case")
			return
		}
		h(w, r, sess)
	}
}

// decodeJSON reads a console request body, refusing unknown fields and anything too large.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		} else {
			writeError(w, http.StatusBadRequest, "malformed request: "+err.Error())
		}
		return false
	}
	return true
}

// Me is the signed-in user.
type Me struct {
	User       string    `json:"user"`
	Role       auth.Role `json:"role"`
	NPI        string    `json:"npi,omitempty"`
	Credential string    `json:"credential,omitempty"`
	// ClinicianDenials says this deployment lets only clinicians deny, so the console can say so before a review.
	ClinicianDenials bool     `json:"clinicianDenials"`
	Scopes           []string `json:"scopes,omitempty"`
	Via              string   `json:"via,omitempty"`
	// Editing says the console's policy editor is on.
	Editing bool `json:"editing"`
}

func (cfg Config) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	token, sess, err := cfg.Sessions.SignIn(body.Name, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) || errors.Is(err, auth.ErrLocked) {
			cfg.Audit.Append("system", "user.sign_in_failed", "", map[string]string{"name": body.Name, "why": err.Error()})
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := cfg.Audit.Append("user:"+sess.User, "user.signed_in", "", nil); err != nil {
		cfg.Sessions.SignOut(token)
		writeError(w, http.StatusInternalServerError, "the sign-in could not be recorded: "+err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: cfg.Secure,
		SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, Me{User: sess.User, Role: sess.Role})
}

func (cfg Config) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if sess, ok := cfg.Sessions.Get(c.Value); ok {
			cfg.Audit.Append("user:"+sess.User, "user.signed_out", "", nil)
		}
		cfg.Sessions.SignOut(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: cfg.Secure,
		SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (cfg Config) me(w http.ResponseWriter, r *http.Request, s auth.Session) {
	me := Me{User: s.User, Role: s.Role, ClinicianDenials: cfg.ClinicianDenials, Scopes: s.Scopes, Via: s.Via,
		Editing: cfg.PolicyDir != ""}
	if u, ok, err := cfg.Sessions.Account(s.User); err == nil && ok {
		me.NPI, me.Credential = u.NPI, u.Credential
	}
	writeJSON(w, http.StatusOK, me)
}

// Summary is a policy as the console lists it.
type Summary struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Version  string            `json:"version"`
	Source   string            `json:"source"`
	Evidence []policy.Argument `json:"evidence"`
	Services []policy.Service  `json:"services"`
}

func (cfg Config) listPolicies(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	out := []Summary{}
	for _, p := range cfg.set.all() {
		s := Summary{ID: p.ID, Title: p.Title, Version: p.Version, Source: p.Source, Evidence: []policy.Argument{},
			Services: p.Services}
		for _, a := range p.Arguments {
			if a.Kind == policy.Evidence {
				s.Evidence = append(s.Evidence, a)
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, out)
}

func (cfg Config) policy(w http.ResponseWriter, r *http.Request) (*policy.Policy, bool) {
	p := cfg.set.get(r.PathValue("id"))
	ok := p != nil
	if !ok {
		writeError(w, http.StatusNotFound, "no such policy")
	}
	return p, ok
}

func (cfg Config) getPolicy(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := cfg.policy(w, r); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

// policyMap is every combination of the policy's evidence and its outcome, for the console's map.
func (cfg Config) policyMap(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := cfg.policy(w, r); ok {
		m, err := decide.DecisionMap(p)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}

func (cfg Config) policySource(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := cfg.policy(w, r); ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(policy.Format(p)))
	}
}

// CheckReport is a policy's exhaustive check and its warnings.
type CheckReport struct {
	*decide.Report
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings"`
}

func (cfg Config) checkPolicy(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := cfg.policy(w, r); ok {
		rep := decide.Check(p)
		warnings := policy.Lint(p)
		if warnings == nil {
			warnings = []string{}
		}
		writeJSON(w, http.StatusOK, CheckReport{Report: rep, OK: rep.OK(), Warnings: warnings})
	}
}

// decide tries a policy on evidence entered by hand. Nothing is recorded: it is for authors and reviewers to see what a
// policy does, not a way to decide a request.
func (cfg Config) decide(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var req decide.Request
	if !decodeJSON(w, r, &req) {
		return
	}
	p := cfg.set.get(req.Policy)
	ok := p != nil
	if !ok {
		writeError(w, http.StatusNotFound, "no such policy")
		return
	}
	res, err := decide.Decide(p, req)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// CaseSummary is a case as the list shows it: no evidence, no proof.
type CaseSummary struct {
	ID          string          `json:"id"`
	Created     string          `json:"created"`
	Updated     string          `json:"updated"`
	Status      cases.Status    `json:"status"`
	Policy      string          `json:"policy"`
	PolicyTitle string          `json:"policyTitle"`
	Item        string          `json:"item"`
	Service     string          `json:"service"`
	Claim       string          `json:"claim"`
	Patient     string          `json:"patient"`
	Decision    string          `json:"decision"`
	Reason      decide.Reason   `json:"reason"`
	Review      string          `json:"review,omitempty"`
	Delivery    *cases.Delivery `json:"delivery,omitempty"`
	GoldCard    bool            `json:"goldCard,omitempty"` // approved under an exemption
	Proposed    string          `json:"proposed,omitempty"` // a decision the engine proposed to a clinician
	Expedited   bool            `json:"expedited,omitempty"`
	// Due is when the decision is due (RFC 3339), and Extended whether its timeframe was extended: the queue is worked
	// by it.
	Due      string `json:"due"`
	Extended bool   `json:"extended,omitempty"`
}

func (cfg Config) listCases(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	status := cases.Status(r.URL.Query().Get("status"))
	switch status {
	case "", cases.Decided, cases.Pended, cases.Reviewed, cases.Superseded:
	default:
		writeError(w, http.StatusBadRequest, "status is decided, pended, reviewed or superseded")
		return
	}
	out := []CaseSummary{}
	for _, c := range cfg.Cases.List(status) {
		if !sess.Sees(c.Policy) {
			continue
		}
		s := CaseSummary{ID: c.ID, Created: c.Created, Updated: c.Updated, Status: c.Status, Policy: c.Policy,
			PolicyTitle: c.PolicyTitle, Item: c.Item, Service: c.Service, Claim: c.Claim, Patient: c.Patient,
			Decision: c.Answer.Decision, Delivery: c.Delivery, GoldCard: c.Exemption != nil, Expedited: c.Expedited(),
			Due: c.Due().UTC().Format(time.RFC3339), Extended: c.Extension != nil}
		if c.Proposed != nil {
			s.Proposed = c.Proposed.Decision
		}
		if c.Result != nil {
			s.Reason = c.Result.Reason
		}
		if c.Review != nil {
			s.Review = c.Review.Decision
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, out)
}

func (cfg Config) extensionDays() int {
	if cfg.ExtensionDays <= 0 || cfg.ExtensionDays > cases.MaxExtensionDays {
		return cases.MaxExtensionDays
	}
	return cfg.ExtensionDays
}

// CaseDetail is a case with its history.
type CaseDetail struct {
	*cases.Case
	History []audit.Record `json:"history"`
	// Receipts are the receipts of the case's decisions, which anyone holding one can check.
	Receipts []Receipt `json:"receipts"`
	Due      string    `json:"due"`
	// MaxExtensionDays is the longest extension this server allows, for the console's form.
	MaxExtensionDays int `json:"maxExtensionDays"`
	// Redecided is the policy's decision on the request as it now stands, when documents arrived or findings were
	// confirmed since; RedecideError says why it could not be made.
	Redecided     *Redecision `json:"redecided,omitempty"`
	RedecideError string      `json:"redecideError,omitempty"`
}

func (cfg Config) getCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	if err := cfg.Cases.Viewed(c.ID, s.User); err != nil {
		writeError(w, http.StatusInternalServerError, "reading the case could not be recorded: "+err.Error())
		return
	}
	h := cfg.Cases.History(c.ID)
	d := CaseDetail{Case: c, History: h, Receipts: receiptsFor(h), Due: c.Due().UTC().Format(time.RFC3339),
		MaxExtensionDays: cfg.extensionDays()}
	if rd, err := cfg.redecide(c); err != nil {
		d.RedecideError = err.Error()
	} else {
		d.Redecided = rd
	}
	writeJSON(w, http.StatusOK, d)
}

func (cfg Config) caseBundle(w http.ResponseWriter, r *http.Request, s auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	raw, err := cfg.Cases.Bundle(c)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, cases.ErrBundleChanged) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if err := cfg.Cases.Viewed(c.ID, s.User); err != nil {
		writeError(w, http.StatusInternalServerError, "reading the case could not be recorded: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(raw)
}

func validNPI(s string) bool { return auth.ValidNPI(s) }

func (cfg Config) reviewCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Decision    string `json:"decision"`
		Reason      string `json:"reason"`
		ReviewerNPI string `json:"reviewerNpi"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ReviewerNPI != "" && !validNPI(body.ReviewerNPI) {
		writeError(w, http.StatusBadRequest, "the reviewer's NPI is ten digits with a valid check digit")
		return
	}
	acct, ok, err := cfg.Sessions.Account(s.User)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "the session has ended; sign in again")
		return
	}
	rv := cases.Review{Decision: body.Decision, Reason: body.Reason, ReviewerNPI: body.ReviewerNPI}
	if acct.Clinician() {
		// A clinician's decision carries the NPI and credential their account records, not whatever a form says.
		if body.ReviewerNPI != "" && body.ReviewerNPI != acct.NPI {
			writeError(w, http.StatusBadRequest, "that is not the NPI your account records; leave it empty")
			return
		}
		rv.ReviewerNPI, rv.Credential = acct.NPI, acct.Credential
	} else if cfg.ClinicianDenials && body.Decision == "deny" {
		writeError(w, http.StatusForbidden, "here only a clinician may deny: an administrator records your NPI and "+
			"credential with vaglio user clinician, or approve it or leave it for a clinician")
		return
	}
	c, err := cfg.Cases.Review(r.PathValue("id"), s.User, rv)
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
		return
	case errors.Is(err, cases.ErrNotPended):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cfg.Deliver != nil && c.Request != "" {
		cfg.Deliver(*c)
	}
	writeJSON(w, http.StatusOK, c)
}

// AuditPage is a page of the audit log.
type AuditPage struct {
	Records []audit.Record `json:"records"`
	Total   int            `json:"total"`
}

func (cfg Config) listAudit(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	total, _ := cfg.Audit.Head()
	writeJSON(w, http.StatusOK, AuditPage{Records: cfg.Audit.Records(after, limit), Total: total})
}

// Verification is the result of checking the audit log's chain from its first record.
type Verification struct {
	OK      bool   `json:"ok"`
	Records int    `json:"records"`
	Head    string `json:"head"`
	Error   string `json:"error,omitempty"`
}

func (cfg Config) verifyAudit(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.AuditPath == "" {
		writeError(w, http.StatusNotImplemented, "the audit log's file is not known to this server")
		return
	}
	n, head, err := audit.Verify(cfg.AuditPath)
	v := Verification{OK: err == nil, Records: n, Head: head}
	if err != nil {
		v.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, v)
}

// spa serves the console, answering any path that is not a file with index.html so deep links survive a reload.
func spa(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:]
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(assets, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
