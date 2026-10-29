package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/editor"
)

// Drafter drafts policies from coverage documents uploaded in the console (serve -draft-model-url).
type Drafter struct {
	Model draft.Model
	Name  string // the model, as the draft's header and the log name it
	// Rounds is how many drafts the model may try; 0 is 6.
	Rounds int
	// Twice drafts each document a second time, with Second (Model again when nil), and compares the two readings
	// record by record (serve -draft-twice): where they disagree, the job says where and the saved draft is headed so.
	Twice      bool
	Second     draft.Model
	SecondName string

	mu   sync.Mutex
	jobs map[string]*DraftJob
	busy bool
	last string // the latest job, so a console opened later finds it
}

// DraftJob is one document being drafted, as the console polls it.
type DraftJob struct {
	ID       string         `json:"id"`
	Policy   string         `json:"policy"`
	Document string         `json:"document"` // its name
	SHA256   string         `json:"sha256"`
	State    string         `json:"state"` // running, passed, failed
	Started  string         `json:"started"`
	Rounds   []DraftRound   `json:"rounds"`
	Error    string         `json:"error,omitempty"`
	Saved    bool           `json:"saved"`           // the last draft is saved as the policy's draft, for an author
	Check    *decide.Report `json:"check,omitempty"` // the passing draft's exhaustive check
	// Also is the other policies a passing draft holds, saved as drafts beside the first: a document holding its
	// services to different criteria is drafted as one policy per service.
	Also []string `json:"also,omitempty"`
	// Twice is, when the document was drafted twice, whether the two readings agree and where they do not.
	Twice *TwiceResult `json:"twice,omitempty"`
}

// TwiceResult is the comparison of two drafts of one document.
type TwiceResult struct {
	Agree    bool     `json:"agree"`
	Problems []string `json:"problems,omitempty"`
	Report   string   `json:"report"`
}

// DraftRound is one round's findings, sorted by kind (draft.Kind).
type DraftRound struct {
	Findings []Finding `json:"findings"`
}

// Finding is one thing the checker found in a draft.
type Finding struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// maxDocument bounds an uploaded coverage document.
const maxDocument = 8 << 20

// draftFromDocument starts drafting a policy from an uploaded coverage document: text, or a PDF when the server has
// an extractor for it. One at a time per server: each costs model calls.
func (cfg Config) draftFromDocument(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	d := cfg.Drafter
	if d == nil {
		writeError(w, http.StatusNotFound, "this server drafts from documents only with serve -draft-model-url")
		return
	}
	var body struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		ContentType string `json:"contentType"`
		Data        string `json:"data"` // base64
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocument*4/3+4096)
	if !decodeJSON(w, r, &body) {
		return
	}
	if !editor.ValidID(body.ID) {
		writeError(w, http.StatusBadRequest, "the policy id is 2 to 64 lower case letters, digits or '_', starting with a letter")
		return
	}
	dir := editor.Dir{Path: cfg.PolicyDir}
	if _, ok, _ := dir.Draft(body.ID); ok {
		writeError(w, http.StatusConflict, "a draft of "+body.ID+" exists; open it, or remove it first")
		return
	}
	if cfg.set.get(body.ID) != nil {
		writeError(w, http.StatusConflict, body.ID+" is a policy in force; draft its next version from its own screen")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || len(raw) == 0 || len(raw) > maxDocument {
		writeError(w, http.StatusBadRequest, "the document is empty, too large, or not base64")
		return
	}
	var text string
	switch {
	case body.ContentType == "text/plain" || strings.HasSuffix(strings.ToLower(body.Name), ".txt"):
		text = string(raw)
	default:
		if cfg.Extractors.For(body.ContentType) == nil {
			writeError(w, http.StatusUnsupportedMediaType, "this server reads text, and PDFs only with serve -extract pdftotext")
			return
		}
		if text, _, err = cfg.Extractors.Extract(r.Context(), body.ContentType, raw); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "the document could not be read: "+err.Error())
			return
		}
	}
	if len(strings.TrimSpace(text)) < 200 {
		writeError(w, http.StatusUnprocessableEntity, "the document has almost no text; a scan needs -extract tesseract")
		return
	}
	sum := sha256.Sum256([]byte(text))
	job := &DraftJob{ID: newJobID(), Policy: body.ID, Document: body.Name, SHA256: hex.EncodeToString(sum[:]),
		State: "running", Started: time.Now().UTC().Format(time.RFC3339), Rounds: []DraftRound{}}

	d.mu.Lock()
	if d.busy {
		d.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "a document is being drafted already; try again when it is done")
		return
	}
	if d.jobs == nil {
		d.jobs = map[string]*DraftJob{}
	}
	d.busy, d.jobs[job.ID], d.last = true, job, job.ID
	d.mu.Unlock()

	if _, err := cfg.Audit.Append("user:"+s.User, "policy.drafting", "", map[string]string{"policy": body.ID,
		"document": body.Name, "sha256": job.SHA256, "model": d.Name}); err != nil {
		d.mu.Lock()
		d.busy = false
		delete(d.jobs, job.ID)
		d.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "the drafting could not be recorded, so it is not started: "+err.Error())
		return
	}
	go cfg.runDraft(job, text, s.User)
	writeJSON(w, http.StatusAccepted, d.snapshot(job))
}

