package saml

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Regenerating the Entra fixture without the identifiers of the tenant it came from.
//
// Why this exists rather than the captured document being kept as it arrived. A real assertion names the directory that issued it: the tenant
// identifier, the object identifier of the account, and a user principal name containing the domain. None of that is secret in the cryptographic
// sense, and all of it identifies a specific organisation and person, which a published test fixture should not.
//
// The document cannot simply be edited, because it is signed. Changing one character of it invalidates the signature, and the signature is the
// entire point: five tests use this fixture to check that verification succeeds against a document written the way Microsoft writes one, with no
// namespace prefixes on Assertion or Signature. Keycloak's fixture covers the opposite case. Those are the two sides of the canonicalisation
// handling that was once wrong here, so losing either halves the coverage.
//
// So the values are replaced and the document is signed again with a key generated here. What is preserved is the thing the tests rely on: the
// shape. Element and attribute order, the absence of prefixes, the placement of the signature inside the assertion, and the claim URIs are all as
// Entra produced them. What changes is the identifiers and the key.
//
// Skipped unless VAGLIO_REGEN_FIXTURES is set, because it rewrites files in testdata. Run it with:
//
//	VAGLIO_REGEN_FIXTURES=1 go test ./internal/saml/ -run TestRegenerateEntraFixture
//
// The check that this worked is not inside this test. It is that the five tests in entraassertion_test.go still pass afterwards - the verifier is
// the oracle, and a fixture it accepts is a fixture that exercises the path.
func TestRegenerateEntraFixture(t *testing.T) {
	if os.Getenv("VAGLIO_REGEN_FIXTURES") == "" {
		t.Skip("set VAGLIO_REGEN_FIXTURES=1 to rewrite the Entra fixture")
	}

	raw, err := os.ReadFile("testdata/entra-response.xml")
	if err != nil {
		t.Fatal(err)
	}

	doc := string(raw)

	// The scrub works by shape rather than by listing the values it removes.
	//
	// The first version of this named the real tenant identifier, account identifier and domain as literals, which meant the file whose purpose is
	// to remove those values from the repository contained all three of them. Matching structurally instead - any GUID, any onmicrosoft.com
	// domain - removes them without recording them, and has the side benefit of working on any capture rather than only on one.
	//
	// Each substitution asserts it matched something. A scrub that silently finds nothing and reports success is the exact failure this project
	// keeps finding in other people's software.

	guid := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

	// Document and assertion identifiers are left alone: the signature references the assertion by its identifier, and they name nothing outside
	// this file. Only GUIDs that appear as directory identifiers are replaced, which are the ones inside claim values and issuer URLs.
	replaced := map[string]string{}
	isSynthetic := map[string]struct{}{}
	synthetic := []string{
		"00000000-1111-2222-3333-444444444444",
		"55555555-6666-7777-8888-999999999999",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}

	// Every GUID that follows one of these markers, not just the first.
	//
	// The first version of this searched for the marker with strings.Index, which always returns the earliest occurrence - so once that one had
	// been replaced with a synthetic value the loop stopped, and every later claim was left untouched. It missed the account object identifier and
	// reported success. Found by checking the output rather than by reading the loop, which is the only reason it was found at all.
	for _, part := range []string{"sts.windows.net/", "login.microsoftonline.com/", "<AttributeValue>"} {
		offset := 0
		for {
			i := strings.Index(doc[offset:], part)
			if i < 0 {
				break
			}
			at := offset + i + len(part)
			offset = at

			found := guid.FindString(doc[at:])
			if found == "" {
				continue
			}

			// Only a GUID that begins immediately here, or the search would reach across into an unrelated part of the document.
			if !strings.HasPrefix(doc[at:], found) {
				continue
			}

			if _, already := isSynthetic[found]; already {
				continue
			}

			to, seen := replaced[found]
			if !seen {
				if len(replaced) >= len(synthetic) {
					t.Fatalf("more distinct directory identifiers than synthetic replacements prepared")
				}
				to = synthetic[len(replaced)]
				replaced[found] = to
				isSynthetic[to] = struct{}{}
			}
			doc = strings.ReplaceAll(doc, found, to)
		}
	}

	if len(replaced) == 0 {
		t.Fatal("no directory identifiers were found to replace, so this scrub is not doing what it says")
	}

	// Any onmicrosoft.com tenant domain, which is what carries a name into a user principal name.
	domain := regexp.MustCompile(`[A-Za-z0-9-]+\.onmicrosoft\.com`)
	if m := domain.FindAllString(doc, -1); len(m) == 0 {
		t.Fatal("no tenant domain found, so the fixture is not shaped as expected")
	}
	doc = domain.ReplaceAllString(doc, "example.onmicrosoft.com")

	// Private addresses, which name the machine the capture was taken on.
	//
	// The consumer endpoint in a captured assertion is whatever address the sign-in was actually driven against, which during development is
	// somebody's own machine on their own network. It appears in the Recipient and Destination attributes and in the audience, so it cannot be left
	// as it arrived.
	privateAddr := regexp.MustCompile(`(?:10|127)\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(?:1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}`)
	if m := privateAddr.FindAllString(doc, -1); len(m) > 0 {
		doc = privateAddr.ReplaceAllString(doc, "sp.example.test")
	}

	// The NameID is opaque and identifies nothing by itself, but it was minted by a real directory for a real account, so it goes too. The shape
	// is kept - same length, same alphabet - because a fixture whose NameID looked unlike a real one would be a worse example.
	nameID := regexp.MustCompile(`(<NameID Format="[^"]*">)([^<]+)(</NameID>)`)
	if !nameID.MatchString(doc) {
		t.Fatal("no NameID found, so the fixture is not shaped as expected")
	}
	doc = nameID.ReplaceAllString(doc, "${1}AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA${3}")

	// A signing key and certificate to match. Self-signed, because what the tests need is a key the verifier can be pointed at, not a chain.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Perfuse SAML Test Identity Provider"},
		// Dated to bracket the instant the fixture's assertion was issued, so a test that pins the clock to that moment sees a certificate that
		// was valid then. A not-yet-valid certificate would fail for a reason that has nothing to do with what is being tested.
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	// Sign the assertion, not the response: the reference in the existing signature points at the assertion's identifier.
	assertionStart := strings.Index(doc, "<Assertion ")
	assertionEnd := strings.Index(doc, "</Assertion>")
	if assertionStart < 0 || assertionEnd < 0 {
		t.Fatal("no unprefixed Assertion element found; this fixture exists to cover the unprefixed form")
	}
	assertionEnd += len("</Assertion>")
	assertion := doc[assertionStart:assertionEnd]

	// Replace the existing signature with the placeholder the signer expects. The digest is taken over the assertion with the signature removed,
	// which is what the enveloped-signature transform means.
	sigStart := strings.Index(assertion, "<Signature ")
	sigEnd := strings.Index(assertion, "</Signature>")
	if sigStart < 0 || sigEnd < 0 {
		t.Fatal("no unprefixed Signature element inside the assertion")
	}
	sigEnd += len("</Signature>")
	withPlaceholder := assertion[:sigStart] + "{SIGNATURE}" + assertion[sigEnd:]

	idMatch := regexp.MustCompile(`<Assertion [^>]*ID="([^"]+)"`).FindStringSubmatch(assertion)
	if idMatch == nil {
		t.Fatal("the assertion has no ID attribute")
	}

	// The empty prefix is the whole reason this fixture is separate from Keycloak's. Signing it with "ds" would produce a document that verifies
	// and covers nothing the other fixture does not already cover.
	signature, err := signDocument(withPlaceholder, idMatch[1], key, "")
	if err != nil {
		t.Fatal(err)
	}

	signedAssertion := strings.Replace(withPlaceholder, "{SIGNATURE}", signature, 1)
	doc = doc[:assertionStart] + signedAssertion + doc[assertionEnd:]

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	if err := os.WriteFile("testdata/entra-response.xml", []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/entra-idp.crt", certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Log("rewrote testdata/entra-response.xml and testdata/entra-idp.crt; now run the Entra assertion tests, which are the check that it worked")
}
