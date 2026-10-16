package server

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/saml"
)

// samlIdP is a stand-in identity provider that answers the way Microsoft Entra ID does: no email attribute, the user
// principal name in "name", groups by object id under schemas.microsoft.com, and an opaque persistent NameID.
type samlIdP struct {
	key  *rsa.PrivateKey
	cert string
}

func newSAMLIdP(t *testing.T) samlIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return samlIdP{key: key, cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

const (
	samlEntity = "https://vaglio.example/saml"
	samlACSURL = "https://127.0.0.1:8740/api/sso/saml"
	entraGroup = "5f1c0f3e-0000-4000-8000-000000000001"
)

// respond answers request id (empty: unsolicited) for the user principal name upn, signed with key.
func (i samlIdP) respond(t *testing.T, key *rsa.PrivateKey, inResponseTo, upn string, groups ...string) string {
	t.Helper()
	now := time.Now().UTC()
	id := fmt.Sprintf("_r%d", now.UnixNano())
	var gs strings.Builder
	for _, g := range groups {
		gs.WriteString(`<AttributeValue>` + g + `</AttributeValue>`)
	}
	attrs := `<Attribute Name="http://schemas.microsoft.com/identity/claims/displayname"><AttributeValue>Kim Lee</AttributeValue></Attribute>` +
		`<Attribute Name="http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"><AttributeValue>` + upn + `</AttributeValue></Attribute>`
	if len(groups) > 0 {
		attrs += `<Attribute Name="http://schemas.microsoft.com/ws/2008/06/identity/claims/groups">` + gs.String() + `</Attribute>`
	}
	reply := ""
	if inResponseTo != "" {
		reply = ` InResponseTo="` + inResponseTo + `"`
	}
	body := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="` + id + `" Version="2.0" IssueInstant="` +
		now.Format(time.RFC3339) + `" Destination="` + samlACSURL + `"` + reply + `>` +
		`<Issuer xmlns="urn:oasis:names:tc:SAML:2.0:assertion">https://sts.windows.net/tenant/</Issuer>{SIGNATURE}` +
		`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>` +
		`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="` + id + `_a" IssueInstant="` + now.Format(time.RFC3339) +
		`" Version="2.0"><Issuer>https://sts.windows.net/tenant/</Issuer>` +
		`<Subject><NameID Format="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">99d-dljf9omwoabcgx3kpkzm22vv</NameID></Subject>` +
		`<Conditions NotBefore="` + now.Add(-time.Minute).Format(time.RFC3339) + `" NotOnOrAfter="` +
		now.Add(5*time.Minute).Format(time.RFC3339) + `"><AudienceRestriction><Audience>` + samlEntity +
		`</Audience></AudienceRestriction></Conditions>` +
		`<AttributeStatement>` + attrs + `</AttributeStatement><AuthnStatement SessionIndex="_s"/></Assertion></samlp:Response>`
	sig, err := saml.SignDocument(body, id, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Replace(body, "{SIGNATURE}", sig, 1)))
}

var requestID = regexp.MustCompile(` ID="([^"]+)"`)

// TestSAMLSignIn: a sign-in started here and answered by the provider signs the account in; the response is refused
// when it answers no request of ours, answers one twice, is signed by another key, or names someone outside the group.
func TestSAMLSignIn(t *testing.T) {
	idp := newSAMLIdP(t)
	sp, err := saml.New(saml.Config{EntityID: samlEntity, ACSPath: samlACSURL, IdPSSOURL: "https://login.example/saml2",
		IdPCertPEM: idp.cert})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, "", func(c *Config) { c.SSO = NewSAML(sp, "", entraGroup, "", "Sign in with Entra") })
	if err := e.users.SetSSO("kim", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if w := e.raw("GET", "/api/sso", "", nil, ""); !strings.Contains(w.Body.String(), `"enabled": true`) {
		t.Fatalf("sso info: %s", w.Body)
	}
	start := func() string {
		w := e.raw("GET", "/api/sso/start", "", nil, "")
		to, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || to.Host != "login.example" {
			t.Fatalf("start: %d %s", w.Code, to)
		}
		raw, _ := base64.StdEncoding.DecodeString(to.Query().Get("SAMLRequest"))
		xml, _ := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
		m := requestID.FindSubmatch(xml)
		if m == nil {
			t.Fatalf("no request id in %s", xml)
		}
		return string(m[1])
	}
	post := func(resp string) *http.Response {
		// The provider's form POST: cross-site, no JSON, no cookie of ours.
		w := e.raw("POST", "/api/sso/saml", "SAMLResponse="+url.QueryEscape(resp), nil, "application/x-www-form-urlencoded")
		return w.Result()
	}
	session := func(r *http.Response) bool {
		for _, c := range r.Cookies() {
			if c.Name == cookieName && c.Value != "" {
				return true
			}
		}
		return false
	}

	id := start()
	ok := idp.respond(t, idp.key, id, "Kim@Plan.example", entraGroup)
	if r := post(ok); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/" || !session(r) {
		t.Fatalf("a good sign-in: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := post(ok); session(r) {
		t.Error("the same response signed in twice")
	}
	if r := post(idp.respond(t, idp.key, "", "kim@plan.example", entraGroup)); session(r) {
		t.Error("an unsolicited response signed in")
	}
	if r := post(idp.respond(t, idp.key, "_never-sent", "kim@plan.example", entraGroup)); session(r) {
		t.Error("an answer to a request never sent signed in")
	}
	other := newSAMLIdP(t)
	if r := post(idp.respond(t, other.key, start(), "kim@plan.example", entraGroup)); session(r) {
		t.Error("a response signed by another key signed in")
	}
	if r := post(idp.respond(t, idp.key, start(), "kim@plan.example")); session(r) {
		t.Error("someone outside the group signed in")
	}
	if r := post(idp.respond(t, idp.key, start(), "ana@plan.example", entraGroup)); session(r) ||
		!strings.Contains(r.Header.Get("Location"), "sso=unknown") {
		t.Error("a name with no account here signed in")
	}
	var refusals int
	for _, rec := range e.log.Records(0, 0) {
		if rec.Action == "user.sign_in_failed" && strings.Contains(string(rec.Data), `"via":"saml"`) {
			refusals++
		}
	}
	if refusals != 6 {
		t.Errorf("%d refusals in the audit log, want 6", refusals)
	}
}

// The account name read from the Entra ID response Perfuse captured (internal/saml/testdata; identifiers replaced and
// signed again with a test key, Entra's shape kept): Entra sends no email and an opaque NameID, so the name must come
// from its user principal name.
func TestSAMLNameFromARealEntraResponse(t *testing.T) {
	raw, err := os.ReadFile("../saml/testdata/entra-response.xml")
	if err != nil {
		t.Fatal(err)
	}
	crt, _ := os.ReadFile("../saml/testdata/entra-idp.crt")
	sp, err := saml.New(saml.Config{EntityID: "urn:perfuse:local-signin", ACSPath: "https://sp.example.test:8544/auth/saml/acs",
		IdPSSOURL: "https://login.microsoftonline.com/00000000-1111-2222-3333-444444444444/saml2", IdPCertPEM: string(crt)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := sp.VerifyAt(raw, time.Date(2026, 9, 20, 1, 20, 0, 0, time.UTC),
		func(id string) bool { return id == "_5f38855a20de8466ac2e7063c8ebcfdf" })
	if err != nil {
		t.Fatal(err)
	}
	name := samlName(a, "")
	if name == "" || name == strings.ToLower(a.NameID) || strings.Contains(name, "@") {
		t.Errorf("the account name from Entra is %q (NameID %q)", name, a.NameID)
	}
}
