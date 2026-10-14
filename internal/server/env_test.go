package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func init() { auth.Iterations = 1000 }

// env is a server over a fresh data directory, with one account per role.
type env struct {
	h         http.Handler
	log       *audit.Log
	cases     *cases.Store
	users     *auth.Users
	passwords map[string]string
	mu        sync.Mutex
	delivered []cases.Case
}

func newEnv(t *testing.T, token string) *env { return newEnvWith(t, token, nil) }

// newEnvWith is newEnv with the configuration changed by with before the server is made.
func newEnvWith(t *testing.T, token string, with func(*Config)) *env {
	t.Helper()
	dir := t.TempDir()
	var ps = map[string]*policy.Policy{}
	for _, f := range []string{"lumbar_mri", "bariatric_surgery"} {
		p, err := policy.LoadFile("../../policies/" + f + policy.Ext)
		if err != nil {
			t.Fatal(err)
		}
		ps[p.ID] = p
	}
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	cs, err := cases.Open(l, filepath.Join(dir, "bundles"))
	if err != nil {
		t.Fatal(err)
	}
	users, _ := auth.OpenUsers(filepath.Join(dir, "users.json"))
	e := &env{log: l, cases: cs, users: users, passwords: map[string]string{}}
	for _, r := range []auth.Role{auth.Viewer, auth.Reviewer, auth.Admin} {
		pw, err := users.Set(string(r), r)
		if err != nil {
			t.Fatal(err)
		}
		e.passwords[string(r)] = pw
	}
	cfg := Config{Policies: ps, AllowedHosts: []string{"127.0.0.1:8740"}, Token: token, Sessions: auth.NewSessions(users),
		Audit: l, AuditPath: filepath.Join(dir, "audit.jsonl"), Cases: cs,
		Deliver: func(c cases.Case) { e.mu.Lock(); e.delivered = append(e.delivered, c); e.mu.Unlock() }}
	if with != nil {
		with(&cfg)
	}
	e.h = New(cfg)
	return e
}

// login signs a role in and returns its session cookie.
func (e *env) login(t *testing.T, role string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": role, "password": e.passwords[role]})
	w := e.raw("POST", "/api/login", string(body), nil, "application/json")
	if w.Code != 200 {
		t.Fatalf("login %s: %d %s", role, w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func (e *env) raw(method, path, body string, cookie *http.Cookie, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8740"
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// signedIn is the handler as a signed-in browser of this role uses it: its cookie on every request, and JSON on every
// console request that changes anything.
func (e *env) signedIn(t *testing.T, role string) http.Handler {
	c := e.login(t, role)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(c)
		if r.Header.Get("Content-Type") == "" && r.Method != "GET" {
			r.Header.Set("Content-Type", "application/json")
		}
		e.h.ServeHTTP(w, r)
	})
}
