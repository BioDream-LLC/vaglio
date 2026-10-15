package outside

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

const src = `policy knee
  title "Knee"
  version 1
  source "s"
  service cpt 27447

evidence iq
  text "The licensed criterion is met"
  clause "c"
  find criteria acme "KNEE-3"

evidence ortho
  text "Ordered by an orthopaedic surgeon"
  clause "c"
  find provider specialty 207X00000X

evidence net
  text "In the plan's network"
  clause "c"
  find provider in-network

criterion unmet
  text "Not met."
  clause "c"
  defeated by iq

decision approve approves
  text "Approve."
  clause "c"
  defeated by unmet

decision deny denies
  text "Deny."
  clause "c"
  defeated by iq
`

func request(t *testing.T, npi string) (*pas.Request, []byte) {
	t.Helper()
	b := pastest.Bundle([]string{"27447"})
	if npi != "" {
		pastest.Map(pastest.Entries(b)[3])["identifier"] = []any{map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": npi}}
	}
	raw := []byte(pastest.JSON(b))
	var back map[string]any
	json.Unmarshal(raw, &back)
	req, problems := pas.Read(back, false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return req, raw
}

func TestFindings(t *testing.T) {
	p, err := policy.Parse("knee.vaglio", src)
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"result":"met","reason":"Kellgren-Lawrence grade 4 on the radiology report","reference":"2026.1"}`
	var got map[string]any
	crit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(answer))
	}))
	defer crit.Close()
	svc, err := NewCriteriaService("acme", crit.URL, "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "network.csv"), []byte("npi,name\n1234567893,Example Clinic\n"), 0o600)
	net, err := LoadNetwork(filepath.Join(dir, "network.csv"))
	if err != nil {
		t.Fatal(err)
	}
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("number") == "1234567893" {
			w.Write([]byte(`{"result_count":1,"results":[{"taxonomies":[{"code":"207X00000X"},{"code":"208600000X"}]}]}`))
			return
		}
		w.Write([]byte(`{"result_count":0,"results":[]}`))
	}))
	defer reg.Close()
	s := &Sources{Criteria: map[string]*CriteriaService{"acme": svc}, Network: net, Registry: &NPPES{URL: reg.URL + "/"}}
	req, raw := request(t, "1234567893")
	fs, notes := s.Findings(context.Background(), p, req, raw, "cpt|27447")
	if len(notes) != 0 || len(fs) != 3 {
		t.Fatalf("%+v %v", fs, notes)
	}
	for _, f := range fs {
		if f.Finding != decide.Present {
			t.Errorf("%s: %s", f.Argument, f.Finding)
		}
	}
	if got["criterion"] != "KNEE-3" || got["bundle"] == nil {
		t.Errorf("sent %v", got)
	}
	x := evidence.ExtractWith(p, req, fs)
	res, err := decide.Decide(p, x.Request(p.ID))
	if err != nil || res.Outcome != decide.Approved {
		t.Fatalf("%+v %v", res, err)
	}
	if proof, _ := json.Marshal(res.Proof); !strings.Contains(string(proof), "Kellgren") || !strings.Contains(string(proof), "criteria:acme/KNEE-3 (2026.1)") {
		t.Fatalf("the proof does not cite the criteria service: %s", proof)
	}

	// An unknown provider, out of network: the registry says nothing (not addressed), the network says absent.
	req, raw = request(t, "1245319599")
	fs, _ = s.Findings(context.Background(), p, req, raw, "cpt|27447")
	byArg := map[string]decide.Finding{}
	for _, f := range fs {
		byArg[f.Argument] = f.Finding
	}
	if byArg["net"] != decide.Absent || byArg["ortho"] != "" {
		t.Fatalf("%v", byArg)
	}
	// not-met without a reason is not used; unknown is nothing; an error is a note.
	answer = `{"result":"not-met"}`
	fs, notes = s.Findings(context.Background(), p, req, raw, "cpt|27447")
	for _, f := range fs {
		if f.Argument == "iq" {
			t.Fatal("not-met without its reason was used")
		}
	}
	if len(notes) == 0 {
		t.Fatal("no note")
	}
	answer = `{"result":"unknown"}`
	if fs, _ = s.Findings(context.Background(), p, req, raw, "cpt|27447"); len(fs) != 1 { // only the network's
		t.Fatalf("%+v", fs)
	}
	// No sources configured: notes, no findings.
	fs, notes = (&Sources{}).Findings(context.Background(), p, req, raw, "cpt|27447")
	if len(fs) != 0 || len(notes) != 3 {
		t.Fatalf("%+v %v", fs, notes)
	}
	if _, err := NewCriteriaService("x", "http://criteria.example.com", "", nil); err == nil {
		t.Fatal("plain http off this machine")
	}
	os.WriteFile(filepath.Join(dir, "bad.csv"), []byte("1234567890\n"), 0o600)
	if _, err := LoadNetwork(filepath.Join(dir, "bad.csv")); err == nil {
		t.Fatal("an invalid NPI in the network file")
	}
}
