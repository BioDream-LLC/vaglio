package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeProvider is an identity provider for tests: discovery, keys, and a token endpoint that hands out whatever ID token
// the test sets.
type fakeProvider struct {
	srv      *httptest.Server
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	idToken  string
	verifier string // the last code_verifier sent
}

func newFake(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{}
	var err error
	if f.rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if f.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	b64 := base64.RawURLEncoding.EncodeToString
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/auth",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "r1", "use": "sig", "n": b64(f.rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(f.rsaKey.E)).Bytes())},
			{"kty": "EC", "kid": "e1", "crv": "P-256", "x": b64(f.ecKey.X.FillBytes(make([]byte, 32))),
				"y": b64(f.ecKey.Y.FillBytes(make([]byte, 32)))},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		r.ParseForm()
		f.verifier = r.Form.Get("code_verifier")
		if id != "vaglio" || secret != "s3cret" || r.Form.Get("code") != "good-code" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": f.idToken})
	})
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) sign(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	signed := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(signed))
	var sig []byte
	var err error
	switch alg {
	case "RS256":
		sig, err = rsa.SignPKCS1v15(rand.Reader, f.rsaKey, crypto.SHA256, sum[:])
	case "PS256":
		sig, err = rsa.SignPSS(rand.Reader, f.rsaKey, crypto.SHA256, sum[:], nil)
	case "ES256":
		r, s, e := ecdsa.Sign(rand.Reader, f.ecKey, sum[:])
		sig, err = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...), e
	case "none":
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

func TestSignIn(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	p, err := Discover(ctx, Config{Issuer: f.srv.URL, ClientID: "vaglio", ClientSecret: "s3cret",
		RedirectURL: "http://127.0.0.1:8740/api/sso/callback", Group: "pa-reviewers"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := Random(48)
	u, _ := url.Parse(p.AuthURL("st", "n1", verifier))
	q := u.Query()
	sum := sha256.Sum256([]byte(verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("code_challenge_method") != "S256" ||
		q.Get("state") != "st" || q.Get("nonce") != "n1" || !strings.HasPrefix(q.Get("scope"), "openid ") {
		t.Errorf("auth URL %s", u)
	}
	if strings.Contains(u.String(), verifier) {
		t.Error("the PKCE verifier left the server")
	}

	now := time.Now()
	good := func() map[string]any {
		return map[string]any{"iss": f.srv.URL, "aud": "vaglio", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
			"nonce": "n1", "preferred_username": "Dr.Lee", "groups": []string{"pa-reviewers"}}
	}
	for _, alg := range []string{"RS256", "PS256", "ES256"} {
		kid := "r1"
		if alg == "ES256" {
			kid = "e1"
		}
		f.idToken = f.sign(t, alg, kid, good())
		raw, err := p.Exchange(ctx, "good-code", verifier)
		if err != nil {
			t.Fatal(err)
		}
		if f.verifier != verifier {
			t.Error("the verifier was not sent with the code")
		}
		c, err := p.Verify(ctx, raw, "n1")
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		if name, err := p.Name(c); err != nil || name != "dr.lee" {
			t.Errorf("%s: name %q %v", alg, name, err)
		}
	}
	if _, err := p.Exchange(ctx, "bad-code", verifier); err == nil {
		t.Error("a refused code")
	}

	bad := map[string]func(map[string]any){
		"another issuer":          func(c map[string]any) { c["iss"] = "https://evil.example" },
		"another audience":        func(c map[string]any) { c["aud"] = "someone-else" },
		"expired":                 func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() },
		"issued in the future":    func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() },
		"another nonce":           func(c map[string]any) { c["nonce"] = "n2" },
		"no expiry":               func(c map[string]any) { delete(c, "exp") },
		"shared audience, no azp": func(c map[string]any) { c["aud"] = []string{"vaglio", "other"} },
	}
	for name, change := range bad {
		c := good()
		change(c)
		if _, err := p.Verify(ctx, f.sign(t, "RS256", "r1", c), "n1"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	shared := good()
	shared["aud"], shared["azp"] = []string{"vaglio", "other"}, "vaglio"
	if _, err := p.Verify(ctx, f.sign(t, "RS256", "r1", shared), "n1"); err != nil {
		t.Errorf("a shared audience issued to this client: %v", err)
	}
	// Forged, unsigned, the wrong key's kind, an unknown key, an HMAC "signature".
	tok := f.sign(t, "RS256", "r1", good())
	parts := strings.Split(tok, ".")
	forged := good()
	forged["preferred_username"] = "admin"
	fc, _ := json.Marshal(forged)
	for name, raw := range map[string]string{
		"forged claims":      parts[0] + "." + base64.RawURLEncoding.EncodeToString(fc) + "." + parts[2],
		"alg none":           f.sign(t, "none", "r1", good()),
		"RS256 on an EC key": strings.Join(append([]string{}, strings.Split(f.sign(t, "RS256", "e1", good()), ".")...), "."),
		"unknown key":        f.sign(t, "RS256", "nope", good()),
		"HS256":              f.sign(t, "none", "r1", good())[:len(f.sign(t, "none", "r1", good()))-1] + "AAAA",
		"not a JWT":          "abc",
	} {
		if _, err := p.Verify(ctx, raw, "n1"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Outside the required group.
	out := good()
	out["groups"] = []string{"billing"}
	c, err := p.Verify(ctx, f.sign(t, "RS256", "r1", out), "n1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Name(c); err == nil {
		t.Error("someone outside the group was named")
	}
}

func TestDiscoverRefuses(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	base := Config{Issuer: f.srv.URL, ClientID: "vaglio", ClientSecret: "s3cret", RedirectURL: "https://vaglio.example/api/sso/callback"}
	for name, change := range map[string]func(*Config){
		"plain http off this machine":   func(c *Config) { c.Issuer = "http://idp.example" },
		"an issuer that says otherwise": func(c *Config) { c.Issuer = f.srv.URL + "/" },
		"no secret":                     func(c *Config) { c.ClientSecret = "" },
		"an http callback elsewhere":    func(c *Config) { c.RedirectURL = "http://vaglio.example/cb" },
	} {
		c := base
		change(&c)
		if _, err := Discover(ctx, c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
