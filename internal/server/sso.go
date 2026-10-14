package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/oidc"
)

// SSO is single sign-on through an OpenID Connect provider. The provider proves who someone is; the account here, made
// by an administrator with vaglio user sso, says what they may do. A name the provider vouches for with no such account
// is refused, never created: an identity provider is not a list of who may decide prior authorizations.
type SSO struct {
	Provider *oidc.Provider
	Label    string // what the sign-in button says: "Sign in with Example Health"
	pending  *pendingSignIns
}

// pendingSignIns are sign-ins sent to the provider and not back yet, by state.
type pendingSignIns struct {
	mu sync.Mutex
	m  map[string]pendingSignIn
}

type pendingSignIn struct {
	nonce, verifier string
	started         time.Time
}

const signInWindow = 10 * time.Minute

func (p *pendingSignIns) add(state string, s pendingSignIn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if time.Since(v.started) > signInWindow {
			delete(p.m, k)
		}
	}
	if len(p.m) > 10000 {
		return // a flood of abandoned sign-ins: refuse new ones rather than grow without bound
	}
	p.m[state] = s
}

// take removes and returns a pending sign-in: each state is good once.
func (p *pendingSignIns) take(state string) (pendingSignIn, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.m[state]
	delete(p.m, state)
	if ok && time.Since(s.started) > signInWindow {
		return pendingSignIn{}, false
	}
	return s, ok
}

const ssoCookie = "vaglio_sso"

// ssoInfo tells the sign-in screen whether to offer single sign-on.
func (cfg Config) ssoInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": cfg.SSO.Provider != nil, "label": cfg.SSO.Label})
}

// ssoStart sends the browser to the provider. The state is also set in a cookie, so the callback is accepted only in the
// browser that started it.
func (cfg Config) ssoStart(w http.ResponseWriter, r *http.Request) {
	if cfg.SSO.Provider == nil {
		writeError(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	state, nonce, verifier := oidc.Random(24), oidc.Random(24), oidc.Random(48)
	cfg.SSO.pending.add(state, pendingSignIn{nonce: nonce, verifier: verifier, started: time.Now()})
	// Lax, not Strict: the provider's redirect back is a cross-site navigation, and a Strict cookie would not come with it.
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: state, Path: "/api/sso", MaxAge: int(signInWindow.Seconds()),
		HttpOnly: true, Secure: cfg.Secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, cfg.SSO.Provider.AuthURL(state, nonce, verifier), http.StatusFound)
}

// ssoCallback finishes a sign-in: the code is exchanged, the ID token verified, and the account it names signed in.
// Every refusal is in the audit log, and says nothing to the browser that would help someone probing for accounts.
func (cfg Config) ssoCallback(w http.ResponseWriter, r *http.Request) {
	if cfg.SSO.Provider == nil {
		writeError(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	fail := func(why string) {
		cfg.Audit.Append("system", "user.sign_in_failed", "", map[string]string{"via": "sso", "why": why})
		http.Redirect(w, r, "/#/signin?sso=failed", http.StatusFound)
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail("the provider answered " + e)
		return
	}
	state := q.Get("state")
	c, err := r.Cookie(ssoCookie)
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: "", Path: "/api/sso", MaxAge: -1, HttpOnly: true,
		Secure: cfg.Secure, SameSite: http.SameSiteLaxMode})
	if err != nil || state == "" || c.Value != state {
		fail("the state does not match the browser's: a sign-in this browser did not start")
		return
	}
	pend, ok := cfg.SSO.pending.take(state)
	if !ok {
		fail("an unknown or expired sign-in")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := cfg.SSO.Provider.Exchange(ctx, q.Get("code"), pend.verifier)
	if err != nil {
		fail(err.Error())
		return
	}
	claims, err := cfg.SSO.Provider.Verify(ctx, raw, pend.nonce)
	if err != nil {
		fail(err.Error())
		return
	}
	name, err := cfg.SSO.Provider.Name(claims)
	if err != nil {
		fail(err.Error())
		return
	}
	token, sess, err := cfg.Sessions.SignInSSO(name)
	if err != nil {
		cfg.Audit.Append("system", "user.sign_in_failed", "", map[string]string{"via": "sso", "name": name, "why": err.Error()})
		http.Redirect(w, r, "/#/signin?sso=unknown", http.StatusFound)
		return
	}
	if _, err := cfg.Audit.Append("user:"+sess.User, "user.signed_in", "", map[string]string{"via": "sso"}); err != nil {
		cfg.Sessions.SignOut(token)
		writeError(w, http.StatusInternalServerError, "the sign-in could not be recorded: "+err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: cfg.Secure,
		SameSite: http.SameSiteStrictMode})
	// A Strict session cookie is not sent on this cross-site redirect's landing; the console's first request, made from
	// its own page, is same-site and carries it.
	http.Redirect(w, r, "/", http.StatusFound)
}

// NewSSO wraps a discovered provider for the server.
func NewSSO(p *oidc.Provider, label string) SSO {
	if label == "" {
		label = "Sign in with your organization"
	}
	return SSO{Provider: p, Label: label, pending: &pendingSignIns{m: map[string]pendingSignIn{}}}
}
