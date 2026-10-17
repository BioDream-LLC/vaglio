// Package tlsserve gives the server its certificate and its HTTPS-only listener.
//
// A deployment either brings a certificate (its own certificate authority's, or a public one), or lets Vaglio make one.
// Vaglio does not make a bare self-signed certificate: it makes a small certificate authority of its own, once, and
// issues the server's certificate from it. A reviewer's computer trusts that authority once (or IT pushes it to every
// computer), and after that the server certificate can renew itself without a browser warning ever coming back.
//
// The authority is name-constrained: it can only vouch for this server's own names (localhost, the loopback
// addresses and -hostname). A computer that trusts it has not handed Vaglio, or anyone who steals its key, the power to
// impersonate any other site.
package tlsserve

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// File names in the authority's directory (DATA/tls). ca.crt is public: it is the file reviewers' computers trust.
const (
	CAFile   = "ca.crt"
	caKey    = "ca.key"
	CertFile = "server.crt"
	KeyFile  = "server.key"
)

const (
	caLife     = 10 * 365 * 24 * time.Hour
	serverLife = 90 * 24 * time.Hour
	// renewBefore is how long before it expires the server certificate is replaced.
	renewBefore = 30 * 24 * time.Hour
)

// Names are what the certificate covers: DNS names and IP addresses.
type Names struct {
	DNS []string
	IPs []net.IP
}

// NamesFor returns the names a server listening on addr and reached as hostname answers to: always localhost and the
// loopback addresses, plus hostname, plus addr's own address when it is a specific one.
func NamesFor(addr, hostname string) (Names, error) {
	n := Names{DNS: []string{"localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1).To4(), net.IPv6loopback}}
	if addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return n, err
		}
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			n.addIP(ip)
		} else if ip == nil && host != "" {
			n.addDNS(host)
		}
	}
	if hostname != "" {
		if ip := net.ParseIP(hostname); ip != nil {
			n.addIP(ip)
		} else {
			n.addDNS(hostname)
		}
	}
	return n, nil
}

// With is n with one more name, a DNS name or an address.
func (n Names) With(name string) Names {
	out := Names{DNS: slices.Clone(n.DNS), IPs: slices.Clone(n.IPs)}
	if ip := net.ParseIP(name); ip != nil {
		out.addIP(ip)
	} else if name != "" {
		out.addDNS(name)
	}
	return out
}

func (n *Names) addDNS(d string) {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	if !slices.Contains(n.DNS, d) {
		n.DNS = append(n.DNS, d)
	}
}

func (n *Names) addIP(ip net.IP) {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, have := range n.IPs {
		if have.Equal(ip) {
			return
		}
	}
	n.IPs = append(n.IPs, ip)
}

// Authority is Vaglio's own certificate authority in dir.
type Authority struct {
	Dir  string
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// Created reports that this call made the authority (or replaced one that could not vouch for the names).
	Created bool
	// Replaced is the old authority's file name when it was replaced; empty otherwise.
	Replaced string
}

// Fingerprint is the authority certificate's SHA-256, the value to compare before trusting it.
func (a *Authority) Fingerprint() string { return Fingerprint(a.Cert) }

// Fingerprint formats a certificate's SHA-256 as colon-separated hex.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// Open loads the authority in dir, making it when there is none. An authority whose name constraints do not cover
// names is set aside (renamed, never deleted) and a new one made, since it could not vouch for this server.
func Open(dir string, names Names) (*Authority, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a, err := Load(dir)
	switch {
	case err == nil && a.covers(names):
		return a, nil
	case err == nil:
		old := fmt.Sprintf("ca-replaced-%s.crt", time.Now().UTC().Format("20060102T150405Z"))
		if err := fsx.Replace(filepath.Join(dir, CAFile), filepath.Join(dir, old)); err != nil {
			return nil, err
		}
		os.Remove(filepath.Join(dir, caKey))
		os.Remove(filepath.Join(dir, CertFile))
		os.Remove(filepath.Join(dir, KeyFile))
		a, err = create(dir, names)
		if a != nil {
			a.Replaced = old
		}
		return a, err
	case errors.Is(err, os.ErrNotExist):
		return create(dir, names)
	default:
		return nil, err
	}
}

// Load reads the authority in dir without making one.
func Load(dir string) (*Authority, error) {
	cert, err := readCert(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, err
	}
	key, err := readKey(filepath.Join(dir, caKey))
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("%s does not belong to %s", caKey, CAFile)
	}
	return &Authority{Dir: dir, Cert: cert, key: key}, nil
}

// covers reports whether the authority's name constraints permit every name, and it is not near its end.
func (a *Authority) covers(n Names) bool {
	if time.Until(a.Cert.NotAfter) < 2*serverLife {
		return false
	}
	for _, d := range n.DNS {
		ok := false
		for _, p := range a.Cert.PermittedDNSDomains {
			if strings.EqualFold(d, p) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	for _, ip := range n.IPs {
		ok := false
		for _, r := range a.Cert.PermittedIPRanges {
			if r.Contains(ip) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func create(dir string, n Names) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	var ranges []*net.IPNet
	for _, ip := range n.IPs {
		bits := 8 * len(ip)
		ranges = append(ranges, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Vaglio local authority " + serial.Text(16)[:8],
			Organization: []string{"Vaglio (vouches for this server only)"},
		},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         n.DNS,
		PermittedIPRanges:           ranges,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	// The key first: a ca.crt with no key beside it would be an authority that can issue nothing.
	if err := writeKey(filepath.Join(dir, caKey), key); err != nil {
		return nil, err
	}
	if err := writePEM(filepath.Join(dir, CAFile), "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	return &Authority{Dir: dir, Cert: cert, key: key, Created: true}, nil
}

// EnsureServer makes sure DIR/server.crt is a certificate from this authority, covering names and not within
// renewBefore of its end; it issues a new one when it is not. It reports whether it issued one.
func (a *Authority) EnsureServer(n Names) (bool, error) {
	certPath, keyPath := filepath.Join(a.Dir, CertFile), filepath.Join(a.Dir, KeyFile)
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if c, err := x509.ParseCertificate(pair.Certificate[0]); err == nil && a.issued(c) &&
			time.Until(c.NotAfter) > renewBefore && namesIn(c, n) {
			return false, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	serial, err := serialNumber()
	if err != nil {
		return false, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: n.DNS[len(n.DNS)-1]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(serverLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     n.DNS,
		IPAddresses:  n.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.Cert, &key.PublicKey, a.key)
	if err != nil {
		return false, err
	}
	// The key is written before the certificate, so a reader that sees the new certificate finds its key.
	if err := writeKey(keyPath, key); err != nil {
		return false, err
	}
	var chain bytes.Buffer
	pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: a.Cert.Raw})
	return true, writeFile(certPath, chain.Bytes(), 0o644)
}

func (a *Authority) issued(c *x509.Certificate) bool {
	return c.CheckSignatureFrom(a.Cert) == nil
}

func namesIn(c *x509.Certificate, n Names) bool {
	for _, d := range n.DNS {
		if c.VerifyHostname(d) != nil {
			return false
		}
	}
	for _, ip := range n.IPs {
		if c.VerifyHostname(ip.String()) != nil {
			return false
		}
	}
	return true
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func readCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return x509.ParseCertificate(b.Bytes)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, fmt.Errorf("%s holds no PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an ECDSA key", path)
	}
	return ec, nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	return writeFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode)
}

// writeFile replaces path atomically, so a server reloading it never reads half a file.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return fsx.Replace(tmp.Name(), path)
}
