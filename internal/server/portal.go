package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/intake"
	"github.com/biodream-llc/vaglio/internal/letter"
)

// The provider portal: an office signed in with a provider account (vaglio user add -role provider, then
// vaglio user provider -npi ...) sends a request with its documents, sees what was decided and why, sends what a pended
// request asks for, and appeals a denial. It sees the requests of its own NPIs only, and nothing of the plan's screens:
// no proof internals, no other provider, no audit log. Each of its actions is in the audit log under its account.

// anySession wraps a handler any signed-in session may call, staff or provider.
func (cfg Config) anySession(h func(http.ResponseWriter, *http.Request, auth.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		sess, ok := cfg.Sessions.Get(c.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "the session has ended; sign in again")
			return
		}
		h(w, r, sess)
	}
}

// portal wraps a handler for provider accounts only, with the account's record.
func (cfg Config) portal(h func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
	return cfg.anySession(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if s.Role != auth.Provider {
			writeError(w, http.StatusForbidden, "the provider portal is for provider accounts")
			return
		}
		acct, ok, err := cfg.Sessions.Account(s.User)
		if err != nil || !ok || acct.Role != auth.Provider {
			writeError(w, http.StatusUnauthorized, "the session has ended; sign in again")
			return
		}
		h(w, r, acct)
	})
}

// PortalRequest is a request as its provider sees it: what was asked, what was decided and why, what is needed.
type PortalRequest struct {
	ID          string   `json:"id"`
	Received    string   `json:"received"`
	Service     string   `json:"service"`
	Title       string   `json:"title"`
	Patient     string   `json:"patient"`
	ProviderNPI string   `json:"providerNpi"`
	Status      string   `json:"status"` // approved, denied, waiting, with-a-clinician, appealed, replaced
	Why         string   `json:"why,omitempty"`
	Requested   []string `json:"requested,omitempty"`
	Due         string   `json:"due,omitempty"`
	Receipt     string   `json:"receipt,omitempty"`
	Appeal      string   `json:"appeal,omitempty"` // received, upheld or overturned
	AppealDue   string   `json:"appealDue,omitempty"`
	Sent        int      `json:"sent,omitempty"` // documents sent after the request
}

func (cfg Config) portalView(c *cases.Case) PortalRequest {
	p := PortalRequest{ID: c.ID, Received: c.Created, Service: c.Service, Title: c.PolicyTitle, Patient: c.Patient,
		ProviderNPI: c.ProviderNPI, Why: c.Answer.Why, Sent: len(c.Received)}
	switch final := c.Final(); {
	case c.Status == cases.Superseded:
		p.Status = "replaced"
	case final == "approve":
		p.Status = "approved"
	case final == "deny":
		p.Status = "denied"
	case c.Proposed != nil || c.Escalated != nil:
		p.Status, p.Due = "with-a-clinician", c.Due().UTC().Format(time.RFC3339)
	default:
		p.Status, p.Due, p.Requested = "waiting", c.Due().UTC().Format(time.RFC3339), c.Answer.Requested
	}
	if c.Review != nil {
		p.Why = c.Review.Reason
	}
	if c.AppealTaken != nil {
		p.Appeal, p.AppealDue = "received", c.AppealTaken.Due
	}
	if c.Appeal != nil {
		p.Appeal, p.Why = c.Appeal.Outcome, c.Appeal.Reason
		if c.Appeal.Outcome == "overturned" {
			p.Status = "approved"
		}
	}
	if rs := receiptsFor(cfg.Cases.History(c.ID)); len(rs) > 0 {
		p.Receipt = rs[len(rs)-1].Receipt
	}
	return p
}

// portalCase is the path's case when the account acts for its provider, else 404 (never "forbidden": whether another
// office's request exists is not for this one to learn).
func (cfg Config) portalCase(w http.ResponseWriter, r *http.Request, acct auth.User) (*cases.Case, bool) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok || !acct.ActsFor(c.ProviderNPI) {
		writeError(w, http.StatusNotFound, "no such request")
		return nil, false
	}
	return c, true
}

func (cfg Config) portalMe(w http.ResponseWriter, r *http.Request, acct auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{"user": acct.Name, "npis": acct.ProviderNPIs})
}

func (cfg Config) portalList(w http.ResponseWriter, r *http.Request, acct auth.User) {
	out := []PortalRequest{}
	for _, c := range cfg.Cases.Select(func(c *cases.Case) bool { return acct.ActsFor(c.ProviderNPI) }) {
		out = append(out, cfg.portalView(&c))
	}
	// Newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	writeJSON(w, http.StatusOK, out)
}

