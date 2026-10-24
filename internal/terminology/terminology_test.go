package terminology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/cql"
)

// A stand-in server giving a value set of 2,500 codes in pages, one of them nested: the whole expansion comes back.
func TestExpandPages(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		if r.URL.Path != "/ValueSet/$expand" || r.URL.Query().Get("url") != "http://example.org/vs/big" {
			w.WriteHeader(404)
			w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"diagnostics":"no such value set"}]}`))
			return
		}
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n, _ := strconv.Atoi(r.URL.Query().Get("count"))
		var cs []any
		for i := off; i < min(off+n, 2500); i++ {
			c := map[string]any{"system": "http://snomed.info/sct", "code": strconv.Itoa(1000 + i)}
			if i == 7 {
				c["contains"] = []any{map[string]any{"system": "http://snomed.info/sct", "code": "child"}}
			}
			cs = append(cs, c)
		}
		json.NewEncoder(w).Encode(map[string]any{"resourceType": "ValueSet", "version": "2026", "expansion": map[string]any{
			"total": 2501, "offset": off, "contains": cs, "timestamp": "2026-10-24T00:00:00Z"}})
	}))
	defer srv.Close()
	s := &Server{Base: "http://127.0.0.1" + strings.TrimPrefix(srv.URL, "http://127.0.0.1")}
	raw, err := s.Expand(context.Background(), "http://example.org/vs/big", "")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := cql.NewValueSet(raw)
	if err != nil {
		t.Fatal(err)
	}
	if vs.Len() != 2501 || !vs.Has(cql.Code{System: "http://snomed.info/sct", Code: "child"}) {
		t.Errorf("%d codes", vs.Len())
	}
	if len(asked) != 3 {
		t.Errorf("asked %d times: %v", len(asked), asked)
	}
	if _, err := s.Expand(context.Background(), "http://example.org/vs/none", ""); err == nil ||
		!strings.Contains(err.Error(), "no such value set") {
		t.Errorf("a missing value set: %v", err)
	}
	if _, err := (&Server{Base: "http://tx.example"}).Expand(context.Background(), "x", ""); err == nil {
		t.Error("plain http to another machine was allowed")
	}
}

// VAGLIO_TEST_TX=https://tx.fhir.org/r4 expands a real VSAC value set the home oxygen library names.
func TestRealServer(t *testing.T) {
	base := os.Getenv("VAGLIO_TEST_TX")
	if base == "" {
		t.Skip("VAGLIO_TEST_TX not set")
	}
	raw, err := (&Server{Base: base}).Expand(context.Background(), "http://cts.nlm.nih.gov/fhir/ValueSet/2.16.840.1.113762.1.4.1219.25", "")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := cql.NewValueSet(raw)
	if err != nil || vs.Len() == 0 {
		t.Fatalf("%v, %d codes", err, vs.Len())
	}
	t.Logf("%d codes", vs.Len())
}
