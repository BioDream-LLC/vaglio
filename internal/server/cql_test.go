package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/cqlrun"
	"github.com/biodream-llc/vaglio/internal/outside"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// TestCQLEvidence: a policy whose evidence is a CQL define decides on the request's own record: a COPD Condition
// approves, its absence denies, and the proof cites the define and the Condition it read.
func TestCQLEvidence(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cql"), 0o755)
	os.WriteFile(filepath.Join(dir, "cql", "o2.cql"), []byte("library O2 version '1'\nusing FHIR version '4.0.1'\n"+
		"context Patient\ndefine \"COPD\": exists ([Condition] C where C.code.coding.code = 'J44.9' "+
		"and C.clinicalStatus.coding.code = 'active')\n"), 0o644)
	pin, _ := cqlrun.Pin(dir, "cql/o2.cql")
	p, err := policy.Parse("o2.vaglio", "policy o2\n  title \"Oxygen\"\n  version 1.0\n  source \"Example\"\n"+
		"  service cpt 72148\n  "+pin+"\n\nevidence copd\n  text \"COPD\"\n  clause \"Covered for COPD.\"\n"+
		"  find cql O2 \"COPD\"\n\ncriterion no_copd\n  text \"No COPD\"\n  clause \"Covered for COPD.\"\n"+
		"  defeated by copd\n\ndecision approve approves\n  text \"Approve\"\n  clause \"Covered for COPD.\"\n"+
		"  defeated by no_copd\n\ndecision deny denies\n  text \"Deny\"\n  clause \"Covered for COPD.\"\n  defeated by copd\n")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, "", func(c *Config) {
		c.Policies = map[string]*policy.Policy{p.ID: p}
		c.Outside = &outside.Sources{CQL: &cqlrun.Cache{Dir: dir}}
	})
	with := pastest.Bundle([]string{"72148"}, pastest.Condition("c1", "J44.9", "COPD", "active", "confirmed"))
	a := submit(t, e, with)
	if a.Decision != "approve" {
		t.Fatalf("with COPD: %+v", a)
	}
	c, _ := e.cases.Get(a.Case)
	quoted := false
	for _, f := range c.Evidence.Findings {
		quoted = quoted || (strings.HasPrefix(f.Reference, "cql:O2.COPD") && strings.Contains(f.Quote, "Condition/c1"))
	}
	if !quoted {
		t.Errorf("the proof does not cite the define: %+v", c.Evidence.Findings)
	}
	without := pastest.Bundle([]string{"72148"}, pastest.Condition("c2", "I10", "HTN", "active", "confirmed"))
	pastest.Map(pastest.Entries(without)[0])["identifier"] = []any{map[string]any{"system": "x", "value": "2"}}
	if a := submit(t, e, without); a.Decision != "deny" {
		t.Fatalf("without COPD: %+v", a)
	}
}
