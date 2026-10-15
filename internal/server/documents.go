package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/reader"
)

// DocumentModel is the model that reads attached documents: one the deployment trusts with patient records.
type DocumentModel struct {
	Model draft.Model
	Name  string // recorded with each reading
}

// Redecision is what the policy decides on the request as it now stands: with the documents received since, and the
// findings a reviewer confirmed. It decides nothing by itself; it is what the reviewer weighs.
type Redecision struct {
	PolicyVersion string              `json:"policyVersion"`
	Confirmed     int                 `json:"confirmed"`
	Received      int                 `json:"received"`
	Result        *decide.Result      `json:"result"`
	Evidence      evidence.Extraction `json:"evidence"`
}

// request is a case's request with what arrived since, read.
func (cfg Config) request(c *cases.Case) (*pas.Request, error) {
	b, err := cfg.Cases.Entries(c)
	if err != nil {
		return nil, err
	}
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		return nil, errors.New("the stored request no longer reads")
	}
	return req, nil
}

// redecide decides a case again, with the confirmed findings and the documents received. Nil when there is neither.
func (cfg Config) redecide(c *cases.Case) (*Redecision, error) {
	confirmed := 0
	for _, cf := range c.Confirmations {
		if cf.Confirmed {
			confirmed++
		}
	}
	if confirmed == 0 && len(c.Received) == 0 {
		return nil, nil
	}
	p := cfg.set.get(c.Policy)
	if p == nil {
		return nil, errors.New("the policy that decided this case is no longer loaded")
	}
	req, err := cfg.request(c)
	if err != nil {
		return nil, err
	}
	x := evidence.Extract(p, req)
	r := x.Request(p.ID)
	for _, cf := range c.Confirmations {
		if !cf.Confirmed {
			continue
		}
		a := decide.Assertion{Argument: cf.Argument, Finding: cf.Finding,
			Citation: decide.Citation{Reference: cf.Document, Quote: cf.Quote}}
		r = merge(r, a)
	}
	res, err := decide.Decide(p, r)
	if err != nil {
		return nil, err
	}
	return &Redecision{PolicyVersion: p.Version, Confirmed: confirmed, Received: len(c.Received), Result: res, Evidence: x}, nil
}

// merge adds a confirmed finding to a request: agreeing with what the record already says, it adds nothing; disagreeing,
// the evidence is contradicted, which a person settles, as the record's own contradictions are.
func merge(r decide.Request, a decide.Assertion) decide.Request {
	for _, n := range r.Contradicted {
		if n == a.Argument {
			return r
		}
	}
	for i, e := range r.Evidence {
		if e.Argument != a.Argument {
			continue
		}
		if e.Finding != a.Finding {
			r.Evidence = append(r.Evidence[:i:i], r.Evidence[i+1:]...)
			r.Contradicted = append(r.Contradicted, a.Argument)
		}
		return r
	}
	r.Evidence = append(r.Evidence, a)
	return r
}

// DocumentList is a case's readable documents, and those that cannot be read.
type DocumentList struct {
	Reader    bool              `json:"reader"` // a document model is configured
	Fetch     bool              `json:"fetch"`  // documents sent later can be fetched from the PAS server
	Documents []reader.Document `json:"documents"`
	Skipped   []string          `json:"skipped"`
}

func (cfg Config) caseDocuments(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	req, err := cfg.request(c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	docs, skipped := reader.DocumentsWith(r.Context(), req, cfg.Extractors, false)
	if docs == nil {
		docs = []reader.Document{}
	}
	if skipped == nil {
		skipped = []string{}
	}
	writeJSON(w, http.StatusOK, DocumentList{Reader: cfg.Reader != nil, Fetch: cfg.FetchDocuments != nil && c.Request != "",
		Documents: docs, Skipped: skipped})
}

// readDocument has the document model read one of a case's documents. The reading is recorded before it is shown.
func (cfg Config) readDocument(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if cfg.Reader == nil {
		writeError(w, http.StatusNotFound, "no document model is configured (vaglio serve -document-model-url)")
		return
	}
	var body struct {
		Document string `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	p := cfg.set.get(c.Policy)
	if p == nil {
		writeError(w, http.StatusConflict, "the policy that decided this case is no longer loaded")
		return
	}
	req, err := cfg.request(c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	docs, skipped := reader.DocumentsWith(ctx, req, cfg.Extractors, true)
	var doc *reader.Document
	for i := range docs {
		if docs[i].Reference == body.Document {
			doc = &docs[i]
		}
	}
	if doc == nil {
		for _, sk := range skipped {
			if strings.HasPrefix(sk, body.Document+":") {
				writeError(w, http.StatusUnprocessableEntity, sk)
				return
			}
		}
		writeError(w, http.StatusNotFound, "the request holds no readable document "+body.Document)
		return
	}
	rd, err := reader.Read(ctx, cfg.Reader.Model, cfg.Reader.Name, p, *doc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the document model did not answer: "+err.Error())
		return
	}
	if _, err := cfg.Cases.Read(c.ID, s.User, *rd); err != nil {
		writeError(w, http.StatusInternalServerError, "the reading could not be recorded, so it is not shown: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rd)
}

func (cfg Config) confirmProposal(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Reading   int  `json:"reading"`
		Index     int  `json:"index"`
		Confirmed bool `json:"confirmed"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := cfg.Cases.Confirm(r.PathValue("id"), s.User, body.Reading, body.Index, body.Confirmed)
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// fetchDocuments asks the PAS server for what the provider sent since the request was answered.
func (cfg Config) fetchDocuments(w http.ResponseWriter, r *http.Request, s auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "no such case")
		return
	case cfg.FetchDocuments == nil:
		writeError(w, http.StatusNotFound, "no PAS server is configured to fetch from (vaglio serve -pas-url)")
		return
	case c.Request == "":
		writeError(w, http.StatusConflict, "the PAS server gave this request no id, so there is nothing to ask it about")
		return
	}
	entries, err := cfg.FetchDocuments(r.Context(), c.Request)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(entries) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"received": 0})
		return
	}
	got, err := cfg.Cases.Receive(c.ID, entries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.readSoon(got) // what arrived is read like what came with the request
	writeJSON(w, http.StatusOK, map[string]any{"received": len(entries)})
}

// documentOriginal serves a document as the provider sent it, for the reviewer to confirm a proposal against the
// original rather than its extracted text. It is sent as a download, never rendered in the console's origin, and the
// view is in the audit log like any other reading of the case.
func (cfg Config) documentOriginal(w http.ResponseWriter, r *http.Request, s auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	req, err := cfg.request(c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ref := r.URL.Query().Get("ref")
	docs, _ := reader.DocumentsWith(r.Context(), req, cfg.Extractors, false)
	for _, d := range docs {
		if d.Reference != ref || len(d.Raw) == 0 {
			continue
		}
		if err := cfg.Cases.Viewed(c.ID, s.User); err != nil {
			writeError(w, http.StatusInternalServerError, "reading the case could not be recorded: "+err.Error())
			return
		}
		ct := d.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ext := map[string]string{"application/pdf": ".pdf", "image/png": ".png", "image/jpeg": ".jpg", "image/tiff": ".tif",
			"text/plain": ".txt", "text/html": ".html"}[ct]
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", `attachment; filename="document`+ext+`"`)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Write(d.Raw)
		return
	}
	writeError(w, http.StatusNotFound, "the request holds no document "+ref)
}
