package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func init() { Iterations = 1000 } // the work factor is not what these tests test

func users(t *testing.T) (*Users, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	u, err := OpenUsers(path)
	if err != nil {
		t.Fatal(err)
	}
	return u, path
}

func TestAccounts(t *testing.T) {
	u, path := users(t)
	if n, _ := u.Count(); n != 0 {
		t.Fatalf("a missing file has %d users", n)
	}
	pw, err := u.Set("dr.lee", Reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 20 {
		t.Errorf("password %d characters", len(pw))
	}
	for _, bad := range []string{"Dr", "a", "x y", "1abc", "has/slash"} {
		if _, err := u.Set(bad, Viewer); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if _, err := u.Set("ok", "owner"); err == nil {
		t.Error("unknown role accepted")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows keeps no Unix modes
		t.Errorf("users file mode %v", info.Mode().Perm())
	}
	list, _ := u.List()
	if len(list) != 1 || list[0].Hash != "" || list[0].Role != Reviewer {
		t.Errorf("list %+v", list)
	}
	// Another process (the command line) changes the file; this one sees it.
	other, _ := OpenUsers(path)
	if _, err := other.Set("admin", Admin); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if n, _ := u.Count(); n != 2 {
		t.Errorf("a change made elsewhere was not seen: %d users", n)
	}
	if err := u.Remove("nobody"); err == nil {
		t.Error("removing an unknown user succeeded")
	}
}

func TestRoles(t *testing.T) {
	if !Admin.Can(Reviewer) || !Reviewer.Can(Viewer) || Viewer.Can(Reviewer) || Reviewer.Can(Admin) || Admin.Can("") {
		t.Error("role order")
	}
}

func TestSessions(t *testing.T) {
	u, path := users(t)
	pw, _ := u.Set("dr.lee", Reviewer)
	s := NewSessions(u)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	if _, _, err := s.SignIn("dr.lee", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := s.SignIn("nobody", pw); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown name: %v", err)
	}
	token, sess, err := s.SignIn("dr.lee", pw)
	if err != nil || sess.Role != Reviewer {
		t.Fatal(err)
	}
	if got, ok := s.Get(token); !ok || got.User != "dr.lee" {
		t.Fatal("a fresh session is not valid")
	}
	clock = clock.Add(29 * time.Minute)
	if _, ok := s.Get(token); !ok {
		t.Fatal("a session used within the idle limit expired")
	}
	clock = clock.Add(31 * time.Minute)
	if _, ok := s.Get(token); ok {
		t.Fatal("an idle session is still valid")
	}

	// A session ends when its account's role changes, or the account goes.
	token, _, _ = s.SignIn("dr.lee", pw)
	other, _ := OpenUsers(path)
	time.Sleep(10 * time.Millisecond)
	other.Set("dr.lee", Viewer)
	if _, ok := s.Get(token); ok {
		t.Error("a session outlived a change of role")
	}

	// The absolute limit holds even for a session in constant use.
	pw, _ = u.Set("dr.lee", Reviewer)
	token, _, _ = s.SignIn("dr.lee", pw)
	for i := 0; i < 26; i++ {
		clock = clock.Add(29 * time.Minute)
		s.Get(token)
	}
	if _, ok := s.Get(token); ok {
		t.Error("a session outlived the 12 hour limit")
	}
	s.SignOut(token)
}

func TestGuessingIsLimited(t *testing.T) {
	u, _ := users(t)
	pw, _ := u.Set("dr.lee", Reviewer)
	s := NewSessions(u)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	for i := 0; i < maxFailures; i++ {
		s.SignIn("dr.lee", "guess")
	}
	if _, _, err := s.SignIn("dr.lee", pw); !errors.Is(err, ErrLocked) {
		t.Fatalf("the right password after %d guesses: %v", maxFailures, err)
	}
	clock = clock.Add(failureWindow + time.Second)
	if _, _, err := s.SignIn("dr.lee", pw); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestScopes(t *testing.T) {
	u, path := users(t)
	if _, err := u.Set("ana", Viewer); err != nil {
		t.Fatal(err)
	}
	if err := u.SetScopes("ana", []string{"author", "policy:lumbar_mri", "author", " "}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"root", "policy:", "policy:Bad-ID"} {
		if err := u.SetScopes("ana", []string{bad}); err == nil {
			t.Errorf("scope %q was accepted", bad)
		}
	}
	got, _, _ := u.Get("ana")
	if len(got.Scopes) != 2 || got.Scopes[0] != "author" {
		t.Errorf("scopes %v", got.Scopes)
	}
	s := NewSessions(u)
	pw, _ := u.Set("ana", Viewer) // a new password keeps the scopes
	tok, sess, err := s.SignIn("ana", pw)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.Has(ScopeAuthor) || sess.Has(ScopeAudit) || !sess.Sees("lumbar_mri") || sess.Sees("cpap_osa") {
		t.Errorf("session %+v", sess)
	}
	if admin := (Session{Role: Admin}); !admin.Has(ScopeAudit) || !admin.Sees("anything") {
		t.Error("an administrator is narrowed")
	}
	if plain := (Session{Role: Reviewer}); !plain.Sees("anything") || plain.Has(ScopeAuthor) {
		t.Error("no scopes should see every policy and hold no scope")
	}
	// Changing the scopes ends the session, as changing the role does.
	if err := u.SetScopes("ana", []string{"author"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(tok); ok {
		t.Error("the session outlived a change of scopes")
	}
	// A scope this version does not know, written into the file by hand, is refused rather than ignored.
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), `"author"`, `"superuser"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := OpenUsers(path); err == nil {
		t.Error("an unknown scope was accepted")
	}
}

func TestSSOAccounts(t *testing.T) {
	u, _ := users(t)
	s := NewSessions(u)
	if _, _, err := s.SignInSSO("kim"); !errors.Is(err, ErrNotSSO) {
		t.Errorf("an unknown name signed in: %v", err)
	}
	pw, _ := u.Set("kim", Reviewer)
	if _, _, err := s.SignInSSO("kim"); !errors.Is(err, ErrNotSSO) {
		t.Errorf("a password account was taken over through SSO: %v", err)
	}
	if err := u.SetSSO("kim", Reviewer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SignIn("kim", pw); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("an SSO account took its old password: %v", err)
	}
	if _, _, err := s.SignIn("kim", ""); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("an SSO account took an empty password: %v", err)
	}
	tok, sess, err := s.SignInSSO("kim")
	if err != nil || sess.Via != "sso" || sess.Role != Reviewer {
		t.Fatalf("%v %+v", err, sess)
	}
	// Turned back into a password account, its SSO sessions end.
	if _, err := u.Set("kim", Reviewer); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(tok); ok {
		t.Error("an SSO session outlived the account's SSO")
	}
}

// A stranger who knows a name can lock it, but not out of a browser its owner has signed in from before; that browser
// has its own count, and a forged, old or borrowed cookie counts as a stranger's.
func TestKnownBrowserIsNotLockedOut(t *testing.T) {
	u, _ := users(t)
	pw, _ := u.Set("dr.lee", Reviewer)
	pw2, _ := u.Set("dr.kim", Reviewer)
	s := NewSessions(u)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	_, _, device, err := s.SignInDevice("dr.lee", pw, "")
	if err != nil || device == "" {
		t.Fatalf("first sign-in: %q %v", device, err)
	}
	_, _, kimDevice, _ := s.SignInDevice("dr.kim", pw2, "")
	for i := 0; i < maxFailures+3; i++ {
		s.SignIn("dr.lee", "guess")
	}
	if _, _, err := s.SignIn("dr.lee", pw); !errors.Is(err, ErrLocked) {
		t.Fatalf("a stranger's browser after the guesses: %v", err)
	}
	if _, _, _, err := s.SignInDevice("dr.lee", pw, device); err != nil {
		t.Fatalf("the owner's browser was locked out by a stranger: %v", err)
	}
	// Someone else's browser, a forged cookie and an expired one are strangers.
	for name, c := range map[string]string{"another name's": kimDevice, "forged": device[:len(device)-2] + "xx",
		"empty": ""} {
		if _, _, _, err := s.SignInDevice("dr.lee", pw, c); !errors.Is(err, ErrLocked) {
			t.Errorf("a %s cookie got past the lock: %v", name, err)
		}
	}
	// Guessing from the known browser is limited too, on its own count.
	_, _, device, _ = s.SignInDevice("dr.lee", pw, device)
	for i := 0; i < maxFailures; i++ {
		s.SignInDevice("dr.lee", "guess", device)
	}
	if _, _, _, err := s.SignInDevice("dr.lee", pw, device); !errors.Is(err, ErrLocked) {
		t.Fatalf("guesses from a known browser are not limited: %v", err)
	}
	clock = clock.Add(DeviceLife + time.Hour)
	if s.KnownDevice("dr.lee", device) {
		t.Error("a device cookie older than DeviceLife is still known")
	}
}

func TestDeviceKeyKept(t *testing.T) {
	dir := t.TempDir()
	a, err := DeviceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceKey(dir)
	if err != nil || string(a) != string(b) || len(a) != 32 {
		t.Fatalf("the key was not kept: %v", err)
	}
}

// TestStrangerLocksOnlyThemselves: guessing at a name from one address locks that address out of the name, not the
// owner on a new browser elsewhere; guessing spread over many addresses meets the name-wide limit, which still never
// reaches a browser the owner has used before.
func TestStrangerLocksOnlyThemselves(t *testing.T) {
	u, _ := users(t)
	pw, _ := u.Set("dr.lee", Reviewer)
	s := NewSessions(u)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	_, _, device, _ := s.SignInFrom("dr.lee", pw, "", "198.51.100.9")
	for i := 0; i < maxFailures+5; i++ {
		s.SignInFrom("dr.lee", "guess", "", "203.0.113.7")
	}
	if _, _, _, err := s.SignInFrom("dr.lee", pw, "", "203.0.113.7"); !errors.Is(err, ErrLocked) {
		t.Fatalf("the guessing address got in with the right password: %v", err)
	}
	if _, _, _, err := s.SignInFrom("dr.lee", pw, "", "192.0.2.44"); err != nil {
		t.Fatalf("the owner on a new browser elsewhere was locked out by one stranger: %v", err)
	}
	// Spread over many addresses, each staying under its own limit.
	for i := 0; i < maxNameFailures; i++ {
		s.SignInFrom("dr.lee", "guess", "", "10.0.0."+strconv.Itoa(i/(maxFailures-1)))
	}
	if _, _, _, err := s.SignInFrom("dr.lee", pw, "", "192.0.2.200"); !errors.Is(err, ErrLocked) {
		t.Fatalf("guessing from many addresses is not limited on the name: %v", err)
	}
	if _, _, _, err := s.SignInFrom("dr.lee", pw, device, "192.0.2.200"); err != nil {
		t.Fatalf("the owner's known browser was locked out by many strangers: %v", err)
	}
	clock = clock.Add(failureWindow + time.Second)
	if _, _, _, err := s.SignInFrom("dr.lee", pw, "", "192.0.2.200"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}
