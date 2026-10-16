package saml

import (
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests that a response has to answer a request this server sent.
//
// The attack this closes is quiet and does not look like an attack from either end. An attacker signs in legitimately as themselves,
// keeps the response their identity provider issued, and gets a victim's browser to post it to the assertion consumer endpoint. The
// signature is genuine, the conditions hold, the audience is right, and nothing in the document is forged. The victim is now inside
// the attacker's account.
//
// What that is worth depends on the account, and in an integration engine it is worth a great deal: the victim may be an
// administrator whose next few clicks - reading a channel, exporting a profile, running a script - happen as somebody else, and the
// audit log records them under the attacker's name. There is no forensic trace that anything unusual happened.
//
// The package had no notion of InResponseTo at all. It was not parsed, and BuildAuthnRequest generated a request id and threw it
// away, so no response could be tied to a request even in principle.

// buildResponseInReplyTo builds a valid response that names the request it answers.
func buildResponseInReplyTo(t *testing.T, id, inResponseTo string, key *rsa.PrivateKey) string {
	t.Helper()

	now := time.Now().UTC()

	inner := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s_a" IssueInstant="%s" Version="2.0">`+
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
		id,
		now.Format(time.RFC3339),
		now.Add(-1*time.Minute).Format(time.RFC3339),
		now.Add(5*time.Minute).Format(time.RFC3339),
	)

	inReplyAttr := ""
	if inResponseTo != "" {
		inReplyAttr = fmt.Sprintf(` InResponseTo="%s"`, inResponseTo)
	}

	body := fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="%s" Version="2.0" IssueInstant="%s"`+
			` Destination="https://sp.example.com/saml/acs"%s>`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.example.com</saml:Issuer>`+
			`{SIGNATURE}`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		id, now.Format(time.RFC3339), inReplyAttr, inner,
	)

	return strings.Replace(body, `{SIGNATURE}`, signElementAs(t, body, id, key, nil), 1)
}

// solicitedSP is a service provider that will not accept an unsolicited response.
func solicitedSP(t *testing.T) *ServiceProvider {
	t.Helper()

	sp, err := New(Config{
		EntityID:   "https://sp.example.com",
		ACSPath:    "https://sp.example.com/saml/acs",
		IdPSSOURL:  "https://idp.example.com/sso",
		IdPCertPEM: testPEM,
	})
	if err != nil {
		t.Fatal(err)
	}

	return sp
}

// oneOutstanding tracks a single request id and consumes it when it is recognised.
func oneOutstanding(id string) func(string) bool {
	seen := false

	return func(got string) bool {
		if seen || got != id {
			return false
		}

		seen = true

		return true
	}
}

func TestAResponseAnsweringOurRequestIsAccepted(t *testing.T) {
	resetReplayCache()

	sp := solicitedSP(t)
	response := buildResponseInReplyTo(t, "_inreply_ok", "_req_abc", testKey)

	assertion, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), oneOutstanding("_req_abc"))
	if err != nil {
		t.Fatalf("a response answering our own request was rejected: %v", err)
	}

	if assertion.NameID != "user@example.com" {
		t.Errorf("NameID = %q", assertion.NameID)
	}
}

func TestAResponseAnsweringSomebodyElsesRequestIsRefused(t *testing.T) {
	// The attack. A genuine, correctly signed response, whose only fault is that this browser never asked for it.
	resetReplayCache()

	sp := solicitedSP(t)
	response := buildResponseInReplyTo(t, "_inreply_other", "_req_belonging_to_the_attacker", testKey)

	// This server is waiting for a different request.
	_, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), oneOutstanding("_req_we_actually_sent"))
	if err == nil {
		t.Fatal("a response to a request this server never sent was accepted")
	}

	if !strings.Contains(err.Error(), "did not send or has already answered") {
		t.Errorf("refused, but not for the reason under test: %v", err)
	}
}

func TestTheSameResponseCannotAnswerARequestTwice(t *testing.T) {
	// Recognising a request has to consume it. A response is the answer to one login attempt, and answering the same attempt twice
	// is a replay - which the assertion-id cache also catches, so this is the second of two independent barriers rather than the
	// only one. Two barriers is the point: the replay cache is in memory and does not survive a restart.
	resetReplayCache()

	sp := solicitedSP(t)
	response := buildResponseInReplyTo(t, "_inreply_twice", "_req_once", testKey)
	encoded := base64.StdEncoding.EncodeToString([]byte(response))
	outstanding := oneOutstanding("_req_once")

	if _, err := sp.ParseResponseFor(encoded, outstanding); err != nil {
		t.Fatalf("the first use was rejected: %v", err)
	}

	if _, err := sp.ParseResponseFor(encoded, outstanding); err == nil {
		t.Fatal("the same response was accepted twice")
	}
}

func TestAnUnsolicitedResponseIsRefusedByDefault(t *testing.T) {
	// Identity-provider-initiated sign-on is a real feature that real sites want, and it is also what makes a captured response
	// reusable. Refused unless a site has said it wants it, so the cost is accepted deliberately rather than inherited.
	resetReplayCache()

	sp := solicitedSP(t)
	response := buildResponseInReplyTo(t, "_inreply_none", "", testKey)

	_, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), oneOutstanding("_req_x"))
	if err == nil {
		t.Fatal("an unsolicited response was accepted with AllowUnsolicited off")
	}

	if !strings.Contains(err.Error(), "unsolicited") {
		t.Errorf("refused, but not as an unsolicited response: %v", err)
	}
}

func TestAnUnsolicitedResponseIsAcceptedWhenTheSiteAsksForIt(t *testing.T) {
	// The positive control for the test above. Without it, that test would pass just as well against a service provider that
	// rejected everything.
	resetReplayCache()

	sp, err := New(Config{
		EntityID:         "https://sp.example.com",
		ACSPath:          "https://sp.example.com/saml/acs",
		IdPSSOURL:        "https://idp.example.com/sso",
		IdPCertPEM:       testPEM,
		AllowUnsolicited: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	response := buildResponseInReplyTo(t, "_inreply_none_ok", "", testKey)

	if _, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), nil); err != nil {
		t.Fatalf("an unsolicited response was rejected with AllowUnsolicited on: %v", err)
	}
}

func TestAResponseNamingARequestWithNothingTrackingRequestsIsRefused(t *testing.T) {
	// The combination that must not be waved through: the caller asked for the safe path and cannot supply what it needs. Guessing
	// which way they meant it is how a check becomes decorative.
	resetReplayCache()

	sp := solicitedSP(t)
	response := buildResponseInReplyTo(t, "_inreply_untracked", "_req_something", testKey)

	_, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), nil)
	if err == nil {
		t.Fatal("a response naming a request was accepted with nothing tracking requests")
	}

	if !strings.Contains(err.Error(), "no request is being tracked") {
		t.Errorf("refused, but not for the reason under test: %v", err)
	}
}

func TestInResponseToIsCheckedOnlyAfterTheSignature(t *testing.T) {
	// Ordering, and it matters. An unsigned document's InResponseTo is attacker-controlled and tells us nothing, so a service that
	// checked it first would be reasoning about a value it has no reason to believe - and would report the wrong fault, sending
	// somebody to look at their request tracking when the real problem is that the response is forged.
	resetReplayCache()

	bad := newAttacker(t)

	// Signed by the wrong key, and naming a request that genuinely is outstanding.
	response := buildResponseInReplyTo(t, "_inreply_order", "_req_real", bad.key)

	sp := solicitedSP(t)

	_, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), oneOutstanding("_req_real"))
	if err == nil {
		t.Fatal("a response signed by the wrong key was accepted")
	}

	if strings.Contains(err.Error(), "did not send") || strings.Contains(err.Error(), "unsolicited") {
		t.Errorf("the signature failure was reported as a request-tracking problem: %v", err)
	}
}

func TestBuildAuthnRequestReturnsAnIDThatCanBeCheckedLater(t *testing.T) {
	// The two halves have to fit: the id handed out at the start must be the one the response names at the end. It used to be
	// generated and discarded, which is why none of this could be checked.
	sp := solicitedSP(t)

	redirectURL, requestID, err := sp.BuildAuthnRequest("/dashboard")
	if err != nil {
		t.Fatal(err)
	}

	if requestID == "" {
		t.Fatal("no request id was returned")
	}

	if !strings.Contains(redirectURL, "SAMLRequest=") {
		t.Error("the redirect carries no SAMLRequest")
	}

	resetReplayCache()

	response := buildResponseInReplyTo(t, "_inreply_roundtrip", requestID, testKey)

	if _, err := sp.ParseResponseFor(base64.StdEncoding.EncodeToString([]byte(response)), oneOutstanding(requestID)); err != nil {
		t.Fatalf("a response naming the id BuildAuthnRequest returned was rejected: %v", err)
	}
}
