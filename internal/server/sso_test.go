package server

import (
	"context"
	"crypto"
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

	"github.com/biodream-llc/vaglio/internal/oidc"
)

// TestSingleSignOn runs the whole sign-in against a fake provider: start, the provider's redirect back with a code, the
// ID token verified, and a session for the account the provider named - only if that account is marked for SSO.
func TestSingleSignOn(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var nonce, user string
	mux := http.NewServeMux()
	idp := httptest.NewServer(mux)
	defer idp.Close()
	b64 := base64.RawURLEncoding.EncodeToString
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": idp.URL, "authorization_endpoint": idp.URL + "/auth",
			"token_endpoint": idp.URL + "/token", "jwks_uri": idp.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k", "n": b64(key.N.Bytes()),
			"e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k"})
		c, _ := json.Marshal(map[string]any{"iss": idp.URL, "aud": "vaglio", "exp": time.Now().Add(time.Hour).Unix(),
			"nonce": nonce, "preferred_username": user})
		signed := b64(h) + "." + b64(c)
		sum := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signed + "." + b64(sig)})
	})
	prov, err := oidc.Discover(context.Background(), oidc.Config{Issuer: idp.URL, ClientID: "vaglio", ClientSecret: "x",
		RedirectURL: "http://127.0.0.1:8740/api/sso/callback"})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, "", func(c *Config) { c.SSO = NewSSO(prov, "") })
	if err := e.users.SetSSO("kim", "reviewer"); err != nil {
		t.Fatal(err)
	}

	signIn := func(name string) *httptest.ResponseRecorder {
		user = name
		start := e.raw("GET", "/api/sso/start", "", nil, "")
		if start.Code != http.StatusFound {
			t.Fatalf("start: %d", start.Code)
		}
		to, _ := url.Parse(start.Header().Get("Location"))
		nonce = to.Query().Get("nonce")
		state := to.Query().Get("state")
		var stateCookie *http.Cookie
		for _, c := range start.Result().Cookies() {
			if c.Name == ssoCookie {
				stateCookie = c
			}
		}
		return e.raw("GET", "/api/sso/callback?code=c&state="+state, "", stateCookie, "")
	}

	w := signIn("Kim")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session")
	}
	var me Me
	json.Unmarshal(e.raw("GET", "/api/me", "", sess, "").Body.Bytes(), &me)
	if me.Via != "sso" || me.Role != "reviewer" || me.User != "kim" {
		t.Errorf("me: %+v", me)
	}

	// A name the provider vouches for, with no SSO account here, is refused; so is a password account's name.
	if w := signIn("stranger"); !strings.Contains(w.Header().Get("Location"), "sso=unknown") {
		t.Errorf("a stranger: %s", w.Header().Get("Location"))
	}
	if w := signIn("admin"); !strings.Contains(w.Header().Get("Location"), "sso=unknown") {
		t.Errorf("a password account taken over: %s", w.Header().Get("Location"))
	}

	// A callback from another browser (no state cookie) signs nobody in; the browser that started it then can, once.
	user = "kim"
	start := e.raw("GET", "/api/sso/start", "", nil, "")
	to, _ := url.Parse(start.Header().Get("Location"))
	nonce = to.Query().Get("nonce")
	if w := e.raw("GET", "/api/sso/callback?code=c&state="+to.Query().Get("state"), "", nil, ""); !strings.Contains(
		w.Header().Get("Location"), "sso=failed") {
		t.Errorf("no state cookie: %s", w.Header().Get("Location"))
	}
	var stateCookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == ssoCookie {
			stateCookie = c
		}
	}
	callback := "/api/sso/callback?code=c&state=" + to.Query().Get("state")
	if w := e.raw("GET", callback, "", stateCookie, ""); w.Header().Get("Location") != "/" {
		t.Errorf("the starting browser: %s", w.Header().Get("Location"))
	}
	if w := e.raw("GET", callback, "", stateCookie, ""); !strings.Contains(w.Header().Get("Location"), "sso=failed") {
		t.Errorf("a replayed state: %s", w.Header().Get("Location"))
	}
	failures := 0
	for _, r := range e.log.Records(0, 0) {
		if r.Action == "user.sign_in_failed" && strings.Contains(string(r.Data), `"via":"sso"`) {
			failures++
		}
	}
	if failures != 4 {
		t.Errorf("%d failed SSO sign-ins logged, want 4", failures)
	}
}
