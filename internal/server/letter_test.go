package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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

// With the plan's letter settings, a denial gives the plan's own appeal deadline as a date (the line of business's when
// it has one), its addresses and numbers, a fast appeal's hours, member services and the receipt page; an extension
// says how long and why.
func TestLettersWithThePlansSettings(t *testing.T) {
	plan := &letter.Plan{Name: "Example Health Plan", Phone: "1-800-555-0100", TTY: "711", Hours: "8 a.m. to 8 p.m.",
		CheckURL: "https://plan.example/#/receipt",
		Appeal: letter.Appeal{Days: 60, ExpeditedHours: 72, Mail: []string{"Appeals, PO Box 1", "Springfield, IL 62701"},
			Fax: "1-800-555-0101", Portal: "https://plan.example/appeals",
			External: map[string]string{"en": "You can ask the state for a fair hearing within 120 days."}},
		Lines: map[string]letter.Appeal{"medicare-advantage": {Days: 65}}}
	e := newEnvWith(t, "", func(c *Config) { c.Letters = plan; c.ExtensionDays = 14 })
	viewer := e.signedIn(t, "viewer")
	get := func(id, lang string) string {
		w := do(viewer, "GET", "/api/cases/"+id+"/letter?lang="+lang, "")
		var l letter.Letter
		json.Unmarshal(w.Body.Bytes(), &l)
		var b strings.Builder
		for _, s := range l.Sections {
			b.WriteString(s.Heading + "\n" + strings.Join(s.Paragraphs, "\n") + "\n" + strings.Join(s.Items, "\n") + "\n" +
				strings.Join(s.After, "\n") + "\n")
		}
		return b.String()
	}
	in := func(days int) string {
		d := time.Now().AddDate(0, 0, days)
		return fmt.Sprintf("%s %d, %d", d.Month(), d.Day(), d.Year())
	}
	d := submit(t, e, request("", "", "", denies...))
	body := get(d.Case, "en")
	for _, want := range []string{"ask Example Health Plan for an appeal", "Ask by " + in(60), "(60 days from the date",
		"By mail: Appeals, PO Box 1, Springfield, IL 62701", "By fax: 1-800-555-0101", "Online: https://plan.example/appeals",
		"within 72 hours", "Call Example Health Plan at 1-800-555-0100 (TTY 711), 8 a.m. to 8 p.m.",
		"enter the receipt at https://plan.example/#/receipt"} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "member materials say how") {
		t.Error("the generic words are still there")
	}
	if es := get(d.Case, "es"); !strings.Contains(es, "Pídala a más tardar el") || !strings.Contains(es, "Por fax: 1-800-555-0101") {
		t.Errorf("Spanish:\n%s", es)
	}
	ma := submit(t, e, withCoverage(request("", "", "", denies...), "111"))
	if body := get(ma.Case, "en"); !strings.Contains(body, "Ask by "+in(65)) {
		t.Errorf("Medicare Advantage's own deadline:\n%s", body)
	}
	p := submit(t, e, request(testNPI, "", "", pends...))
	rev := e.signedIn(t, "reviewer")
	if w := do(rev, "POST", "/api/cases/"+p.Case+"/extend", `{"days":14,"reason":"evidence","note":"Waiting for the outside MRI report."}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if body := get(p.Case, "en"); !strings.Contains(body, "up to 14 more days") || !strings.Contains(body, "Why: Waiting for the outside MRI report.") {
		t.Errorf("extension:\n%s", body)
	}
}
