package server

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/feed"
)

func TestExportEvents(t *testing.T) {
	e := newEnv(t, "")
	a := pendedCase(t, e, "resp-1")
	reviewer, admin := e.signedIn(t, "reviewer"), e.signedIn(t, "admin")
	do(reviewer, "GET", "/api/cases/"+a.Case, "") // a view: not an event
	do(reviewer, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"approve","reason":"ok"}`)
	if w := do(reviewer, "GET", "/api/export/events", ""); w.Code != 403 {
		t.Errorf("a reviewer exported: %d", w.Code)
	}
	w := do(admin, "GET", "/api/export/events?after=0", "")
	var types []string
	sc := bufio.NewScanner(strings.NewReader(w.Body.String()))
	for sc.Scan() {
		var ev feed.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Hash == "" {
			t.Fatalf("%v %s", err, sc.Text())
		}
		types = append(types, ev.Type)
	}
	if w.Header().Get("Content-Type") != "application/x-ndjson" || strings.Join(types, " ") != "case.opened case.reviewed" {
		t.Errorf("%s %v", w.Header().Get("Content-Type"), types)
	}
	last := e.log.Records(0, 0)
	if last[len(last)-1].Action != "export.read" {
		t.Errorf("the export was not recorded")
	}
}
