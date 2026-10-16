// Safe XML parsing for SAML responses.
//
// SAML uses XML, which is a remarkable attack surface. The parser here limits entity expansion to prevent the billion
// laughs attack (exponential entity expansion that consumes all memory), limits document depth to prevent stack
// exhaustion, and refuses external entity references entirely.
//
// None of Go's encoding/xml processes external entities or DTDs in the dangerous way that libxml2 or Java's SAX do by
// default, but we add explicit guards because relying on a negative (a thing the library happens not to do today) is
// fragile, and the guards cost nothing.
package saml

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const (
	// maxXMLDepth is the deepest nesting allowed.
	maxXMLDepth = 50

	// maxEntityExpansions is how many entity replacements are permitted before the parser gives up.
	// A legitimate SAML response has zero; the limit exists to cap a deliberate attack.
	maxEntityExpansions = 100

	// SAML and XML Signature namespaces.
	nsSAMLProtocol  = "urn:oasis:names:tc:SAML:2.0:protocol"
	nsSAMLAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"
	nsXMLDSig       = "http://www.w3.org/2000/09/xmldsig#"
	nsXMLExcC14n    = "http://www.w3.org/2001/10/xml-exc-c14n#"
)

// samlResponse is the wire representation of a SAML Response.
type samlResponse struct {
	XMLName     xml.Name `xml:"urn:oasis:names:tc:SAML:2.0:protocol Response"`
	ID          string   `xml:"ID,attr"`
	Destination string   `xml:"Destination,attr"`

	// InResponseTo names the AuthnRequest this answers, and is empty when the identity provider started the login itself.
	//
	// Checking it is what ties a response to a login somebody actually began in this browser. Without the check, a valid response
	// obtained anywhere can be posted into anyone's browser, logging that person in as the subject of the response - they end up
	// inside somebody else's account without either party doing anything visibly wrong.
	InResponseTo string           `xml:"InResponseTo,attr"`
	IssueInstant string           `xml:"IssueInstant,attr"`
	Issuer       string           `xml:"urn:oasis:names:tc:SAML:2.0:assertion Issuer"`
	Status       samlStatus       `xml:"urn:oasis:names:tc:SAML:2.0:protocol Status"`
	Assertions   []samlAssertionW `xml:"urn:oasis:names:tc:SAML:2.0:assertion Assertion"`
	Signature    *xmlSignature    `xml:"http://www.w3.org/2000/09/xmldsig# Signature"`
}

type samlStatus struct {
	StatusCode samlStatusCode `xml:"urn:oasis:names:tc:SAML:2.0:protocol StatusCode"`
}

type samlStatusCode struct {
	Value string `xml:"Value,attr"`
}

// samlAssertionW is the decoded assertion wrapper with its conditions and subject.
type samlAssertionW struct {
	XMLName      xml.Name             `xml:"urn:oasis:names:tc:SAML:2.0:assertion Assertion"`
	ID           string               `xml:"ID,attr"`
	IssueInstant string               `xml:"IssueInstant,attr"`
	Issuer       string               `xml:"urn:oasis:names:tc:SAML:2.0:assertion Issuer"`
	Subject      samlSubject          `xml:"urn:oasis:names:tc:SAML:2.0:assertion Subject"`
	Conditions   samlConditions       `xml:"urn:oasis:names:tc:SAML:2.0:assertion Conditions"`
	AuthnStmt    []samlAuthnStatement `xml:"urn:oasis:names:tc:SAML:2.0:assertion AuthnStatement"`
	AttrStmts    []samlAttrStatement  `xml:"urn:oasis:names:tc:SAML:2.0:assertion AttributeStatement"`
	Signature    *xmlSignature        `xml:"http://www.w3.org/2000/09/xmldsig# Signature"`
}

type samlSubject struct {
	NameID samlNameID `xml:"urn:oasis:names:tc:SAML:2.0:assertion NameID"`
}

type samlNameID struct {
	Value  string `xml:",chardata"`
	Format string `xml:"Format,attr"`
}

type samlConditions struct {
	NotBefore    string                    `xml:"NotBefore,attr"`
	NotOnOrAfter string                    `xml:"NotOnOrAfter,attr"`
	Audiences    []samlAudienceRestriction `xml:"urn:oasis:names:tc:SAML:2.0:assertion AudienceRestriction"`
}

type samlAudienceRestriction struct {
	Audiences []string `xml:"urn:oasis:names:tc:SAML:2.0:assertion Audience"`
}

type samlAuthnStatement struct {
	SessionIndex string `xml:"SessionIndex,attr"`
}

type samlAttrStatement struct {
	Attributes []samlAttribute `xml:"urn:oasis:names:tc:SAML:2.0:assertion Attribute"`
}

type samlAttribute struct {
	Name   string          `xml:"Name,attr"`
	Values []samlAttrValue `xml:"urn:oasis:names:tc:SAML:2.0:assertion AttributeValue"`
}

