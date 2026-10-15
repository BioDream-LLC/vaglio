package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/acme"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
)

// acmeOptions are serve's -acme flags: a public certificate for -hostname from an ACME authority
// (Let's Encrypt unless -acme-directory names another), kept in DATA/tls/acme and renewed 30 days before
// it runs out. Until the first one arrives, and whenever the authority cannot be reached, Vaglio's own
// authority's certificate stays in service: the console is never without https.
type acmeOptions struct {
	On        bool
	Email     string
	Directory string
	CA        string // a PEM file of authorities to trust for -acme-directory (a test authority such as Pebble)
	HTTPPort  int    // where the http-01 challenge is answered; the authority asks on 80
}

const (
	acmeCertFile = "cert.pem"
	acmeKeyFile  = "key.pem"
)

func (o acmeOptions) check(hostnames []string, tlsCert string) error {
	if !o.On {
		if o.Email != "" || o.Directory != "" || o.CA != "" {
			return errors.New("-acme-email, -acme-directory and -acme-ca go with -acme")
		}
		return nil
	}
	if tlsCert != "" {
		return errors.New("-acme and -tls-cert are two ways to get the server's certificate: choose one")
	}
	if len(hostnames) == 0 {
		return errors.New("-acme needs -hostname: the public name the certificate is for")
	}
	for _, h := range hostnames {
		if net.ParseIP(h) != nil {
			return errors.New("-acme needs a name in -hostname, not an address: public authorities issue for names")
		}
	}
	if o.HTTPPort < 0 || o.HTTPPort > 65535 {
		return errors.New("-acme-http-port is a port number, or 0 to answer only by tls-alpn-01 on port 443")
	}
	return nil
}

// startACME puts a kept ACME certificate in service at once, when there is a current one, and returns
// the manager that gets and renews it. Its ConfigForClient answers tls-alpn-01 on Vaglio's own port.
func startACME(o acmeOptions, dataDir string, hostnames []string, httpsPort int, certs *tlsserve.Source) (*acme.Manager, error) {
	dir := filepath.Join(dataDir, "tls", "acme")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certFile, keyFile := filepath.Join(dir, acmeCertFile), filepath.Join(dir, acmeKeyFile)
	if keptCovers(certFile, keyFile, hostnames) {
		if err := certs.Use(certFile, keyFile); err != nil {
			log.Printf("tls: the kept certificate in %s does not load, Vaglio's own stays in service: %v", certFile, err)
		}
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if o.CA != "" {
		c, err := tlsserve.Client(o.CA, 30*time.Second)
		if err != nil {
			return nil, fmt.Errorf("-acme-ca: %w", err)
		}
		hc = c
	}
	var domains []string
	for _, h := range hostnames {
		domains = append(domains, strings.ToLower(h))
	}
	m := &acme.Manager{
		Dir:  dir,
		HTTP: hc,
		Log:  slog.Default(),
		Settings: func() acme.Settings {
			return acme.Settings{Enabled: true, Domains: domains, Email: o.Email, Directory: o.Directory,
				HTTPPort: o.HTTPPort, HTTPSPort: httpsPort, Challenge: challengeFor(o.HTTPPort, httpsPort)}
		},
		Current: func() acme.Current {
			leaf := certs.Leaf()
			return acme.Current{Names: leaf.DNSNames, NotAfter: leaf.NotAfter, SelfSigned: certs.Local()}
		},
		Install: func(certPEM, keyPEM string) error {
			// The key first: a certificate whose key is not on disk yet would not load.
			if err := writeAtomic(keyFile, []byte(keyPEM)); err != nil {
				return err
			}
			if err := writeAtomic(certFile, []byte(certPEM)); err != nil {
				return err
			}
			return certs.Use(certFile, keyFile)
		},
	}
	return m, nil
}

// challengeFor picks how the authority's proof is answered: tls-alpn-01 when Vaglio itself is on 443, else
// http-01 on -acme-http-port. With no http port and Vaglio elsewhere, the authority could reach neither.
func challengeFor(httpPort, httpsPort int) string {
	if httpsPort == 443 && httpPort == 0 {
		return "tls-alpn-01"
	}
	if httpsPort == 443 {
		return "" // the manager's own choice: tls-alpn-01 on 443
	}
	return "http-01"
}

// keptCovers says whether a certificate kept from an earlier run names every hostname and is still current.
func keptCovers(certFile, keyFile string, hostnames []string) bool {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || pair.Leaf == nil || !time.Now().Before(pair.Leaf.NotAfter) {
		return false
	}
	for _, h := range hostnames {
		if pair.Leaf.VerifyHostname(h) != nil {
			return false
		}
	}
	return true
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runACME keeps the certificate until ctx ends, and says at start what it is waiting for.
func runACME(ctx context.Context, m *acme.Manager, o acmeOptions, hostnames []string, httpsPort int, local bool) {
	dir := o.Directory
	if dir == "" {
		dir = acme.LetsEncrypt
	}
	how := "http-01 on port " + strconv.Itoa(o.HTTPPort)
	if challengeFor(o.HTTPPort, httpsPort) != "http-01" {
		how = "tls-alpn-01 on port 443"
	}
	if local {
		log.Printf("tls: certificate for %s from %s (%s); Vaglio's own serves until it arrives", strings.Join(hostnames, ", "), dir, how)
	} else {
		log.Printf("tls: renewed from %s (%s) 30 days before it runs out", dir, how)
	}
	go func() {
		// Say once, plainly, when the first attempt fails: the reason is in the manager's status.
		time.Sleep(90 * time.Second)
		if st := m.Status(); st.State == "failed" || st.PortProblem != "" {
			log.Printf("tls: no certificate from the authority yet: %s", st.Detail)
		}
	}()
	m.Run(ctx)
}
