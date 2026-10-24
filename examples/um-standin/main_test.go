package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/um"
)

// TestExampleConfig: examples/um.json, pointed at this stand-in, opens a case and reads a reviewer's decision back.
func TestExampleConfig(t *testing.T) {
	s := &server{cases: map[string]*Case{}, key: "k"}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	cfg, err := um.LoadConfig("../um.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.URL, cfg.DecisionURL = srv.URL+"/cases", srv.URL+"/cases/{ref}"
	conn, err := cfg.Connect(func(string) (string, error) { return "k", nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	o, err := conn.Open(ctx, um.Packet{Case: "c1", Why: "Needs the report.", ProofPDF: []byte("%PDF-1.4")})
	if err != nil || o.Ref != "UM-1" || string(s.cases["UM-1"].proof) != "%PDF-1.4" {
		t.Fatalf("%+v %v", o, err)
	}
	if _, decided, _ := conn.Poll(ctx, "UM-1"); decided {
		t.Fatal("decided before anyone did")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/decide/UM-1", strings.NewReader("decision=deny&note=No+new+findings&name=Dr+Rivera&npi=1234567893&credential=MD"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.routes().ServeHTTP(w, r)
	d, decided, err := conn.Poll(ctx, "UM-1")
	if err != nil || !decided || d.Decision != "deny" || d.Case != "c1" || d.NPI != "1234567893" || d.Reviewer != "Dr Rivera" {
		t.Fatalf("%+v %v %v", d, decided, err)
	}
}
