package saml

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"strings"
)

// Reading an identity provider's metadata document.
//
// Why this exists. Every SAML provider publishes one of these, and until now Vaglio asked an operator to do the extraction by hand: find
// the signing certificate in the XML, strip the whitespace out of the base64, wrap it in PEM headers with the right line breaks, and paste
// the sign-on URL separately. That is XML surgery as a configuration step, and a certificate with a stray space in it fails with a
// signature error that says nothing about formatting.
//
// It is also the difference between configuring SAML from the browser and only appearing to. The panel accepted a certificate and a URL,
// which is not the same as being able to point Vaglio at a provider.
//
// What it deliberately does not do. It does not fetch. A parser that reaches out to a URL of its own accord is one that can be aimed at an
// internal address by whoever can edit the configuration, so fetching is the caller's business and happens where that decision is visible.
//
// Element names are matched without namespaces on purpose. Entra writes them unprefixed, Keycloak and Shibboleth use md:, ADFS has used
// both, and all of them are the same document. Go's encoding/xml matches any namespace when a tag names only a local element, which is the
// tolerant reading and the correct one here.

// idpMetadataDoc mirrors the parts of a metadata document Vaglio uses.
type idpMetadataDoc struct {
	XMLName xml.Name

	// EntityID is on the EntityDescriptor.
	EntityID string `xml:"entityID,attr"`

	// Wrapped is the EntityDescriptor list, for a document published by a federation rather than by one provider.
	Wrapped []idpMetadataDoc `xml:"EntityDescriptor"`

	IDPDescriptor *struct {
		KeyDescriptors []struct {
			Use string `xml:"use,attr"`

			// The certificate sits under KeyInfo/X509Data/X509Certificate, and there can be more than one.
			Certificates []string `xml:"KeyInfo>X509Data>X509Certificate"`
		} `xml:"KeyDescriptor"`

		SingleSignOnServices []struct {
			Binding  string `xml:"Binding,attr"`
			Location string `xml:"Location,attr"`
		} `xml:"SingleSignOnService"`
	} `xml:"IDPSSODescriptor"`
}

// IDPMetadata is what an identity provider's metadata document says about it.
type IDPMetadata struct {
	// EntityID is the provider's own identifier, which every assertion it issues carries as the Issuer.
	EntityID string

	// SSOURL is where a sign-on request is sent, for HTTP-Redirect binding.
	SSOURL string

	// SSOPostURL is the same for HTTP-POST binding, empty when the provider does not offer it.
	SSOPostURL string

	// Certificates are the signing certificates, in document order.
	//
	// More than one is normal and is not a problem to be resolved here: a provider rotating keys publishes both, and an assertion may be
	// signed with either. Keeping them all is what makes a rotation invisible rather than an outage.
	Certificates []*x509.Certificate
}

// CertPEM returns the certificates as PEM, which is the form the configuration file holds.
func (m *IDPMetadata) CertPEM() string {
	var b strings.Builder
	for _, cert := range m.Certificates {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	}
	return b.String()
}

const (
	bindingRedirect = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	bindingPOST     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
)

// ParseIDPMetadata reads a SAML 2.0 metadata document and returns what Vaglio needs to trust the provider.
//
// Strict about the two things that would otherwise fail later and unhelpfully: a document with no signing certificate cannot verify
// anything, and one with no sign-on URL leaves nowhere to send anybody. Both are refused here, where the message can name the document,
// rather than at somebody's first attempt to sign in.
func ParseIDPMetadata(doc []byte) (*IDPMetadata, error) {
	var parsed idpMetadataDoc
	if err := xml.Unmarshal(doc, &parsed); err != nil {
		return nil, fmt.Errorf("saml: parsing the metadata document: %w", err)
	}

	// A federation publishes several descriptors wrapped in an EntitiesDescriptor. The first that describes an identity provider is the
	// one wanted; a document wrapping only service providers is a different mistake and is reported as such below.
	if parsed.XMLName.Local == "EntitiesDescriptor" {
		var found bool
		for _, inner := range parsed.Wrapped {
			if inner.IDPDescriptor != nil {
				parsed = inner
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("saml: the document is an EntitiesDescriptor and none of its %d entries describes an identity "+
				"provider", len(parsed.Wrapped))
		}
	} else if parsed.XMLName.Local != "EntityDescriptor" {
		return nil, fmt.Errorf("saml: the metadata document's root is <%s>, expected <EntityDescriptor>", parsed.XMLName.Local)
	}

	if parsed.EntityID == "" {
		return nil, fmt.Errorf("saml: the metadata document has no entityID, so assertions from it could not be attributed to anybody")
	}

	out := &IDPMetadata{EntityID: parsed.EntityID}

	if parsed.IDPDescriptor == nil {
		return nil, fmt.Errorf("saml: %q publishes no IDPSSODescriptor, so it is not an identity provider. A document describing a "+
			"service provider looks very similar and is the usual mistake here", out.EntityID)
	}

	for _, svc := range parsed.IDPDescriptor.SingleSignOnServices {
		switch svc.Binding {
		case bindingRedirect:
			if out.SSOURL == "" {
				out.SSOURL = strings.TrimSpace(svc.Location)
			}
		case bindingPOST:
			if out.SSOPostURL == "" {
				out.SSOPostURL = strings.TrimSpace(svc.Location)
			}
		}
	}

	if out.SSOURL == "" && out.SSOPostURL == "" {
		return nil, fmt.Errorf("saml: %q publishes no SingleSignOnService for redirect or POST binding", out.EntityID)
	}

	// Only the signing certificates.
	//
	// A KeyDescriptor carries a use of "signing" or "encryption", and an absent use means both. Taking an encryption certificate for a
	// signing one produces a digest mismatch - the same symptom as a canonicalisation defect, which would send the next reader back over
	// ground this repository has already covered painfully.
	for _, kd := range parsed.IDPDescriptor.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}

		for _, raw := range kd.Certificates {
			cert, err := parseBase64Certificate(raw)
			if err != nil {
				return nil, fmt.Errorf("saml: %q publishes a signing certificate that cannot be read: %w", out.EntityID, err)
			}
			out.Certificates = append(out.Certificates, cert)
		}
	}

	if len(out.Certificates) == 0 {
		return nil, fmt.Errorf("saml: %q publishes no signing certificate, so no assertion from it could be verified. A provider that "+
			"has not been given a signing key yet publishes a document that otherwise looks complete", out.EntityID)
	}

	return out, nil
}

// parseBase64Certificate decodes the contents of an X509Certificate element.
//
// The whitespace removal is the whole reason this is a function. Providers wrap the base64 at seventy-six characters or indent it to match
// the surrounding XML, and base64 decoding fails on either. Doing this by hand in a text editor is what configuring SAML used to require.
func parseBase64Certificate(text string) (*x509.Certificate, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, text)

	if cleaned == "" {
		return nil, fmt.Errorf("the certificate element is empty")
	}

	der, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("the certificate is not valid base64: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("the certificate does not parse as X.509: %w", err)
	}

	return cert, nil
}
