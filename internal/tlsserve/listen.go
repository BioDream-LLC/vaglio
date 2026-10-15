package tlsserve

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Source hands the TLS stack the current certificate. It rereads the files when they change (a renewed certificate
// is picked up without a restart), and, when it serves Vaglio's own authority's certificate, renews that before it
// runs out. A file that fails to load keeps the last good certificate in service, and the failure is logged.
type Source struct {
	certFile, keyFile string
	renew             func() (bool, error) // nil when the deployment brought its own certificate

	mu      sync.Mutex
	cert    *tls.Certificate
	leaf    *x509.Certificate
	modTime time.Time
	checked time.Time
}

// checkEvery bounds how often the files are looked at: a handshake should not cost a stat call.
const checkEvery = 10 * time.Second

// FileSource serves the certificate and key in the files a deployment gave.
func FileSource(certFile, keyFile string) (*Source, error) {
	s := &Source{certFile: certFile, keyFile: keyFile}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// LocalSource serves the authority's server certificate for names, issuing and renewing it as needed.
func LocalSource(a *Authority, names Names) (*Source, error) {
	s := &Source{
		certFile: a.Dir + string(os.PathSeparator) + CertFile,
		keyFile:  a.Dir + string(os.PathSeparator) + KeyFile,
		renew:    func() (bool, error) { return a.EnsureServer(names) },
	}
	if _, err := s.renew(); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Source) load() error {
	fi, err := os.Stat(s.certFile)
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	pair.Leaf = leaf
	s.cert, s.leaf, s.modTime = &pair, leaf, fi.ModTime()
	return nil
}

// Use switches to the certificate and key in these files (one from an ACME authority, say), from the next handshake.
// Vaglio's own authority stops renewing: the files' owner keeps them current, and a change to them is picked up as
// with -tls-cert. When the files do not load, nothing changes.
func (s *Source) Use(certFile, keyFile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := &Source{certFile: certFile, keyFile: keyFile}
	if err := n.load(); err != nil {
		return err
	}
	s.certFile, s.keyFile, s.renew = certFile, keyFile, nil
	s.cert, s.leaf, s.modTime, s.checked = n.cert, n.leaf, n.modTime, time.Now()
	return nil
}

// Local says whether the certificate in service is Vaglio's own authority's.
func (s *Source) Local() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renew != nil
}

// Leaf is the certificate being served now.
func (s *Source) Leaf() *x509.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaf
}

// GetCertificate is tls.Config.GetCertificate.
func (s *Source) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) >= checkEvery {
		s.checked = time.Now()
		s.refresh()
	}
	return s.cert, nil
}

func (s *Source) refresh() {
	if s.renew != nil && time.Until(s.leaf.NotAfter) < renewBefore {
		if _, err := s.renew(); err != nil {
			log.Printf("tls: renewing the server certificate failed, the current one stays in service: %v", err)
		}
	}
	fi, err := os.Stat(s.certFile)
	if err != nil || fi.ModTime().Equal(s.modTime) {
		return
	}
	old := s.leaf
	if err := s.load(); err != nil {
		log.Printf("tls: %s changed but does not load, the previous certificate stays in service: %v", s.certFile, err)
		return
	}
	if !old.Equal(s.leaf) {
		log.Printf("tls: now serving the certificate in %s (expires %s)", s.certFile, s.leaf.NotAfter.Format(time.DateOnly))
	}
}

// Config is the server's TLS configuration: TLS 1.2 at least, certificates from src.
func Config(src *Source) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: src.GetCertificate,
		NextProtos:     []string{"http/1.1"},
	}
}

// Listen listens on addr for HTTPS only. A connection that opens with plain HTTP instead of a TLS handshake gets one
// answer, a redirect to the same address over https, and is closed; it never reaches the handler. The redirect only
// ever names a host in allowed (the Host headers the server accepts), so it cannot be turned against another site.
func Listen(addr string, cfg *tls.Config, allowed []string) (net.Listener, error) {
	inner, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return Wrap(inner, cfg, allowed), nil
}

// Wrap is Listen on a listener the caller made.
func Wrap(inner net.Listener, cfg *tls.Config, allowed []string) net.Listener {
	l := &httpsOnly{Listener: inner, cfg: cfg, allowed: allowed, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.loop()
	return l
}

type httpsOnly struct {
	net.Listener
	cfg     *tls.Config
	allowed []string

	conns     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

// sniffTimeout bounds how long a new connection may take to send its first byte, and a plain-HTTP one its request.
const sniffTimeout = 10 * time.Second

func (l *httpsOnly) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			l.errMu.Lock()
			l.err = err
			l.errMu.Unlock()
			l.Close()
			return
		}
		// Each connection is looked at on its own goroutine: one that sends nothing must not hold up the others.
		go l.sort(c)
	}
}

func (l *httpsOnly) sort(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	pc := &peeked{Conn: c, r: br}
	if first[0] != 0x16 { // 0x16 opens every TLS handshake record
		l.redirect(pc)
		return
	}
	c.SetReadDeadline(time.Time{})
	select {
	case l.conns <- tls.Server(pc, l.cfg):
	case <-l.done:
		c.Close()
	}
}

func (l *httpsOnly) redirect(c *peeked) {
	defer c.Close()
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(c.r, 64<<10)))
	if err != nil {
		return
	}
	host := strings.ToLower(req.Host)
	resp := &http.Response{ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Close: true}
	resp.Header.Set("Connection", "close")
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	var body string
	switch {
	case !slices.Contains(l.allowed, host):
		resp.StatusCode = http.StatusMisdirectedRequest
		body = "This server answers over https only.\n"
	case req.Method != http.MethodGet && req.Method != http.MethodHead:
		// A form or an API call sent over plain http has already exposed what it carried; do not invite a resend.
		resp.StatusCode = http.StatusBadRequest
		body = "This server answers over https only. What was just sent went unencrypted: use https://" + host + "/.\n"
	default:
		resp.StatusCode = http.StatusPermanentRedirect
		resp.Header.Set("Location", "https://"+host+req.URL.RequestURI())
		body = "Moved to https.\n"
	}
	resp.Status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	resp.ContentLength = int64(len(body))
	resp.Body = io.NopCloser(strings.NewReader(body))
	c.SetWriteDeadline(time.Now().Add(sniffTimeout))
	resp.Write(c)
}

func (l *httpsOnly) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		l.errMu.Lock()
		defer l.errMu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *httpsOnly) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}

// peeked is a connection whose first bytes were read into r to see what protocol it speaks.
type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }
