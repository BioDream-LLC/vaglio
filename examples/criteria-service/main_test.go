package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/outside"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// The example answers Vaglio's own client: met, not met with its reason, and unknown when the request does not say.
func TestWithVaglio(t *testing.T) {
	srv := httptest.NewServer(handler("k"))
	defer srv.Close()
	svc, err := outside.NewCriteriaService("example", srv.URL, "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{ID: "bariatric", Version: "1"}
	for _, tc := range []struct {
		bmi  any
		want decide.Finding
	}{{42.0, decide.Present}, {35.5, decide.Absent}, {nil, ""}} {
		b := pastest.Bundle([]string{"43644"})
		if tc.bmi != nil {
			entries := pastest.Entries(b)
			b["entry"] = append(entries, map[string]any{"fullUrl": "urn:uuid:bmi", "resource": map[string]any{
				"resourceType": "Observation", "status": "final", "code": map[string]any{"coding": []any{
					map[string]any{"system": "http://loinc.org", "code": "39156-5"}}},
				"valueQuantity": map[string]any{"value": tc.bmi, "unit": "kg/m2"}}})
		}
		raw, _ := json.Marshal(b)
		got, cite, err := svc.Ask(context.Background(), "EXAMPLE-BMI-40", p, "cpt|43644", raw)
		if err != nil || got != tc.want {
			t.Fatalf("BMI %v: %q %+v %v", tc.bmi, got, cite, err)
		}
	}
	w := httptest.NewRecorder()
	handler("k").ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
}
