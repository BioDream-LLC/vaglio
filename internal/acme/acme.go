// Package acme gets certificates from an ACME authority (RFC 8555), such as Let's Encrypt: an account,
// an order for the names, the http-01 or tls-alpn-01 (RFC 8737) proof that the server answers for
// them, and the certificate. Only the standard library: ES256 signatures from crypto/ecdsa.
package acme

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LetsEncrypt and LetsEncryptStaging are Let's Encrypt's directories.
const (
	LetsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// ALPNProto is the TLS application protocol tls-alpn-01 proofs are asked with.
const ALPNProto = "acme-tls/1"

// Solver proves control of a name: it serves the key authorization for a token, by HTTP at
// /.well-known/acme-challenge/<token> (http-01) or as a TLS certificate for acme-tls/1 (tls-alpn-01).
type Solver interface {
	// Kinds lists the challenge types it can answer, best first.
	Kinds() []string
	Present(kind, domain, token, keyAuth string) error
	CleanUp(kind, domain, token string)
}

// Client talks to one ACME authority with one account key.
type Client struct {
	DirectoryURL string
	Key          *ecdsa.PrivateKey
	HTTP         *http.Client
	// Poll is how often an order or authorization is checked while the authority works (default 2 s).
	Poll time.Duration

	mu    sync.Mutex
	dir   *directory
	kid   string
	nonce string
}

type directory struct {
	NewNonce   string `json:"newNonce"`
	NewAccount string `json:"newAccount"`
	NewOrder   string `json:"newOrder"`
	Meta       struct {
		TermsOfService string `json:"termsOfService"`
	} `json:"meta"`
}

// Problem is an error the authority answered with (RFC 8555 section 6.7).
type Problem struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func (p *Problem) Error() string {
	t := strings.TrimPrefix(p.Type, "urn:ietf:params:acme:error:")
	if p.Detail != "" {
		return t + ": " + p.Detail
	}
	return t
}

// NewKey makes an account or certificate key (ECDSA P-256).
func NewKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// jwk is the account key as a JSON Web Key, members in the order the thumbprint needs (RFC 7638).
func (c *Client) jwk() string {
	pub, _ := c.Key.PublicKey.ECDH()
	raw := pub.Bytes() // 0x04 || X || Y
	return `{"crv":"P-256","kty":"EC","x":"` + b64(raw[1:33]) + `","y":"` + b64(raw[33:65]) + `"}`
}

// KeyAuth is a token's key authorization: the token, a dot and the account key's thumbprint.
func (c *Client) KeyAuth(token string) string {
	sum := sha256.Sum256([]byte(c.jwk()))
	return token + "." + b64(sum[:])
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) directory(ctx context.Context) (*directory, error) {
	c.mu.Lock()
	d := c.dir
	c.mu.Unlock()
	if d != nil {
		return d, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.DirectoryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("the ACME directory answered %s", resp.Status)
	}
	d = &directory{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(d); err != nil {
		return nil, fmt.Errorf("the ACME directory could not be read: %w", err)
	}
	if d.NewNonce == "" || d.NewAccount == "" || d.NewOrder == "" {
		return nil, errors.New("the ACME directory lists no newNonce, newAccount or newOrder")
	}
	c.mu.Lock()
	c.dir = d
	c.mu.Unlock()
	return d, nil
}

func (c *Client) takeNonce(ctx context.Context) (string, error) {
	c.mu.Lock()
	n := c.nonce
	c.nonce = ""
	c.mu.Unlock()
	if n != "" {
		return n, nil
	}
	d, err := c.directory(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, d.NewNonce, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if n = resp.Header.Get("Replay-Nonce"); n == "" {
		return "", errors.New("the ACME authority gave no nonce")
	}
	return n, nil
}

// sign makes a flattened JWS (RFC 7515) of payload for url: with the key itself before the account
// exists, by the account URL after. A nil payload is a POST-as-GET.
func (c *Client) sign(url, nonce string, payload []byte, withJWK bool) ([]byte, error) {
	prot := `{"alg":"ES256","nonce":` + strconv.Quote(nonce) + `,"url":` + strconv.Quote(url)
	if withJWK {
		prot += `,"jwk":` + c.jwk() + `}`
	} else {
		prot += `,"kid":` + strconv.Quote(c.kid) + `}`
	}
	p64 := ""
	if payload != nil {
		p64 = b64(payload)
	}
	signing := b64([]byte(prot)) + "." + p64
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.Key, sum[:])
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return json.Marshal(map[string]string{"protected": b64([]byte(prot)), "payload": p64, "signature": b64(sig)})
}

// post sends a signed request, once more with a fresh nonce when the authority says badNonce.
func (c *Client) post(ctx context.Context, url string, payload any, out any, withJWK bool) (*http.Response, []byte, error) {
	var body []byte
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return nil, nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		nonce, err := c.takeNonce(ctx)
		if err != nil {
			return nil, nil, err
		}
		jws, err := c.sign(url, nonce, body, withJWK)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jws))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Content-Type", "application/jose+json")
		req.Header.Set("User-Agent", "Vaglio-ACME")
		resp, err := c.client().Do(req)
		if err != nil {
			return nil, nil, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if n := resp.Header.Get("Replay-Nonce"); n != "" {
			c.mu.Lock()
			c.nonce = n
			c.mu.Unlock()
		}
		if resp.StatusCode >= 400 {
			p := &Problem{Status: resp.StatusCode}
			_ = json.Unmarshal(data, p)
			if p.Type == "" {
				p.Type = resp.Status
			}
			if strings.HasSuffix(p.Type, ":badNonce") && attempt < 2 {
				continue
			}
			return resp, data, p
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp, data, fmt.Errorf("the ACME authority's answer could not be read: %w", err)
			}
		}
		return resp, data, nil
	}
}

