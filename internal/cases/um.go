package cases

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biodream-llc/vaglio/internal/audit"
)

// A plan's own case-review (utilization management) system, where its nurses and physicians already work cases: a
// case Vaglio pends is opened there (internal/um), and the reviewer's decision there comes back and closes it here. The
// case-review system stays where the person decides; Vaglio records what they decided, never decides there for them.

// ActUMOpened records the case opened for this one in the plan's case-review system.
const ActUMOpened = "case.um_opened"

// UMLink is a case opened in the plan's case-review system.
type UMLink struct {
	System string `json:"system"`        // the connector's name, as the server's -um file gives it
	Ref    string `json:"ref"`           // that system's id for the case
	URL    string `json:"url,omitempty"` // where the case is, in that system, when it says
	Time   string `json:"time"`
}

func (s *Store) applyUM(r audit.Record) (bool, error) {
	if r.Action != ActUMOpened {
		return false, nil
	}
	c, ok := s.cases[r.Case]
	if !ok {
		return true, fmt.Errorf("case-review link of unknown case %s", r.Case)
	}
	var l UMLink
	if err := json.Unmarshal(r.Data, &l); err != nil {
		return true, err
	}
	l.Time = r.Time
	c.UM, c.Updated = &l, r.Time
	return true, nil
}

// ErrLinked is a second case-review case asked for one case.
var ErrLinked = errors.New("this case is open in the case-review system already")

// LinkUM records the case opened for id in the plan's case-review system.
func (s *Store) LinkUM(id string, l UMLink) (*Case, error) {
	l.Ref = strings.TrimSpace(l.Ref)
	if l.System == "" || l.Ref == "" {
		return nil, errors.New("a case-review link names its system and that system's id for the case")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if c.UM != nil {
		return nil, ErrLinked
	}
	if _, err := s.write("um:"+l.System, ActUMOpened, id, l); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// ByUMRef is the case linked to ref in a case-review system.
func (s *Store) ByUMRef(system, ref string) (*Case, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		if c := s.cases[id]; c.UM != nil && c.UM.System == system && c.UM.Ref == ref {
			out := *c
			return &out, true
		}
	}
	return nil, false
}

// ReviewVia records a decision made in the plan's case-review system: the reviewer's name there (by), the system and
// its id for the case in rv.Via and rv.Reference. The record is the system's, not a Vaglio account's.
func (s *Store) ReviewVia(id, by string, rv Review) (*Case, error) {
	if rv.Via == "" || strings.TrimSpace(by) == "" {
		return nil, errors.New("a decision from the case-review system names the system and the reviewer")
	}
	return s.review(id, "um:"+rv.Via, strings.TrimSpace(by), rv)
}
