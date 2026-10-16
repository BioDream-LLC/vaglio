// Package saml implements a SAML 2.0 Service Provider.
//
// It comes from Perfuse (https://github.com/biodream-llc/perfuse, Apache License 2.0; see NOTICE), whose verification
// signed a real person in through a live Microsoft Entra ID tenant and through Keycloak. The fixtures in testdata come
// from those sign-ins. The Entra one has the tenant's identifiers replaced and is signed again with a test key
// (regenfixture_test.go), keeping Entra's shape: element order, no namespace prefixes, its claim URIs. The names in
// them (urn:perfuse:...) stay as Perfuse's, since changing them means signing them again.
//
// Written against the standard library for the same reason as the OIDC package: authentication is the one
// place where an unaudited dependency is least acceptable, and the attack surface of a SAML library is larger than most
// because SAML's security rests on XML canonicalization and signature verification, both of which have produced CVEs in
// every major implementation.
//
// The scope is deliberately narrow: verify that an IdP signed an assertion, that the assertion is fresh and for us, and
// report what it said about the person. The SP does not implement single logout, artifact binding, or encrypted
// assertions - those are added when a deployment needs them, not speculatively.
//
// Security rules:
//   - Never trust a certificate embedded in the SAML response. Only the configured IdP certificate is used.
//   - Verify the signature first, then read identity from the SAME signed node (XSW protection).
//   - Reject expired or not-yet-valid assertions (30 second clock skew tolerance).
//   - Reject replayed assertion IDs within their validity window.
//   - Reject XML with DOCTYPE/ENTITY declarations (billion laughs prevention).
package saml

