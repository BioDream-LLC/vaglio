// Package oidc signs people in through an organization's identity provider, by OpenID Connect's authorization code flow
// with PKCE, on the standard library alone.
//
// It does only what a confidential web client needs: discovery, the authorization URL, the code exchange, and the ID
// token's verification - its signature against the provider's published keys (RSA or ECDSA; "none" and HMAC are
// refused), its issuer, audience, expiry and nonce. It never decides what anyone may do: it says who the provider vouches
// for, by one claim, and the server's own accounts decide the rest.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config is a client registered with the provider.
type Config struct {
	Issuer       string // the provider's issuer URL, exactly as its tokens say it
	ClientID     string
	ClientSecret string
	RedirectURL  string   // this server's callback, as registered with the provider
	Scopes       []string // requested besides openid; default profile and email
	// NameClaim is the claim naming the account here: preferred_username by default. Its value, lower cased, must be the
	// name of an account marked for single sign-on.
	NameClaim string
	// Group, when set, is a group the person must belong to, read from GroupsClaim (default "groups").
	Group       string
	GroupsClaim string
	HTTP        *http.Client
}

// Provider is a discovered identity provider.
type Provider struct {
	cfg  Config
	meta struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
	now     func() time.Time
}

// secure is whether a URL may carry sign-ins: https, or http to this machine only (a provider under test).
func secure(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return fmt.Errorf("%s must be https (plain http only to this machine)", raw)
}

// Discover reads the provider's configuration. A provider whose configuration names another issuer is refused: tokens
// it issues would not verify, and it may not be the provider meant.
func Discover(ctx context.Context, cfg Config) (*Provider, error) {
	for _, u := range []string{cfg.Issuer, cfg.RedirectURL} {
		if err := secure(u); err != nil {
			return nil, err
		}
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("single sign-on needs the client id and secret registered with the provider")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.NameClaim == "" {
		cfg.NameClaim = "preferred_username"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"profile", "email"}
	}
	p := &Provider{cfg: cfg, now: time.Now}
	wellKnown := strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	if err := p.getJSON(ctx, wellKnown, &p.meta); err != nil {
		return nil, fmt.Errorf("discovering %s: %w", cfg.Issuer, err)
	}
	if p.meta.Issuer != cfg.Issuer {
		if strings.Contains(p.meta.Issuer, "{tenantid}") {
			// Microsoft Entra ID's shared endpoints (common, organizations, consumers) publish a template, not an issuer:
			// tokens name the tenant, so none would verify.
			return nil, fmt.Errorf("the provider at %s is Microsoft Entra ID's shared endpoint, whose issuer is the template %q: "+
				"use your tenant's own, https://login.microsoftonline.com/TENANT-ID/v2.0", cfg.Issuer, p.meta.Issuer)
		}
		return nil, fmt.Errorf("the provider at %s says its issuer is %q", cfg.Issuer, p.meta.Issuer)
	}
	for _, u := range []string{p.meta.Authorization, p.meta.Token, p.meta.JWKS} {
		if err := secure(u); err != nil {
			return nil, fmt.Errorf("the provider's configuration: %w", err)
		}
	}
	return p, nil
}

func (p *Provider) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// Random is a URL-safe random string of n bytes, for states, nonces and PKCE verifiers.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthURL is where to send someone to sign in. The verifier stays here; only its S256 challenge goes to the provider.
func (p *Provider) AuthURL(state, nonce, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURL},
		"scope":                 {"openid " + strings.Join(p.cfg.Scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(p.meta.Authorization, "?") {
		sep = "&"
	}
	return p.meta.Authorization + sep + q.Encode()
}

// Exchange trades an authorization code for the ID token.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.meta.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("the token endpoint answered %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK || body.IDToken == "" {
		return "", fmt.Errorf("the provider refused the code: %s %s", body.Error, body.Desc)
	}
	return body.IDToken, nil
}

// Claims are an ID token's claims.
type Claims map[string]any

func (c Claims) str(k string) string { s, _ := c[k].(string); return s }

// Verify checks an ID token and returns its claims.
func (p *Provider) Verify(ctx context.Context, raw, nonce string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("the ID token is not a signed JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return nil, fmt.Errorf("the ID token's header: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("the ID token's signature is not base64url")
	}
	key, err := p.key(ctx, head.Kid)
	if err != nil {
		return nil, err
	}
	if err := verifySignature(head.Alg, key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}
	var c Claims
	if err := decodePart(parts[1], &c); err != nil {
		return nil, fmt.Errorf("the ID token's claims: %w", err)
	}
	if c.str("iss") != p.cfg.Issuer {
		return nil, fmt.Errorf("the ID token was issued by %q, not %q", c.str("iss"), p.cfg.Issuer)
	}
	if !audience(c, p.cfg.ClientID) {
		return nil, errors.New("the ID token is not for this client")
	}
	now := p.now()
	const skew = 60 * time.Second
	exp, ok := c["exp"].(float64)
	if !ok || now.After(time.Unix(int64(exp), 0).Add(skew)) {
		return nil, errors.New("the ID token has expired")
	}
	if iat, ok := c["iat"].(float64); ok && time.Unix(int64(iat), 0).After(now.Add(5*time.Minute)) {
		return nil, errors.New("the ID token was issued in the future")
	}
	if nonce == "" || c.str("nonce") != nonce {
		return nil, errors.New("the ID token's nonce is not this sign-in's: it may be replayed")
	}
	return c, nil
}

