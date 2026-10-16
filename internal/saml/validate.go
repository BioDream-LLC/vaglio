// Signature and condition validation for SAML assertions.
//
// The security model is: verify the signature first, then read identity from the same signed node. Never from a
// different node, never from a certificate embedded in the response, and never from an assertion whose conditions
// have expired.
package saml

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"
)

// clockSkew is the tolerance for clock differences between the SP and IdP.
const clockSkew = 30 * time.Second

// replayCache tracks assertion IDs that have been seen, rejecting replays.
//
// In-memory and sync.Map-based rather than an external store, which is correct for a single-process deployment. A
// cluster would need a shared store, and that is a configuration concern rather than a library concern.
var replayCache replayStore

type replayStore struct {
	seen sync.Map // assertion ID → expiry time
}

// markSeen records an assertion ID. Returns false if the ID was already seen (replay).
func (r *replayStore) markSeen(id string, expiry time.Time) bool {
	if _, loaded := r.seen.LoadOrStore(id, expiry); loaded {
		return false
	}
	return true
}

// cleanup removes expired entries. Called opportunistically.
func (r *replayStore) cleanup() {
	now := time.Now()
	r.seen.Range(func(key, value any) bool {
		if expiry, ok := value.(time.Time); ok && now.After(expiry) {
			r.seen.Delete(key)
		}
		return true
	})
}

// resetForTesting clears the replay cache (test use only).
func resetReplayCache() {
	replayCache.seen = sync.Map{}
}

// validateSignature verifies an XML digital signature against the configured IdP certificate.
//
// Security: this function ONLY uses the provided certificate, never a certificate embedded in the SAML response.
// An attacker who can inject XML can also inject a certificate that matches their injected signature.
func validateSignature(root *xmlNode, sig *xmlSignature, cert *x509.Certificate) error {
	if sig == nil {
		return fmt.Errorf("saml: no signature present")
	}
	if cert == nil {
		return fmt.Errorf("saml: no IdP certificate configured")
	}

	// Step 1: Verify the digest of the referenced element.
	refURI := sig.SignedInfo.Reference.URI
	refNode, err := resolveSignatureReference(root, refURI)
	if err != nil {
		return fmt.Errorf("saml: resolve signature reference: %w", err)
	}

	// Apply transforms: enveloped-signature then exc-c14n.
	transformed := refNode
	var c14nPrefixes []string
	for _, t := range sig.SignedInfo.Reference.Transforms.Transforms {
		switch t.Algorithm {
		case "http://www.w3.org/2000/09/xmldsig#enveloped-signature":
			transformed = removeSignature(transformed)
		case "http://www.w3.org/2001/10/xml-exc-c14n#":
			if t.InclusiveNamespaces != nil && t.InclusiveNamespaces.PrefixList != "" {
				c14nPrefixes = strings.Fields(t.InclusiveNamespaces.PrefixList)
			}
		}
	}

	canonical := canonicalizeNode(transformed, c14nPrefixes)
	digest := sha256.Sum256(canonical)

	expectedDigest, err := base64.StdEncoding.DecodeString(
		strings.TrimSpace(sig.SignedInfo.Reference.DigestValue),
	)
	if err != nil {
		return fmt.Errorf("saml: decode digest value: %w", err)
	}

	// Check digest method.
	if sig.SignedInfo.Reference.DigestMethod.Algorithm != "http://www.w3.org/2001/04/xmlenc#sha256" &&
		sig.SignedInfo.Reference.DigestMethod.Algorithm != "http://www.w3.org/2000/09/xmldsig#sha256" {
		return fmt.Errorf("saml: unsupported digest method %q (only SHA-256 supported)", sig.SignedInfo.Reference.DigestMethod.Algorithm)
	}

	if len(expectedDigest) != sha256.Size {
		return fmt.Errorf("saml: digest value has wrong length %d (expected %d)", len(expectedDigest), sha256.Size)
	}
	if !hmacEqual(digest[:], expectedDigest) {
		return fmt.Errorf("saml: digest mismatch (document was modified or canonicalization differs)")
	}

	// Step 2: Verify the signature over SignedInfo.
	// Canonicalize SignedInfo.
	var siPrefixes []string
	if sig.SignedInfo.CanonicalizationMethod.InclusiveNamespaces != nil {
		siPrefixes = strings.Fields(sig.SignedInfo.CanonicalizationMethod.InclusiveNamespaces.PrefixList)
	}

	// We need the raw SignedInfo element. Find it in the tree.
	sigNode := findElement(root, nsXMLDSig, "Signature")
	if sigNode == nil {
		return fmt.Errorf("saml: Signature element not found in tree")
	}
	siNode := findElement(sigNode, nsXMLDSig, "SignedInfo")
	if siNode == nil {
		return fmt.Errorf("saml: SignedInfo element not found in tree")
	}

	siCanonical := canonicalizeNode(siNode, siPrefixes)
	siDigest := sha256.Sum256(siCanonical)

	sigBytes, err := base64.StdEncoding.DecodeString(
		strings.TrimSpace(sig.SigValue),
	)
	if err != nil {
		return fmt.Errorf("saml: decode signature value: %w", err)
	}

	// Check signature method.
	if sig.SignedInfo.SignatureMethod.Algorithm != "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256" {
		return fmt.Errorf("saml: unsupported signature method %q (only RSA-SHA256 supported)",
			sig.SignedInfo.SignatureMethod.Algorithm)
	}

	// Verify RSA-SHA256 signature.
	rsaPub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("saml: IdP certificate does not have an RSA public key")
	}

	if err := rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, siDigest[:], sigBytes); err != nil {
		return fmt.Errorf("saml: RSA signature verification failed: %w", err)
	}

	return nil
}

