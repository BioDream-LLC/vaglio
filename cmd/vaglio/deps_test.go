package main

import (
	"os"
	"strings"
	"testing"
)

// TestOnlyPerfuse: the program's only Go dependency is Perfuse's published packages (BioDream's own): the threat
// model's supply-chain claim, kept true by a test rather than by reading go.mod.
func TestOnlyPerfuse(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	inRequire := false
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		switch {
		case len(f) == 0:
		case f[0] == "require" && len(f) > 1 && f[1] == "(":
			inRequire = true
		case inRequire && f[0] == ")":
			inRequire = false
		case f[0] == "require" && len(f) > 1:
			if f[1] != "github.com/biodream-llc/perfuse" {
				t.Errorf("a dependency other than Perfuse: %s", line)
			}
		case inRequire && f[0] != "github.com/biodream-llc/perfuse":
			t.Errorf("a dependency other than Perfuse: %s", line)
		case f[0] == "replace":
			t.Errorf("a replace directive: %s", line)
		}
	}
	if _, err := os.Stat("../../go.sum"); err == nil {
		sum, _ := os.ReadFile("../../go.sum")
		for _, line := range strings.Split(strings.TrimSpace(string(sum)), "\n") {
			if line != "" && !strings.HasPrefix(line, "github.com/biodream-llc/perfuse ") {
				t.Errorf("go.sum names another module: %s", line)
			}
		}
	}
}
