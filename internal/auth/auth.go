// Package auth is who may use the console, and their sessions.
//
// Accounts live in one file, users.json in the data directory, written only by the vaglio user commands and read by the
// server whenever it changes, so an account removed on the command line stops working at its next request. Passwords are
// generated, never chosen: a generated password is long enough that no rule about what it must contain is needed, and is
// shown once. They are stored as PBKDF2-SHA256 hashes.
//
// Sessions are held in memory, so a restart signs everyone out. That is the safe direction for a server holding patient
// data, and it means a session token is never written anywhere.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Role is what a user may do. Each includes the ones before it.
type Role string

const (
	Viewer   Role = "viewer"   // read policies, cases and their evidence
	Reviewer Role = "reviewer" // also decide pended cases: a clinician
	Admin    Role = "admin"    // also read the audit log
)

// Provider is an account of a provider's office, outside the ladder: it reaches the provider portal only, for the
// requests of the NPIs its account records, and nothing of the plan's own screens.
const Provider Role = "provider"

var rank = map[Role]int{Viewer: 1, Reviewer: 2, Admin: 3}

// Can is whether a role includes another. A provider's includes none: it has no place on the ladder.
func (r Role) Can(need Role) bool { return rank[r] >= rank[need] && rank[need] > 0 }

// Valid is whether r is a known role.
func (r Role) Valid() bool { return rank[r] > 0 || r == Provider }

// User is one account.
type User struct {
	Name       string `json:"name"`
	Role       Role   `json:"role"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	Created    string `json:"created"`
	// NPI and Credential mark a clinician: a licensed person competent to decide medical necessity. Where only a
	// clinician may deny care (the server's -clinician-denials), a denial needs both, and records them.
	NPI        string `json:"npi,omitempty"`
	Credential string `json:"credential,omitempty"` // as the person holds it, such as "MD, orthopaedic surgery"
	// Scopes narrow or widen what the role allows, one job at a time (see Scope). An administrator has every one.
	Scopes []string `json:"scopes,omitempty"`
	// SSO marks an account that signs in only through the organization's identity provider: it has no password, and the
	// provider's sign-in is accepted for it only because an administrator made it, with its role, here.
	SSO bool `json:"sso,omitempty"`
	// ProviderNPIs are the NPIs a provider account acts for: it sees and sends requests of these only.
	ProviderNPIs []string `json:"providerNpis,omitempty"`
}

// Scopes. A role is a ladder (viewer, reviewer, admin); a scope is one job beside it, for the people whose work is not on
// the ladder:
//
//	author      writes and saves policy drafts (signing one off still takes a clinician)
//	audit       reads and verifies the audit log, without the rest of an administrator's power
//	appeals     records the outcome of appeals, without deciding cases
//	intake      keys in requests that arrived by fax, phone, mail or a portal, and works the fax inbox
//	policy:ID   sees and decides only the cases of policy ID (repeatable); with none, every policy's
const (
	ScopeAuthor  = "author"
	ScopeAudit   = "audit"
	ScopeAppeals = "appeals"
	ScopeIntake  = "intake"
	ScopePolicy  = "policy:"
)

var policyScopeRe = regexp.MustCompile(`^policy:[a-z][a-z0-9_]{1,63}$`)

// ValidScope is whether s is a scope this version knows.
func ValidScope(s string) bool {
	switch s {
	case ScopeAuthor, ScopeAudit, ScopeAppeals, ScopeIntake:
		return true
	}
	return policyScopeRe.MatchString(s)
}

// Clinician is whether the account is a clinician's.
func (u User) Clinician() bool { return u.NPI != "" && u.Credential != "" }

type file struct {
	Users []User `json:"users"`
}

// Iterations is the PBKDF2 work factor for new passwords (OWASP's figure for SHA-256). Tests lower it.
var Iterations = 600_000

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,31}$`)

func hashPassword(password string, salt []byte, iterations int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(key), nil
}