func (cfg Config) runDraft(job *DraftJob, text, user string) {
	d := cfg.Drafter
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	req := draft.Request{ID: job.Policy, Document: text,
		Name: job.Document + " with " + d.Name, Rounds: rounds(d.Rounds),
		OnRound: func(_ int, a draft.Attempt) {
			round := DraftRound{Findings: []Finding{}}
			for _, f := range a.Failures {
				round.Findings = append(round.Findings, Finding{Kind: draft.Kind(f), Text: f})
			}
			d.mu.Lock()
			job.Rounds = append(job.Rounds, round)
			d.mu.Unlock()
		}}
	var res *draft.Result
	var twin *draft.Twin
	var err error
	if d.Twice {
		second := d.Second
		if second == nil {
			second = d.Model
		} else {
			req.Name += " and " + d.SecondName
		}
		twin, err = draft.Twice(ctx, d.Model, second, req)
		res = twin.First
		if res == nil {
			res = &draft.Result{}
		}
	} else {
		res, err = draft.Draft(ctx, d.Model, req)
	}
	head := ""
	if twin != nil && err == nil {
		head = twin.Commented()
	}
	// The last draft is saved either way: a passing one to be read and signed off, a failing one for an author to fix
	// with the checker beside it. A failing one says so at its top.
	src := ""
	switch {
	case err == nil:
		src = head + res.Source
	case res != nil && len(res.Attempts) > 0:
		last := res.Attempts[len(res.Attempts)-1]
		src = "# DRAFT that does NOT pass its checks, machine-drafted from " + job.Document + " with " + d.Name +
			". The checker's findings are in the console and the audit log.\n\n" + draft.Source(last.Answer)
	}
	saved := false
	dir := editor.Dir{Path: cfg.PolicyDir}
	save := func(id, src string) bool {
		if _, e := cfg.Audit.Append("user:"+user, ActDraftSaved, "", map[string]string{"policy": id,
			"sha256": editor.Hash([]byte(src))}); e != nil {
			return false
		}
		_, e := dir.SaveDraft(id, []byte(src))
		return e == nil
	}
	firstID := job.Policy
	if err == nil && res.Policy != nil {
		firstID = res.Policy.ID
	}
	if src != "" {
		saved = save(firstID, src)
	}
	var also []string
	if err == nil {
		for _, p := range res.Policies[1:] {
			if save(p.ID, head+res.Sources[p.ID]) {
				also = append(also, p.ID)
			}
		}
	}
	var check *decide.Report
	if err == nil && res.Policy != nil {
		check = decide.Check(res.Policy)
	}
	passed := err == nil
	_, _ = cfg.Audit.Append("user:"+user, "policy.drafted", "", map[string]any{"policy": job.Policy,
		"sha256": job.SHA256, "model": d.Name, "rounds": len(job.Rounds), "passed": passed,
		"twice": twin != nil, "readingsAgree": twin != nil && err == nil && twin.Agree()})

	d.mu.Lock()
	defer d.mu.Unlock()
	d.busy = false
	job.Saved, job.Check, job.Also = saved, check, also
	if twin != nil && err == nil {
		job.Twice = &TwiceResult{Agree: twin.Agree(), Problems: twin.Problems, Report: twin.Report()}
	}
	job.Policy = firstID // a split draft's first policy is named ID_service
	if passed {
		job.State = "passed"
	} else {
		job.State = "failed"
		if err != draft.ErrNoDraft {
			job.Error = err.Error()
		}
	}
}

// draftJob is a drafting's progress.
func (cfg Config) draftJob(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	d := cfg.Drafter
	if d == nil {
		writeError(w, http.StatusNotFound, "no such drafting")
		return
	}
	d.mu.Lock()
	job, ok := d.jobs[r.PathValue("job")]
	d.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "no such drafting")
		return
	}
	writeJSON(w, http.StatusOK, d.snapshot(job))
}

// draftingInfo says whether this server drafts from documents, with which model, and which kinds of file it reads.
func (cfg Config) draftingInfo(w http.ResponseWriter, _ *http.Request, _ auth.Session) {
	out := map[string]any{"enabled": cfg.Drafter != nil && cfg.PolicyDir != "", "pdf": cfg.Extractors.For("application/pdf") != nil}
	if d := cfg.Drafter; d != nil {
		out["model"] = d.Name
		d.mu.Lock()
		if d.last != "" {
			out["last"], out["busy"] = d.last, d.busy
		}
		d.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Drafter) snapshot(j *DraftJob) DraftJob {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := *j
	c.Rounds = append([]DraftRound{}, j.Rounds...) // never null: the console reads its length
	return c
}

func newJobID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func rounds(n int) int {
	if n <= 0 {
		return 6
	}
	return n
}