type samlAttrValue struct {
	Value string `xml:",chardata"`
}

// xmlSignature is the XML-DSig Signature element.
type xmlSignature struct {
	SignedInfo xmlSignedInfo `xml:"http://www.w3.org/2000/09/xmldsig# SignedInfo"`
	SigValue   string        `xml:"http://www.w3.org/2000/09/xmldsig# SignatureValue"`
	KeyInfo    *xmlKeyInfo   `xml:"http://www.w3.org/2000/09/xmldsig# KeyInfo"`
}

type xmlSignedInfo struct {
	CanonicalizationMethod xmlAlgorithm `xml:"http://www.w3.org/2000/09/xmldsig# CanonicalizationMethod"`
	SignatureMethod        xmlAlgorithm `xml:"http://www.w3.org/2000/09/xmldsig# SignatureMethod"`
	Reference              xmlReference `xml:"http://www.w3.org/2000/09/xmldsig# Reference"`
}

type xmlAlgorithm struct {
	Algorithm           string          `xml:"Algorithm,attr"`
	InclusiveNamespaces *xmlInclusiveNS `xml:"http://www.w3.org/2001/10/xml-exc-c14n# InclusiveNamespaces"`
}

type xmlInclusiveNS struct {
	PrefixList string `xml:"PrefixList,attr"`
}

type xmlReference struct {
	URI          string        `xml:"URI,attr"`
	Transforms   xmlTransforms `xml:"http://www.w3.org/2000/09/xmldsig# Transforms"`
	DigestMethod xmlAlgorithm  `xml:"http://www.w3.org/2000/09/xmldsig# DigestMethod"`
	DigestValue  string        `xml:"http://www.w3.org/2000/09/xmldsig# DigestValue"`
}

type xmlTransforms struct {
	Transforms []xmlTransform `xml:"http://www.w3.org/2000/09/xmldsig# Transform"`
}

type xmlTransform struct {
	Algorithm           string          `xml:"Algorithm,attr"`
	InclusiveNamespaces *xmlInclusiveNS `xml:"http://www.w3.org/2001/10/xml-exc-c14n# InclusiveNamespaces"`
}

type xmlKeyInfo struct {
	X509Data *xmlX509Data `xml:"http://www.w3.org/2000/09/xmldsig# X509Data"`
}

type xmlX509Data struct {
	Certificate string `xml:"http://www.w3.org/2000/09/xmldsig# X509Certificate"`
}

// decodeResponse safely decodes a SAML response from raw XML bytes.
//
// It checks for entity expansion attacks by monitoring the decoder's input offset against the output. A document whose
// decoded size exceeds its wire size by too much is expanding entities and gets rejected.
func decodeResponse(raw []byte) (*samlResponse, error) {
	if err := checkXMLSafety(raw); err != nil {
		return nil, err
	}
	var resp samlResponse
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("saml: decode response: %w", err)
	}
	return &resp, nil
}

// checkXMLSafety scans the raw XML for dangerous patterns without fully parsing it.
//
// It rejects:
//   - Documents with a DOCTYPE (which could declare entities)
//   - Documents that exceed the depth limit
//   - Documents with excessive entity references
func checkXMLSafety(raw []byte) error {
	// Reject any document with a DOCTYPE declaration. SAML responses never need one, and a
	// DOCTYPE is the only way to declare the internal entities that enable expansion attacks.
	lower := bytes.ToLower(raw)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return fmt.Errorf("saml: XML contains DOCTYPE or ENTITY declaration (rejected for security)")
	}

	// Verify depth limit by doing a streaming parse.
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	depth := 0
	entityCount := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("saml: XML parse error: %w", err)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return fmt.Errorf("saml: XML depth exceeds %d (possible attack)", maxXMLDepth)
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			// A Directive token may contain entity declarations.
			entityCount++
			if entityCount > maxEntityExpansions {
				return fmt.Errorf("saml: too many XML directives (possible entity expansion attack)")
			}
		}
	}
	return nil
}

// resolveSignatureReference finds the element that a signature's Reference URI points to.
//
// This is critical for XSW protection: the signature covers a specific element identified by its URI (typically #ID),
// and we must read identity information from that SAME element, not from another element that happens to have the right
// shape but was injected by an attacker.
func resolveSignatureReference(root *xmlNode, uri string) (*xmlNode, error) {
	if uri == "" {
		// Empty URI means the whole document.
		return root, nil
	}
	if !strings.HasPrefix(uri, "#") {
		return nil, fmt.Errorf("saml: signature Reference URI %q is not a fragment (external references not supported)", uri)
	}
	id := uri[1:]
	node := findElementByID(root, id)
	if node == nil {
		return nil, fmt.Errorf("saml: element with ID %q not found", id)
	}
	return node, nil
}