// Register makes the account (or finds it, when the key already has one), agreeing to the terms.
func (c *Client) Register(ctx context.Context, email string) error {
	d, err := c.directory(ctx)
	if err != nil {
		return err
	}
	req := map[string]any{"termsOfServiceAgreed": true}
	if email != "" {
		req["contact"] = []string{"mailto:" + email}
	}
	resp, _, err := c.post(ctx, d.NewAccount, req, nil, true)
	if err != nil {
		return fmt.Errorf("the ACME account: %w", err)
	}
	kid := resp.Header.Get("Location")
	if kid == "" {
		return errors.New("the ACME authority gave no account address")
	}
	c.mu.Lock()
	c.kid = kid
	c.mu.Unlock()
	return nil
}

type order struct {
	Status         string   `json:"status"`
	Authorizations []string `json:"authorizations"`
	Finalize       string   `json:"finalize"`
	Certificate    string   `json:"certificate"`
	Error          *Problem `json:"error"`
}

type authorization struct {
	Status     string `json:"status"`
	Identifier struct {
		Value string `json:"value"`
	} `json:"identifier"`
	Challenges []challenge `json:"challenges"`
}

type challenge struct {
	Type   string   `json:"type"`
	URL    string   `json:"url"`
	Token  string   `json:"token"`
	Status string   `json:"status"`
	Error  *Problem `json:"error"`
}