// Generate returns a new password: 20 characters from 62, about 119 bits.
func Generate() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz0123456789abc"
	out := make([]byte, 20)
	for i := range out {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// Users is the accounts file.
type Users struct {
	path string
	mu   sync.Mutex
	mod  time.Time
	size int64
	byID map[string]User
}

// OpenUsers reads the accounts file at path; a missing file is no accounts.
func OpenUsers(path string) (*Users, error) {
	u := &Users{path: path}
	if err := u.reload(); err != nil {
		return nil, err
	}
	return u, nil
}

// reload rereads the file if it changed. The caller holds mu, or owns u.
func (u *Users) reload() error {
	info, err := os.Stat(u.path)
	if errors.Is(err, os.ErrNotExist) {
		u.byID, u.mod = map[string]User{}, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	if u.byID != nil && info.ModTime().Equal(u.mod) && info.Size() == u.size {
		return nil
	}
	raw, err := os.ReadFile(u.path)
	if err != nil {
		return err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("%s: %w", u.path, err)
	}
	byID := map[string]User{}
	for _, us := range f.Users {
		if !us.Role.Valid() {
			return fmt.Errorf("%s: user %s has unknown role %q", u.path, us.Name, us.Role)
		}
		for _, sc := range us.Scopes {
			if !ValidScope(sc) {
				// Refuse rather than guess: a scope this version does not know might be one meant to narrow access.
				return fmt.Errorf("%s: user %s has unknown scope %q", u.path, us.Name, sc)
			}
		}
		byID[us.Name] = us
	}
	u.byID, u.mod, u.size = byID, info.ModTime(), info.Size()
	return nil
}

func (u *Users) save() error {
	f := file{Users: []User{}}
	for _, us := range u.byID {
		f.Users = append(f.Users, us)
	}
	sort.Slice(f.Users, func(i, j int) bool { return f.Users[i].Name < f.Users[j].Name })
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.path), ".users-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), u.path); err != nil {
		return err
	}
	u.byID = nil // reread, picking up the new mtime
	return u.reload()
}

// Count is the number of accounts.
func (u *Users) Count() (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return 0, err
	}
	return len(u.byID), nil
}

// List returns the accounts, without their hashes.
func (u *Users) List() ([]User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return nil, err
	}
	out := []User{}
	for _, us := range u.byID {
		us.Salt, us.Hash = "", ""
		out = append(out, us)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Set creates an account or gives an existing one a new role and password, returning the generated password.
func (u *Users) Set(name string, role Role) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("%q is not a usable name: 2 to 32 lower case letters, digits, '.', '_' or '-', starting with a letter", name)
	}
	if !role.Valid() {
		return "", fmt.Errorf("role is viewer, reviewer, admin or provider, not %q", role)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return "", err
	}
	return u.setLocked(name, role, Generate())
}

// SetPassword creates or resets a viewer account with a password the deployment chose: the public demonstration's
// published account (serve -demo-account). Only a viewer: a password anyone knows must never decide or change anything.
func (u *Users) SetPassword(name, password string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a usable name", name)
	}
	if len(password) < 8 {
		return errors.New("a password needs 8 characters or more")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	if old, ok := u.byID[name]; ok && old.Role != Viewer {
		return fmt.Errorf("%s is a %s; a published password is only ever a viewer's", name, old.Role)
	}
	_, err := u.setLocked(name, Viewer, password)
	return err
}

// setLocked sets an account's role and password. The caller holds u.mu and has reloaded.
func (u *Users) setLocked(name string, role Role, password string) (string, error) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	h, err := hashPassword(password, salt, Iterations)
	if err != nil {
		return "", err
	}
	nu := User{Name: name, Role: role, Salt: base64.RawStdEncoding.EncodeToString(salt), Hash: h,
		Iterations: Iterations, Created: time.Now().UTC().Format(time.RFC3339)}
	if old, ok := u.byID[name]; ok {
		nu.Created, nu.NPI, nu.Credential, nu.Scopes = old.Created, old.NPI, old.Credential, old.Scopes
	}
	u.byID[name] = nu
	return password, u.save()
}

// SetSSO creates an account that signs in only through single sign-on, or makes an existing one so (its password is
// removed), with the given role.
func (u *Users) SetSSO(name string, role Role) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a usable name: 2 to 32 lower case letters, digits, '.', '_' or '-', starting with a letter", name)
	}
	if !role.Valid() {
		return fmt.Errorf("role is viewer, reviewer or admin, not %q", role)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	nu := User{Name: name, Role: role, SSO: true, Created: time.Now().UTC().Format(time.RFC3339)}
	if old, ok := u.byID[name]; ok {
		nu.Created, nu.NPI, nu.Credential, nu.Scopes = old.Created, old.NPI, old.Credential, old.Scopes
	}
	u.byID[name] = nu
	return u.save()
}

