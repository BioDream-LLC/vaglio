package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/letter"
)

func TestLetters(t *testing.T) {
	e := newEnv(t, "")
	viewer := e.signedIn(t, "viewer")
	get := func(id, lang string) (*letter.Letter, int) {
		w := do(viewer, "GET", "/api/cases/"+id+"/letter?lang="+lang, "")
		var l letter.Letter
		json.Unmarshal(w.Body.Bytes(), &l)
		return &l, w.Code
	}
	text := func(l *letter.Letter) string {
		var b strings.Builder
		b.WriteString(l.Title)
		for _, s := range l.Sections {
			b.WriteString("\n" + s.Heading + "\n" + strings.Join(s.Paragraphs, "\n") + "\n" + strings.Join(s.Items, "\n") + "\n" + s.Quote)
		}
		return b.String()
	}

	d := submit(t, e, request("", "", "", denies...))
	l, code := get(d.Case, "en")
	if code != 200 {
		t.Fatalf("%d", code)
	}
	body := text(l)
	for _, want := range []string{"Not approved", "did not approve", "A repeat MRI within six months", "appeal",
		"Shown not to be the case:", "What would change this decision"} {
		if !strings.Contains(body, want) {
			t.Errorf("the denial letter lacks %q:\n%s", want, body)
		}
	}
	if l.Receipt != d.Receipt || l.Quoted != "" {
		t.Errorf("receipt %q, want %q; quoted %q", l.Receipt, d.Receipt, l.Quoted)
	}
	es, _ := get(d.Case, "es")
	if !strings.Contains(text(es), "No aprobada") || !strings.Contains(text(es), "apelación") || es.Quoted == "" {
		t.Errorf("the Spanish letter:\n%s", text(es))
	}

	p := submit(t, e, request("", "", "", pends...))
	l, _ = get(p.Case, "en")
	if body := text(l); !strings.Contains(body, "More information needed") || !strings.Contains(body, "What is needed") ||
		!strings.Contains(body, "Nothing missing is held against you") || strings.Contains(body, "appeal:") {
		t.Errorf("the pend letter:\n%s", body)
	}
	if _, code := get(p.Case, "fr"); code != 400 {
		t.Errorf("a letter in French: %d", code)
	}

	// The denial appealed: the letter says when it was received and when it is due, then, upheld, the further review.
	reviewer := e.signedIn(t, "reviewer")
	if w := do(reviewer, "POST", "/api/cases/"+d.Case+"/appeal/received",
		`{"from":"member","received":"2026-10-15T10:00:00Z","reason":"My doctor says it is needed."}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	l, _ = get(d.Case, "en")
	if body := text(l); !strings.Contains(body, "Your appeal was received on October 15, 2026") ||
		!strings.Contains(body, "November 14, 2026") || strings.Contains(body, "can ask your health plan for an appeal") {
		t.Errorf("the letter while appealed:\n%s", body)
	}
	e.users.SetClinician("reviewer", "1234567893", "MD")
	if w := do(e.signedIn(t, "reviewer"), "POST", "/api/cases/"+d.Case+"/appeal",
		`{"outcome":"upheld","reason":"A repeat MRI within six months without new findings."}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	l, _ = get(d.Case, "en")
	if body := text(l); !strings.Contains(body, "The appeal's reason") || !strings.Contains(body, "independent organization") {
		t.Errorf("the letter after the appeal:\n%s", body)
	}
}
