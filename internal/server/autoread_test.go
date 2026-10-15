package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

// waitFor polls until f holds or a few seconds pass.
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func withNote(b map[string]any, id, note string) map[string]any {
	doc := pastest.Document(id, "Clinic note")
	pastest.Map(doc)["content"] = []any{map[string]any{"attachment": map[string]any{"contentType": "text/plain",
		"data": base64.StdEncoding.EncodeToString([]byte(note))}}}
	b["entry"] = append(pastest.Entries(b), doc)
	return b
}

// A pended request's documents are read on arrival; when the quote-checked findings approve, the case is approved with
// no person and delivered; when they do not, it waits with the proposals ready. Every automatic approval is sampled at
// 100%, checked once, and a disagreement is counted.
func TestAutomaticReading(t *testing.T) {
	const worse = "Since the last study her leg weakness is much worse."
	e := newEnvWith(t, "", func(c *Config) {
		c.Reader = &DocumentModel{Name: "fake", Model: fakeModel(`{"findings":[
			{"evidence":"new_or_worsening_symptoms","finding":"present","quote":"` + worse + `"}]}`)}
		c.AutoRead = &AutoRead{Approve: true, SamplePercent: 100, Workers: 1}
	})
	// Therapy failed and a recent MRI, with nothing said of change: pends. The note says the symptoms are worse.
	a := submit(t, e, withNote(request("", "", "", therapy, priorYes), "n1", "Clinic note. "+worse))
	if a.Decision != "pend" {
		t.Fatalf("%s", a.Decision)
	}
	waitFor(t, func() bool { c, _ := e.cases.Get(a.Case); return c.Status == cases.Reviewed })
	c, _ := e.cases.Get(a.Case)
	if !c.Review.Automatic || c.Review.Decision != "approve" || !c.Review.Sampled || !strings.Contains(c.Review.Reason, worse) ||
		len(c.Readings) != 1 {
		t.Fatalf("review: %+v", c.Review)
	}
	if len(e.delivered) != 0 {
		t.Fatal("delivered without a PAS request id") // the test requests carry none
	}
	// A note that says nothing the policy reads: read, and left for a reviewer.
	e2 := newEnvWith(t, "", func(c *Config) {
		c.Reader = &DocumentModel{Name: "fake", Model: fakeModel(`{"findings":[]}`)}
		c.AutoRead = &AutoRead{Approve: true, Workers: 1}
	})
	b := submit(t, e2, withNote(request("", "", "", therapy, priorYes), "n2", "Clinic note. Doing well."))
	waitFor(t, func() bool { c, _ := e2.cases.Get(b.Case); return len(c.Readings) == 1 })
	if c, _ := e2.cases.Get(b.Case); c.Status != cases.Pended || c.Review != nil {
		t.Fatalf("approved on nothing: %+v", c.Review)
	}
	// The after-the-fact check: a viewer may not, a disagreement needs its reason, and it is counted.
	reviewer, viewer := e.signedIn(t, "reviewer"), e.signedIn(t, "viewer")
	var rep AutomaticReport
	json.Unmarshal(do(viewer, "GET", "/api/reports/automatic", "").Body.Bytes(), &rep)
	if !rep.On || rep.Approved != 1 || len(rep.ToCheck) != 1 || rep.Reader.Read != 1 || rep.Reader.Approved != 1 {
		t.Fatalf("report: %+v %+v", rep, rep.Reader)
	}
	if w := do(viewer, "POST", "/api/cases/"+a.Case+"/qa", `{"agree":true}`); w.Code != 403 {
		t.Errorf("a viewer checked: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/qa", `{"agree":false}`); w.Code != 400 {
		t.Errorf("a disagreement without a reason: %d", w.Code)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/qa", `{"agree":false,"note":"the note is about another patient"}`); w.Code != 200 {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	if w := do(reviewer, "POST", "/api/cases/"+a.Case+"/qa", `{"agree":true}`); w.Code != 400 {
		t.Errorf("checked twice: %d", w.Code)
	}
	json.Unmarshal(do(viewer, "GET", "/api/reports/automatic", "").Body.Bytes(), &rep)
	if rep.Checked != 1 || rep.Disagree != 1 || *rep.DisagreePercent != 100 || len(rep.Disputed) != 1 || len(rep.ToCheck) != 0 {
		t.Fatalf("after the check: %+v", rep)
	}
	// The receipt says the documents decided it; the letter says so in words.
	var rc map[string]any
	rs := receiptsFor(e.log.ForCase(a.Case))
	json.Unmarshal(do(viewer, "GET", "/api/receipts/"+rs[len(rs)-1].Receipt, "").Body.Bytes(), &rc)
	if rc["by"] != "documents" {
		t.Errorf("receipt: %v", rc)
	}
	var l map[string]any
	json.Unmarshal(do(reviewer, "GET", "/api/cases/"+a.Case+"/letter", "").Body.Bytes(), &l)
	if raw, _ := json.Marshal(l); !strings.Contains(string(raw), "documents your provider sent") {
		t.Errorf("letter: %s", raw)
	}
	if !sample("x", 100) || sample("x", 0) {
		t.Error("sample")
	}
}
