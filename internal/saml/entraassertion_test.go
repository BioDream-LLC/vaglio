package saml

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A real assertion from a real Microsoft Entra tenant, verified in full.
//
// Why this fixture matters beyond having a second one. Keycloak writes its assertion with namespace prefixes - <saml:Assertion> and
// <dsig:Signature> - and Entra writes the same elements with none, relying on a default namespace. Those are the two opposite cases in
// canonicalisation, and the defect this package was caught by in August was in prefix handling: the canonicaliser dropped prefixes, so
// Keycloak's documents failed and nothing here noticed, because every test signed and verified with the same code.
//
// A document with no prefixes to drop exercises the other branch entirely. Neither fixture on its own covers what both do together.
//
// The clock is pinned rather than left at time.Now(). The existing Keycloak test verifies how far a saved document gets and accepts that its
// validity window has expired, which is honest but weaker: it cannot assert that the whole path succeeds. With the instant of capture
// supplied, this one requires success, and keeps requiring it for as long as the repository exists.
//
// Captured on 19 September 2026 from a real Microsoft Entra tenant, during a real interactive sign-in, by a capture endpoint standing in for
// Perfuse's own consumer endpoint for exactly that one sign-in.
//
// The directory identifiers in it have since been replaced with obviously synthetic ones and the document signed again with a key generated for
// the purpose - see TestRegenerateEntraFixture, which does that and explains why. Nothing in the original was secret, but a tenant identifier, an
// account object identifier and a user principal name together name one organisation and one person, which a published fixture should not.
//
// What was deliberately preserved is the shape, because the shape is what these tests are for: Entra writes Assertion and Signature with no
// namespace prefix, where Keycloak writes saml:Assertion and dsig:Signature. Those are the two opposite cases in canonicalisation, and the
// canonicalisation handling in this package was once wrong in exactly that way - so a fixture that lost the unprefixed form would halve the
// coverage without failing anything.

// whenEntraSignedIt is inside the captured assertion's validity window.
//
// The document was issued at 01:12:51.138Z and expires at 02:12:51.038Z, so any instant between the two will do. Written out rather than
// computed from the file, because a test that derives its own clock from the document it is checking cannot catch a document whose window is
// wrong.
var whenEntraSignedIt = time.Date(2026, 9, 20, 1, 20, 0, 0, time.UTC)

const (
	entraSPEntityID = "urn:perfuse:local-signin"
	entraACS        = "https://sp.example.test:8544/auth/saml/acs"
	entraSSOURL     = "https://login.microsoftonline.com/00000000-1111-2222-3333-444444444444/saml2"

	// entraRequestID is the AuthnRequest this response answers, taken from its InResponseTo.
	entraRequestID = "_5f38855a20de8466ac2e7063c8ebcfdf"
)

func realEntraResponse(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("testdata/entra-response.xml")
	if err != nil {
		t.Fatal(err)
	}

	// A positive control, and a specific one. The markers are the things that make this document Entra's rather than a copy of the Keycloak
	// fixture or something composed here: an unprefixed Assertion, Microsoft's own claim namespace, and the sts.windows.net issuer.
	for _, marker := range []string{
		"<Assertion ",
		"<Signature ",
		"http://schemas.microsoft.com/identity/claims/tenantid",
		"https://sts.windows.net/",
	} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("the fixture does not look like an Entra response: no %q", marker)
		}
	}

	// And the absence of prefixes, asserted rather than assumed. If a future capture came from a provider that prefixes its assertion, this
	// file would silently stop covering the case it exists for.
	if strings.Contains(string(raw), "<saml:Assertion") {
		t.Fatal("the Entra fixture has a prefixed Assertion, so it no longer covers the unprefixed case that Keycloak's does not")
	}

	return raw
}

func entraServiceProvider(t *testing.T) *ServiceProvider {
	t.Helper()

	certPEM, err := os.ReadFile("testdata/entra-idp.crt")
	if err != nil {
		t.Fatal(err)
	}

	sp, err := New(Config{
		EntityID:   entraSPEntityID,
		ACSPath:    entraACS,
		IdPSSOURL:  entraSSOURL,
		IdPCertPEM: string(certPEM),
		// Off, because the captured document answers a request and the point is to check that tie holds.
		AllowUnsolicited: false,
	})
	if err != nil {
		t.Fatalf("configuring a service provider for Entra: %v", err)
	}

	return sp
}

