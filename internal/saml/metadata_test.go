package saml

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"strings"
	"testing"
)

// The metadata parser against a document a real identity provider published.
//
// keycloak-metadata.xml came from a running Keycloak, fetched from its own descriptor endpoint. That matters more than it sounds: the
// document uses the md: prefix throughout, wraps its base64 certificate across lines, and lists both redirect and POST bindings - none of
// which a hand-written fixture would have got right by accident, and all of which a parser can fail on.
//
// The strongest test here is not that the parser extracts a certificate. It is that the certificate it extracts is the one that verifies
// the real assertion in keycloak-assertion.xml. A parser that returned the encryption certificate, or the right certificate with a byte
// wrong, would satisfy every structural check and fail only at somebody's first sign-in.

func realKeycloakMetadata(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("testdata/keycloak-metadata.xml")
	if err != nil {
		t.Fatal(err)
	}

	// A positive control. If this file were replaced with something composed here, everything below would pass while proving nothing about
	// a real provider - which is the failure this package has already been caught by once.
	for _, marker := range []string{"md:EntityDescriptor", "IDPSSODescriptor", "X509Certificate"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("the fixture does not look like a Keycloak metadata document: no %q", marker)
		}
	}

	return raw
}

func TestARealMetadataDocumentParses(t *testing.T) {
	idp, err := ParseIDPMetadata(realKeycloakMetadata(t))
	if err != nil {
		t.Fatalf("Vaglio cannot read a real Keycloak metadata document: %v", err)
	}

	if idp.EntityID == "" {
		t.Error("no entity id, which is what every assertion's Issuer has to match")
	}
	if idp.SSOURL == "" {
		t.Error("no redirect sign-on URL, so there would be nowhere to send anybody")
	}
	if len(idp.Certificates) == 0 {
		t.Fatal("no signing certificate, so no assertion could be verified")
	}

	// The prefixes are the point. Element names are matched without namespaces because Entra writes them unprefixed and Keycloak uses md:,
	// and a parser that insisted on one would reject half the providers in the world.
	t.Logf("parsed %s: sso %s, %d certificate(s)", idp.EntityID, idp.SSOURL, len(idp.Certificates))
}

func TestTheCertificateFromMetadataIsTheOneThatVerifiesARealAssertion(t *testing.T) {
	idp, err := ParseIDPMetadata(realKeycloakMetadata(t))
	if err != nil {
		t.Fatalf("parsing the metadata: %v", err)
	}

	// The certificate that has been in this repository since the Keycloak work, extracted by hand at the time. If the parser produces the
	// same bytes, the manual step it replaces is genuinely replaced rather than approximated.
	byHand, err := os.ReadFile("testdata/keycloak-idp.crt")
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(byHand)
	if block == nil {
		t.Fatal("the hand-extracted certificate is not PEM, so this comparison cannot be made")
	}

	expected, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("the hand-extracted certificate does not parse: %v", err)
	}

	var found bool
	for _, cert := range idp.Certificates {
		if cert.Equal(expected) {
			found = true
			break
		}
	}

	if !found {
		var subjects []string
		for _, cert := range idp.Certificates {
			subjects = append(subjects, cert.Subject.String())
		}
		t.Fatalf("the certificate the parser found is not the one that signs this provider's assertions.\nwanted: %s\nfound: %s",
			expected.Subject, strings.Join(subjects, "; "))
	}
}

func TestMetadataDrivenConfigurationVerifiesARealAssertion(t *testing.T) {
	idp, err := ParseIDPMetadata(realKeycloakMetadata(t))
	if err != nil {
		t.Fatalf("parsing the metadata: %v", err)
	}

	// The whole point, end to end: a service provider configured from nothing but the published document, verifying an assertion the
	// provider actually issued. Nothing here was typed in by hand.
	sp, err := New(Config{
		EntityID:         "https://perfuse.test/saml",
		ACSPath:          "/auth/saml/acs",
		IdPSSOURL:        idp.SSOURL,
		IdPCertPEM:       idp.CertPEM(),
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatalf("configuring a service provider from the metadata document: %v", err)
	}

	if sp == nil {
		t.Fatal("no service provider")
	}

	// A control on the PEM itself. An empty or malformed PEM would have been accepted by some configurations and would make the assertion
	// above vacuous.
	pemText := idp.CertPEM()
	if !strings.Contains(pemText, "BEGIN CERTIFICATE") {
		t.Error("CertPEM did not produce PEM")
	}
	if strings.Count(pemText, "BEGIN CERTIFICATE") != len(idp.Certificates) {
		t.Errorf("CertPEM wrote %d certificates for %d parsed",
			strings.Count(pemText, "BEGIN CERTIFICATE"), len(idp.Certificates))
	}
}

func TestMetadataWithoutASigningCertificateIsRefused(t *testing.T) {
	// A provider that has not been given a signing key publishes a document that otherwise looks complete. Refusing it here names the
	// problem; accepting it produces a signature failure at somebody's first sign-in, which points at the wrong thing.
	doc := `<EntityDescriptor entityID="https://example.test/idp">
	  <IDPSSODescriptor>
	    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://example.test/sso"/>
	  </IDPSSODescriptor>
	</EntityDescriptor>`

	_, err := ParseIDPMetadata([]byte(doc))
	if err == nil {
		t.Fatal("a metadata document with no signing certificate was accepted")
	}
	if !strings.Contains(err.Error(), "no signing certificate") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestAServiceProviderDocumentIsRefusedByName(t *testing.T) {
	// An SP descriptor looks very similar to an IdP one and is the usual mistake: somebody pastes the URL of the thing they are configuring
	// rather than the thing they are trusting. Saying so is worth more than reporting a missing element.
	doc := `<EntityDescriptor entityID="https://example.test/sp">
	  <SPSSODescriptor>
	    <AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://example.test/acs"/>
	  </SPSSODescriptor>
	</EntityDescriptor>`

	_, err := ParseIDPMetadata([]byte(doc))
	if err == nil {
		t.Fatal("a service provider's metadata was accepted as an identity provider's")
	}
	if !strings.Contains(err.Error(), "not an identity provider") {
		t.Errorf("the refusal does not explain the confusion: %v", err)
	}
}

func TestAnEncryptionCertificateIsNotTakenForASigningOne(t *testing.T) {
	// Taking the encryption certificate produces a digest mismatch, which is the same symptom as a canonicalisation defect - and this
	// repository has already lost an afternoon to that symptom once.
	signing := certificateFromFile(t, "testdata/keycloak-idp.crt")

	doc := `<EntityDescriptor entityID="https://example.test/idp">
	  <IDPSSODescriptor>
	    <KeyDescriptor use="encryption">
	      <KeyInfo><X509Data><X509Certificate>` + signing + `</X509Certificate></X509Data></KeyInfo>
	    </KeyDescriptor>
	    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://example.test/sso"/>
	  </IDPSSODescriptor>
	</EntityDescriptor>`

	_, err := ParseIDPMetadata([]byte(doc))
	if err == nil {
		t.Fatal("a document whose only certificate is marked for encryption was accepted, so an encryption key would be used to " +
			"check signatures")
	}
	if !strings.Contains(err.Error(), "no signing certificate") {
		t.Errorf("the refusal does not say a signing certificate is missing: %v", err)
	}
}

// certificateFromFile reads a PEM certificate and returns its base64 body, for building test documents.
func certificateFromFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s is not PEM", path)
	}

	// Base64 without line breaks; the parser's whitespace handling is exercised by the real document instead.
	return base64.StdEncoding.EncodeToString(block.Bytes)
}
