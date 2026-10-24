package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The JWS is ES256 over protected.payload, verifiable with the JWK it carries, and the thumbprint
// is RFC 7638's (members in order, no spaces).
func TestSignAndThumbprint(t *testing.T) {
	k, _ := NewKey()
	c := &Client{Key: k}
	raw, err := c.sign("https://ca.test/new-acct", "nonce1", []byte(`{"a":1}`), true)
	if err != nil {
		t.Fatal(err)
	}
	var jws struct{ Protected, Payload, Signature string }
	json.Unmarshal(raw, &jws)
	prot, _ := base64.RawURLEncoding.DecodeString(jws.Protected)
	var hdr struct {
		Alg, Nonce, URL string
		JWK             struct{ X, Y string }
	}
	if err := json.Unmarshal(prot, &hdr); err != nil || hdr.Alg != "ES256" || hdr.Nonce != "nonce1" || hdr.URL != "https://ca.test/new-acct" {
		t.Fatalf("protected %s %v", prot, err)
	}
	x, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.X)
	y, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.Y)
	pub := ecdsa.PublicKey{Curve: k.Curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	sig, _ := base64.RawURLEncoding.DecodeString(jws.Signature)
	sum := sha256.Sum256([]byte(jws.Protected + "." + jws.Payload))
	if len(sig) != 64 || !ecdsa.Verify(&pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify with the JWK")
	}
	if !strings.HasPrefix(c.jwk(), `{"crv":"P-256","kty":"EC","x":"`) || strings.Contains(c.jwk(), " ") {
		t.Fatalf("jwk %s", c.jwk())
	}
	if ka := c.KeyAuth("tok"); !strings.HasPrefix(ka, "tok.") || len(ka) != 4+43 {
		t.Fatalf("key authorization %s", ka)
	}
	// tls-alpn-01: the answer carries the SHA-256 of the key authorization, critical.
	cert, err := ALPNCert("vaglio.example.com", "tok.abc")
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	found := false
	for _, e := range leaf.Extensions {
		if e.Id.Equal(idPeAcmeIdentifier) && e.Critical && len(e.Value) == 34 && e.Value[0] == 0x04 && e.Value[1] == 32 {
			found = true
		}
	}
	if !found || leaf.DNSNames[0] != "vaglio.example.com" {
		t.Fatal("acmeIdentifier extension missing")
	}
}

