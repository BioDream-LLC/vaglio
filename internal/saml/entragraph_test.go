package saml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// A Microsoft Graph client, just enough of one to stand up a SAML application in a throwaway Entra tenant.
//
// Why this is test scaffolding rather than product code. Perfuse does not manage identity providers; it consumes them. The only reason to
// talk to Graph is to arrange the conditions for a verification, so it lives with the tests and ships with nothing.
//
// Why the verification matters. Keycloak found a defect that a thousand of this package's own tests could not: the canonicaliser dropped
// namespace prefixes, so no real identity provider could ever have signed anybody in, and every test passed because each one signed and
// verified with the same code. One provider proves an implementation matches one vendor. Entra is what most customers actually run, and
// every difficult part of SAML is a place where a vendor's output differs from a reading of the standard.
//
// Credentials come from the environment and are never written anywhere. Absent, every test here skips, so make check stays green on a
// machine that has none - the same arrangement as the Keycloak and UDAP live tests.

// entraConfig is what the environment has to supply.
type entraConfig struct {
	TenantID     string
	ClientID     string
	clientSecret string
}

// entraFromEnv reads the credentials, or skips.
func entraFromEnv(t *testing.T) entraConfig {
	t.Helper()

	cfg := entraConfig{
		TenantID:     os.Getenv("VAGLIO_ENTRA_TENANT_ID"),
		ClientID:     os.Getenv("VAGLIO_ENTRA_CLIENT_ID"),
		clientSecret: os.Getenv("VAGLIO_ENTRA_CLIENT_SECRET"),
	}

	// Named individually so a half-configured environment says which one is missing rather than skipping with one vague sentence. The
	// secret is reported as present or absent and never printed.
	var missing []string
	if cfg.TenantID == "" {
		missing = append(missing, "VAGLIO_ENTRA_TENANT_ID")
	}
	if cfg.ClientID == "" {
		missing = append(missing, "VAGLIO_ENTRA_CLIENT_ID")
	}
	if cfg.clientSecret == "" {
		missing = append(missing, "VAGLIO_ENTRA_CLIENT_SECRET")
	}

	if len(missing) > 0 {
		t.Skipf("no Entra tenant configured: %s not set. See the single sign-on chapter of the manual for what the app registration needs",
			strings.Join(missing, ", "))
	}

	return cfg
}

// graph is an authenticated Microsoft Graph client.
type graph struct {
	token  string
	client *http.Client
	t      *testing.T
}

// newGraph gets an application token by client credentials.
func newGraph(t *testing.T, cfg entraConfig) *graph {
	t.Helper()

	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
		"grant_type":    {"client_credentials"},
	}

	endpoint := "https://login.microsoftonline.com/" + url.PathEscape(cfg.TenantID) + "/oauth2/v2.0/token"

	client := &http.Client{Timeout: 30 * time.Second}

	res, err := client.PostForm(endpoint, form)
	if err != nil {
		t.Skipf("cannot reach Entra to get a token (%v)", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the token response: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		// Entra's error body names the problem precisely - a wrong secret, a missing consent, an unknown tenant - and it contains no
		// secret of ours, so it is worth showing in full.
		t.Fatalf("Entra refused the client credentials with %d. Check the secret has not expired and that admin consent was granted "+
			"for the application permissions:\n%s", res.StatusCode, body)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding the token response: %v", err)
	}
	if out.AccessToken == "" {
		t.Fatal("Entra returned a token response with no access token in it")
	}

	return &graph{token: out.AccessToken, client: client, t: t}
}

