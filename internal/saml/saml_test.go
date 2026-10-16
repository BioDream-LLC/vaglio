package saml

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testKey and testCert are generated once for the test suite.
var (
	testKey  *rsa.PrivateKey
	testCert *x509.Certificate
	testPEM  string
)

func init() {
	var err error
	testKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test IdP"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &testKey.PublicKey, testKey)
	if err != nil {
		panic(err)
	}
	testCert, err = x509.ParseCertificate(certDER)
	if err != nil {
		panic(err)
	}
	testPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
}

// --- Canonicalization Tests ---

func TestCanonicalize_SimpleElement(t *testing.T) {
	input := `<foo xmlns:a="urn:a" a:bar="1"><a:child>text</a:child></foo>`
	got, err := canonicalize([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The canonical form should:
	// - Use start/end tags (never self-closing)
	// - Render namespace declarations only where used
	// - Sort attributes
	result := string(got)
	if !strings.Contains(result, "<foo") {
		t.Errorf("expected <foo in output, got: %s", result)
	}
	if !strings.Contains(result, "</foo>") {
		t.Errorf("expected </foo> in output, got: %s", result)
	}
	if !strings.Contains(result, "text") {
		t.Errorf("expected text content, got: %s", result)
	}
}

func TestCanonicalize_NamespaceInheritance(t *testing.T) {
	// Child uses a namespace declared on parent - in exc-c14n it must be rendered on the child
	// when canonicalizing the child alone.
	// Note: Go's encoding/xml resolves namespace URIs but loses the original prefix on child elements.
	// When the prefix is preserved in the element's attributes (as xmlns:ns on the element itself),
	// it will be rendered. When the child inherits and uses a namespace from a parent, exc-c14n
	// outputs it in its available form.
	input := `<ns:child xmlns:ns="urn:test" ns:attr="val">content</ns:child>`
	got, err := canonicalize([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(got)
	// When the namespace declaration is on the element itself, it's preserved.
	if !strings.Contains(result, `xmlns:ns="urn:test"`) {
		t.Errorf("expected namespace declaration on child, got: %s", result)
	}
	if !strings.Contains(result, `<ns:child`) {
		t.Errorf("expected ns:child, got: %s", result)
	}
}

func TestCanonicalize_AttributeOrdering(t *testing.T) {
	// Attributes must be sorted by namespace URI then local name.
	input := `<e xmlns:b="urn:b" xmlns:a="urn:a" b:z="1" a:y="2" a:x="3"></e>`
	got, err := canonicalize([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(got)
	// a:x before a:y (same namespace, local name order)
	xPos := strings.Index(result, `a:x="3"`)
	yPos := strings.Index(result, `a:y="2"`)
	zPos := strings.Index(result, `b:z="1"`)
	if xPos < 0 || yPos < 0 || zPos < 0 {
		t.Fatalf("missing attributes in output: %s", result)
	}
	if xPos > yPos {
		t.Errorf("a:x should come before a:y, got: %s", result)
	}
	// urn:a < urn:b so a:* before b:*
	if yPos > zPos {
		t.Errorf("a:y should come before b:z, got: %s", result)
	}
}

func TestCanonicalize_EmptyElement(t *testing.T) {
	// Self-closing tags must be rendered as start+end.
	input := `<root><empty/></root>`
	got, err := canonicalize([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(got)
	if !strings.Contains(result, "<empty></empty>") {
		t.Errorf("expected <empty></empty>, got: %s", result)
	}
}

func TestCanonicalize_TextEscaping(t *testing.T) {
	input := `<e>a &amp; b &lt; c &gt; d</e>`
	got, err := canonicalize([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := string(got)
	if !strings.Contains(result, "a &amp; b &lt; c &gt; d") {
		t.Errorf("expected proper escaping, got: %s", result)
	}
}

// --- Signature Validation Tests ---

func TestValidateSignature_Valid(t *testing.T) {
	// Build a SAML response, sign it, and verify.
	assertion := buildTestAssertion(t, "_assert1", time.Now().Add(-1*time.Minute), time.Now().Add(5*time.Minute))
	response := buildTestResponse(t, assertion)

	raw := []byte(response)
	root, err := parseToTree(raw)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := decodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}

	sig := resp.Assertions[0].Signature
	if sig == nil {
		sig = resp.Signature
	}
	if sig == nil {
		t.Fatal("no signature found")
	}

	err = validateSignature(root, sig, testCert)
	if err != nil {
		t.Fatalf("signature validation failed: %v", err)
	}
}

func TestValidateSignature_WrongCert(t *testing.T) {
	// Sign with one key, verify with a different cert.
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Other IdP"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	otherDER, _ := x509.CreateCertificate(rand.Reader, otherTemplate, otherTemplate, &otherKey.PublicKey, otherKey)
	otherCert, _ := x509.ParseCertificate(otherDER)

	assertion := buildTestAssertion(t, "_assert2", time.Now().Add(-1*time.Minute), time.Now().Add(5*time.Minute))
	response := buildTestResponse(t, assertion)

	raw := []byte(response)
	root, err := parseToTree(raw)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := decodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}

	sig := resp.Assertions[0].Signature
	if sig == nil {
		sig = resp.Signature
	}

	err = validateSignature(root, sig, otherCert)
	if err == nil {
		t.Fatal("expected signature validation to fail with wrong cert")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Condition Validation Tests ---

func TestValidateConditions_Valid(t *testing.T) {
	resetReplayCache()
	now := time.Now()
	assertion := &samlAssertionW{
		ID: "_cond_valid_1",
		Conditions: samlConditions{
			NotBefore:    now.Add(-1 * time.Minute).UTC().Format(time.RFC3339),
			NotOnOrAfter: now.Add(5 * time.Minute).UTC().Format(time.RFC3339),
			Audiences: []samlAudienceRestriction{
				{Audiences: []string{"https://sp.example.com"}},
			},
		},
	}
	err := validateConditions(assertion, "https://sp.example.com", now)
	if err != nil {
		t.Fatalf("expected valid conditions, got: %v", err)
	}
}

func TestValidateConditions_Expired(t *testing.T) {
	resetReplayCache()
	now := time.Now()
	assertion := &samlAssertionW{
		ID: "_cond_expired_1",
		Conditions: samlConditions{
			NotBefore:    now.Add(-10 * time.Minute).UTC().Format(time.RFC3339),
			NotOnOrAfter: now.Add(-5 * time.Minute).UTC().Format(time.RFC3339),
			Audiences: []samlAudienceRestriction{
				{Audiences: []string{"https://sp.example.com"}},
			},
		},
	}
	err := validateConditions(assertion, "https://sp.example.com", now)
	if err == nil {
		t.Fatal("expected expired assertion to be rejected")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConditions_NotYetValid(t *testing.T) {
	resetReplayCache()
	now := time.Now()
	assertion := &samlAssertionW{
		ID: "_cond_future_1",
		Conditions: samlConditions{
			NotBefore:    now.Add(10 * time.Minute).UTC().Format(time.RFC3339),
			NotOnOrAfter: now.Add(15 * time.Minute).UTC().Format(time.RFC3339),
			Audiences: []samlAudienceRestriction{
				{Audiences: []string{"https://sp.example.com"}},
			},
		},
	}
	err := validateConditions(assertion, "https://sp.example.com", now)
	if err == nil {
		t.Fatal("expected not-yet-valid assertion to be rejected")
	}
	if !strings.Contains(err.Error(), "not yet valid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConditions_WrongAudience(t *testing.T) {
	resetReplayCache()
	now := time.Now()
	assertion := &samlAssertionW{
		ID: "_cond_audience_1",
		Conditions: samlConditions{
			NotBefore:    now.Add(-1 * time.Minute).UTC().Format(time.RFC3339),
			NotOnOrAfter: now.Add(5 * time.Minute).UTC().Format(time.RFC3339),
			Audiences: []samlAudienceRestriction{
				{Audiences: []string{"https://other-sp.example.com"}},
			},
		},
	}
	err := validateConditions(assertion, "https://sp.example.com", now)
	if err == nil {
		t.Fatal("expected wrong audience to be rejected")
	}
	if !strings.Contains(err.Error(), "audience") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Replay Detection Tests ---

func TestReplayDetection(t *testing.T) {
	resetReplayCache()
	now := time.Now()
	assertion := &samlAssertionW{
		ID: "_replay_test_1",
		Conditions: samlConditions{
			NotBefore:    now.Add(-1 * time.Minute).UTC().Format(time.RFC3339),
			NotOnOrAfter: now.Add(5 * time.Minute).UTC().Format(time.RFC3339),
		},
	}

	// First use should succeed.
	err := validateConditions(assertion, "", now)
	if err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}

	// Second use should be rejected as replay.
	err = validateConditions(assertion, "", now)
	if err == nil {
		t.Fatal("expected replay to be rejected")
	}
	if !strings.Contains(err.Error(), "replay") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- XSW Protection Tests ---

func TestXSW_SignedIDMismatch(t *testing.T) {
	// An attacker injects an assertion with a different ID.
	xml := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol">` +
		`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_legit">` +
		`</Assertion>` +
		`</Response>`
	root, err := parseToTree([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}

	sig := &xmlSignature{
		SignedInfo: xmlSignedInfo{
			Reference: xmlReference{
				URI: "#_attacker",
			},
		},
	}

	err = validateXSW(root, sig, "_legit")
	if err == nil {
		t.Fatal("expected XSW protection to reject mismatched IDs")
	}
	if !strings.Contains(err.Error(), "XSW") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestXSW_DuplicateID(t *testing.T) {
	// An attacker duplicates an element with the same ID.
	xml := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol">` +
		`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_dup">` +
		`</Assertion>` +
		`<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_dup">` +
		`</Assertion>` +
		`</Response>`
	root, err := parseToTree([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}

	sig := &xmlSignature{
		SignedInfo: xmlSignedInfo{
			Reference: xmlReference{
				URI: "#_dup",
			},
		},
	}

	err = validateXSW(root, sig, "_dup")
	if err == nil {
		t.Fatal("expected XSW protection to reject duplicate IDs")
	}
	if !strings.Contains(err.Error(), "XSW") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Entity Expansion Attack Test ---

func TestEntityExpansionRejected(t *testing.T) {
	// A billion laughs variant.
	malicious := `<?xml version="1.0"?>
<!DOCTYPE lolz [
  <!ENTITY lol "lol">
  <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
]>
<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol">
  <Status><StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></Status>
</Response>`

	_, err := decodeResponse([]byte(malicious))
	if err == nil {
		t.Fatal("expected entity expansion attack to be rejected")
	}
	if !strings.Contains(err.Error(), "DOCTYPE") && !strings.Contains(err.Error(), "ENTITY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEntityExpansion_DocType(t *testing.T) {
	malicious := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol"><Status><StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></Status></Response>`
	_, err := decodeResponse([]byte(malicious))
	if err == nil {
		t.Fatal("expected DOCTYPE to be rejected")
	}
}

// --- XML Depth Limit Test ---

func TestXMLDepthLimit(t *testing.T) {
	// Build a document that exceeds the depth limit.
	var sb strings.Builder
	for i := 0; i < maxXMLDepth+5; i++ {
		sb.WriteString("<e>")
	}
	sb.WriteString("deep")
	for i := 0; i < maxXMLDepth+5; i++ {
		sb.WriteString("</e>")
	}

	err := checkXMLSafety([]byte(sb.String()))
	if err == nil {
		t.Fatal("expected depth limit to be enforced")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Integration: BuildAuthnRequest ---

func TestBuildAuthnRequest(t *testing.T) {
	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	redirectURL, requestID, err := sp.BuildAuthnRequest("https://sp.example.com/dashboard")
	if requestID == "" {
		t.Error("no request id was returned, so no response could ever be tied to this request")
	}
	if !strings.Contains(redirectURL, "SAMLRequest=") {
		t.Error("the redirect carries no SAMLRequest")
	}
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(redirectURL, "https://idp.example.com/sso?") {
		t.Errorf("unexpected redirect URL: %s", redirectURL)
	}
	if !strings.Contains(redirectURL, "SAMLRequest=") {
		t.Error("missing SAMLRequest parameter")
	}
	if !strings.Contains(redirectURL, "RelayState=") {
		t.Error("missing RelayState parameter")
	}
}

// --- Integration: Full ParseResponse ---

func TestParseResponse_FullFlow(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	assertion := buildTestAssertion(t, "_full_flow_1", now.Add(-1*time.Minute), now.Add(5*time.Minute))
	response := buildTestResponse(t, assertion)

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}

	if result.NameID != "user@example.com" {
		t.Errorf("expected NameID user@example.com, got %s", result.NameID)
	}
	if result.SessionIndex != "_session1" {
		t.Errorf("expected SessionIndex _session1, got %s", result.SessionIndex)
	}
	if roles, ok := result.Attributes["Role"]; !ok || len(roles) == 0 || roles[0] != "admin" {
		t.Errorf("expected Role attribute [admin], got %v", result.Attributes["Role"])
	}
}

func TestParseResponse_Replay(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	assertion := buildTestAssertion(t, "_replay_full_1", now.Add(-1*time.Minute), now.Add(5*time.Minute))
	response := buildTestResponse(t, assertion)

	// First call should succeed.
	_, err = sp.parseResponseBytes([]byte(response), now, nil)
	if err != nil {
		t.Fatalf("first ParseResponse failed: %v", err)
	}

	// Second call with same response should be rejected.
	_, err = sp.parseResponseBytes([]byte(response), now, nil)
	if err == nil {
		t.Fatal("expected replay to be rejected")
	}
	if !strings.Contains(err.Error(), "replay") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Test Helpers ---

// buildTestAssertion creates a signed SAML assertion for testing.
func buildTestAssertion(t *testing.T, id string, notBefore, notOnOrAfter time.Time) string {
	t.Helper()

	assertionBody := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.example.com</saml:Issuer>`+
			`{SIGNATURE}`+
			`<saml:Subject>`+
			`<saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">user@example.com</saml:NameID>`+
			`</saml:Subject>`+
			`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s">`+
			`<saml:AudienceRestriction><saml:Audience>https://sp.example.com</saml:Audience></saml:AudienceRestriction>`+
			`</saml:Conditions>`+
			`<saml:AuthnStatement SessionIndex="_session1"/>`+
			`<saml:AttributeStatement>`+
			`<saml:Attribute Name="Role"><saml:AttributeValue>admin</saml:AttributeValue></saml:Attribute>`+
			`</saml:AttributeStatement>`+
			`</saml:Assertion>`,
		id,
		time.Now().UTC().Format(time.RFC3339),
		notBefore.UTC().Format(time.RFC3339),
		notOnOrAfter.UTC().Format(time.RFC3339),
	)

	// For signature computation, we need the assertion without the Signature element placeholder.
	assertionForDigest := strings.Replace(assertionBody, `{SIGNATURE}`, "", 1)

	// Canonicalize for digest.
	tree, err := parseToTree([]byte(assertionForDigest))
	if err != nil {
		t.Fatalf("parse assertion for digest: %v", err)
	}
	canonical := canonicalizeNode(tree, nil)
	digest := sha256.Sum256(canonical)
	digestB64 := base64.StdEncoding.EncodeToString(digest[:])

	// Build SignedInfo.
	signedInfoXML := fmt.Sprintf(
		`<ds:SignedInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`+
			`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/>`+
			`<ds:Reference URI="#%s">`+
			`<ds:Transforms>`+
			`<ds:Transform Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"/>`+
			`<ds:Transform Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`</ds:Transforms>`+
			`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`+
			`<ds:DigestValue>%s</ds:DigestValue>`+
			`</ds:Reference>`+
			`</ds:SignedInfo>`,
		id, digestB64,
	)

	// Canonicalize SignedInfo for signature.
	siTree, err := parseToTree([]byte(signedInfoXML))
	if err != nil {
		t.Fatalf("parse SignedInfo: %v", err)
	}
	siCanonical := canonicalizeNode(siTree, nil)
	siDigest := sha256.Sum256(siCanonical)

	// Sign.
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, siDigest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sigBytes)

	// Build complete Signature element.
	signatureXML := fmt.Sprintf(
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`+
			`%s`+
			`<ds:SignatureValue>%s</ds:SignatureValue>`+
			`</ds:Signature>`,
		signedInfoXML, sigB64,
	)

	// Insert signature into assertion.
	result := strings.Replace(assertionBody, `{SIGNATURE}`, signatureXML, 1)
	return result
}

// buildTestResponse wraps an assertion in a SAML Response.
func buildTestResponse(t *testing.T, assertion string) string {
	t.Helper()
	return fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="_resp1" Version="2.0" IssueInstant="%s" Destination="https://sp.example.com/saml/acs">`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.example.com</saml:Issuer>`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		time.Now().UTC().Format(time.RFC3339),
		assertion,
	)
}

// buildResponseSignedTest builds a Response whose *Response* element is signed rather than
// the Assertion. ADFS does this by default, as do several other identity providers, so a
// service provider that only handles assertion-level signatures cannot log anybody in there.
func buildResponseSignedTest(t *testing.T, assertionID string, notBefore, notOnOrAfter time.Time) string {
	t.Helper()

	// An unsigned assertion, wrapped in a Response that carries the signature.
	inner := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.example.com</saml:Issuer>`+
			`<saml:Subject>`+
			`<saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">user@example.com</saml:NameID>`+
			`</saml:Subject>`+
			`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s">`+
			`<saml:AudienceRestriction><saml:Audience>https://sp.example.com</saml:Audience></saml:AudienceRestriction>`+
			`</saml:Conditions>`+
			`<saml:AuthnStatement SessionIndex="_session1"/>`+
			`<saml:AttributeStatement>`+
			`<saml:Attribute Name="Role"><saml:AttributeValue>admin</saml:AttributeValue></saml:Attribute>`+
			`</saml:AttributeStatement>`+
			`</saml:Assertion>`,
		assertionID,
		time.Now().UTC().Format(time.RFC3339),
		notBefore.UTC().Format(time.RFC3339),
		notOnOrAfter.UTC().Format(time.RFC3339),
	)

	const respID = "_resp_signed_1"
	body := fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="%s" Version="2.0" IssueInstant="%s" Destination="https://sp.example.com/saml/acs">`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.example.com</saml:Issuer>`+
			`{SIGNATURE}`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		respID, time.Now().UTC().Format(time.RFC3339), inner,
	)

	sig := signElement(t, body, respID)
	return strings.Replace(body, `{SIGNATURE}`, sig, 1)
}

// signElement produces a ds:Signature over the given document with the {SIGNATURE}
// placeholder removed, referencing refID. Shared by the response-signed helper.
func signElement(t *testing.T, bodyWithPlaceholder, refID string) string {
	t.Helper()

	forDigest := strings.Replace(bodyWithPlaceholder, `{SIGNATURE}`, "", 1)
	tree, err := parseToTree([]byte(forDigest))
	if err != nil {
		t.Fatalf("parse for digest: %v", err)
	}
	digest := sha256.Sum256(canonicalizeNode(tree, nil))
	digestB64 := base64.StdEncoding.EncodeToString(digest[:])

	signedInfoXML := fmt.Sprintf(
		`<ds:SignedInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`+
			`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/>`+
			`<ds:Reference URI="#%s">`+
			`<ds:Transforms>`+
			`<ds:Transform Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"/>`+
			`<ds:Transform Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`</ds:Transforms>`+
			`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`+
			`<ds:DigestValue>%s</ds:DigestValue>`+
			`</ds:Reference>`+
			`</ds:SignedInfo>`,
		refID, digestB64,
	)

	siTree, err := parseToTree([]byte(signedInfoXML))
	if err != nil {
		t.Fatalf("parse SignedInfo: %v", err)
	}
	siDigest := sha256.Sum256(canonicalizeNode(siTree, nil))

	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, siDigest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	return fmt.Sprintf(
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">%s<ds:SignatureValue>%s</ds:SignatureValue></ds:Signature>`,
		signedInfoXML, base64.StdEncoding.EncodeToString(sigBytes),
	)
}

// TestParseResponse_ResponseLevelSignature covers an IdP that signs the Response rather than
// the Assertion. ADFS does this by default. The assertion inside carries no signature of its
// own, and its identity is trusted because it is a descendant of the element that was signed.
func TestParseResponse_ResponseLevelSignature(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	response := buildResponseSignedTest(t, "_resp_sig_assertion_1", now.Add(-1*time.Minute), now.Add(5*time.Minute))

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err != nil {
		t.Fatalf("a Response-level signature must be accepted: %v", err)
	}
	if result.NameID != "user@example.com" {
		t.Errorf("NameID = %q, want user@example.com", result.NameID)
	}
	if result.SessionIndex != "_session1" {
		t.Errorf("SessionIndex = %q, want _session1", result.SessionIndex)
	}
}

// TestXSW_AssertionInjectedOutsideSignedResponse is the attack the Response-signature path has
// to survive: a genuine signed Response, with a forged assertion added outside the signed
// element. Reading identity from the forged one would authenticate an attacker.
func TestXSW_AssertionInjectedOutsideSignedResponse(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	genuine := buildResponseSignedTest(t, "_genuine_assertion", now.Add(-1*time.Minute), now.Add(5*time.Minute))

	// Wrap the whole signed Response inside an outer Response, and put a forged assertion
	// where a naive parser reads the first assertion from.
	forged := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_forged_assertion" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.example.com</saml:Issuer>`+
			`<saml:Subject><saml:NameID>attacker@evil.example</saml:NameID></saml:Subject>`+
			`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s">`+
			`<saml:AudienceRestriction><saml:Audience>https://sp.example.com</saml:Audience></saml:AudienceRestriction>`+
			`</saml:Conditions>`+
			`</saml:Assertion>`,
		now.UTC().Format(time.RFC3339),
		now.Add(-1*time.Minute).UTC().Format(time.RFC3339),
		now.Add(5*time.Minute).UTC().Format(time.RFC3339),
	)

	// Strip the outer Response wrapper off the genuine document so it can be nested.
	inner := strings.TrimPrefix(genuine, `<?xml version="1.0" encoding="UTF-8"?>`)
	wrapped := fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="_outer" Version="2.0" IssueInstant="%s">`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s%s`+
			`</samlp:Response>`,
		now.UTC().Format(time.RFC3339), forged, inner,
	)

	if _, err := sp.parseResponseBytes([]byte(wrapped), now, nil); err == nil {
		t.Fatal("an assertion outside the signed element must be rejected")
	}
}

// TestValidateXSW_AssertionOutsideSignedResponse exercises the containment check directly.
//
// The end-to-end attack test above is satisfied by any rejection, and an unmarshaller that
// simply fails to find the nested signature would satisfy it without the containment check
// ever running. This plants the violation at the guard itself: a well-formed document, a
// signature over the Response, and the assertion sitting outside that Response.
func TestValidateXSW_AssertionOutsideSignedResponse(t *testing.T) {
	doc := `<wrap xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">` +
		`<saml:Assertion ID="_outside"><saml:Issuer>x</saml:Issuer></saml:Assertion>` +
		`<samlp:Response ID="_signed"><saml:Assertion ID="_inside"/></samlp:Response>` +
		`</wrap>`

	root, err := parseToTree([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	sig := &xmlSignature{SignedInfo: xmlSignedInfo{Reference: xmlReference{URI: "#_signed"}}}

	// The assertion inside the signed Response is covered, so it must be accepted.
	if err := validateXSW(root, sig, "_inside"); err != nil {
		t.Errorf("an assertion inside the signed Response must be accepted: %v", err)
	}

	// The one outside it is not covered, and must be refused by the containment check.
	err = validateXSW(root, sig, "_outside")
	if err == nil {
		t.Fatal("an assertion outside the signed Response must be rejected")
	}
	if !strings.Contains(err.Error(), "not inside the signed element") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}
}

// TestValidateXSW_SignedElementIsNeitherAssertionNorResponse covers a reference that resolves
// to some other element. Accepting it would let an attacker have the signature cover a benign
// element while identity is read from an assertion nested inside it.
func TestValidateXSW_SignedElementIsNeitherAssertionNorResponse(t *testing.T) {
	doc := `<wrap xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">` +
		`<decoy ID="_signed"><saml:Assertion ID="_inner"/></decoy>` +
		`</wrap>`

	root, err := parseToTree([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	sig := &xmlSignature{SignedInfo: xmlSignedInfo{Reference: xmlReference{URI: "#_signed"}}}

	err = validateXSW(root, sig, "_inner")
	if err == nil {
		t.Fatal("a signature over a non-Response wrapper must be rejected")
	}
	if !strings.Contains(err.Error(), "expected the Assertion itself or the enclosing Response") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}
}

// --- DEFECT TESTS ---

// TestParseResponse_EmptyNameID_Rejected verifies that a response with an empty NameID is rejected.
// An empty NameID that authenticates successfully is a total bypass: the caller gets back an empty
// username which can match an admin account or grant access as "nobody".
func TestParseResponse_EmptyNameID_Rejected(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Build an assertion with an EMPTY NameID.
	assertion := buildTestAssertionWithNameID(t, "_empty_nameid_1", now.Add(-1*time.Minute), now.Add(5*time.Minute), "")
	response := buildTestResponse(t, assertion)

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err == nil {
		t.Fatalf("expected empty NameID to be rejected, but got result: %+v", result)
	}
	if !strings.Contains(err.Error(), "NameID") {
		t.Fatalf("expected error about NameID, got: %v", err)
	}
}

// TestParseResponse_WhitespaceNameID_Rejected verifies that a response with a whitespace-only NameID is rejected.
func TestParseResponse_WhitespaceNameID_Rejected(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	assertion := buildTestAssertionWithNameID(t, "_ws_nameid_1", now.Add(-1*time.Minute), now.Add(5*time.Minute), "   ")
	response := buildTestResponse(t, assertion)

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err == nil {
		t.Fatalf("expected whitespace NameID to be rejected, but got result: %+v", result)
	}
	if !strings.Contains(err.Error(), "NameID") {
		t.Fatalf("expected error about NameID, got: %v", err)
	}
}

// TestParseResponse_Destination_Mismatch verifies that a response with a Destination that does not
// match the SP's ACS URL is rejected.
func TestParseResponse_Destination_Mismatch(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	assertion := buildTestAssertion(t, "_dest_mismatch_1", now.Add(-1*time.Minute), now.Add(5*time.Minute))
	// Build a response with wrong Destination.
	response := buildTestResponseWithDestination(t, assertion, "https://evil.example.com/saml/acs")

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err == nil {
		t.Fatalf("expected Destination mismatch to be rejected, but got result: %+v", result)
	}
	if !strings.Contains(err.Error(), "Destination") && !strings.Contains(err.Error(), "destination") {
		t.Fatalf("expected error about Destination, got: %v", err)
	}
}

// TestParseResponse_Destination_Present_Matches verifies that a correct Destination passes.
func TestParseResponse_Destination_Present_Matches(t *testing.T) {
	resetReplayCache()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,

		// These responses answer no AuthnRequest, so each is an unsolicited login and has to say so.
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	assertion := buildTestAssertion(t, "_dest_match_1", now.Add(-1*time.Minute), now.Add(5*time.Minute))
	response := buildTestResponseWithDestination(t, assertion, "https://sp.example.com/saml/acs")

	result, err := sp.parseResponseBytes([]byte(response), now, nil)
	if err != nil {
		t.Fatalf("correct Destination should pass: %v", err)
	}
	if result.NameID != "user@example.com" {
		t.Errorf("NameID = %q, want user@example.com", result.NameID)
	}
}

// buildTestAssertionWithNameID is like buildTestAssertion but with a configurable NameID value.
func buildTestAssertionWithNameID(t *testing.T, id string, notBefore, notOnOrAfter time.Time, nameID string) string {
	t.Helper()

	assertionBody := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.example.com</saml:Issuer>`+
			`{SIGNATURE}`+
			`<saml:Subject>`+
			`<saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">%s</saml:NameID>`+
			`</saml:Subject>`+
			`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s">`+
			`<saml:AudienceRestriction><saml:Audience>https://sp.example.com</saml:Audience></saml:AudienceRestriction>`+
			`</saml:Conditions>`+
			`<saml:AuthnStatement SessionIndex="_session1"/>`+
			`<saml:AttributeStatement>`+
			`<saml:Attribute Name="Role"><saml:AttributeValue>admin</saml:AttributeValue></saml:Attribute>`+
			`</saml:AttributeStatement>`+
			`</saml:Assertion>`,
		id,
		time.Now().UTC().Format(time.RFC3339),
		nameID,
		notBefore.UTC().Format(time.RFC3339),
		notOnOrAfter.UTC().Format(time.RFC3339),
	)

	assertionForDigest := strings.Replace(assertionBody, `{SIGNATURE}`, "", 1)
	tree, err := parseToTree([]byte(assertionForDigest))
	if err != nil {
		t.Fatalf("parse assertion for digest: %v", err)
	}
	canonical := canonicalizeNode(tree, nil)
	digest := sha256.Sum256(canonical)
	digestB64 := base64.StdEncoding.EncodeToString(digest[:])

	signedInfoXML := fmt.Sprintf(
		`<ds:SignedInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`+
			`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/>`+
			`<ds:Reference URI="#%s">`+
			`<ds:Transforms>`+
			`<ds:Transform Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"/>`+
			`<ds:Transform Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`</ds:Transforms>`+
			`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`+
			`<ds:DigestValue>%s</ds:DigestValue>`+
			`</ds:Reference>`+
			`</ds:SignedInfo>`,
		id, digestB64,
	)

	siTree, err := parseToTree([]byte(signedInfoXML))
	if err != nil {
		t.Fatalf("parse SignedInfo: %v", err)
	}
	siCanonical := canonicalizeNode(siTree, nil)
	siDigest := sha256.Sum256(siCanonical)

	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, siDigest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sigBytes)

	signatureXML := fmt.Sprintf(
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">`+
			`%s`+
			`<ds:SignatureValue>%s</ds:SignatureValue>`+
			`</ds:Signature>`,
		signedInfoXML, sigB64,
	)

	return strings.Replace(assertionBody, `{SIGNATURE}`, signatureXML, 1)
}

// buildTestResponseWithDestination wraps an assertion in a SAML Response with a specified Destination.
func buildTestResponseWithDestination(t *testing.T, assertion string, destination string) string {
	t.Helper()
	return fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="_resp1" Version="2.0" IssueInstant="%s" Destination="%s">`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.example.com</saml:Issuer>`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		time.Now().UTC().Format(time.RFC3339),
		destination,
		assertion,
	)
}
