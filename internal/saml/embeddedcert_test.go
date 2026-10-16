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

// A certificate carried inside a SAML response must never be trusted.
//
// This is the full authentication bypass. An attacker signs a response with a key they own, embeds the matching certificate in
// KeyInfo, and a verifier that reads the certificate from the document it is verifying checks the attacker's signature against the
// attacker's key. It succeeds, and the attacker is whoever they said they were - any user, any role.
//
// The package gets this right and says so in three separate comments. There was no test. A comment asserting a security property is
// worth nothing against a later edit: reading KeyInfo is the obvious thing to do when a certificate is sitting right there, the
// tests would all have passed, and nothing would have complained. That is the same shape as every other defect found in this
// codebase this week - correct code, no guard, one edit from silent failure - except that here the failure is that anybody can log in
// as anybody.

// attacker is a key and certificate belonging to somebody who is not the identity provider.
type attacker struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func newAttacker(t *testing.T) attacker {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "Attacker, posing as the IdP"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return attacker{
		key:  key,
		cert: cert,
		pem:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// signElementAs signs like signElement but with a chosen key, and embeds a certificate in KeyInfo.
//
// Embedding it is the point. A real attack puts the certificate in the document precisely because it is where a careless verifier
// will look, so a test of this property has to include it rather than merely sign with the wrong key - that case is already covered
// by TestValidateSignature_WrongCert and is a different mistake.
func signElementAs(t *testing.T, bodyWithPlaceholder, refID string, key *rsa.PrivateKey, embedCertDER []byte) string {
	t.Helper()

	forDigest := strings.Replace(bodyWithPlaceholder, `{SIGNATURE}`, "", 1)

	tree, err := parseToTree([]byte(forDigest))
	if err != nil {
		t.Fatalf("parse for digest: %v", err)
	}

	digest := sha256.Sum256(canonicalizeNode(tree, nil))

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
		refID, base64.StdEncoding.EncodeToString(digest[:]),
	)

	siTree, err := parseToTree([]byte(signedInfoXML))
	if err != nil {
		t.Fatalf("parse SignedInfo: %v", err)
	}

	siDigest := sha256.Sum256(canonicalizeNode(siTree, nil))

	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, siDigest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	keyInfo := ""
	if embedCertDER != nil {
		keyInfo = fmt.Sprintf(
			`<ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo>`,
			base64.StdEncoding.EncodeToString(embedCertDER),
		)
	}

	return fmt.Sprintf(
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">%s<ds:SignatureValue>%s</ds:SignatureValue>%s</ds:Signature>`,
		signedInfoXML, base64.StdEncoding.EncodeToString(sigBytes), keyInfo,
	)
}

// buildResponseSignedBy builds a Response signed at the Response level by the given key, carrying that key's certificate in KeyInfo.
func buildResponseSignedBy(t *testing.T, id string, key *rsa.PrivateKey, embedCertDER []byte) string {
	t.Helper()

	now := time.Now().UTC()

	inner := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s_a" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.example.com</saml:Issuer>`+
			`<saml:Subject>`+
			`<saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">attacker@example.com</saml:NameID>`+
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
		now.Format(time.RFC3339),
		now.Add(-1*time.Minute).Format(time.RFC3339),
		now.Add(5*time.Minute).Format(time.RFC3339),
	)

	body := fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="%s" Version="2.0" IssueInstant="%s" Destination="https://sp.example.com/saml/acs">`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.example.com</saml:Issuer>`+
			`{SIGNATURE}`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		id, now.Format(time.RFC3339), inner,
	)

	sig := signElementAs(t, body, id, key, embedCertDER)

	return strings.Replace(body, `{SIGNATURE}`, sig, 1)
}

func TestACertificateInsideTheResponseIsNeverTrusted(t *testing.T) {
	resetReplayCache()

	bad := newAttacker(t)

	// Signed by the attacker, with the attacker's own certificate embedded where a careless verifier would find it.
	response := buildResponseSignedBy(t, "_embedded_1", bad.key, bad.cert.Raw)

	// The service provider is configured with the real identity provider's certificate, as it would be in production.
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

	encoded := base64.StdEncoding.EncodeToString([]byte(response))

	assertion, err := sp.ParseResponse(encoded)
	if err == nil {
		t.Fatalf("a response signed by an attacker was accepted, logging in %q with role from the document: "+
			"the certificate in KeyInfo was trusted", assertion.NameID)
	}

	// The rejection has to be about the signature. Passing for some other reason - a malformed document, a rejected condition -
	// would leave the property untested while looking tested, which is worse than having no test.
	if !strings.Contains(err.Error(), "verification failed") && !strings.Contains(err.Error(), "signature") {
		t.Fatalf("rejected, but not as a signature failure, so this test may be passing for the wrong reason: %v", err)
	}
}

func TestTheSameResponseIsAcceptedWhenTheAttackersCertIsTheConfiguredOne(t *testing.T) {
	// The positive control for the test above, and the reason it can be believed.
	//
	// Byte for byte the same response, accepted once the attacker's certificate is the one the service provider was configured with.
	// That proves the rejection above is specifically about which certificate is trusted, rather than about a document this test
	// happens to have built wrongly - which is exactly how a security test comes to pass while protecting nothing.
	resetReplayCache()

	bad := newAttacker(t)
	response := buildResponseSignedBy(t, "_embedded_2", bad.key, bad.cert.Raw)

	sp, err := New(Config{
		EntityID:         "https://sp.example.com",
		ACSPath:          "https://sp.example.com/saml/acs",
		IdPSSOURL:        "https://idp.example.com/sso",
		IdPCertPEM:       bad.pem,
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertion, err := sp.ParseResponse(base64.StdEncoding.EncodeToString([]byte(response)))
	if err != nil {
		t.Fatalf("the response is not otherwise valid, so the rejection above proves nothing: %v", err)
	}

	if assertion.NameID != "attacker@example.com" {
		t.Errorf("NameID = %q, want attacker@example.com", assertion.NameID)
	}
}

func TestAResponseWithNoEmbeddedCertificateStillVerifies(t *testing.T) {
	// KeyInfo is optional, and a verifier that came to depend on it would break every identity provider that omits it. Cheap to
	// state, and it pins the shape of the fix: the element is parsed and ignored, not required and ignored.
	resetReplayCache()

	response := buildResponseSignedBy(t, "_embedded_3", testKey, nil)

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

	if _, err := sp.ParseResponse(base64.StdEncoding.EncodeToString([]byte(response))); err != nil {
		t.Fatalf("a response with no embedded certificate was rejected: %v", err)
	}
}
