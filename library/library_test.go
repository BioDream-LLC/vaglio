package library

import (
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/precedence"
)

// Every library policy loads, is in force, is named by its file, and no two of them tie for a request.
func TestPolicies(t *testing.T) {
	ps, err := Policies()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 12 {
		t.Errorf("%d library policies", len(ps))
	}
	var all []*policy.Policy
	for id, p := range ps {
		if _, err := Source(id); err != nil {
			t.Errorf("%s: its file is not %s%s", id, id, policy.Ext)
		}
		all = append(all, p)
	}
	if why := precedence.Conflicts(all); why != "" {
		t.Error(why)
	}
}
