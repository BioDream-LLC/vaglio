package saml

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Standing up a SAML application in the Entra tenant, so a real Entra document can be verified.
//
// The sequence Graph requires is not obvious and is worth writing down, because each step fails differently if it is skipped:
//
//  1. Instantiate the generic application template. Entra's gallery templates each have an id, and 8adf8e6e-34ba-4c9b-8b12-a89b2de1c2f1
//     is the one meaning "an application not in the gallery". Creating an application directly instead produces one with no service
//     principal, and SAML settings live on the service principal.
//  2. Set preferredSingleSignOnMode to "saml" on the service principal. Without it the application exists and the federation metadata
//     document is not published, which reads as a missing tenant rather than a missing setting.
//  3. Set identifierUris and the reply URL on the application. The identifier is the audience our assertions must carry.
//  4. Add a token signing certificate. Entra does not create one until asked, and the metadata document is published without a key until
//     it exists - so verification fails with no signing certificate rather than with anything about configuration.
//
// Everything created is deleted afterwards. A tenant that accumulates one application per test run becomes impossible to read, and the
// next person cannot tell which of forty applications is the live one.

// nonGalleryTemplateID asks Entra for the id of its "not in the gallery" application template.
//
// Looked up rather than written down. The first version of this carried an id from memory, which was wrong in everything after the first
// segment, and Graph's answer was "App with appId ... does not exist" - true, unhelpful, and indistinguishable from a permissions problem
// until you check. A lookup by name cannot rot and cannot be mistyped.
func nonGalleryTemplateID(t *testing.T, g *graph) string {
	t.Helper()

	res := g.do(http.MethodGet, "/applicationTemplates?$filter="+url.QueryEscape("displayName eq 'Custom'"), nil)

	list, ok := res["value"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("Entra lists no application template called Custom, so there is no way to create a non-gallery application: %v", res)
	}

	first, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("the template list holds %T rather than an object", list[0])
	}

	// It has to support SAML, or the application created from it cannot be given SAML sign-on and the failure arrives two calls later.
	modes, _ := first["supportedSingleSignOnModes"].([]any)
	var supportsSAML bool
	for _, m := range modes {
		if s, _ := m.(string); s == "saml" {
			supportsSAML = true
			break
		}
	}
	if !supportsSAML {
		t.Fatalf("the Custom application template does not list saml among its supported sign-on modes: %v", modes)
	}

	return g.str(first, "id")
}

// entraApp is a provisioned application, with what is needed to talk to it.
type entraApp struct {
	// DisplayName is what it is called in the tenant, so a leftover can be recognised.
	DisplayName string

	// AppID is the application (client) id, which selects the signing certificate in the metadata document.
	AppID string

	// ObjectID and ServicePrincipalID are the two different ids Graph needs for the two halves of the configuration.
	ObjectID           string
	ServicePrincipalID string

	// EntityID is the audience our assertions must carry, and MetadataURL is where Entra publishes its signing certificate.
	EntityID    string
	MetadataURL string
}

// provisionEntraSAMLApp creates and configures an application, and registers its deletion.
func provisionEntraSAMLApp(t *testing.T, g *graph, cfg entraConfig, acsURL string) *entraApp {
	t.Helper()

	// The run is in the name, so a leftover from an interrupted run can be told from the one in use.
	name := fmt.Sprintf("perfuse-saml-verification-%d", time.Now().UnixNano())

	created := g.do(http.MethodPost, "/applicationTemplates/"+nonGalleryTemplateID(t, g)+"/instantiate",
		map[string]any{"displayName": name})

	app, ok := created["application"].(map[string]any)
	if !ok {
		t.Fatalf("the instantiate response has no application object: %v", created)
	}
	sp, ok := created["servicePrincipal"].(map[string]any)
	if !ok {
		t.Fatalf("the instantiate response has no servicePrincipal object: %v", created)
	}

	out := &entraApp{
		DisplayName:        name,
		AppID:              g.str(app, "appId"),
		ObjectID:           g.str(app, "id"),
		ServicePrincipalID: g.str(sp, "id"),
	}

	// The audience. A URN rather than a URL, because it identifies rather than locates, and Entra will otherwise try to be helpful about
	// a URL that does not resolve.
	out.EntityID = "urn:perfuse:saml:" + name
	out.MetadataURL = federationMetadataURL(cfg.TenantID, out.AppID)

	t.Cleanup(func() {
		// Deleted by object id, and failures here are reported rather than fatal: a test that has already passed should not be turned
		// red by a tidy-up problem, but a tenant filling with applications has to be visible.
		g.deleteQuietly("/applications/" + out.ObjectID)
	})

	// No wait here, deliberately. Graph is eventually consistent after instantiate, and the obvious response - poll until a GET of the new
	// object succeeds - does not work: a GET of the service principal returns 200 while a PATCH of the same id returns 404. Readable is not
	// writable, so waiting for a read proves nothing about the write that follows. configureSAML retries the writes instead.
	configureSAML(t, g, out, acsURL)

	return out
}

// configureSAML turns the application into a SAML one and gives it a signing certificate.
func configureSAML(t *testing.T, g *graph, app *entraApp, acsURL string) {
	t.Helper()

	// SAML mode first. The metadata document is not published until this is set.
	g.doWriteEventually(http.MethodPatch, "/servicePrincipals/"+app.ServicePrincipalID,
		map[string]any{"preferredSingleSignOnMode": "saml"})

	// Then the identifier and the reply URL, which are on the application rather than the service principal.
	g.doWriteEventually(http.MethodPatch, "/applications/"+app.ObjectID, map[string]any{
		"identifierUris": []string{app.EntityID},
		"web": map[string]any{
			"redirectUris": []string{acsURL},
		},
	})

	// And a signing certificate. Entra creates none by itself, and the metadata document is published without a key until one exists -
	// which fails verification with "no signing certificate" and says nothing about configuration.
	g.doWriteEventually(http.MethodPost, "/servicePrincipals/"+app.ServicePrincipalID+"/addTokenSigningCertificate",
		map[string]any{
			"displayName": "CN=perfuse-verification",
			"endDateTime": time.Now().Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
}

// waitForMetadata polls until the federation metadata document is published and carries a certificate.
//
// Separate from waitForApplication because they become ready at different times, and a document fetched too early is a document with no
// X509Certificate in it - which fails verification in a way that points at our verifier rather than at the wait.
func waitForMetadata(t *testing.T, url string) []byte {
	t.Helper()

	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(120 * time.Second)

	var last string
	for {
		res, err := client.Get(url)
		if err == nil {
			body := readAllAndClose(t, res)
			if res.StatusCode == http.StatusOK && strings.Contains(string(body), "X509Certificate") {
				return body
			}
			last = fmt.Sprintf("status %d, %d bytes, certificate present: %t",
				res.StatusCode, len(body), strings.Contains(string(body), "X509Certificate"))
		} else {
			last = err.Error()
		}

		if time.Now().After(deadline) {
			t.Fatalf("Entra never published usable federation metadata at %s. Last attempt: %s", url, last)
		}
		time.Sleep(3 * time.Second)
	}
}
