package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
)

// extendCase extends a waiting case's decision timeframe (reviewer). The enrollee must be told, in writing, why; the
// note is that reason, and the case and the metrics carry it.
func (cfg Config) extendCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Days   int    `json:"days"`
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := cfg.Cases.Extend(r.PathValue("id"), s.User, cases.Extension{Days: body.Days, Reason: body.Reason,
		Note: body.Note}, cfg.ExtensionDays, time.Now())
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
	case errors.Is(err, cases.ErrNotPending):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// AppealOutcome is one appeal decided in the appeals system, as its feed sends it.
type AppealOutcome struct {
	// Claim and Item find the case: the prior authorization request's Claim identifier (system|value, as the case
	// records it) and the item's sequence. Case, the case id, may be given instead.
	Claim string `json:"claim,omitempty"`
	Item  string `json:"item,omitempty"`
	Case  string `json:"case,omitempty"`
	// Reference is the appeal's id in the appeals system. It is required: it is how a delivery made twice is recognised.
	Reference string `json:"reference"`
	Outcome   string `json:"outcome"` // upheld or overturned
	Reason    string `json:"reason"`
	Decided   string `json:"decided,omitempty"` // YYYY-MM-DD
	By        string `json:"by,omitempty"`      // who decided it there, for the record
}

// AppealResult is what became of one outcome in a feed delivery.
type AppealResult struct {
	Reference string `json:"reference"`
	Case      string `json:"case,omitempty"`
	Status    string `json:"status"` // recorded, already-recorded, or refused
	Error     string `json:"error,omitempty"`
}

// maxFeed bounds one delivery.
const maxFeed = 1000

// appealsFeed takes appeal outcomes from the payer's appeals system, by bearer token (-appeals-token-env), and records
// each against the denial it decides. Each outcome is answered on its own: one the feed sends twice is "already-recorded",
// one that names no denied case is refused, and neither stops the rest.
func (cfg Config) appealsFeed(w http.ResponseWriter, r *http.Request) {
	if cfg.AppealsToken == "" {
		writeError(w, http.StatusNotFound, "the appeals feed is off: start the server with -appeals-token-env")
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.AppealsToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "a bearer token is required")
		return
	}
	var batch []AppealOutcome
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "the feed sends a JSON array of appeal outcomes: "+err.Error())
		return
	}
	if len(batch) > maxFeed {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d outcomes per delivery", maxFeed))
		return
	}
	out := make([]AppealResult, 0, len(batch))
	for _, a := range batch {
		out = append(out, cfg.RecordAppeal(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// RecordAppeal records one appeal outcome from a feed or an import.
func (cfg Config) RecordAppeal(a AppealOutcome) AppealResult {
	res := AppealResult{Reference: a.Reference, Status: "refused"}
	if strings.TrimSpace(a.Reference) == "" {
		res.Error = "an outcome needs its reference: the appeal's id in the appeals system"
		return res
	}
	id, err := cfg.findDenial(a)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Case = id
	reason := strings.TrimSpace(a.Reason)
	if a.By != "" && reason != "" {
		reason += " (decided by " + strings.TrimSpace(a.By) + ")"
	}
	_, err = cfg.Cases.Appealed(id, "appeals-feed", cases.Appeal{Outcome: a.Outcome, Reason: reason, Decided: a.Decided,
		Reference: a.Reference})
	switch {
	case errors.Is(err, cases.ErrAlreadyRecorded):
		res.Status = "already-recorded"
	case err != nil:
		res.Error = err.Error()
	default:
		res.Status = "recorded"
	}
	return res
}

// findDenial is the case an outcome decides: by id, or the one denied case for the Claim and item. A Claim resubmitted
// leaves its earlier cases superseded, so the denial is the one case not superseded; more than one is refused, since
// recording an appeal against the wrong denial would be worse than recording none.
func (cfg Config) findDenial(a AppealOutcome) (string, error) {
	if a.Case != "" {
		if _, ok := cfg.Cases.Get(a.Case); !ok {
			return "", errors.New("no case " + a.Case)
		}
		return a.Case, nil
	}
	if a.Claim == "" {
		return "", errors.New("an outcome names its case, or the Claim (and item) it appeals")
	}
	matches := cfg.Cases.Select(func(c *cases.Case) bool {
		return c.Claim == a.Claim && (a.Item == "" || c.Item == a.Item) && c.Final() == "deny"
	})
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no denied request for Claim %s item %s", a.Claim, a.Item)
	case 1:
		return matches[0].ID, nil
	}
	return "", fmt.Errorf("Claim %s has %d denied items; name the item", a.Claim, len(matches))
}
