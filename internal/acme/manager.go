package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Settings say what the manager gets and how it proves the names.
type Settings struct {
	Enabled   bool
	Domains   []string
	Email     string
	Directory string // a directory URL; empty for Let's Encrypt
	// Challenge is http-01, tls-alpn-01, or empty to choose: tls-alpn-01 when Vaglio listens
	// on port 443, http-01 otherwise.
	Challenge string
	HTTPPort  int // where http-01 is answered, besides Vaglio's own port (default 80)
	HTTPSPort int // Vaglio's port
}

// Current describes the certificate in use.
type Current struct {
	Names      []string
	NotAfter   time.Time
	SelfSigned bool
}

// Status is what the manager last did, for the server's status.
type Status struct {
	State     string    `json:"state"` // off, ok, working, failed
	Detail    string    `json:"detail,omitempty"`
	LastTry   time.Time `json:"lastTry,omitzero"`
	NextTry   time.Time `json:"nextTry,omitzero"`
	NotAfter  time.Time `json:"notAfter,omitzero"`
	Challenge string    `json:"challenge,omitempty"`
	Listening string    `json:"listening,omitempty"` // the http-01 port it answers on, when it could open it
	// PortProblem says why the http-01 port could not be opened, and how to allow it.
	PortProblem string `json:"portProblem,omitempty"`
}

// Manager keeps a certificate from an ACME authority: it gets one when the names change or the one
// in use is self-signed, renews it 30 days before it expires, and answers the challenges.
type Manager struct {
	Dir      string // where the account key is kept
	Settings func() Settings
	Current  func() Current
	Install  func(certPEM, keyPEM string) error
	Log      *slog.Logger
	// Renew is how long before expiry it renews (default 30 days).
	Renew time.Duration
	// HTTP is the client for the authority (tests trust their own CA with it).
	HTTP *http.Client

	mu      sync.Mutex
	tokens  map[string]string           // http-01 token -> key authorization
	alpn    map[string]*tls.Certificate // tls-alpn-01 domain -> answer
	status  Status
	wake    chan struct{}
	failed  int
	ln      net.Listener
	lnPort  int
	lnError string
}

// Wake makes the manager look again at once (after the settings change).
func (m *Manager) Wake() {
	m.mu.Lock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	w := m.wake
	m.failed = 0
	m.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

// Status reports what the manager last did.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	if m.ln != nil {
		s.Listening = strconv.Itoa(m.lnPort)
	}
	if m.lnError != "" {
		s.PortProblem = m.lnError
		if s.Detail == "" {
			s.Detail = m.lnError
		}
	}
	return s
}

func (m *Manager) setStatus(f func(*Status)) {
	m.mu.Lock()
	f(&m.status)
	m.mu.Unlock()
}

// Run checks every hour, and whenever woken, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	w := m.wake
	m.mu.Unlock()
	defer m.closeListener()
	next := time.Now()
	for {
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-w:
		case <-time.After(wait):
		}
		next = m.check(ctx)
	}
}

