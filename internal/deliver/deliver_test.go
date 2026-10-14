package deliver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
)

func reviewed(t *testing.T) (*cases.Store, cases.Case) {
	t.Helper()
	dir := t.TempDir()
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s, err := cases.Open(l, filepath.Join(dir, "bundles"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Open(cases.Opening{Item: "2", Request: "resp-1", Answer: cases.Answer{Decision: "pend"}}, []byte(`{}`))
	rc, err := s.Review(c.ID, "dr.lee", cases.Review{Decision: "deny", Reason: "Exam normal.", ReviewerNPI: "1234567893"})
	if err != nil {
		t.Fatal(err)
	}
	return s, *rc
}

func TestSendsDecideParameters(t *testing.T) {
	var got map[string]any
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fhir/Claim/$decide" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.WriteHeader(200)
		io.WriteString(w, `{"resourceType":"Bundle"}`)
	}))
	defer ts.Close()
	store, c := reviewed(t)
	cl := &Client{Base: ts.URL + "/fhir/", Token: "tok"}
	cl.Deliver(context.Background(), store, c)
	if auth != "Bearer tok" {
		t.Errorf("auth %q", auth)
	}
	params := map[string]any{}
	for _, p := range got["parameter"].([]any) {
		pm := p.(map[string]any)
		for k, v := range pm {
			if k != "name" {
				params[pm["name"].(string)] = v
			}
		}
	}
	want := map[string]any{"claimResponse": "resp-1", "decision": "deny", "reason": "Exam normal.", "item": 2.0, "reviewer": "1234567893"}
	for k, v := range want {
		if params[k] != v {
			t.Errorf("%s = %v, want %v", k, params[k], v)
		}
	}
	if cs, _ := store.Get(c.ID); cs.Delivery == nil || !cs.Delivery.OK {
		t.Errorf("delivery %+v", cs.Delivery)
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		final  bool
	}{{409, true}, {404, true}, {400, true}, {500, false}, {401, false}, {503, false}} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"resourceType":"OperationOutcome","issue":[{"diagnostics":"nothing left to decide"}]}`)
		}))
		store, c := reviewed(t)
		(&Client{Base: ts.URL}).Deliver(context.Background(), store, c)
		cs, _ := store.Get(c.ID)
		if cs.Delivery.OK || cs.Delivery.Final != tc.final {
			t.Errorf("%d: %+v", tc.status, cs.Delivery)
		}
		if tc.status == 409 && cs.Delivery.Error != "the PAS server answered 409: nothing left to decide" {
			t.Errorf("error %q", cs.Delivery.Error)
		}
		if retried := len(store.Undelivered()) == 1; retried == tc.final {
			t.Errorf("%d: retried %v", tc.status, retried)
		}
		ts.Close()
	}
	store, c := reviewed(t)
	(&Client{Base: "http://127.0.0.1:1"}).Deliver(context.Background(), store, c)
	if len(store.Undelivered()) != 1 {
		t.Error("an unreachable server is not retried")
	}
}
