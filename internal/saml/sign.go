package saml

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// SignDocument produces an XML signature over a document, for testing and for standing in as an identity provider.
//
// Exported reluctantly and for one reason: producing a valid XML signature requires the same canonicalisation the verifier uses, and
// that is unexported. Anything outside this package that needs to build a signed response - the API layer's sign-in tests, a local
// identity provider used to try the flow by hand - would otherwise have to reimplement exclusive canonicalisation, and a second
// implementation would drift from the first. When it drifted, the tests would go on passing while agreeing about the wrong bytes.
//
// bodyWithPlaceholder must contain the literal {SIGNATURE} where the signature belongs. The digest is taken over the document with
// that placeholder removed, which is what the enveloped-signature transform means.
//
// This signs. It verifies nothing and grants nothing, so it is not a way around any check in this package: a document signed with a
// key the service provider is not configured to trust is rejected exactly as before.
func SignDocument(bodyWithPlaceholder, refID string, key *rsa.PrivateKey) (string, error) {
	return signDocument(bodyWithPlaceholder, refID, key, "ds")
}

// signDocument signs with a chosen namespace prefix, or with none.
//
// The prefix is a parameter because it is the one part of a signature that this package has been wrong about before. Providers disagree: Keycloak
// writes ds:Signature and saml:Assertion, Microsoft Entra writes Signature and Assertion with no prefix at all, and ADFS has been seen doing
// both. Canonicalisation has to produce the same bytes either way, and the defect found in August was in exactly that handling.
//
// Being able to emit both shapes means the test fixtures can cover both without depending on having captured a document from each vendor. Pass ""
// for the unprefixed form, where the namespace is declared as the default instead.
func signDocument(bodyWithPlaceholder, refID string, key *rsa.PrivateKey, prefix string) (string, error) {
	if key == nil {
		return "", fmt.Errorf("saml: no signing key")
	}

	if !strings.Contains(bodyWithPlaceholder, "{SIGNATURE}") {
		return "", fmt.Errorf("saml: the document has no {SIGNATURE} placeholder")
	}

	forDigest := strings.Replace(bodyWithPlaceholder, "{SIGNATURE}", "", 1)

	tree, err := parseToTree([]byte(forDigest))
	if err != nil {
		return "", fmt.Errorf("saml: parse for digest: %w", err)
	}

	digest := sha256.Sum256(canonicalizeNode(tree, nil))

	// q qualifies an element name with the chosen prefix, or leaves it bare.
	q := func(name string) string {
		if prefix == "" {
			return name
		}

		return prefix + ":" + name
	}

	// The namespace declaration that goes with it. Unprefixed means the default namespace, which is what Entra writes.
	nsDecl := `xmlns="http://www.w3.org/2000/09/xmldsig#"`
	if prefix != "" {
		nsDecl = `xmlns:` + prefix + `="http://www.w3.org/2000/09/xmldsig#"`
	}

	signedInfoXML := fmt.Sprintf(
		`<%s %s>`+
			`<%s Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`<%s Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/>`+
			`<%s URI="#%s">`+
			`<%s>`+
			`<%s Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"/>`+
			`<%s Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"/>`+
			`</%s>`+
			`<%s Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>`+
			`<%s>%s</%s>`+
			`</%s>`+
			`</%s>`,
		q("SignedInfo"), nsDecl,
		q("CanonicalizationMethod"),
		q("SignatureMethod"),
		q("Reference"), refID,
		q("Transforms"),
		q("Transform"),
		q("Transform"),
		q("Transforms"),
		q("DigestMethod"),
		q("DigestValue"), base64.StdEncoding.EncodeToString(digest[:]), q("DigestValue"),
		q("Reference"),
		q("SignedInfo"),
	)

	siTree, err := parseToTree([]byte(signedInfoXML))
	if err != nil {
		return "", fmt.Errorf("saml: parse SignedInfo: %w", err)
	}

	siDigest := sha256.Sum256(canonicalizeNode(siTree, nil))

	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, siDigest[:])
	if err != nil {
		return "", fmt.Errorf("saml: sign: %w", err)
	}

	// The SignedInfo already carries the namespace declaration, so it is not repeated on the Signature element when a prefix is in use. In the
	// unprefixed form it has to be declared on Signature, because a default namespace does not inherit upwards from a child.
	return fmt.Sprintf(
		`<%s %s>%s<%s>%s</%s></%s>`,
		q("Signature"), nsDecl,
		signedInfoXML,
		q("SignatureValue"), base64.StdEncoding.EncodeToString(sigBytes), q("SignatureValue"),
		q("Signature"),
	), nil
}
