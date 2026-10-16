package server

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/oidc"
	"github.com/biodream-llc/vaglio/internal/saml"
)

// SAMLSignOn is single sign-on through a SAML 2.0 identity provider (Microsoft Entra ID, Okta, ADFS, Keycloak). As with
// OpenID Connect, the provider proves who someone is and the account here, made with vaglio user sso, says what they may
// do: a name with no such account is refused, never created.
type SAMLSignOn struct {
	SP *saml.ServiceProvider
	// NameAttribute names the attribute holding the account name. Empty: an email address, then a user principal name
	// (Entra ID sends no email, and its UPN in "name"), then the NameID.
	NameAttribute string
	// Group, when set, is a group the person must be in, read from GroupsAttribute (default groups; Entra ID's is
	// http://schemas.microsoft.com/ws/2008/06/identity/claims/groups, matched by its last part, and its values are
	// object ids, not names).
	Group, GroupsAttribute string
	pending                *samlRequests
}

// NewSAML wraps a service provider for the server.
func NewSAML(sp *saml.ServiceProvider, nameAttr, group, groupsAttr, label string) SSO {
	if label == "" {
		label = "Sign in with your organization"
	}
	if groupsAttr == "" {
		groupsAttr = "groups"
	}
	return SSO{Label: label, SAML: &SAMLSignOn{SP: sp, NameAttribute: nameAttr, Group: group, GroupsAttribute: groupsAttr,
		pending: &samlRequests{m: map[string]time.Time{}}}}
}

// samlRequests are the AuthnRequests sent and not answered yet: a response is accepted only as the answer to one of them,
// once, within the sign-in window. That is what ties a response to a sign-in this server began (the identity provider's
// form POST is cross-site, so no cookie of ours comes with it).
type samlRequests struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (q *samlRequests) add(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, at := range q.m {
		if time.Since(at) > signInWindow {
			delete(q.m, k)
		}
	}
	if len(q.m) > 10000 {
		return false
	}
	q.m[id] = time.Now()
	return true
}

func (q *samlRequests) take(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	at, ok := q.m[id]
	delete(q.m, id)
	return ok && time.Since(at) <= signInWindow
}

func (cfg Config) samlStart(w http.ResponseWriter, r *http.Request) {
	to, id, err := cfg.SSO.SAML.SP.BuildAuthnRequest("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !cfg.SSO.SAML.pending.add(id) {
		writeError(w, http.StatusServiceUnavailable, "too many sign-ins are in progress; try again in a few minutes")
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// samlACS takes the identity provider's response. Trusted only after its signature (by the configured certificate,
// never one inside the response), audience, destination, conditions, replay and InResponseTo checks all pass.
func (cfg Config) samlACS(w http.ResponseWriter, r *http.Request) {
	s := cfg.SSO.SAML
	if s == nil {
		writeError(w, http.StatusNotFound, "SAML sign-in is not configured")
		return
	}
	fail := func(why string) {
		cfg.Audit.Append("system", "user.sign_in_failed", "", map[string]string{"via": "saml", "why": why})
		http.Redirect(w, r, "/#/signin?sso=failed", http.StatusSeeOther)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	encoded := r.PostFormValue("SAMLResponse")
	if encoded == "" {
		fail("the identity provider sent no response")
		return
	}
	a, err := s.SP.ParseResponseFor(encoded, s.pending.take)
	if err != nil {
		fail(err.Error())
		return
	}
	if s.Group != "" && !hasValue(attrValues(a, s.GroupsAttribute), s.Group) {
		names := make([]string, 0, len(a.Attributes))
		for n := range a.Attributes {
			names = append(names, n)
		}
		sort.Strings(names)
		// The attributes it did send are recorded: reading the wrong one looks exactly like being in no group.
		fail("not a member of " + s.Group + " (attributes sent: " + strings.Join(names, ", ") + ")")
		return
	}
	name := samlName(a, s.NameAttribute)
	if name == "" {
		fail("the assertion names nobody")
		return
	}
	token, sess, err := cfg.Sessions.SignInSSO(name)
	if err != nil {
		cfg.Audit.Append("system", "user.sign_in_failed", "", map[string]string{"via": "saml", "name": name, "why": err.Error()})
		http.Redirect(w, r, "/#/signin?sso=unknown", http.StatusSeeOther)
		return
	}
	if _, err := cfg.Audit.Append("user:"+sess.User, "user.signed_in", "", map[string]string{"via": "saml"}); err != nil {
		cfg.Sessions.SignOut(token)
		writeError(w, http.StatusInternalServerError, "the sign-in could not be recorded: "+err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: cfg.Secure,
		SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// attrValues are an attribute's values, matched by name or by its last part, case aside: providers name one attribute
// by a URI (Entra ID uses schemas.microsoft.com where published examples show schemas.xmlsoap.org) or by a short name.
func attrValues(a *saml.Assertion, want string) []string {
	want = strings.ToLower(want)
	for name, vs := range a.Attributes {
		got := strings.ToLower(name)
		if got == want || strings.HasSuffix(got, "/"+want) {
			return vs
		}
	}
	return nil
}

func hasValue(vs []string, want string) bool {
	for _, v := range vs {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func firstAttr(a *saml.Assertion, names ...string) string {
	for _, n := range names {
		for _, v := range attrValues(a, n) {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// samlName is the account name an assertion vouches for (oidc.AccountName: lower cased, before any @). With no
// attribute set: an email address; then the
// user principal name, where Entra ID puts it (it sends no email, and its NameID is an opaque persistent id by default),
// taken only when it has an @, since "name" is a display name at other providers; then the NameID.
func samlName(a *saml.Assertion, attr string) string {
	if attr != "" {
		return oidc.AccountName(firstAttr(a, attr))
	}
	if v := firstAttr(a, "email", "mail", "emailaddress"); v != "" {
		return oidc.AccountName(v)
	}
	if v := firstAttr(a, "upn", "userprincipalname", "name"); strings.Contains(v, "@") {
		return oidc.AccountName(v)
	}
	return oidc.AccountName(a.NameID)
}
