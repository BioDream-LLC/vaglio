// Package dtr makes a policy's Da Vinci DTR questionnaire from the policy itself.
//
// A payer's questionnaire and its medical policy are usually two documents kept by two teams, and they drift: the form
// asks what the policy no longer reads, and the policy reads what the form never asks, so a complete-looking request
// pends anyway. Here there is one source. Every question in the questionnaire is one a decision reads - a yes/no item
// whose linkId is the policy's - and every question the policy reads is in it, so a provider who answers every item has
// sent everything the policy can ask for in a questionnaire.
//
// Evidence the policy finds only in the record (a Condition, a Procedure, an Observation) is not asked: the record is
// the better source, and an answer would only be one more thing to contradict it.
package dtr

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/biodream-llc/vaglio/internal/policy"
)

// Item is one question.
type Item struct {
	LinkID   string `json:"linkId"`
	Text     string `json:"text"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Questionnaire is the FHIR R4 Questionnaire for a policy.
type Questionnaire struct {
	ResourceType string   `json:"resourceType"`
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	Version      string   `json:"version"`
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	SubjectType  []string `json:"subjectType"`
	Description  string   `json:"description"`
	Item         []Item   `json:"item"`
}

var idUnsafe = regexp.MustCompile(`[^A-Za-z0-9.-]`)

// ErrNoQuestionnaire is a policy that names no questionnaire, so none of its answers would be read.
var ErrNoQuestionnaire = errors.New("the policy names no questionnaire (questionnaire in its header), so no answers to one would be read")

// For returns the questionnaire whose answers the policy reads, in the order the policy declares its evidence.
func For(p *policy.Policy) (*Questionnaire, error) {
	if p.Questionnaire == "" {
		return nil, ErrNoQuestionnaire
	}
	q := &Questionnaire{
		ResourceType: "Questionnaire",
		// A FHIR id allows letters, digits, '-' and '.', up to 64; a policy id may hold '_'.
		ID:          idUnsafe.ReplaceAllString(p.ID, "-"),
		URL:         p.Questionnaire,
		Version:     p.Version,
		Name:        name(p.ID),
		Title:       p.Title,
		Status:      "active",
		SubjectType: []string{"Patient"},
		Description: fmt.Sprintf("The questions policy %s v%s reads, made from the policy by Vaglio: answer each yes or no only "+
			"when the record supports the answer, and leave unanswered what it does not address. An unanswered question is "+
			"asked again; it is never a reason to deny.", p.ID, p.Version),
		Item: []Item{},
	}
	if p.Status == policy.Draft {
		q.Status = "draft"
	}
	for _, a := range p.Arguments {
		if a.Kind != policy.Evidence {
			continue
		}
		for _, s := range a.Find {
			if s.Question != "" {
				// Not required: "not addressed" is a legitimate answer, and forcing yes or no would make a provider
				// guess, which is exactly what the absence rule exists to stop.
				q.Item = append(q.Item, Item{LinkID: s.Question, Text: question(a.Text), Type: "boolean"})
			}
		}
	}
	if len(q.Item) == 0 {
		return nil, fmt.Errorf("policy %s names a questionnaire but reads no question from it", p.ID)
	}
	return q, nil
}

// question words a piece of evidence as a question: "Six weeks of physical therapy." becomes "Six weeks of physical
// therapy?". Text already ending in a question mark is kept.
func question(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasSuffix(t, "?") {
		return t
	}
	return strings.TrimRight(t, ".") + "?"
}

// name is a policy id as a FHIR computable name: lumbar_mri becomes LumbarMri.
func name(id string) string {
	var b strings.Builder
	for _, w := range strings.Split(id, "_") {
		if w != "" {
			b.WriteString(strings.ToUpper(w[:1]) + w[1:])
		}
	}
	return b.String()
}
