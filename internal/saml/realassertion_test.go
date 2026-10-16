package saml

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

// A real assertion, from a real Keycloak, kept as a fixture.
//
// This file exists because every other test in this package signed with the same code it verified with, and self-agreement is not
// evidence. When the first genuine Keycloak assertion arrived it failed outright: the canonical form said <Assertion xmlns="..."> where
// Keycloak had written <saml:Assertion ...>, so the digest never matched and no real identity provider could ever have signed anybody
// in. Five canonicalisation tests passed throughout, because both halves of each was this package.
//
// The fixture is a document nothing here produced, which is the whole point of keeping it. It also means the regression can be caught
// without a container runtime: verifying against a live Keycloak needs Docker, and a check that needs Docker is a check that stops
// being run.
//
// It carries no real data. The realm is created by scripts/keycloak-saml-setup.sh with an invented user at a .test domain, and the
// signing key is generated fresh for that realm.

func realKeycloakResponse(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("testdata/keycloak-assertion.xml")
	if err != nil {
		t.Fatal(err)
	}

	// A positive control: if the fixture were truncated or replaced with something this package generated, everything below would
	// pass while proving nothing about a real provider.
	for _, marker := range []string{"dsig:Signature", "saml:Assertion", "samlp:Response"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("the fixture does not look like a Keycloak response: no %q", marker)
		}
	}

	return raw
}

func TestTheCanonicalFormOfARealAssertionMatchesTheDigestItWasSignedWith(t *testing.T) {
	// The assertion that would have caught the defect on the day it was written.
	//
	// A digest is a claim about bytes, so this compares the bytes: the digest Keycloak put in the document against the one computed
	// from the document. Nothing about signatures or certificates is involved - the failure was upstream of both, in reproducing the
	// canonical form the signer used.
	raw := realKeycloakResponse(t)

	root, err := parseToTree(raw)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := decodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(resp.Assertions) == 0 {
		t.Fatal("no assertion in the fixture")
	}

	sig := resp.Assertions[0].Signature
	if sig == nil {
		sig = resp.Signature
	}

	if sig == nil {
		t.Fatal("the fixture carries no signature")
	}

	refNode, err := resolveSignatureReference(root, sig.SignedInfo.Reference.URI)
	if err != nil {
		t.Fatal(err)
	}

	canon := canonicalizeNode(removeSignature(refNode), nil)
	sum := sha256.Sum256(canon)
	got := base64.StdEncoding.EncodeToString(sum[:])

	if got != sig.SignedInfo.Reference.DigestValue {
		head := string(canon)
		if len(head) > 200 {
			head = head[:200]
		}

		t.Errorf("the canonical form does not match what Keycloak signed.\n  document: %s\n  computed: %s\n  ours starts: %s",
			sig.SignedInfo.Reference.DigestValue, got, head)
	}
}

func TestAPrefixedElementKeepsItsPrefixInTheCanonicalForm(t *testing.T) {
	// The specific defect, stated as a property rather than as a digest.
	//
	// Keycloak writes the assertion as saml:Assertion and declares xmlns:saml on the root, while also declaring the same URI as the
	// element's default namespace. Reverse-mapping the URI to a prefix found the default one and produced <Assertion xmlns="...">,
	// which is a different document. Worse, the lookup ranged over a Go map, so which prefix it found was not deterministic - the
	// same assertion could have verified on one attempt and failed on the next.
	raw := realKeycloakResponse(t)

	root, err := parseToTree(raw)
	if err != nil {
		t.Fatal(err)
	}

	assertion := findElement(root, nsSAMLAssertion, "Assertion")
	if assertion == nil {
		t.Fatal("no assertion element")
	}

	if assertion.Prefix != "saml" {
		t.Fatalf("the assertion's prefix is %q, want saml: the prefix the document wrote is not being recorded", assertion.Prefix)
	}

	canon := string(canonicalizeNode(removeSignature(assertion), nil))

	if !strings.HasPrefix(canon, "<saml:Assertion ") {
		t.Errorf("the canonical form drops the prefix, so it is a different document from the one signed: %s", canon[:120])
	}

	// The inherited declaration has to be emitted, because the prefix is visibly utilised and the ancestor that declared it is not
	// part of the signed subtree. Leaving it out produces XML that is not well-formed on its own.
	if !strings.Contains(canon[:200], `xmlns:saml="`+nsSAMLAssertion+`"`) {
		t.Errorf("the inherited namespace declaration is missing, so the canonical subtree is not well-formed: %s", canon[:200])
	}
}

func TestCanonicalisingTheSameDocumentTwiceGivesTheSameBytes(t *testing.T) {
	// Determinism, checked repeatedly rather than once, because the bug it guards against was a map range.
	//
	// A canonicaliser that is merely usually right is worse than one that is consistently wrong: the failure appears as an identity
	// provider that works until it doesn't, and nothing in the document changed between the two attempts.
	raw := realKeycloakResponse(t)

	var first string

	for i := range 25 {
		root, err := parseToTree(raw)
		if err != nil {
			t.Fatal(err)
		}

		assertion := findElement(root, nsSAMLAssertion, "Assertion")
		got := string(canonicalizeNode(removeSignature(assertion), nil))

		if i == 0 {
			first = got

			continue
		}

		if got != first {
			t.Fatalf("attempt %d produced different bytes from the first, so canonicalisation is not deterministic", i+1)
		}
	}
}

func TestARealAssertionVerifiesEndToEnd(t *testing.T) {
	// The whole path over a real document: signature, cross-referencing, conditions. Conditions are long expired in a saved fixture,
	// so this checks how far it gets rather than that it succeeds - which is honest, and still catches the failure that mattered.
	resetReplayCache()

	raw := realKeycloakResponse(t)

	certPEM, err := os.ReadFile("testdata/keycloak-idp.crt")
	if err != nil {
		t.Skip("no captured certificate")
	}

	sp, err := New(Config{
		EntityID:         "https://perfuse.test",
		ACSPath:          "http://127.0.0.1:8099/auth/saml/acs",
		IdPSSOURL:        "http://127.0.0.1:8080/realms/perfuse/protocol/saml",
		IdPCertPEM:       string(certPEM),
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The captured response answers the request that was outstanding when it was captured, so the id is recognised here. Passing nil
	// instead would fail on request tracking before reaching the signature, which is the wrong thing to be testing.
	_, err = sp.parseResponseBytes(raw, time.Now(), func(string) bool { return true })

	// Expected to fail on the validity window, and only on that. Anything else means the document is not being read correctly, and
	// the message says which - a signature failure here is the regression this file exists to catch.
	if err == nil {
		t.Log("the fixture still validates, which means its conditions have not yet expired")

		return
	}

	if strings.Contains(err.Error(), "digest") || strings.Contains(err.Error(), "verification failed") {
		t.Errorf("a real assertion failed its signature check: %v", err)
	}

	if !strings.Contains(err.Error(), "NotOnOrAfter") && !strings.Contains(err.Error(), "expired") &&
		!strings.Contains(err.Error(), "no longer") {
		t.Errorf("the fixture failed for an unexpected reason: %v", err)
	}
}