// Against Pebble, Let's Encrypt's own test authority (an independent implementation of RFC 8555):
// http-01 on the manager's own port, then tls-alpn-01 on an HTTPS port, each installing the
// certificate. Set VAGLIO_TEST_PEBBLE to the directory holding pebble and certs/.
func TestPebble(t *testing.T) {
	dir := os.Getenv("VAGLIO_TEST_PEBBLE")
	if dir == "" {
		t.Skip("set VAGLIO_TEST_PEBBLE to run against Pebble")
	}
	cfg := filepath.Join(t.TempDir(), "pebble.json")
	os.WriteFile(cfg, []byte(`{"pebble":{"listenAddress":"127.0.0.1:14000","managementListenAddress":"127.0.0.1:15000",
		"certificate":"`+dir+`/certs/localhost/cert.pem","privateKey":"`+dir+`/certs/localhost/key.pem",
		"httpPort":5002,"tlsPort":5001,"ocspResponderURL":"","externalAccountBindingRequired":false}}`), 0o600)
	cmd := exec.Command(filepath.Join(dir, "pebble"), "-config", cfg, "-strict")
	cmd.Env = append(os.Environ(), "PEBBLE_VA_NOSLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	roots := x509.NewCertPool()
	ca, _ := os.ReadFile(filepath.Join(dir, "certs", "pebble.minica.pem"))
	roots.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:14000"); err == nil {
			c.Close()
			break
		}
		if i > 100 {
			t.Fatal("pebble did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}

	var mu sync.Mutex
	var installed []string
	cur := Current{SelfSigned: true}
	settings := Settings{Enabled: true, Domains: []string{"localhost"}, Email: "admin@example.com",
		Directory: "https://127.0.0.1:14000/dir", Challenge: "http-01", HTTPPort: 5002, HTTPSPort: 8443}
	m := &Manager{Dir: t.TempDir(), HTTP: client,
		Settings: func() Settings { mu.Lock(); defer mu.Unlock(); return settings },
		Current:  func() Current { mu.Lock(); defer mu.Unlock(); return cur },
		Install: func(certPEM, keyPEM string) error {
			c, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
			if err != nil {
				return err
			}
			leaf, _ := x509.ParseCertificate(c.Certificate[0])
			mu.Lock()
			installed = append(installed, leaf.DNSNames[0])
			cur = Current{Names: leaf.DNSNames, NotAfter: leaf.NotAfter}
			mu.Unlock()
			return nil
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m.check(ctx)
	if st := m.Status(); st.State != "ok" || len(installed) != 1 {
		t.Fatalf("http-01: %+v %v", st, installed)
	}
	// Not due: nothing is asked for.
	m.check(ctx)
	if len(installed) != 1 {
		t.Fatal("renewed a certificate that was not due")
	}
	// On demand: the http-01 port is free between orders, held only while one is fetched.
	mu.Lock()
	settings.HTTPOnDemand = true
	mu.Unlock()
	m.check(ctx)
	if l, err := net.Listen("tcp", ":5002"); err != nil {
		t.Fatalf("on demand, the port is held while nothing is due: %v", err)
	} else {
		l.Close()
	}
	mu.Lock()
	cur.NotAfter = time.Now().Add(24 * time.Hour)
	mu.Unlock()
	m.check(ctx)
	if st := m.Status(); st.State != "ok" || len(installed) != 2 {
		t.Fatalf("on demand: %+v %v", st, installed)
	}
	if l, err := net.Listen("tcp", ":5002"); err != nil {
		t.Fatalf("on demand, the port stayed held after the order: %v", err)
	} else {
		l.Close()
	}
	mu.Lock()
	settings.HTTPOnDemand = false
	mu.Unlock()
	// tls-alpn-01 on an HTTPS port, after the certificate is due.
	mu.Lock()
	settings.Challenge, settings.HTTPSPort = "tls-alpn-01", 5001
	cur.NotAfter = time.Now().Add(24 * time.Hour)
	mu.Unlock()
	base := &tls.Config{Certificates: []tls.Certificate{mustSelfSigned(t)}}
	base.GetConfigForClient = m.ConfigForClient(base)
	ln, err := tls.Listen("tcp", ":5001", base)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, http.NotFoundHandler())
	m.check(ctx)
	if st := m.Status(); st.State != "ok" || len(installed) != 3 {
		t.Fatalf("tls-alpn-01: %+v", st)
	}
	// A name the authority cannot reach fails plainly, and backs off.
	mu.Lock()
	settings.Domains, settings.Challenge = []string{"vaglio-unreachable.invalid"}, "http-01"
	mu.Unlock()
	next := m.check(ctx)
	if st := m.Status(); st.State != "failed" || st.Detail == "" || time.Until(next) < 50*time.Minute {
		t.Fatalf("failure: %+v next %s", st, time.Until(next))
	}
}

func mustSelfSigned(t *testing.T) tls.Certificate {
	c, err := ALPNCert("other", "x.y")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The names check: a name reaching this server's challenge answer passes, one that does not is
// explained; the port advice names the fix.
func TestCheckReach(t *testing.T) {
	m := &Manager{}
	srv := httptest.NewServer(m.Handler(http.NotFoundHandler()))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	m.Settings = func() Settings { return Settings{HTTPPort: p} }
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	defer other.Close()
	got := m.CheckReach(context.Background(), []string{"localhost", "no-such-name.invalid"})
	if !got[0].OK || got[1].OK || !strings.Contains(got[1].Detail, "not in DNS") {
		t.Fatalf("%+v", got)
	}
	if len(m.tokens) != 0 {
		t.Fatal("the test token was left behind")
	}
	_, oport, _ := net.SplitHostPort(other.Listener.Addr().String())
	op, _ := strconv.Atoi(oport)
	m.Settings = func() Settings { return Settings{HTTPPort: op} }
	if got := m.CheckReach(context.Background(), []string{"localhost"}); got[0].OK || !strings.Contains(got[0].Detail, "not as Vaglio") {
		t.Fatalf("another server: %+v", got)
	}
	if a := portAdvice(80, &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EACCES)}); !strings.Contains(a, "1024") && !strings.Contains(a, "low ports") {
		t.Fatal(a)
	}
	if a := portAdvice(80, &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}); !strings.Contains(a, "another program") {
		t.Fatal(a)
	}
}

// On demand, with the port taken by another program (certbot renewing another site): no order is begun, and the
// manager says so and looks again in 15 minutes.
func TestOnDemandPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	other, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err == nil {
		other.Close()
		t.Skip("this system lets two listeners share the port")
	}
	m := &Manager{Dir: t.TempDir(), HTTP: http.DefaultClient,
		Settings: func() Settings {
			return Settings{Enabled: true, Domains: []string{"example.org"}, Directory: "https://127.0.0.1:1/dir",
				Challenge: "http-01", HTTPPort: port, HTTPSPort: 8443, HTTPOnDemand: true}
		},
		Current: func() Current { return Current{SelfSigned: true} },
		Install: func(string, string) error { return nil }}
	next := m.check(context.Background())
	if st := m.Status(); st.State != "waiting" || st.Detail == "" || time.Until(next) < 14*time.Minute || time.Until(next) > 16*time.Minute {
		t.Fatalf("%+v next %v", st, time.Until(next))
	}
}