// check gets or renews the certificate when it is due, and says when to look again.
func (m *Manager) check(ctx context.Context) time.Time {
	s := m.Settings()
	m.syncListener(s)
	if !s.Enabled || len(s.Domains) == 0 {
		m.setStatus(func(st *Status) { *st = Status{State: "off"} })
		return time.Now().Add(time.Hour)
	}
	renew := m.Renew
	if renew <= 0 {
		renew = 30 * 24 * time.Hour
	}
	cur := m.Current()
	covered := !cur.SelfSigned
	for _, d := range s.Domains {
		covered = covered && slices.Contains(cur.Names, d)
	}
	due := cur.NotAfter.Add(-renew)
	if covered && time.Now().Before(due) {
		m.setStatus(func(st *Status) {
			st.State, st.Detail, st.NotAfter, st.NextTry = "ok", "", cur.NotAfter, due
		})
		return minTime(due, time.Now().Add(12*time.Hour))
	}
	kind := challengeKind(s)
	m.setStatus(func(st *Status) { st.State, st.Detail, st.LastTry, st.Challenge = "working", "", time.Now(), kind })
	tctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err := m.obtain(tctx, s, kind)
	cancel()
	if err != nil {
		m.mu.Lock()
		m.failed++
		back := time.Hour << min(m.failed-1, 4) // 1, 2, 4, 8, 16 hours: within the authority's limits
		m.mu.Unlock()
		if back > 24*time.Hour {
			back = 24 * time.Hour
		}
		if m.Log != nil {
			m.Log.Warn("certificate from the ACME authority failed", "names", strings.Join(s.Domains, ","), "err", err.Error())
		}
		m.setStatus(func(st *Status) { st.State, st.Detail, st.NextTry = "failed", err.Error(), time.Now().Add(back) })
		return time.Now().Add(back)
	}
	m.mu.Lock()
	m.failed = 0
	m.mu.Unlock()
	cur = m.Current()
	if m.Log != nil {
		m.Log.Info("certificate from the ACME authority installed", "names", strings.Join(s.Domains, ","), "until", cur.NotAfter.Format(time.DateOnly))
	}
	m.setStatus(func(st *Status) {
		st.State, st.Detail, st.NotAfter, st.NextTry = "ok", "", cur.NotAfter, cur.NotAfter.Add(-renew)
	})
	return time.Now().Add(12 * time.Hour)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func challengeKind(s Settings) string {
	if s.Challenge != "" {
		return s.Challenge
	}
	if s.HTTPSPort == 443 {
		return "tls-alpn-01"
	}
	return "http-01"
}

func (m *Manager) obtain(ctx context.Context, s Settings, kind string) error {
	key, err := m.accountKey()
	if err != nil {
		return err
	}
	dir := s.Directory
	if dir == "" {
		dir = LetsEncrypt
	}
	c := &Client{DirectoryURL: dir, Key: key, HTTP: m.HTTP}
	if err := c.Register(ctx, s.Email); err != nil {
		return err
	}
	certPEM, keyPEM, err := c.Obtain(ctx, s.Domains, solver{m, kind})
	if err != nil {
		return err
	}
	return m.Install(string(certPEM), string(keyPEM))
}

// accountKey loads the account key, or makes and keeps one.
func (m *Manager) accountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(m.Dir, "acme-account.pem")
	if b, err := os.ReadFile(path); err == nil {
		return DecodeKey(b)
	}
	k, err := NewKey()
	if err != nil {
		return nil, err
	}
	b, err := EncodeKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := fsx.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

type solver struct {
	m    *Manager
	kind string
}

func (s solver) Kinds() []string { return []string{s.kind} }

func (s solver) Present(kind, domain, token, keyAuth string) error {
	m := s.m
	switch kind {
	case "http-01":
		m.mu.Lock()
		if m.tokens == nil {
			m.tokens = map[string]string{}
		}
		m.tokens[token] = keyAuth
		m.mu.Unlock()
	case "tls-alpn-01":
		c, err := ALPNCert(domain, keyAuth)
		if err != nil {
			return err
		}
		m.mu.Lock()
		if m.alpn == nil {
			m.alpn = map[string]*tls.Certificate{}
		}
		m.alpn[strings.ToLower(domain)] = &c
		m.mu.Unlock()
	default:
		return errors.New("unknown challenge " + kind)
	}
	return nil
}

func (s solver) CleanUp(kind, domain, token string) {
	m := s.m
	m.mu.Lock()
	delete(m.tokens, token)
	delete(m.alpn, strings.ToLower(domain))
	m.mu.Unlock()
}

const challengePath = "/.well-known/acme-challenge/"

// Handler answers http-01 challenges, and passes every other request to next.
func (m *Manager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(r.URL.Path, challengePath); ok && r.Method == http.MethodGet {
			m.mu.Lock()
			ka, found := m.tokens[tok]
			m.mu.Unlock()
			if !found {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte(ka))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ConfigForClient answers a tls-alpn-01 challenge on Vaglio's port; every other client
// gets base (tls.Config.GetConfigForClient).
func (m *Manager) ConfigForClient(base *tls.Config) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		if !slices.Contains(h.SupportedProtos, ALPNProto) {
			return nil, nil
		}
		m.mu.Lock()
		c := m.alpn[strings.ToLower(h.ServerName)]
		m.mu.Unlock()
		if c == nil {
			return nil, errors.New("no ACME challenge for " + h.ServerName)
		}
		return &tls.Config{Certificates: []tls.Certificate{*c}, NextProtos: []string{ALPNProto}, MinVersion: tls.VersionTLS12}, nil
	}
}

// syncListener opens or closes the http-01 port as the settings say. Everything else on it is sent
// to Vaglio over HTTPS.
func (m *Manager) syncListener(s Settings) {
	want := 0
	if s.Enabled && challengeKind(s) == "http-01" {
		want = s.HTTPPort
		if want == 0 {
			want = 80
		}
		if want == s.HTTPSPort {
			want = 0 // Vaglio's own port answers already
		}
	}
	m.mu.Lock()
	same := want == m.lnPort && (want == 0 || m.ln != nil)
	m.mu.Unlock()
	if same {
		return
	}
	m.closeListener()
	if want == 0 {
		return
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(want))
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lnError = portAdvice(want, err)
		m.lnPort = want
		return
	}
	m.ln, m.lnPort, m.lnError = ln, want, ""
	httpsPort, names := s.HTTPSPort, slices.Clone(s.Domains)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !slices.Contains(names, strings.ToLower(host)) {
			http.NotFound(w, r) // only the certificate's own names are sent on, never anywhere a client asks
			return
		}
		target := "https://" + host
		if httpsPort != 443 && httpsPort != 0 {
			target += ":" + strconv.Itoa(httpsPort)
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusPermanentRedirect)
	}))}
	go func() { _ = srv.Serve(ln) }()
}