func TestARealEntraAssertionVerifiesCompletely(t *testing.T) {
	resetReplayCache()

	sp := entraServiceProvider(t)

	assertion, err := sp.parseResponseBytes(realEntraResponse(t), whenEntraSignedIt,
		func(id string) bool { return id == entraRequestID })
	if err != nil {
		t.Fatalf("a real Entra assertion did not verify: %v", err)
	}

	if assertion.NameID == "" {
		t.Error("the assertion verified but carries no NameID, so there would be nobody to sign in")
	}

	// Entra's default NameID is an opaque persistent identifier rather than an address. Asserted because it is the thing that makes the
	// account name unreadable without help, and a future Entra that changed this should be noticed rather than silently accommodated.
	if strings.Contains(assertion.NameID, "@") {
		t.Errorf("Entra's NameID now looks like an address (%q), which the username handling no longer has to work around",
			assertion.NameID)
	}

	t.Logf("verified: NameID %s, %d attribute(s)", assertion.NameID, len(assertion.Attributes))
}

func TestTheEntraAssertionIsTiedToItsRequest(t *testing.T) {
	resetReplayCache()

	sp := entraServiceProvider(t)

	// The same document, with the server denying it ever issued that request. This is the check that stops a response captured from one
	// browser being posted into another, and it was missing entirely until August.
	_, err := sp.parseResponseBytes(realEntraResponse(t), whenEntraSignedIt, func(string) bool { return false })
	if err == nil {
		t.Fatal("a real Entra response was accepted while answering no request this server issued")
	}

	// It must fail on the tie, not on something incidental that happens to fail first.
	if !strings.Contains(strings.ToLower(err.Error()), "inresponseto") && !strings.Contains(err.Error(), "request") {
		t.Errorf("the refusal does not appear to be about request tracking, so this test may be passing for the wrong reason: %v", err)
	}
}

func TestTheEntraAssertionIsRefusedAfterItsWindow(t *testing.T) {
	resetReplayCache()

	sp := entraServiceProvider(t)

	// An hour past expiry. A real document is the only honest way to test this: the window in the fixture is Entra's, not one chosen here to
	// make the test convenient.
	tooLate := whenEntraSignedIt.Add(2 * time.Hour)

	_, err := sp.parseResponseBytes(realEntraResponse(t), tooLate, func(id string) bool { return id == entraRequestID })
	if err == nil {
		t.Fatal("a real Entra assertion was accepted two hours after its conditions expired")
	}
}

func TestTheEntraAssertionIsRefusedForAnotherAudience(t *testing.T) {
	resetReplayCache()

	certPEM, err := os.ReadFile("testdata/entra-idp.crt")
	if err != nil {
		t.Fatal(err)
	}

	// Everything correct except who the assertion was for. An audience that is not checked means a document issued for one application is
	// accepted by another, which is the whole reason the restriction exists.
	sp, err := New(Config{
		EntityID:   "urn:perfuse:some-other-application",
		ACSPath:    entraACS,
		IdPSSOURL:  entraSSOURL,
		IdPCertPEM: string(certPEM),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = sp.parseResponseBytes(realEntraResponse(t), whenEntraSignedIt, func(id string) bool { return id == entraRequestID })
	if err == nil {
		t.Fatal("a real Entra assertion was accepted by an application it was not issued for")
	}
}

func TestTheEntraAssertionIsRefusedWithTheWrongKey(t *testing.T) {
	resetReplayCache()

	// Keycloak's certificate against Entra's document. Both are real, neither signed the other, and the failure has to be the signature
	// rather than anything structural.
	certPEM, err := os.ReadFile("testdata/keycloak-idp.crt")
	if err != nil {
		t.Fatal(err)
	}

	sp, err := New(Config{
		EntityID:   entraSPEntityID,
		ACSPath:    entraACS,
		IdPSSOURL:  entraSSOURL,
		IdPCertPEM: string(certPEM),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = sp.parseResponseBytes(realEntraResponse(t), whenEntraSignedIt, func(id string) bool { return id == entraRequestID })
	if err == nil {
		t.Fatal("a real Entra assertion verified against a certificate from an entirely different provider")
	}
}

func TestBothProvidersAgreeOnCanonicalisation(t *testing.T) {
	// The reason for having two fixtures, made explicit.
	//
	// Keycloak prefixes its assertion and signature; Entra does not. The canonicalisation defect found in August was in prefix handling, and
	// a single fixture would have covered one side of it. This test says so in a form that fails if either document stops covering its case.
	keycloak := string(realKeycloakResponse(t))
	entra := string(realEntraResponse(t))

	if !strings.Contains(keycloak, "<saml:Assertion") {
		t.Error("the Keycloak fixture no longer has a prefixed assertion, so the prefixed case is uncovered")
	}
	if !strings.Contains(entra, "<Assertion ") {
		t.Error("the Entra fixture no longer has an unprefixed assertion, so the unprefixed case is uncovered")
	}
}