func (cfg Config) portalGet(w http.ResponseWriter, r *http.Request, acct auth.User) {
	c, ok := cfg.portalCase(w, r, acct)
	if !ok {
		return
	}
	if err := cfg.Cases.Viewed(c.ID, "provider:"+acct.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "reading the request could not be recorded: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.portalView(c))
}

// portalLetter is the decision letter, as the patient and the provider receive it.
func (cfg Config) portalLetter(w http.ResponseWriter, r *http.Request, acct auth.User) {
	c, ok := cfg.portalCase(w, r, acct)
	if !ok {
		return
	}
	l := r.URL.Query().Get("lang")
	if l == "" {
		l = "en"
	}
	if !letter.Supported(l) {
		writeError(w, http.StatusBadRequest, "letters are written in en or es")
		return
	}
	out, err := letter.WriteFor(*c, l, cfg.portalView(c).Receipt, time.Now(), cfg.Letters)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// portalSubmit sends a new request from the portal: the intake form, with the provider's NPI one of the account's.
// It is decided as any PAS request is, at once.
func (cfg Config) portalSubmit(w http.ResponseWriter, r *http.Request, acct auth.User) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIntake))
	dec.DisallowUnknownFields()
	var f intake.Form
	if err := dec.Decode(&f); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read: "+err.Error())
		return
	}
	if !acct.ActsFor(f.Provider.NPI) {
		writeError(w, http.StatusBadRequest, "the provider's NPI is not one this account acts for: "+strings.Join(acct.ProviderNPIs, ", "))
		return
	}
	f.Channel, f.Received, f.KeyedBy = "portal", "", "provider:"+acct.Name
	if err := f.Check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := f.Bundle(time.Now())
	raw, _ := json.Marshal(b)
	a, status, msg := cfg.decideBundle(raw, "1", "", "portal", "provider:"+acct.Name)
	if status != http.StatusOK {
		writeError(w, status, msg)
		return
	}
	c, _ := cfg.Cases.Get(a.Case)
	writeJSON(w, http.StatusOK, cfg.portalView(c))
}

// portalDocuments sends documents for a request still waiting: they are kept with the case like documents fetched from
// a PAS server, read as those are, and the reviewer sees them.
func (cfg Config) portalDocuments(w http.ResponseWriter, r *http.Request, acct auth.User) {
	var body struct {
		Documents []intake.Document `json:"documents"`
	}
	if !decodeJSONLimit(w, r, &body, maxIntake) {
		return
	}
	c, ok := cfg.portalCase(w, r, acct)
	if !ok {
		return
	}
	if !c.Open() {
		writeError(w, http.StatusConflict, "this request is decided: send an appeal instead")
		return
	}
	if len(body.Documents) == 0 || len(body.Documents) > 10 {
		writeError(w, http.StatusBadRequest, "send 1 to 10 documents")
		return
	}
	var entries []map[string]any
	for i, d := range body.Documents {
		raw, err := base64.StdEncoding.DecodeString(d.Data)
		if err != nil || len(raw) == 0 || len(raw) > intake.MaxDocument {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("document %d is not base64 of at most %d bytes", i+1, intake.MaxDocument))
			return
		}
		if !intake.AcceptedType(d.ContentType) {
			writeError(w, http.StatusBadRequest, "a document is a PDF, a TIFF, PNG or JPEG image, or plain text")
			return
		}
		if strings.TrimSpace(d.Description) == "" {
			writeError(w, http.StatusBadRequest, "say what each document is")
			return
		}
		id := fmt.Sprintf("portal-%d-%d", time.Now().UnixNano(), i)
		entries = append(entries, map[string]any{"fullUrl": "urn:uuid:" + id, "resource": map[string]any{
			"resourceType": "DocumentReference", "id": id, "status": "current", "description": d.Description,
			"subject": map[string]any{"reference": c.Patient}, "date": time.Now().UTC().Format(time.RFC3339),
			"content": []any{map[string]any{"attachment": map[string]any{"contentType": d.ContentType, "data": d.Data,
				"title": d.Description}}}}})
	}
	got, err := cfg.Cases.Receive(c.ID, entries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.readSoon(got)
	writeJSON(w, http.StatusOK, cfg.portalView(got))
}

// portalAppeal appeals a denial from the portal.
func (cfg Config) portalAppeal(w http.ResponseWriter, r *http.Request, acct auth.User) {
	var body struct {
		Reason    string `json:"reason"`
		Expedited bool   `json:"expedited"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.portalCase(w, r, acct)
	if !ok {
		return
	}
	got, err := cfg.Cases.TakeAppeal(c.ID, "provider:"+acct.Name, cases.AppealRequest{From: "provider",
		Received: time.Now().UTC().Format(time.RFC3339), Expedited: body.Expedited, Reason: body.Reason})
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such request")
	case errors.Is(err, cases.ErrNotDenied):
		writeError(w, http.StatusConflict, "only a denied request is appealed")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, cfg.portalView(got))
	}
}
