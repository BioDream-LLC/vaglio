package tlsserve

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func names(t *testing.T, addr, host string) Names {
	t.Helper()
	n, err := NamesFor(addr, host)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthorityIsMadeOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	n := names(t, "127.0.0.1:8740", "")
	a, err := Open(dir, n)
	if err != nil || !a.Created {
		t.Fatalf("first open: %v created=%v", err, a != nil && a.Created)
	}
	b, err := Open(dir, n)
	if err != nil || b.Created || b.Fingerprint() != a.Fingerprint() {
		t.Fatalf("second open made a new authority: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, caKey)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no Unix modes
		t.Fatalf("authority key mode %v", fi.Mode().Perm())
	}
}

func TestAuthorityVouchesOnlyForItsOwnNames(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, names(t, "127.0.0.1:8740", "vaglio.plan.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureServer(names(t, "127.0.0.1:8740", "vaglio.plan.example")); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(a.Cert)
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	for _, h := range []string{"vaglio.plan.example", "localhost", "127.0.0.1", "::1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: h}); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}

	// A certificate the authority's key signs for any other name must not verify: the constraint is the protection.
	other := names(t, "", "bank.example")
	if _, err := a.EnsureServer(other); err != nil {
		t.Fatal(err)
	}
	pair, _ = tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	leaf, _ = x509.ParseCertificate(pair.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "bank.example"}); err == nil {
		t.Fatal("the authority vouched for a name outside its constraints")
	}
}

func TestNewHostnameReplacesTheAuthorityAndKeepsTheOld(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir, names(t, "127.0.0.1:8740", ""))
	b, err := Open(dir, names(t, "127.0.0.1:8740", "vaglio.plan.example"))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Created || b.Replaced == "" || b.Fingerprint() == a.Fingerprint() {
		t.Fatalf("authority not replaced: %+v", b)
	}
	if _, err := os.Stat(filepath.Join(dir, b.Replaced)); err != nil {
		t.Fatalf("old authority not kept: %v", err)
	}
}

func TestServerCertificateIsKeptUntilItNeedsRenewing(t *testing.T) {
	dir := t.TempDir()
	n := names(t, "127.0.0.1:8740", "")
	a, _ := Open(dir, n)
	if made, err := a.EnsureServer(n); err != nil || !made {
		t.Fatalf("first: %v %v", made, err)
	}
	if made, err := a.EnsureServer(n); err != nil || made {
		t.Fatalf("second issued again: %v %v", made, err)
	}
	if made, _ := a.EnsureServer(names(t, "127.0.0.1:8740", "new.example")); !made {
		t.Fatal("a new name did not issue a new certificate")
	}
}

func TestFileSourcePicksUpAReplacedCertificate(t *testing.T) {
	dir := t.TempDir()
	n := names(t, "127.0.0.1:8740", "")
	a, _ := Open(dir, n)
	a.EnsureServer(n)
	src, err := FileSource(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	first := src.Leaf()
	time.Sleep(20 * time.Millisecond)
	a.EnsureServer(names(t, "127.0.0.1:8740", "renewed.example"))
	src.checked = time.Time{}
	src.GetCertificate(nil)
	if src.Leaf().Equal(first) {
		t.Fatal("the replaced certificate was not picked up")
	}

	// A broken file keeps the good certificate in service.
	good := src.Leaf()
	os.WriteFile(filepath.Join(dir, CertFile), []byte("not a certificate"), 0o644)
	src.checked = time.Time{}
	if c, _ := src.GetCertificate(nil); c == nil || !src.Leaf().Equal(good) {
		t.Fatal("a broken file took the certificate out of service")
	}
}

func serveOnce(t *testing.T) (addr string, roots *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	n := names(t, "127.0.0.1:0", "")
	a, _ := Open(dir, n)
	src, err := LocalSource(a, n)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = inner.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	l := Wrap(inner, Config(src), []string{addr, "localhost:" + port})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("a request reached the handler without TLS")
		}
		io.WriteString(w, "ok")
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	roots = x509.NewCertPool()
	roots.AddCert(a.Cert)
	return addr, roots
}

func TestHTTPSWorksAndPlainHTTPIsRedirected(t *testing.T) {
	addr, roots := serveOnce(t)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get("https://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("https body %q", body)
	}

	resp, err = c.Get("http://" + addr + "/cases?id=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+addr+"/cases?id=1" {
		t.Fatalf("plain http: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = c.Post("http://"+addr+"/api/login", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain http POST: %d", resp.StatusCode)
	}
}

func TestRedirectNeverNamesAnotherHost(t *testing.T) {
	addr, _ := serveOnce(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: evil.example\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMisdirectedRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestASilentConnectionDoesNotBlockOthers(t *testing.T) {
	addr, roots := serveOnce(t)
	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	resp, err := c.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestTLSBelow12IsRefused(t *testing.T) {
	addr, roots := serveOnce(t)
	_, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, MaxVersion: tls.VersionTLS11, MinVersion: tls.VersionTLS10})
	if err == nil {
		t.Fatal("a TLS 1.1 handshake succeeded")
	}
}