// SetScopes replaces an account's scopes.
func (u *Users) SetScopes(name string, scopes []string) error {
	clean := []string{}
	seen := map[string]bool{}
	for _, sc := range scopes {
		sc = strings.TrimSpace(sc)
		if sc == "" || seen[sc] {
			continue
		}
		if !ValidScope(sc) {
			return fmt.Errorf("%q is not a scope: author, audit, appeals, intake, or policy:ID", sc)
		}
		seen[sc] = true
		clean = append(clean, sc)
	}
	sort.Strings(clean)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	us, ok := u.byID[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	us.Scopes = clean
	if len(clean) == 0 {
		us.Scopes = nil
	}
	u.byID[name] = us
	return u.save()
}

// SetClinician records an account's NPI and credential, or with both empty clears them.
func (u *Users) SetClinician(name, npi, credential string) error {
	credential = strings.TrimSpace(credential)
	if (npi == "") != (credential == "") {
		return errors.New("a clinician has both an NPI and a credential")
	}
	if npi != "" && !ValidNPI(npi) {
		return fmt.Errorf("%q is not an NPI: ten digits, the last a check digit", npi)
	}
	if len(credential) > 120 {
		return errors.New("the credential is at most 120 characters")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	us, ok := u.byID[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	us.NPI, us.Credential = npi, credential
	u.byID[name] = us
	return u.save()
}

// SetProviderNPIs records the NPIs a provider account acts for.
func (u *Users) SetProviderNPIs(name string, npis []string) error {
	for _, n := range npis {
		if !ValidNPI(n) {
			return fmt.Errorf("%q is not an NPI: ten digits, the last a check digit", n)
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	us, ok := u.byID[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	if us.Role != Provider {
		return fmt.Errorf("%s is not a provider account", name)
	}
	if len(npis) == 0 {
		return errors.New("a provider account acts for at least one NPI")
	}
	us.ProviderNPIs = append([]string(nil), npis...)
	u.byID[name] = us
	return u.save()
}

// ActsFor is whether a provider account acts for an NPI.
func (us User) ActsFor(npi string) bool {
	if us.Role != Provider || npi == "" {
		return false
	}
	for _, n := range us.ProviderNPIs {
		if n == npi {
			return true
		}
	}
	return false
}

// Get returns an account without its password hash.
func (u *Users) Get(name string) (User, bool, error) {
	us, ok, err := u.lookup(name)
	us.Salt, us.Hash = "", ""
	return us, ok, err
}

// ValidNPI is whether s is a National Provider Identifier: ten digits, the last a Luhn check digit.
func ValidNPI(s string) bool {
	if len(s) != 10 {
		return false
	}
	// The Luhn check over "80840" + the first nine digits, as the NPI standard defines its check digit.
	digits := "80840" + s
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if (len(digits)-1-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// Remove deletes an account.
func (u *Users) Remove(name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return err
	}
	if _, ok := u.byID[name]; !ok {
		return fmt.Errorf("no user %q", name)
	}
	delete(u.byID, name)
	return u.save()
}

// lookup returns a user as the file now says.
func (u *Users) lookup(name string) (User, bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.reload(); err != nil {
		return User{}, false, err
	}
	us, ok := u.byID[name]
	return us, ok, nil
}

// dummy is checked against when a name is unknown, so a wrong name takes as long as a wrong password.
var dummy = User{Salt: "AAAAAAAAAAAAAAAAAAAAAA", Hash: "", Iterations: 0}

// check reports whether password is the user's.
func check(us User, password string) bool {
	salt, err := base64.RawStdEncoding.DecodeString(us.Salt)
	if err != nil {
		return false
	}
	iter := us.Iterations
	if iter <= 0 {
		iter = Iterations
	}
	h, err := hashPassword(password, salt, iter)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h), []byte(us.Hash)) == 1
}

// Session is a signed-in user.
type Session struct {
	User    string
	Role    Role
	Scopes  []string
	Via     string // "password" or "sso"
	Created time.Time
	seen    time.Time
}

// Has is whether the session holds a scope. An administrator holds every one.
func (s Session) Has(scope string) bool {
	if s.Role == Admin {
		return true
	}
	if s.Role == Provider {
		return false // a provider's account takes no job of the plan's, whatever its file says
	}
	for _, sc := range s.Scopes {
		if sc == scope {
			return true
		}
	}
	return false
}

// Sees is whether the session may see the cases of a policy: every policy's, unless its scopes name some.
func (s Session) Sees(policyID string) bool {
	if s.Role == Admin {
		return true
	}
	limited := false
	for _, sc := range s.Scopes {
		if id, ok := strings.CutPrefix(sc, ScopePolicy); ok {
			if id == policyID {
				return true
			}
			limited = true
		}
	}
	return !limited
}

// sameGrant is whether an account still grants what a session was given: a changed role or scopes ends the session.
func sameGrant(us User, sess *Session) bool {
	return us.Role == sess.Role && strings.Join(us.Scopes, ",") == strings.Join(sess.Scopes, ",") &&
		(sess.Via != "sso" || us.SSO)
}

// Sessions are the signed-in users, and the guard against guessing passwords.
type Sessions struct {
	users    *Users
	mu       sync.Mutex
	byToken  map[string]*Session
	failures map[string][]time.Time
	now      func() time.Time
	Idle     time.Duration // signed out after this long without a request
	Max      time.Duration // and after this long regardless
}

// NewSessions returns an empty session table over the accounts.
func NewSessions(users *Users) *Sessions {
	return &Sessions{users: users, byToken: map[string]*Session{}, failures: map[string][]time.Time{}, now: time.Now,
		Idle: 30 * time.Minute, Max: 12 * time.Hour}
}

// Guessing limits: this many failed sign-ins for one name within the window locks the name for the rest of the window.
const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
)

// ErrLocked is a name with too many recent failed sign-ins.
var ErrLocked = errors.New("too many failed sign-ins for this name; try again in 15 minutes")

// ErrBadCredentials is a wrong name or password; which one is deliberately not said.
var ErrBadCredentials = errors.New("wrong name or password")

// SignIn checks a name and password and returns a new session token.
func (s *Sessions) SignIn(name, password string) (string, *Session, error) {
	now := s.now()
	s.mu.Lock()
	recent := s.failures[name][:0]
	for _, t := range s.failures[name] {
		if now.Sub(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	s.failures[name] = recent
	locked := len(recent) >= maxFailures
	s.mu.Unlock()
	if locked {
		return "", nil, ErrLocked
	}

	us, ok, err := s.users.lookup(name)
	if err != nil {
		return "", nil, err
	}
	if !ok || us.SSO || us.Hash == "" {
		check(dummy, password) // the same work as a real check, so an SSO-only name is not told apart by timing
		ok = false
	}
	if !ok || !check(us, password) {
		s.mu.Lock()
		s.failures[name] = append(s.failures[name], now)
		s.mu.Unlock()
		return "", nil, ErrBadCredentials
	}

	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	sess := &Session{User: us.Name, Role: us.Role, Scopes: us.Scopes, Via: "password", Created: now, seen: now}
	s.mu.Lock()
	delete(s.failures, name)
	s.byToken[token] = sess
	s.mu.Unlock()
	return token, sess, nil
}

// ErrNotSSO is a single sign-on for a name that has no account allowed to sign in that way.
var ErrNotSSO = errors.New("no account here signs in through single sign-on under that name; an administrator adds one " +
	"with vaglio user sso")

// SignInSSO starts a session for a name the identity provider vouched for. The account must exist and be marked for
// single sign-on: the provider proves who someone is, and this server's accounts say what they may do.
func (s *Sessions) SignInSSO(name string) (string, *Session, error) {
	us, ok, err := s.users.lookup(name)
	if err != nil {
		return "", nil, err
	}
	if !ok || !us.SSO {
		return "", nil, ErrNotSSO
	}
	now := s.now()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	sess := &Session{User: us.Name, Role: us.Role, Scopes: us.Scopes, Via: "sso", Created: now, seen: now}
	s.mu.Lock()
	s.byToken[token] = sess
	s.mu.Unlock()
	return token, sess, nil
}

// Get returns the session for a token, extending it, or false if it has expired or its account has gone or changed role.
func (s *Sessions) Get(token string) (Session, bool) {
	now := s.now()
	s.mu.Lock()
	sess, ok := s.byToken[token]
	if ok && (now.Sub(sess.seen) > s.Idle || now.Sub(sess.Created) > s.Max) {
		delete(s.byToken, token)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return Session{}, false
	}
	us, exists, err := s.users.lookup(sess.User)
	if err != nil || !exists || !sameGrant(us, sess) {
		s.SignOut(token)
		return Session{}, false
	}
	s.mu.Lock()
	sess.seen = now
	out := *sess
	s.mu.Unlock()
	return out, true
}

// Account is a signed-in user's account as the file now says, without its password hash.
func (s *Sessions) Account(name string) (User, bool, error) { return s.users.Get(name) }

// SignOut ends a session.
func (s *Sessions) SignOut(token string) {
	s.mu.Lock()
	delete(s.byToken, token)
	s.mu.Unlock()
}
