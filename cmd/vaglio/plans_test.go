package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlans(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "plans.json")
	os.WriteFile(f, []byte(body), 0o600)
	return f
}

func TestReadPlans(t *testing.T) {
	shared := t.TempDir()
	ok := `{"plans":[{"id":"acme","name":"Acme Health","hostname":"Acme.PA.example","policies":"a/policies"},
		{"id":"blue","hostname":"blue.pa.example","policies":"b/policies","data":"b/data"}]}`
	ps, err := readPlans(writePlans(t, ok), shared)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Hostname != "acme.pa.example" || ps[0].Data != filepath.Join(shared, "plans", "acme") || ps[1].Name != "blue" {
		t.Fatalf("%+v", ps)
	}
	for name, body := range map[string]string{
		"no plans":        `{"plans":[]}`,
		"bad id":          `{"plans":[{"id":"Acme","hostname":"a.example","policies":"p"}]}`,
		"no hostname":     `{"plans":[{"id":"a","policies":"p"}]}`,
		"an address":      `{"plans":[{"id":"a","hostname":"10.0.0.1","policies":"p"}]}`,
		"no policies":     `{"plans":[{"id":"a","hostname":"a.example"}]}`,
		"same hostname":   `{"plans":[{"id":"a","hostname":"x.example","policies":"p"},{"id":"b","hostname":"x.example","policies":"q"}]}`,
		"same policies":   `{"plans":[{"id":"a","hostname":"a.example","policies":"p"},{"id":"b","hostname":"b.example","policies":"p"}]}`,
		"same data":       `{"plans":[{"id":"a","hostname":"a.example","policies":"p","data":"d"},{"id":"b","hostname":"b.example","policies":"q","data":"d"}]}`,
		"same token":      `{"plans":[{"id":"a","hostname":"a.example","policies":"p","tokenEnv":"T"},{"id":"b","hostname":"b.example","policies":"q","tokenEnv":"T"}]}`,
		"unknown field":   `{"plans":[{"id":"a","hostname":"a.example","policies":"p","polices":"x"}]}`,
		"shared data dir": `{"plans":[{"id":"a","hostname":"a.example","policies":"p","data":"` + shared + `"}]}`,
	} {
		if _, err := readPlans(writePlans(t, body), shared); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestByHost(t *testing.T) {
	h := byHost(map[string]http.Handler{
		"a.example": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("A")) }),
		"b.example": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("B")) }),
	})
	for host, want := range map[string]string{"a.example": "A", "B.example:8443": "B", "c.example": "unexpected"} {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %q", host, w.Code, w.Body)
		}
	}
}