// hmacEqual is a constant-time comparison to prevent timing attacks on digest comparison.
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// validateConditions checks that an assertion's conditions are met.
//
// It verifies:
//   - NotBefore: the assertion is not yet valid (with clock skew tolerance)
//   - NotOnOrAfter: the assertion has not expired (with clock skew tolerance)
//   - AudienceRestriction: the assertion is intended for this SP
//   - Replay: the assertion has not been used before
func validateConditions(assertion *samlAssertionW, audience string, now time.Time) error {
	if assertion.ID == "" {
		return fmt.Errorf("saml: assertion has no ID")
	}

	// Parse time bounds.
	notBefore, err := time.Parse(time.RFC3339, assertion.Conditions.NotBefore)
	if err != nil {
		return fmt.Errorf("saml: parse NotBefore: %w", err)
	}
	notOnOrAfter, err := time.Parse(time.RFC3339, assertion.Conditions.NotOnOrAfter)
	if err != nil {
		return fmt.Errorf("saml: parse NotOnOrAfter: %w", err)
	}

	// Check time bounds with clock skew.
	if now.Add(clockSkew).Before(notBefore) {
		return fmt.Errorf("saml: assertion not yet valid (NotBefore=%s, now=%s)", notBefore, now)
	}
	if now.Add(-clockSkew).After(notOnOrAfter) {
		return fmt.Errorf("saml: assertion expired (NotOnOrAfter=%s, now=%s)", notOnOrAfter, now)
	}

	// Audience restriction.
	if audience != "" && len(assertion.Conditions.Audiences) > 0 {
		found := false
		for _, ar := range assertion.Conditions.Audiences {
			for _, a := range ar.Audiences {
				if a == audience {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("saml: audience %q not in assertion's AudienceRestriction", audience)
		}
	}

	// Replay detection.
	// Opportunistically clean expired entries.
	replayCache.cleanup()

	if !replayCache.markSeen(assertion.ID, notOnOrAfter) {
		return fmt.Errorf("saml: assertion ID %q has already been used (replay detected)", assertion.ID)
	}

	return nil
}

// validateXSW checks that the assertion we read identity from is covered by the signature.
//
// XSW (XML Signature Wrapping) attacks work by moving the signed element to an ignored location and placing a forged
// assertion where the application expects to find it. The defence is to resolve the signature's Reference URI and
// confirm the assertion we are about to trust sits inside what was actually signed.
//
// # Why this is not simply "the reference must equal the assertion ID"
//
// Both placements are legitimate and both are in production use:
//
//   - Assertion-level signature. The Reference points at the assertion. Most IdPs, and what Shibboleth emits.
//   - Response-level signature. The Reference points at the Response, and the assertion is an unsigned child of it.
//     ADFS does this by default, so an SP that insists on the first form cannot log anybody in against ADFS.
//
// An earlier version compared the reference against the assertion ID unconditionally, which rejected every
// Response-signed document. Accepting the second form safely requires the containment check below: the assertion must
// be a descendant of the signed element, so an assertion added outside it - the actual attack - is still refused.
func validateXSW(root *xmlNode, sig *xmlSignature, assertionID string) error {
	if sig == nil {
		return fmt.Errorf("saml: no signature for XSW check")
	}
	refURI := sig.SignedInfo.Reference.URI
	if refURI == "" {
		return fmt.Errorf("saml: signature has no Reference URI")
	}
	if !strings.HasPrefix(refURI, "#") {
		return fmt.Errorf("saml: Reference URI %q is not a local fragment", refURI)
	}
	signedID := refURI[1:]

	// Both IDs must be unambiguous. Duplicate IDs are how an attacker makes the verifier and the reader disagree
	// about which element a fragment names, so a document containing two is refused rather than resolved.
	if n := countElementsWithID(root, signedID); n != 1 {
		return fmt.Errorf("saml: XSW protection: found %d elements with signed ID %q (expected exactly 1)", n, signedID)
	}
	if signedID != assertionID {
		if n := countElementsWithID(root, assertionID); n != 1 {
			return fmt.Errorf("saml: XSW protection: found %d elements with assertion ID %q (expected exactly 1)", n, assertionID)
		}
	}

	signed := findElementByID(root, signedID)
	if signed == nil {
		return fmt.Errorf("saml: XSW protection: signed element %q not found", signedID)
	}

	// The direct case: the signature covers the assertion itself.
	if signedID == assertionID {
		return nil
	}

	// The enveloping case: the signature covers an ancestor, so the assertion has to be inside it.
	assertion := findElementByID(signed, assertionID)
	if assertion == nil {
		return fmt.Errorf(
			"saml: XSW protection: assertion %q is not inside the signed element %q, so its contents are not covered by the signature",
			assertionID, signedID)
	}

	// Only a Response may envelope an assertion. Anything else means the reference points somewhere unexpected.
	if !(signed.Space == nsSAMLProtocol && signed.Local == "Response") {
		return fmt.Errorf(
			"saml: XSW protection: signed element %q is <%s>, expected the Assertion itself or the enclosing Response",
			signedID, signed.Local)
	}

	return nil
}

// countElementsWithID counts how many elements in the tree have the given ID value.
func countElementsWithID(n *xmlNode, id string) int {
	if n.IsText {
		return 0
	}
	count := 0
	for _, a := range n.Attrs {
		if (a.Name.Local == "ID" || a.Name.Local == "Id" || a.Name.Local == "id") && a.Value == id {
			count++
			break
		}
	}
	for _, c := range n.Children {
		count += countElementsWithID(c, id)
	}
	return count
}
