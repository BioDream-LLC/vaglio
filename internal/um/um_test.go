package um

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestFHIRTask: the Task posted carries the case, the due date, the missing evidence and the proof as a PDF; its id
// comes from the Location header; a Task still in progress is no decision; once completed by a physician its decision,
// reason, NPI and credential are read, and the same from a pushed Bundle.
func TestFHIRTask(t *testing.T) {
	var mu sync.Mutex
	var posted task
	status := "in-progress"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fhir-token" {
			http.Error(w, "no", 401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/fhir/Task":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &posted)
			w.Header().Set("Location", "http://"+r.Host+"/fhir/Task/t-7/_history/1")
			w.WriteHeader(201)
		case r.Method == "GET" && r.URL.Path == "/fhir/Task/t-7":
			json.NewEncoder(w).Encode(completed(status))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	conn, err := Config{Connector: "fhir", URL: srv.URL + "/fhir/", TokenEnv: "T"}.Connect(func(string) (string, error) {
		return "fhir-token", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := Packet{Case: "c1", Why: "Needs the report.", Due: "2026-10-30T00:00:00Z", Expedited: true, Patient: "Patient/p1",
		Missing: []Item{{Evidence: "report", Text: "The prior study's report"}}, ProofPDF: []byte("%PDF-1.4 x")}
	o, err := conn.Open(ctx, p)
	if err != nil || o.Ref != "t-7" || !strings.HasSuffix(o.URL, "/fhir/Task/t-7") {
		t.Fatalf("%+v %v", o, err)
	}
	inputs := map[string]param{}
	for _, in := range posted.Input {
		inputs[in.Type.Text] = in
	}
	if posted.Identifier[0].System != CaseSystem || posted.Identifier[0].Value != "c1" || posted.Priority != "stat" ||
		posted.Restriction.Period.End != p.Due || posted.For.Reference != "Patient/p1" ||
		inputs["missing"].ValueString != "The prior study's report" || inputs["proof"].ValueAttachment == nil {
		t.Fatalf("posted %+v", posted)
	}
	if _, decided, err := conn.Poll(ctx, "t-7"); err != nil || decided {
		t.Fatalf("in progress: %v %v", decided, err)
	}
	mu.Lock()
	status = "completed"
	mu.Unlock()
	d, decided, err := conn.Poll(ctx, "t-7")
	if err != nil || !decided || d.Decision != "deny" || d.Case != "c1" || d.NPI != "1234567893" || d.Credential != "MD" ||
		d.Reviewer != "Dr. Rivera" || d.Reason != "No new findings." {
		t.Fatalf("%+v %v %v", d, decided, err)
	}
	raw, _ := json.Marshal(map[string]any{"resourceType": "Bundle", "entry": []any{
		map[string]any{"resource": map[string]any{"resourceType": "SubscriptionStatus"}},
		map[string]any{"resource": completed("completed")}}})
	ds, err := conn.Parse(raw)
	if err != nil || len(ds) != 1 || ds[0].Ref != "t-7" || ds[0].Decision != "deny" {
		t.Fatalf("%+v %v", ds, err)
	}
	if _, err := conn.Parse([]byte(`{"resourceType":"Task","id":"x","status":"completed","output":[]}`)); err == nil {
		t.Error("a Task completed with no decision was read as one")
	}
}

func completed(status string) task {
	return task{ResourceType: "Task", ID: "t-7", Status: status, Intent: "order",
		Identifier: []identifier{{System: CaseSystem, Value: "c1"}},
		Owner:      &reference{Display: "Dr. Rivera", Identifier: &identifier{System: npiSystem, Value: "1234567893"}},
		Output: []param{{Type: named("decision"), ValueCode: "deny"}, {Type: named("reason"), ValueString: "No new findings."},
			{Type: named("reviewer-credential"), ValueString: "MD"}}}
}

// TestConfigRefused: plain http to another machine, a field the packet does not have, a decision word mapped to
// neither approve nor deny, and an unknown connector are all refused when the server starts, not when a case comes.
func TestConfigRefused(t *testing.T) {
	none := func(string) (string, error) { return "", nil }
	for name, c := range map[string]Config{
		"http":      {Connector: "rest", URL: "http://um.example/cases"},
		"field":     {Connector: "rest", URL: "https://um.example/cases", Fields: map[string]string{"x": "patiant"}},
		"value":     {Connector: "rest", URL: "https://um.example/cases", DecisionValues: map[string]string{"P": "pend"}},
		"part":      {Connector: "rest", URL: "https://um.example/cases", DecisionFields: map[string]string{"who": "x"}},
		"connector": {Connector: "soap", URL: "https://um.example/cases"},
		"console":   {Connector: "rest", URL: "https://um.example/cases", Console: "http://vaglio.example"},
	} {
		if _, err := c.Connect(none); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (Config{Connector: "rest", URL: "https://um.example/cases", Fields: map[string]string{"a.b": "case",
		"kind": "=prior-auth"}}).Connect(none); err != nil {
		t.Error(err)
	}
}
