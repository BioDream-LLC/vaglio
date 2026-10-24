package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/um"
)

// The plan's case-review system (internal/um): pended cases are opened there, and its reviewers' decisions come back
// to POST /api/feeds/um (or are polled) and close the cases here.

// UMPacket builds what a case is opened with: the case laid out, its latest receipt, and its address in the console
// under console (Vaglio's own https address, as the plan's reviewers reach it), when there is one.
func (cfg Config) UMPacket(console string) func(c cases.Case) (um.Packet, error) {
	return func(c cases.Case) (um.Packet, error) {
		link := ""
		if console != "" {
			link = strings.TrimRight(console, "/") + "/#/cases/" + c.ID
		}
		return um.BuildPacket(c, ReceiptOf(cfg.Cases, c.ID), link)
	}
}

// RecordUMDecision records one decision from the case-review system with the same rules as a decision in the console:
// a reason always; a physician's case or a proposed denial decided only by a clinician (NPI and credential both sent);
// and where only clinicians deny (-clinician-denials), a denial only from one. The decision is recorded under the
// reviewer's name in that system, with the system's name and its id for the case.
func (cfg Config) RecordUMDecision(d um.Decision) um.Result {
	refuse := func(msg string) um.Result { return um.Result{Status: "refused", Error: msg} }
	c, ok := cfg.Cases.Get(d.Case)
	if !ok {
		return refuse("no such case")
	}
	d.Reason, d.NPI, d.Credential = strings.TrimSpace(d.Reason), strings.TrimSpace(d.NPI), strings.TrimSpace(d.Credential)
	if d.Reviewer == "" {
		return refuse("the decision does not say who made it: the reviewer is required")
	}
	if d.NPI != "" && !validNPI(d.NPI) {
		return refuse("the reviewer's NPI is ten digits with a valid check digit")
	}
	clinician := d.NPI != "" && d.Credential != ""
	switch {
	case !clinician && c.Escalated != nil:
		return refuse("this case was sent to a physician: the decision must carry the reviewer's NPI and credential")
	case !clinician && d.Decision == "deny" && (cfg.ClinicianDenials || c.Proposed != nil):
		return refuse("here only a clinician may deny: the decision must carry the reviewer's NPI and credential")
	}
	rv := cases.Review{Decision: d.Decision, Reason: d.Reason, ReviewerNPI: d.NPI, Credential: d.Credential,
		Via: cfg.UM.Name, Reference: d.Ref}
	got, err := cfg.Cases.ReviewVia(d.Case, d.Reviewer, rv)
	switch {
	case errors.Is(err, cases.ErrNotPended):
		why := "the case is not waiting for a reviewer"
		switch {
		case c.Status == cases.Superseded:
			why = "a later request for the same service replaced this one (case " + c.SupersededBy + ")"
		case c.Review != nil:
			why = fmt.Sprintf("it was decided already (%s, by %s)", c.Review.Decision, c.Review.By)
		}
		return refuse(why)
	case err != nil:
		return refuse(err.Error())
	}
	if cfg.Deliver != nil && got.Request != "" {
		cfg.Deliver(*got)
	}
	return um.Result{Status: "recorded"}
}

// umFeed takes the decisions the case-review system pushes, in its own format (the connector reads it).
func (cfg Config) umFeed(w http.ResponseWriter, r *http.Request) {
	if cfg.UM == nil || cfg.UMToken == "" {
		writeError(w, http.StatusNotFound, "the case-review feed is off: start the server with -um and -um-token-env")
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.UMToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "a bearer token is required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	ds, err := cfg.UM.Conn.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "not decisions this server reads: "+err.Error())
		return
	}
	if len(ds) > maxFeed {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d decisions per delivery", maxFeed))
		return
	}
	out := make([]um.Result, 0, len(ds))
	for _, d := range ds {
		out = append(out, cfg.UM.Take(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (cfg Config) umStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.UM == nil {
		writeJSON(w, http.StatusOK, map[string]any{"um": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"um": cfg.UM.Status()})
}

// umHoldsCase is whether a case is worked in the case-review system and not here: reviewers decide it there.
func (cfg Config) umHoldsCase(id string) (string, bool) {
	if cfg.UM == nil || cfg.UM.ReviewHere {
		return "", false
	}
	c, ok := cfg.Cases.Get(id)
	if !ok || c.UM == nil {
		return "", false
	}
	return c.UM.System, true
}
