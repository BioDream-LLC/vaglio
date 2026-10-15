package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/dtr"
	"github.com/biodream-llc/vaglio/internal/intake"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// maxIntake bounds a keyed-in request: its documents travel inside it, base64.
const maxIntake = 24 << 20

// IntakePolicy is a policy a keyed-in request for a service may fall under, with the questions to answer from it.
type IntakePolicy struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Version       string     `json:"version"`
	Authority     string     `json:"authority,omitempty"`
	Lines         []string   `json:"lines,omitempty"`
	States        []string   `json:"states,omitempty"`
	Questionnaire string     `json:"questionnaire,omitempty"`
	Questions     []dtr.Item `json:"questions"`
}

// intakeForm is the questions for a service (?system=cpt&code=72148): those of every policy that may decide it. Which
// one does is settled when the request is decided, from its coverage and the patient's state.
func (cfg Config) intakeForm(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	cfg.activateDue()
	sys, code := r.URL.Query().Get("system"), r.URL.Query().Get("code")
	if url, ok := policy.Systems[sys]; ok {
		sys = url
	}
	out := []IntakePolicy{}
	for _, p := range cfg.set.all() {
		if !p.Decides(sys, code) || !p.InEffect(cfg.today()) {
			continue
		}
		ip := IntakePolicy{ID: p.ID, Title: p.Title, Version: p.Version, Authority: p.Authority, Lines: p.Lines,
			States: p.States, Questionnaire: p.Questionnaire, Questions: []dtr.Item{}}
		if q, err := dtr.For(p); err == nil {
			ip.Questions = q.Item
		}
		out = append(out, ip)
	}
	writeJSON(w, http.StatusOK, out)
}

// IntakeBody is a keyed-in request: the form, and the inbound faxes it was read from, whose images go with it.
type IntakeBody struct {
	intake.Form
	Faxes []string `json:"faxes,omitempty"`
}

// keyIn decides a request keyed in from a fax, a call, a letter or a portal, as one more PAS request: the same policy
// choice, evidence, decision, case and audit record. The case names its channel and who keyed it in.
func (cfg Config) keyIn(w http.ResponseWriter, r *http.Request, s auth.Session) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIntake))
	dec.DisallowUnknownFields()
	var body IntakeBody
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read: "+err.Error())
		return
	}
	f := body.Form
	f.KeyedBy = s.User
	if f.Channel == "ncpdp" {
		writeError(w, http.StatusBadRequest, "an NCPDP ePA request comes from the ePA intermediary, not keyed in")
		return
	}
	for _, id := range body.Faxes {
		fx, ok := cfg.inbox().Get(id)
		if !ok {
			writeError(w, http.StatusBadRequest, "no fax "+id)
			return
		}
		if fx.Done != "" {
			writeError(w, http.StatusConflict, "fax "+id+" has been handled already")
			return
		}
		raw, err := cfg.inbox().Content(fx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		f.Documents = append(f.Documents, intake.Document{Description: "Fax received " + fx.Received + faxFrom(fx),
			ContentType: fx.ContentType, Data: base64.StdEncoding.EncodeToString(raw), Fax: fx.ID})
	}
	if err := f.Check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := f.Bundle(time.Now())
	raw, _ := json.Marshal(b)
	a, status, msg := cfg.decideBundle(raw, "1", "", f.Channel, s.User)
	if status != http.StatusOK {
		writeError(w, status, msg)
		return
	}
	for _, id := range body.Faxes {
		if _, err := cfg.inbox().Close(id, s.User, a.Case, "", time.Now()); err != nil {
			log.Printf("intake: case %s is open but fax %s could not be marked keyed in: %v", a.Case, id, err)
		}
	}
	writeJSON(w, http.StatusOK, a)
}

func faxFrom(f intake.Fax) string {
	if f.From == "" {
		return ""
	}
	return " from " + f.From
}

