package dtr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func load(t *testing.T, name string) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile("../../policies/" + name + policy.Ext)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOneSource: every question a decision reads is in the questionnaire, and nothing else is.
func TestOneSource(t *testing.T) {
	for _, name := range []string{"lumbar_mri", "bariatric_surgery"} {
		p := load(t, name)
		q, err := For(p)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, a := range p.Arguments {
			for _, s := range a.Find {
				if s.Question != "" {
					want[s.Question] = true
				}
			}
		}
		if len(q.Item) != len(want) {
			t.Errorf("%s: %d items, policy reads %d questions", name, len(q.Item), len(want))
		}
		for _, it := range q.Item {
			if !want[it.LinkID] || it.Type != "boolean" || !strings.HasSuffix(it.Text, "?") || it.Required {
				t.Errorf("%s: item %+v", name, it)
			}
		}
		if q.URL != p.Questionnaire || strings.Contains(q.ID, "_") || q.Status != "active" {
			t.Errorf("%s: %s %s %s", name, q.URL, q.ID, q.Status)
		}
		if _, err := json.Marshal(q); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAnsweringEveryItemNeverPendsForAnswers: whatever yes/no answers a provider gives to every item, the decision never
// pends for a question the questionnaire could have asked.
func TestAnsweringEveryItemNeverPendsForAnswers(t *testing.T) {
	p := load(t, "lumbar_mri")
	q, _ := For(p)
	byLink := map[string]string{}
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.Question != "" {
				byLink[s.Question] = a.Name
			}
		}
	}
	for mask := 0; mask < 1<<len(q.Item); mask++ {
		req := decide.Request{Policy: p.ID}
		for i, it := range q.Item {
			f := decide.Absent
			if mask&(1<<i) != 0 {
				f = decide.Present
			}
			req.Evidence = append(req.Evidence, decide.Assertion{Argument: byLink[it.LinkID], Finding: f,
				Citation: decide.Citation{Reference: "QuestionnaireResponse/q#" + it.LinkID, Quote: it.Text}})
		}
		res, err := decide.Decide(p, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Missing {
			for _, it := range q.Item {
				if byLink[it.LinkID] == m.Argument {
					t.Errorf("answers %b: still missing %s, which the questionnaire asks", mask, m.Argument)
				}
			}
		}
	}
}

func TestNoQuestionnaire(t *testing.T) {
	p := load(t, "lumbar_mri")
	p.Questionnaire = ""
	if _, err := For(p); err != ErrNoQuestionnaire {
		t.Errorf("%v", err)
	}
}