// do makes a Graph call and returns the decoded body.
//
// Errors are fatal with Graph's own message included. Graph is unusually good at saying what was wrong with a request, and paraphrasing it
// would lose that.
func (g *graph) do(method, path string, body any) map[string]any {
	g.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			g.t.Fatalf("encoding the %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, "https://graph.microsoft.com/v1.0"+path, reader)
	if err != nil {
		g.t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := g.client.Do(req)
	if err != nil {
		g.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		g.t.Fatalf("reading %s %s: %v", method, path, err)
	}

	if res.StatusCode >= 400 {
		g.t.Fatalf("%s %s returned %d:\n%s", method, path, res.StatusCode, raw)
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		g.t.Fatalf("decoding %s %s: %v\n%s", method, path, err, raw)
	}
	return out
}

// str reads a string from a Graph response, failing rather than returning an empty string.
//
// A missing field that reads as "" propagates into a URL or an id and fails somewhere unrelated, which is how an afternoon goes.
func (g *graph) str(m map[string]any, key string) string {
	g.t.Helper()

	v, ok := m[key]
	if !ok {
		g.t.Fatalf("no %q in the Graph response: %v", key, m)
	}
	s, ok := v.(string)
	if !ok {
		g.t.Fatalf("%q in the Graph response is %T, not a string", key, v)
	}
	if s == "" {
		g.t.Fatalf("%q in the Graph response is empty", key)
	}
	return s
}

// federationMetadataURL is where Entra publishes the document our verifier consumes.
//
// Per application rather than per tenant: the appid parameter selects the signing certificate for that application, and the tenant-wide
// document describes a different key. Omitting it is a mistake that presents as a digest mismatch, which is the same symptom as the
// canonicalisation defect and would send somebody back over ground that is already covered.
func federationMetadataURL(tenantID, appID string) string {
	return fmt.Sprintf(
		"https://login.microsoftonline.com/%s/federationmetadata/2007-06/federationmetadata.xml?appid=%s",
		url.PathEscape(tenantID), url.QueryEscape(appID),
	)
}

// exists reports whether a Graph resource is there yet, without failing when it is not.
func (g *graph) exists(path string) bool {
	g.t.Helper()

	req, err := http.NewRequest(http.MethodGet, "https://graph.microsoft.com/v1.0"+path, nil)
	if err != nil {
		g.t.Fatalf("building the existence check for %s: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)

	res, err := g.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	return res.StatusCode == http.StatusOK
}

// deleteQuietly removes a resource, retrying while Graph says it does not exist, and reporting a failure without failing the test.
//
// A tidy-up problem must not turn a passing verification red, but a tenant quietly filling with applications from every run has to be
// visible to whoever reads the output.
//
// The retry is the important part and the first version did not have it. It treated 404 as success - reasonable-looking, since an object
// that is not there needs no deleting - and a freshly created application answers DELETE with 404 for the same propagation reason it answers
// PATCH with one. So the cleanup reported success and did nothing, and the tenant accumulated an application per run while every test passed.
// That is the shape this repository keeps finding elsewhere, produced here by me, in the code written to avoid making a mess.
func (g *graph) deleteQuietly(path string) {
	deadline := time.Now().Add(60 * time.Second)

	for {
		code := g.deleteOnce(path)
		if code < 400 {
			return
		}

		if code == http.StatusNotFound && time.Now().Before(deadline) {
			time.Sleep(3 * time.Second)
			continue
		}

		// Out of time, or an error that will not improve. Either way the object may still be there and somebody has to know.
		g.t.Logf("could not delete %s (last status %d). Check the tenant for leftovers", path, code)
		return
	}
}

// deleteOnce issues one DELETE and returns the status.
func (g *graph) deleteOnce(path string) int {
	req, err := http.NewRequest(http.MethodDelete, "https://graph.microsoft.com/v1.0"+path, nil)
	if err != nil {
		g.t.Logf("could not build the delete for %s: %v", path, err)
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+g.token)

	res, err := g.client.Do(req)
	if err != nil {
		g.t.Logf("could not delete %s: %v", path, err)
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	return res.StatusCode
}

// readAllAndClose reads a response body and closes it.
func readAllAndClose(t *testing.T, res *http.Response) []byte {
	t.Helper()

	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	return body
}

// doWriteEventually makes a write call, retrying while Graph reports the object does not exist.
//
// Measured behaviour, not defensive guessing. After instantiating an application template, a GET of the new service principal returns 200
// while a PATCH of the same id returns 404 - readable before writable. A single retry five seconds later succeeded, every time it was
// measured.
//
// The first attempt at handling this polled with a GET and then wrote, which is why it kept failing: waiting for a read to succeed proves
// nothing about a write. The only reliable wait for a write is the write.
func (g *graph) doWriteEventually(method, path string, body any) map[string]any {
	g.t.Helper()

	deadline := time.Now().Add(90 * time.Second)
	attempt := 0

	for {
		attempt++

		code, raw, out := g.try(method, path, body)
		if code < 400 {
			if attempt > 1 {
				g.t.Logf("%s %s succeeded on attempt %d", method, path, attempt)
			}
			return out
		}

		// Only the propagation delay is retried. Anything else - a permissions problem, a malformed body - will not improve with time,
		// and retrying it turns a clear error into a ninety second wait followed by the same clear error.
		if code != http.StatusNotFound || time.Now().After(deadline) {
			g.t.Fatalf("%s %s returned %d on attempt %d:\n%s", method, path, code, attempt, raw)
		}

		time.Sleep(3 * time.Second)
	}
}

// try makes a call and returns the status without failing, for callers that decide what to do about it.
func (g *graph) try(method, path string, body any) (int, []byte, map[string]any) {
	g.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			g.t.Fatalf("encoding the %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, "https://graph.microsoft.com/v1.0"+path, reader)
	if err != nil {
		g.t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := g.client.Do(req)
	if err != nil {
		g.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		g.t.Fatalf("reading %s %s: %v", method, path, err)
	}

	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}

	return res.StatusCode, raw, out
}
