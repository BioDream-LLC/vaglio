package tlsserve

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Client is an HTTP client that trusts the system's authorities and, when caFile is given, the authorities in it too:
// another Vaglio's own authority, or a health plan's internal one.
func Client(caFile string, timeout time.Duration) (*http.Client, error) {
	if caFile == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s holds no PEM certificate", caFile)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return &http.Client{Timeout: timeout, Transport: t}, nil
}
