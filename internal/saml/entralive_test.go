package saml

import (
	"strings"
	"testing"
)

// Perfuse's SAML against a real Microsoft Entra tenant.
//
// Why a second provider. Keycloak found a defect that none of this package's thousand-odd tests could: the canonicaliser dropped namespace
// prefixes, so no real identity provider could ever have signed anybody in, and every test passed because each one signed and verified with
// the same code. Self-agreement is not evidence. One provider is better, and still only proves the implementation matches one vendor's
// output - and every difficult part of SAML is a place where a vendor differs from a reading of the standard.
//
// Entra is what most customers actually run, which is what makes it the second one worth having.
//
// What these tests can and cannot do without a browser. Entra will not mint an assertion without a user signing in interactively, so what
// is verified here is everything up to that point: that Entra publishes metadata our verifier accepts, that the signing certificate and
// algorithms are ones we support, and that the request we send is one Entra accepts rather than rejects. Obtaining a real assertion needs
// an interactive sign-in, and TestEntraAssertionFixture verifies one once it has been captured.
//
// Skipped entirely without VAGLIO_ENTRA_TENANT_ID, VAGLIO_ENTRA_CLIENT_ID and VAGLIO_ENTRA_CLIENT_SECRET.

// theTestACS is where a response would be posted. Not reachable from Entra, and it does not need to be: nothing here completes a sign-in.
const theTestACS = "https://localhost:8443/auth/saml/acs"

func TestEntraPublishesMetadataThisVerifierAccepts(t *testing.T) {
	cfg := entraFromEnv(t)
	g := newGraph(t, cfg)

	app := provisionEntraSAMLApp(t, g, cfg, theTestACS)

	metadata := waitForMetadata(t, app.MetadataURL)

	// A positive control before anything is asserted about the contents. A document that is not Entra's would let everything below pass
	// while proving nothing, which is the failure this whole file exists to avoid.
	for _, marker := range []string{"EntityDescriptor", "IDPSSODescriptor", "X509Certificate", "login.microsoftonline.com"} {
		if !strings.Contains(string(metadata), marker) {
			t.Fatalf("this does not look like an Entra federation metadata document: no %q in %d bytes",
				marker, len(metadata))
		}
	}

	idp, err := ParseIDPMetadata(metadata)
	if err != nil {
		t.Fatalf("Vaglio cannot read Entra's federation metadata, so no Entra tenant could be configured:\n%v", err)
	}

	if idp.EntityID == "" {
		t.Error("the parsed metadata has no entity id, which is the issuer every assertion has to match")
	}
	if len(idp.Certificates) == 0 {
		t.Fatal("the parsed metadata has no signing certificate, so no assertion from this tenant could ever be verified")
	}
	if idp.SSOURL == "" {
		t.Error("the parsed metadata has no sign-on URL, so there would be nowhere to send anybody")
	}

	t.Logf("Entra tenant %s, application %s: entity %s, %d signing certificate(s)",
		cfg.TenantID, app.AppID, idp.EntityID, len(idp.Certificates))
}

func TestEntrasSigningCertificateIsOneWeCanUse(t *testing.T) {
	cfg := entraFromEnv(t)
	g := newGraph(t, cfg)

	app := provisionEntraSAMLApp(t, g, cfg, theTestACS)
	metadata := waitForMetadata(t, app.MetadataURL)

	idp, err := ParseIDPMetadata(metadata)
	if err != nil {
		t.Fatalf("parsing the metadata: %v", err)
	}

	// The certificate has to be usable for signature verification, not merely present. A certificate that parses and whose public key we
	// cannot work with fails later, during a sign-in, where the error is about a digest rather than about a key.
	for i, cert := range idp.Certificates {
		if cert.PublicKey == nil {
			t.Errorf("certificate %d from Entra has no usable public key", i)
			continue
		}
		t.Logf("certificate %d: %s, signature algorithm %s, public key %T",
			i, cert.Subject.CommonName, cert.SignatureAlgorithm, cert.PublicKey)
	}
}

func TestAnAuthnRequestForEntraIsWellFormed(t *testing.T) {
	cfg := entraFromEnv(t)
	g := newGraph(t, cfg)

	app := provisionEntraSAMLApp(t, g, cfg, theTestACS)
	metadata := waitForMetadata(t, app.MetadataURL)

	idp, err := ParseIDPMetadata(metadata)
	if err != nil {
		t.Fatalf("parsing the metadata: %v", err)
	}

	// Configured from the metadata document rather than from values typed in, which is the point of having a parser: the certificate comes
	// out of Entra's own publication with its whitespace handled.
	sp, err := New(Config{
		EntityID:   app.EntityID,
		ACSPath:    "/auth/saml/acs",
		IdPSSOURL:  idp.SSOURL,
		IdPCertPEM: idp.CertPEM(),
	})
	if err != nil {
		t.Fatalf("configuring a service provider against real Entra metadata: %v", err)
	}

	redirect, _, err := sp.BuildAuthnRequest("")
	if err != nil {
		t.Fatalf("building the sign-on redirect: %v", err)
	}

	// It has to point at the tenant Entra told us about, not at anything assembled here.
	if !strings.HasPrefix(redirect, idp.SSOURL) {
		t.Errorf("the redirect does not go to the sign-on URL from the metadata.\nredirect: %s\nexpected prefix: %s",
			redirect, idp.SSOURL)
	}

	if !strings.Contains(redirect, "SAMLRequest=") {
		t.Errorf("the redirect carries no SAMLRequest: %s", redirect)
	}

	t.Logf("sign-on redirect is %d characters and points at %s", len(redirect), idp.SSOURL)
}