func audience(c Claims, client string) bool {
	switch a := c["aud"].(type) {
	case string:
		return a == client
	case []any:
		found := false
		for _, x := range a {
			if s, _ := x.(string); s == client {
				found = true
			}
		}
		// With other audiences beside this client, the token must say it was issued to this one.
		return found && (len(a) == 1 || c.str("azp") == client)
	}
	return false
}

// Name is the account name the token vouches for, and whether the person is in the required group.
func (p *Provider) Name(c Claims) (string, error) {
	if p.cfg.Group != "" {
		if names, _ := c["_claim_names"].(map[string]any); names != nil && names[p.cfg.GroupsClaim] != nil && c[p.cfg.GroupsClaim] == nil {
			// Entra ID leaves the groups out of the token when the person is in too many (over 200), and names where to fetch
			// them instead. Vaglio does not fetch them: an app role in the roles claim is the way round it.
			return "", fmt.Errorf("the provider left the %s claim out (too many groups for a token); "+
				"give the group as an app role and read the roles claim instead", p.cfg.GroupsClaim)
		}
		in := false
		switch g := c[p.cfg.GroupsClaim].(type) {
		case []any:
			for _, x := range g {
				if s, _ := x.(string); s == p.cfg.Group {
					in = true
				}
			}
		case string:
			in = g == p.cfg.Group
		}
		if !in {
			return "", fmt.Errorf("not a member of %s", p.cfg.Group)
		}
	}
	name := AccountName(c.str(p.cfg.NameClaim))
	if name == "" {
		return "", fmt.Errorf("the ID token has no %s claim", p.cfg.NameClaim)
	}
	return name, nil
}

// AccountName is the account name here for a name a provider vouches for: lower cased, and the part before an @, since
// account names hold no @ and Microsoft Entra ID's preferred_username is the user principal name (kim@plan.example
// signs in as the account kim). A guest's UPN (kim_other.example#EXT#@...) fits no account name and is refused. A
// tenant whose domains share a local part should name another claim.
func AccountName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if at := strings.IndexByte(v, '@'); at > 0 {
		v = v[:at]
	}
	return v
}

func decodePart(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// key is the provider's signing key by id, fetching the key set again (at most once a minute) for an id it lacks: the
// provider may have rotated its keys.
func (p *Provider) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if p.keys != nil && p.now().Sub(p.fetched) < time.Minute {
		return nil, fmt.Errorf("the provider has no signing key %q", kid)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, N, E, Crv, X, Y string
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, p.meta.JWKS, &set); err != nil {
		return nil, fmt.Errorf("reading the provider's keys: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) > 4 {
				continue
			}
			pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
			if pub.N.BitLen() < 2048 {
				continue // too weak to trust a sign-in to
			}
			keys[k.Kid] = pub
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			default:
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			pub := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
			if !curve.IsOnCurve(pub.X, pub.Y) {
				continue
			}
			keys[k.Kid] = pub
		}
	}
	p.keys, p.fetched = keys, p.now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("the provider has no signing key %q", kid)
}

func verifySignature(alg string, key crypto.PublicKey, signed, sig []byte) error {
	var h hash.Hash
	var ch crypto.Hash
	switch alg {
	case "RS256", "PS256", "ES256":
		h, ch = sha256.New(), crypto.SHA256
	case "RS384", "PS384", "ES384":
		h, ch = sha512.New384(), crypto.SHA384
	case "RS512", "PS512":
		h, ch = sha512.New(), crypto.SHA512
	default:
		return fmt.Errorf("the ID token is signed with %q, which is not accepted", alg)
	}
	h.Write(signed)
	digest := h.Sum(nil)
	switch k := key.(type) {
	case *rsa.PublicKey:
		var err error
		switch alg[:2] {
		case "RS":
			err = rsa.VerifyPKCS1v15(k, ch, digest, sig)
		case "PS":
			err = rsa.VerifyPSS(k, ch, digest, sig, nil)
		default:
			return errors.New("the ID token's algorithm does not match its key")
		}
		if err != nil {
			return errors.New("the ID token's signature does not verify")
		}
	case *ecdsa.PublicKey:
		if alg[:2] != "ES" {
			return errors.New("the ID token's algorithm does not match its key")
		}
		size := (k.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return errors.New("the ID token's signature does not verify")
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		if !ecdsa.Verify(k, digest, r, s) {
			return errors.New("the ID token's signature does not verify")
		}
	default:
		return errors.New("unsupported key")
	}
	return nil
}
