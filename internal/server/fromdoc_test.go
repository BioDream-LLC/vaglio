package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/draft"
)

// answers is a model that answers from a list, then repeats the last.
type answers []string

func (a *answers) Complete(context.Context, []draft.Message) (string, error) {
	out := (*a)[0]
	if len(*a) > 1 {
		*a = (*a)[1:]
	}
	return out, nil
}

func TestDraftFromDocument(t *testing.T) {
	doc, err := os.ReadFile("../../library/sources/L36575-TKA.txt")
	if err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile("../../library/tka.vaglio")
	// First a draft with a clause the document does not have, then the library's reading of it.
	bad := strings.Replace(string(good), `clause "Distal femur fracture; or"`, `clause "A broken thigh bone"`, 1)
	m := &answers{"```vaglio\n" + bad + "\n```", "```vaglio\n" + string(good) + "\n```"}
	e2 := newEnvWith(t, "", func(c *Config) {
		c.PolicyDir = t.TempDir()
		c.Drafter = &Drafter{Model: m, Name: "scripted"}
	})
	admin := e2.signedIn(t, "admin")
	body, _ := json.Marshal(map[string]string{"id": "tka", "name": "L36575-TKA.txt", "contentType": "text/plain",
		"data": base64.StdEncoding.EncodeToString(doc)})
	w := do(admin, "POST", "/api/drafting", string(body))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), `"rounds": null`) {
		t.Errorf("rounds is null before the first round ends: %s", w.Body)
	}
	var job DraftJob
	json.Unmarshal(w.Body.Bytes(), &job)
	for i := 0; i < 100 && job.State == "running"; i++ {
		time.Sleep(20 * time.Millisecond)
		json.Unmarshal(do(admin, "GET", "/api/drafting/"+job.ID, "").Body.Bytes(), &job)
	}
	if job.State != "passed" || len(job.Rounds) != 2 || !job.Saved || job.Check == nil {
		t.Fatalf("%+v", job)
	}
	if f := job.Rounds[0].Findings; len(f) != 1 || f[0].Kind != "quote" {
		t.Errorf("round 1: %+v", f)
	}
	if w := do(admin, "GET", "/api/drafts/tka", ""); w.Code != 200 {
		t.Errorf("the draft was not saved: %d", w.Code)
	}
	// One drafting of an id that has a draft already is refused; text that is barely there too.
	if w := do(admin, "POST", "/api/drafting", string(body)); w.Code != 409 {
		t.Errorf("again: %d", w.Code)
	}
	short, _ := json.Marshal(map[string]string{"id": "x_y", "name": "a.txt", "contentType": "text/plain",
		"data": base64.StdEncoding.EncodeToString([]byte("too short"))})
	if w := do(admin, "POST", "/api/drafting", string(short)); w.Code != 422 {
		t.Errorf("short: %d", w.Code)
	}
	if w := do(e2.signedIn(t, "viewer"), "POST", "/api/drafting", string(body)); w.Code != 403 {
		t.Errorf("a viewer drafted: %d", w.Code)
	}
}
