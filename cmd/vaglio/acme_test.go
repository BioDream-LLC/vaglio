package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/tlsserve"
)

// The -acme flags are refused plainly when they cannot work, and the challenge follows the port.
func TestACMEOptions(t *testing.T) {
	for _, c := range []struct {
		o        acmeOptions
		host     string
		cert     string
		mentions string
	}{
		{acmeOptions{Email: "a@example.com"}, "", "", "go with -acme"},
		{acmeOptions{On: true, HTTPPort: 80}, "", "", "needs -hostname"},
		{acmeOptions{On: true, HTTPPort: 80}, "10.0.0.5", "", "not an address"},
		{acmeOptions{On: true, HTTPPort: 80}, "pa.example.org", "plan.crt", "choose one"},
		{acmeOptions{On: true, HTTPPort: 70000}, "pa.example.org", "", "port number"},
	} {
		err := c.o.check(c.host, c.cert)
		if err == nil || !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%+v %q: %v", c.o, c.host, err)
		}
	}
	if err := (acmeOptions{On: true, HTTPPort: 80}).check("pa.example.org", ""); err != nil {
		t.Fatal(err)
	}
	if challengeFor(80, 8443) != "http-01" || challengeFor(0, 443) != "tls-alpn-01" || challengeFor(80, 443) != "" {
		t.Fatal("challenge choice")
	}
}

// A certificate kept from an earlier run is put in service at start only when it names the host and is current,
// and the source then stops renewing Vaglio's own.
func TestKeptACMECertificate(t *testing.T) {
	data := t.TempDir()
	names, _ := tlsserve.NamesFor("127.0.0.1:0", "pa.example.org")
	a, err := tlsserve.Open(filepath.Join(data, "tls"), names)
	if err != nil {
		t.Fatal(err)
	}
	certs, err := tlsserve.LocalSource(a, names)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(data, "tls", "acme")
	os.MkdirAll(dir, 0o700)
	// Stand-in for a public certificate: one Vaglio's authority issues for another name, then for the right one.
	other, _ := tlsserve.NamesFor("127.0.0.1:0", "elsewhere.example.org")
	b, _ := tlsserve.Open(filepath.Join(t.TempDir(), "tls"), other)
	b.EnsureServer(other)
	copyPair(t, b.Dir, dir)
	if _, err := startACME(acmeOptions{On: true, HTTPPort: 80}, data, "pa.example.org", 8443, certs); err != nil {
		t.Fatal(err)
	}
	if !certs.Local() {
		t.Fatal("a kept certificate for another name was put in service")
	}
	b2, _ := tlsserve.Open(filepath.Join(t.TempDir(), "tls"), names)
	b2.EnsureServer(names)
	copyPair(t, b2.Dir, dir)
	m, err := startACME(acmeOptions{On: true, HTTPPort: 80}, data, "pa.example.org", 8443, certs)
	if err != nil {
		t.Fatal(err)
	}
	if certs.Local() {
		t.Fatal("the kept certificate was not put in service")
	}
	cur := m.Current()
	if cur.SelfSigned || cur.NotAfter.Before(time.Now()) || len(cur.Names) == 0 {
		t.Fatalf("%+v", cur)
	}
	got, _ := certs.GetCertificate(&tls.ClientHelloInfo{ServerName: "pa.example.org"})
	if got.Leaf.VerifyHostname("pa.example.org") != nil {
		t.Fatal("serving the wrong certificate")
	}
}

func copyPair(t *testing.T, from, to string) {
	t.Helper()
	for src, dst := range map[string]string{tlsserve.CertFile: acmeCertFile, tlsserve.KeyFile: acmeKeyFile} {
		b, err := os.ReadFile(filepath.Join(from, src))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(to, dst), b, 0o600)
	}
}
