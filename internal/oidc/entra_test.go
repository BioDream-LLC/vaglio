package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What Microsoft Entra ID does differently, as Perfuse's verification against a live tenant found it.

func TestAccountName(t *testing.T) {
	for in, want := range map[string]string{"Kim@Plan.example": "kim", " kim ": "kim", "kim": "kim", "": ""} {
		if got := AccountName(in); got != want {
			t.Errorf("AccountName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Entra's shared endpoints publish an issuer template; the error says to use the tenant's own.
func TestDiscoverEntraSharedEndpoint(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": "https://login.microsoftonline.com/{tenantid}/v2.0"})
	}))
	defer idp.Close()
	_, err := Discover(context.Background(), Config{Issuer: idp.URL, ClientID: "c", ClientSecret: "s",
		RedirectURL: "http://127.0.0.1/api/sso/callback"})
	if err == nil || !strings.Contains(err.Error(), "TENANT-ID") {
		t.Errorf("%v", err)
	}
}

// With too many groups Entra leaves the groups claim out and names where to fetch them: refused, saying why.
func TestGroupOverage(t *testing.T) {
	p := &Provider{cfg: Config{Group: "g", GroupsClaim: "groups", NameClaim: "preferred_username"}}
	_, err := p.Name(Claims{"preferred_username": "kim@plan.example",
		"_claim_names": map[string]any{"groups": "src1"}, "_claim_sources": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "app role") {
		t.Errorf("%v", err)
	}
	if name, err := p.Name(Claims{"preferred_username": "Kim@plan.example", "groups": []any{"g"}}); err != nil || name != "kim" {
		t.Errorf("%q %v", name, err)
	}
}