import (
	"compress/flate"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Config describes how the service provider connects to an IdP.
type Config struct {
	// EntityID is this SP's entity identifier, used as the Issuer in AuthnRequests and as the Audience for
	// AudienceRestriction validation.
	EntityID string

	// ACSPath is the path portion of the Assertion Consumer Service URL where the IdP posts responses.
	ACSPath string

	// IdPMetadataURL is the IdP's metadata endpoint. Used to discover the SSO URL.
	// If IdPSSOURL is set directly, this is not required.
	IdPMetadataURL string

	// IdPSSOURL is the IdP's Single Sign-On URL (HTTP-Redirect binding).
	IdPSSOURL string

	// IdPCertPEM is the PEM-encoded X.509 certificate of the IdP used to verify response signatures.
	// This is the ONLY certificate trusted for verification - never a cert embedded in the response.
	IdPCertPEM string

	// AllowUnsolicited permits a response that answers no request this server issued.
	//
	// Off by default, and the default is the safe one. An unsolicited response is how identity-provider-initiated sign-on works -
	// somebody clicks Vaglio in a portal and arrives already authenticated - so it is a real feature that real sites want. It is
	// also the thing that makes a captured response reusable against a different browser, because nothing ties the response to a
	// login that browser began.
	//
	// Turning it on is therefore a decision with a cost, and it should be made deliberately rather than inherited from a default.
	AllowUnsolicited bool

	// SignRequests indicates whether AuthnRequests should be signed.
	SignRequests bool
}

// ServiceProvider processes SAML authentication.
type ServiceProvider struct {
	config Config
	cert   *x509.Certificate
}

// Assertion is the verified identity information from a SAML response.
//
// Every field here was extracted from a signed node whose signature was verified against the configured IdP certificate,
// whose conditions passed, and whose assertion ID has not been seen before.
type Assertion struct {
	// NameID is the subject identifier the IdP assigned.
	NameID string

	// SessionIndex is the IdP's session reference, used for logout.
	SessionIndex string

	// Attributes are the SAML attribute statements: attribute name → list of values.
	Attributes map[string][]string

	// NotBefore is the earliest time this assertion is valid.
	NotBefore time.Time

	// NotOnOrAfter is the time after which this assertion must not be accepted.
	NotOnOrAfter time.Time
}

// New creates a ServiceProvider from the given configuration.
//
// It parses the IdP certificate at configuration time rather than at every request, so a misconfigured certificate is
// reported immediately rather than at someone's first login attempt.
func New(cfg Config) (*ServiceProvider, error) {
	if cfg.EntityID == "" {
		return nil, fmt.Errorf("saml: EntityID is required")
	}
	if cfg.IdPCertPEM == "" {
		return nil, fmt.Errorf("saml: IdPCertPEM is required")
	}

	cert, err := parseCertPEM(cfg.IdPCertPEM)
	if err != nil {
		return nil, fmt.Errorf("saml: parse IdP certificate: %w", err)
	}

	return &ServiceProvider{
		config: cfg,
		cert:   cert,
	}, nil
}

// ParseResponse decodes, validates, and extracts identity from a base64-encoded SAML response.
//
// The validation order is deliberate:
//  1. Decode and check XML safety (entity expansion, depth)
//  2. Parse the tree for signature verification
//  3. Verify the XML signature against the configured IdP cert
//  4. XSW protection: confirm the signed node is the one we read from
//  5. Validate conditions: time bounds, audience, replay
//  6. Extract identity from the verified assertion
func (sp *ServiceProvider) ParseResponse(samlResponse string) (*Assertion, error) {
	return sp.ParseResponseFor(samlResponse, nil)
}

// ParseResponseFor parses a response and requires it to answer a request this server issued.
//
// outstanding is asked whether an InResponseTo value names a request that is still waiting, and should consume it so the same
// response cannot be presented twice. Pass nil only for identity-provider-initiated sign-on, which needs AllowUnsolicited.
//
// This is the check that ties a response to a login somebody began in this browser. Without it a response is a bearer token for
// whoever holds it: an attacker completes a login as themselves, keeps the response, and posts it into a victim's browser, and the
// victim is now inside the attacker's account - reading and writing as them, with the audit log showing the attacker's name.
func (sp *ServiceProvider) ParseResponseFor(samlResponse string, outstanding func(id string) bool) (*Assertion, error) {
	raw, err := base64.StdEncoding.DecodeString(samlResponse)
	if err != nil {
		return nil, fmt.Errorf("saml: decode base64 response: %w", err)
	}

	return sp.parseResponseBytes(raw, time.Now(), outstanding)
}

// parseResponseBytes is the internal implementation that accepts a time for testing.
func (sp *ServiceProvider) parseResponseBytes(raw []byte, now time.Time, outstanding func(id string) bool) (*Assertion, error) {
	// Step 1: Check XML safety and decode.
	resp, err := decodeResponse(raw)
	if err != nil {
		return nil, err
	}

	// Check status.
	if resp.Status.StatusCode.Value != "urn:oasis:names:tc:SAML:2.0:status:Success" {
		return nil, fmt.Errorf("saml: response status is %q (not Success)", resp.Status.StatusCode.Value)
	}

	if len(resp.Assertions) == 0 {
		return nil, fmt.Errorf("saml: response contains no assertions")
	}

	// Step 2: Parse into tree for signature verification.
	root, err := parseToTree(raw)
	if err != nil {
		return nil, fmt.Errorf("saml: parse tree: %w", err)
	}

	// Find the assertion to verify.
	assertion := &resp.Assertions[0]

	// Determine which signature to verify: assertion-level or response-level.
	sig := assertion.Signature
	if sig == nil {
		sig = resp.Signature
	}
	if sig == nil {
		return nil, fmt.Errorf("saml: neither the response nor the assertion is signed")
	}

	// Step 3: Verify signature.
	if err := validateSignature(root, sig, sp.cert); err != nil {
		return nil, err
	}

	// Step 4: XSW protection.
	if err := validateXSW(root, sig, assertion.ID); err != nil {
		return nil, err
	}

	// Step 4b: tie the response to a request this server issued.
	//
	// After the signature, deliberately. An unsigned document's InResponseTo is attacker-controlled and tells us nothing, so
	// checking it first would be reasoning about a value we have no reason to believe. After verification it is a claim the identity
	// provider signed.
	if err := sp.validateInResponseTo(resp.InResponseTo, outstanding); err != nil {
		return nil, err
	}

	// Step 5: Validate conditions.
	if err := validateConditions(assertion, sp.config.EntityID, now); err != nil {
		return nil, err
	}

	// Step 6: Validate Destination if present. The Destination attribute tells us this response was
	// intended for our ACS endpoint. If present and it does not match, an attacker may be replaying
	// a response intended for a different SP.
	if resp.Destination != "" && sp.config.ACSPath != "" {
		if resp.Destination != sp.config.ACSPath {
			return nil, fmt.Errorf("saml: Response Destination %q does not match ACS URL %q", resp.Destination, sp.config.ACSPath)
		}
	}

	// Step 7: Extract identity from the verified assertion.
	nameID := strings.TrimSpace(assertion.Subject.NameID.Value)
	if nameID == "" {
		return nil, fmt.Errorf("saml: assertion has empty NameID (authentication would produce an empty username)")
	}

	notBefore, _ := time.Parse(time.RFC3339, assertion.Conditions.NotBefore)
	notOnOrAfter, _ := time.Parse(time.RFC3339, assertion.Conditions.NotOnOrAfter)

	result := &Assertion{
		NameID:       nameID,
		Attributes:   make(map[string][]string),
		NotBefore:    notBefore,
		NotOnOrAfter: notOnOrAfter,
	}

	// Session index.
	if len(assertion.AuthnStmt) > 0 {
		result.SessionIndex = assertion.AuthnStmt[0].SessionIndex
	}

	// Attributes.
	for _, stmt := range assertion.AttrStmts {
		for _, attr := range stmt.Attributes {
			for _, v := range attr.Values {
				result.Attributes[attr.Name] = append(result.Attributes[attr.Name], v.Value)
			}
		}
	}

	return result, nil
}

// BuildAuthnRequest creates the redirect URL for sending the user to the IdP, and the id of the request it built.
//
// Uses HTTP-Redirect binding with deflate encoding per the SAML 2.0 bindings spec.
//
// The caller has to keep the id and recognise it again when the response arrives, which is what ParseResponseFor is for. It used to
// be generated and dropped, so no response could be tied to a request even in principle.
func (sp *ServiceProvider) BuildAuthnRequest(relayState string) (redirectURL, requestID string, err error) {
	if sp.config.IdPSSOURL == "" {
		return "", "", fmt.Errorf("saml: IdPSSOURL not configured")
	}

	id := "_" + randomID()
	instant := time.Now().UTC().Format(time.RFC3339)

	// Build the AuthnRequest XML.
	authnReq := fmt.Sprintf(
		`<samlp:AuthnRequest xmlns:samlp="%s" xmlns:saml="%s"`+
			` ID="%s" Version="2.0" IssueInstant="%s"`+
			` Destination="%s"`+
			` AssertionConsumerServiceURL="%s"`+
			` ProtocolBinding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST">`+
			`<saml:Issuer>%s</saml:Issuer>`+
			`</samlp:AuthnRequest>`,
		nsSAMLProtocol, nsSAMLAssertion,
		id, instant,
		xmlEscape(sp.config.IdPSSOURL),
		xmlEscape(sp.config.ACSPath),
		xmlEscape(sp.config.EntityID),
	)

	// Deflate the request.
	var deflated strings.Builder
	w, err := flate.NewWriter(&deflated, flate.DefaultCompression)
	if err != nil {
		return "", "", fmt.Errorf("saml: deflate: %w", err)
	}
	if _, err := io.WriteString(w, authnReq); err != nil {
		return "", "", fmt.Errorf("saml: deflate write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", "", fmt.Errorf("saml: deflate close: %w", err)
	}

	// Base64 encode.
	encoded := base64.StdEncoding.EncodeToString([]byte(deflated.String()))

	// Build the redirect URL.
	u, err := url.Parse(sp.config.IdPSSOURL)
	if err != nil {
		return "", "", fmt.Errorf("saml: parse IdP SSO URL: %w", err)
	}
	q := u.Query()
	q.Set("SAMLRequest", encoded)
	if relayState != "" {
		q.Set("RelayState", relayState)
	}
	u.RawQuery = q.Encode()

	return u.String(), id, nil
}

// parseCertPEM parses a PEM-encoded X.509 certificate.
func parseCertPEM(pemData string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("PEM block type is %q, want CERTIFICATE", block.Type)
	}
	return x509.ParseCertificate(block.Bytes)
}

// randomID generates a random identifier for AuthnRequests.
// Uses crypto/rand for unpredictability.
func randomID() string {
	b := make([]byte, 16)
	_, _ = randRead(b)
	return fmt.Sprintf("%x", b)
}

// xmlEscape escapes a string for safe inclusion in XML attribute values.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// validateInResponseTo checks that a response answers a request this server issued.
//
// Three cases, and the middle one is the reason this exists.
//
// A response with no InResponseTo is unsolicited: the identity provider started the login. Legitimate, and refused unless the site
// has said it wants that, because it is also how a captured response gets reused.
//
// A response naming a request is accepted only if the caller still recognises the id. Recognising it must also consume it - a
// response is an answer to one login attempt and answering the same attempt twice is a replay.
//
// A response naming a request when nothing is tracking requests is refused rather than waved through. That combination means the
// caller asked for the safe path and cannot provide what it needs, and guessing which way they meant it is how a check becomes
// decorative.
func (sp *ServiceProvider) validateInResponseTo(inResponseTo string, outstanding func(id string) bool) error {
	if inResponseTo == "" {
		if !sp.config.AllowUnsolicited {
			return fmt.Errorf("saml: the response answers no request this server sent, and unsolicited responses are not allowed")
		}

		return nil
	}

	if outstanding == nil {
		return fmt.Errorf("saml: the response names request %q but no request is being tracked", inResponseTo)
	}

	if !outstanding(inResponseTo) {
		return fmt.Errorf("saml: the response names request %q, which this server did not send or has already answered", inResponseTo)
	}

	return nil
}

// VerifyAt verifies a response as at a given instant, which is what makes a captured document testable.
//
// Exported for tests in other packages that need a real provider's assertion to work with. Deliberately not an unverified parser: an entry
// point that skipped the signature would be the wrong shape to offer, and a caller wanting attributes from a document should still have to
// prove the document is genuine. The clock is a parameter because a saved assertion's validity window is in the past, and the alternative -
// accepting an expired document - is not something to build a seam for.
func (sp *ServiceProvider) VerifyAt(raw []byte, now time.Time, outstanding func(id string) bool) (*Assertion, error) {
	return sp.parseResponseBytes(raw, now, outstanding)
}