func (m *Manager) closeListener() {
	m.mu.Lock()
	ln := m.ln
	m.ln, m.lnPort = nil, 0
	m.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// portAdvice says, in plain words, why the http-01 port would not open and what allows it.
func portAdvice(port int, err error) string {
	p := strconv.Itoa(port)
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
		exe, _ := os.Executable()
		if exe == "" {
			exe = "vaglio"
		}
		if runtime.GOOS == "linux" {
			return "port " + p + " is below 1024 and Vaglio may not open it: run `sudo setcap cap_net_bind_service=+ep " + exe +
				"` (again after each upgrade), or under systemd add AmbientCapabilities=CAP_NET_BIND_SERVICE; or forward port " + p + " on the router to Vaglio's port"
		}
		return "port " + p + " may not be opened by Vaglio (" + err.Error() + "): run it with the right to open low ports, or forward port " + p + " on the router to Vaglio's port"
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return "port " + p + " is used by another program (a web server?): stop it, or have it pass /.well-known/acme-challenge/ to Vaglio"
	}
	return "port " + p + " could not be opened for the http-01 challenge (" + err.Error() + "): forward it to Vaglio's port"
}

// Reach is what a check of one name found: whether a request for a test file, sent to the name
// from this server, came back to Vaglio.
type Reach struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// CheckReach asks each name for a test challenge file, through public DNS and the network, the way
// the authority will (http-01, port 80), and says whether this server answered. A router that does
// not loop its own public address back (no "hairpin NAT") makes a working setup fail from inside, so
// a failure is a warning, not a verdict.
func (m *Manager) CheckReach(ctx context.Context, names []string) []Reach {
	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	token := "vaglio-check-" + hex.EncodeToString(tok)
	want := "check." + hex.EncodeToString(tok)
	m.mu.Lock()
	if m.tokens == nil {
		m.tokens = map[string]string{}
	}
	m.tokens[token] = want
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.tokens, token); m.mu.Unlock() }()
	port := "80"
	if m.Settings != nil {
		if s := m.Settings(); s.HTTPPort > 0 {
			port = strconv.Itoa(s.HTTPPort) // a test's port; the authority always uses 80
		}
	}
	cl := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var out []Reach
	for _, n := range names {
		r := Reach{Name: n}
		addrs, err := net.DefaultResolver.LookupHost(ctx, n)
		switch {
		case err != nil:
			r.Detail = "not in DNS: " + err.Error()
		default:
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(n, port)+challengePath+token, nil)
			resp, err := cl.Do(req)
			if err != nil {
				r.Detail = "points at " + strings.Join(addrs, ", ") + ", but port 80 did not answer (" + err.Error() + "); a router without hairpin NAT fails this check from inside even when it works from outside"
				break
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == 200 && string(b) == want {
				r.OK, r.Detail = true, "points at "+strings.Join(addrs, ", ")+" and reached Vaglio on port 80"
			} else {
				r.Detail = "port 80 at " + strings.Join(addrs, ", ") + " answered, but not as Vaglio (status " + strconv.Itoa(resp.StatusCode) + "): another server or the router answers there"
			}
		}
		out = append(out, r)
	}
	return out
}