func (cfg Config) inbox() *intake.Inbox { return cfg.Faxes }

// faxesOn answers 404 when no fax inbox is configured.
func (cfg Config) faxesOn(w http.ResponseWriter) bool {
	if cfg.Faxes == nil {
		writeError(w, http.StatusNotFound, "no fax inbox: start the server with -fax-token-env")
		return false
	}
	return true
}

// ActFaxReceived and ActFaxSetAside record the fax inbox's events.
const (
	ActFaxReceived = "fax.received"
	ActFaxSetAside = "fax.set_aside"
)

// receiveFax takes a fax from the plan's fax service: POST /api/feeds/fax with the bearer token from -fax-token-env,
// the fax as the body (PDF, TIFF, PNG or JPEG), and optionally ?from=NUMBER&pages=N. It is kept, its text extracted for
// whoever keys it in, and listed in the console's fax inbox.
func (cfg Config) receiveFax(w http.ResponseWriter, r *http.Request) {
	if !cfg.faxesOn(w) {
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if cfg.FaxToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.FaxToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "the fax service's bearer token is needed")
		return
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, intake.MaxDocument))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "a fax is at most 8 MB")
		return
	}
	pages, _ := strconv.Atoi(r.URL.Query().Get("pages"))
	from := r.URL.Query().Get("from")
	if len(from) > 32 {
		from = from[:32]
	}
	text := ""
	if x := cfg.Extractors.For(ct); x != nil {
		if t, err := x.Extract(r.Context(), raw); err == nil {
			text = t
		} else {
			log.Printf("fax: %s could not make text of it: %v", x.Name, err)
		}
	}
	f, err := cfg.inbox().Add(raw, ct, from, pages, text, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := cfg.Audit.Append("fax-service", ActFaxReceived, "", map[string]any{"fax": f.ID, "sha256": f.SHA256,
		"from": f.From, "pages": f.Pages}); err != nil {
		writeError(w, http.StatusInternalServerError, "the fax could not be recorded: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fax": f.ID})
}

func (cfg Config) listFaxes(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if !cfg.faxesOn(w) {
		return
	}
	list, err := cfg.inbox().List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (cfg Config) getFax(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.faxesOn(w) {
		return
	}
	f, ok := cfg.inbox().Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such fax")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// faxContent serves a fax's image, as a sandboxed download (a fax may hold anything), and records the reading.
func (cfg Config) faxContent(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.faxesOn(w) {
		return
	}
	f, ok := cfg.inbox().Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such fax")
		return
	}
	raw, err := cfg.inbox().Content(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := cfg.Audit.Append("user:"+s.User, "fax.viewed", "", map[string]string{"fax": f.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "the reading could not be recorded, so it is not shown: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("inline") != "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="fax-`+f.ID+ext(f.ContentType)+`"`)
	}
	_, _ = w.Write(raw)
}

func ext(ct string) string {
	switch ct {
	case "application/pdf":
		return ".pdf"
	case "image/tiff":
		return ".tif"
	case "image/png":
		return ".png"
	}
	return ".jpg"
}

// setAsideFax closes a fax that is not a request to key in (a duplicate, a misdial, something else), with the reason.
func (cfg Config) setAsideFax(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.faxesOn(w) {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" || len(body.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "say why it is set aside, in at most 500 characters")
		return
	}
	id := r.PathValue("id")
	if _, err := cfg.Audit.Append("user:"+s.User, ActFaxSetAside, "", map[string]string{"fax": id, "reason": body.Reason}); err != nil {
		writeError(w, http.StatusInternalServerError, "not recorded, so not set aside: "+err.Error())
		return
	}
	f, err := cfg.inbox().Close(id, s.User, "", body.Reason, time.Now())
	switch {
	case errors.Is(err, intake.ErrDone):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusNotFound, "no such fax")
	default:
		writeJSON(w, http.StatusOK, f)
	}
}