// Obtain orders a certificate for domains, proves each name with the solver and returns the
// certificate chain and its new private key, both in PEM form.
func (c *Client) Obtain(ctx context.Context, domains []string, s Solver) (certPEM, keyPEM []byte, err error) {
	if c.kid == "" {
		return nil, nil, errors.New("register the ACME account first")
	}
	d, err := c.directory(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]map[string]string, len(domains))
	for i, n := range domains {
		ids[i] = map[string]string{"type": "dns", "value": n}
	}
	var o order
	resp, _, err := c.post(ctx, d.NewOrder, map[string]any{"identifiers": ids}, &o, false)
	if err != nil {
		return nil, nil, fmt.Errorf("the order: %w", err)
	}
	orderURL := resp.Header.Get("Location")
	for _, au := range o.Authorizations {
		if err := c.authorize(ctx, au, s); err != nil {
			return nil, nil, err
		}
	}
	key, err := NewKey()
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains}, key)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := c.post(ctx, o.Finalize, map[string]string{"csr": b64(csr)}, &o, false); err != nil {
		return nil, nil, fmt.Errorf("finalizing the order: %w", err)
	}
	for o.Status != "valid" {
		if o.Status == "invalid" {
			if o.Error != nil {
				return nil, nil, fmt.Errorf("the order failed: %w", o.Error)
			}
			return nil, nil, errors.New("the order failed")
		}
		if err := c.wait(ctx); err != nil {
			return nil, nil, err
		}
		if _, _, err := c.post(ctx, orderURL, nil, &o, false); err != nil {
			return nil, nil, fmt.Errorf("checking the order: %w", err)
		}
	}
	_, chain, err := c.post(ctx, o.Certificate, nil, nil, false)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading the certificate: %w", err)
	}
	if !bytes.Contains(chain, []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, nil, errors.New("the ACME authority sent no certificate")
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func (c *Client) wait(ctx context.Context) error {
	p := c.Poll
	if p <= 0 {
		p = 2 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p):
		return nil
	}
}

// authorize proves one name: the first challenge type the solver can answer, then waits for the verdict.
func (c *Client) authorize(ctx context.Context, url string, s Solver) error {
	var a authorization
	if _, _, err := c.post(ctx, url, nil, &a, false); err != nil {
		return fmt.Errorf("the authorization: %w", err)
	}
	if a.Status == "valid" {
		return nil
	}
	var ch *challenge
	for _, k := range s.Kinds() {
		for i := range a.Challenges {
			if a.Challenges[i].Type == k {
				ch = &a.Challenges[i]
				break
			}
		}
		if ch != nil {
			break
		}
	}
	if ch == nil {
		return fmt.Errorf("the authority offers no challenge Vaglio can answer for %s (it can do %s)", a.Identifier.Value, strings.Join(s.Kinds(), ", "))
	}
	if err := s.Present(ch.Type, a.Identifier.Value, ch.Token, c.KeyAuth(ch.Token)); err != nil {
		return err
	}
	defer s.CleanUp(ch.Type, a.Identifier.Value, ch.Token)
	if _, _, err := c.post(ctx, ch.URL, map[string]any{}, nil, false); err != nil {
		return fmt.Errorf("answering the %s challenge: %w", ch.Type, err)
	}
	for {
		if err := c.wait(ctx); err != nil {
			return err
		}
		if _, _, err := c.post(ctx, url, nil, &a, false); err != nil {
			return fmt.Errorf("checking the authorization: %w", err)
		}
		switch a.Status {
		case "valid":
			return nil
		case "invalid", "deactivated", "expired", "revoked":
			for _, x := range a.Challenges {
				if x.Error != nil {
					return fmt.Errorf("%s could not be proved (%s): %w", a.Identifier.Value, x.Type, x.Error)
				}
			}
			return fmt.Errorf("%s could not be proved: the authorization is %s", a.Identifier.Value, a.Status)
		}
	}
}

// idPeAcmeIdentifier is the certificate extension a tls-alpn-01 answer carries (RFC 8737).
var idPeAcmeIdentifier = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}

// ALPNCert is the self-signed certificate that answers a tls-alpn-01 challenge for domain.
func ALPNCert(domain, keyAuth string) (tls.Certificate, error) {
	key, err := NewKey()
	if err != nil {
		return tls.Certificate{}, err
	}
	sum := sha256.Sum256([]byte(keyAuth))
	ext, err := asn1.Marshal(sum[:])
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "ACME challenge"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{domain},
		ExtraExtensions: []pkix.Extension{{Id: idPeAcmeIdentifier, Critical: true, Value: ext}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: crypto.Signer(key)}, nil
}

// EncodeKey and DecodeKey keep an account key in PEM form.
func EncodeKey(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func DecodeKey(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM key")
	}
	return x509.ParseECPrivateKey(blk.Bytes)
}
