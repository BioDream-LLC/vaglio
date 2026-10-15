package server

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
)

// The work queue: open cases (waiting for a decision, or an appeal for its outcome) in the order they are worked, who
// has each, and the work a reviewer records on one: taking it, handing it on, sending it to a physician, peer-to-peer
// calls, and appeals taken in. The order is a stated rule (cases.RankAt) and only orders: it decides nothing.

// QueueItem is an open case as the queue shows it.
type QueueItem struct {
	CaseSummary
	Rank        cases.Rank           `json:"rank"`
	WorkDue     string               `json:"workDue"`
	Assigned    string               `json:"assigned,omitempty"`
	Physician   bool                 `json:"physician,omitempty"` // only a clinician may decide it
	Appeal      *cases.AppealRequest `json:"appeal,omitempty"`    // an appeal waiting for its outcome
	NextCall    string               `json:"nextCall,omitempty"`  // a scheduled peer-to-peer call, RFC 3339
	Overdue     bool                 `json:"overdue,omitempty"`
	MayTakeThis bool                 `json:"mayTake"` // the signed-in reviewer may take and decide it
}

// needsClinician is whether only a clinician may decide an open case: it was sent to a physician, the engine proposed
// a denial to a clinician, or it is an appeal of a denial (whose upholding is a physician's: 42 CFR 422.590(h)(2)).
func (cfg Config) needsClinician(c *cases.Case) bool {
	return c.Escalated != nil || c.Proposed != nil || (c.AppealTaken != nil && c.Appeal == nil)
}

// mayWork is whether acct may take and decide an open case.
func (cfg Config) mayWork(acct auth.User, c *cases.Case) bool {
	if !acct.Role.Can(auth.Reviewer) {
		return false
	}
	if cfg.needsClinician(c) && !acct.Clinician() {
		return false
	}
	if t := c.AppealTaken; t != nil && c.Appeal == nil && strings.EqualFold(t.Denier, acct.Name) {
		return false // the denier does not decide its appeal
	}
	return true
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

func (cfg Config) queueItem(c *cases.Case, acct auth.User, now time.Time) QueueItem {
	q := QueueItem{CaseSummary: cfg.summary(c), Rank: c.RankAt(now), WorkDue: c.WorkDue().UTC().Format(time.RFC3339),
		Physician: cfg.needsClinician(c), Overdue: !now.Before(c.WorkDue()), MayTakeThis: cfg.mayWork(acct, c)}
	if c.Assigned != nil {
		q.Assigned = c.Assigned.To
	}
	if c.AppealTaken != nil && c.Appeal == nil {
		q.Appeal = c.AppealTaken
	}
	for _, p := range c.PeerToPeer {
		if p.Status == cases.P2PScheduled && (q.NextCall == "" || p.At < q.NextCall) {
			q.NextCall = p.At
		}
	}
	return q
}

// account is the signed-in person's account, or a written refusal.
func (cfg Config) account(w http.ResponseWriter, s auth.Session) (auth.User, bool) {
	acct, ok, err := cfg.Sessions.Account(s.User)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "the session has ended; sign in again")
		return auth.User{}, false
	}
	return acct, true
}

// queue lists open cases in working order. who: mine, unassigned, or every open case (default); tier=physician keeps
// the cases only a clinician may decide.
func (cfg Config) queue(w http.ResponseWriter, r *http.Request, s auth.Session) {
	acct, ok := cfg.account(w, s)
	if !ok {
		return
	}
	who, tier := r.URL.Query().Get("who"), r.URL.Query().Get("tier")
	if who != "" && who != "mine" && who != "unassigned" && who != "all" {
		writeError(w, http.StatusBadRequest, "who is mine, unassigned or all")
		return
	}
	now := cfg.now()
	open := cfg.Cases.Select(func(c *cases.Case) bool { return c.Open() && s.Sees(c.Policy) })
	cases.SortQueue(open, now)
	out := []QueueItem{}
	for i := range open {
		c := &open[i]
		mine := c.Assigned != nil && c.Assigned.To == s.User
		free := c.Assigned == nil || c.Assigned.To == ""
		if (who == "mine" && !mine) || (who == "unassigned" && !free) || (tier == "physician" && !cfg.needsClinician(c)) {
			continue
		}
		out = append(out, cfg.queueItem(c, acct, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// takeNext gives the signed-in reviewer the first unassigned case of the queue they may decide.
func (cfg Config) takeNext(w http.ResponseWriter, r *http.Request, s auth.Session) {
	acct, ok := cfg.account(w, s)
	if !ok {
		return
	}
	c, err := cfg.Cases.TakeNext(s.User, cfg.now(), func(c *cases.Case) bool { return s.Sees(c.Policy) && cfg.mayWork(acct, c) })
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "nothing in the queue is waiting for you")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, cfg.queueItem(c, acct, cfg.now()))
	}
}

// openCase is the case of the request's path, visible to the session, or a written refusal.
func (cfg Config) openCase(w http.ResponseWriter, r *http.Request, s auth.Session) (*cases.Case, bool) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok || !s.Sees(c.Policy) {
		writeError(w, http.StatusNotFound, "no such case")
		return nil, false
	}
	return c, true
}

func workError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case or call")
	case errors.Is(err, cases.ErrNotOpen), errors.Is(err, cases.ErrNotDenied):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// assignCase gives a case to a reviewer. A reviewer takes a case or hands their own back; an administrator gives any
// case to anyone who may decide it.
func (cfg Config) assignCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		To string `json:"to"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	acct, ok := cfg.account(w, s)
	if !ok {
		return
	}
	c, ok := cfg.openCase(w, r, s)
	if !ok {
		return
	}
	held := c.Assigned != nil && c.Assigned.To != ""
	if !acct.Role.Can(auth.Admin) {
		switch {
		case body.To != "" && body.To != s.User:
			writeError(w, http.StatusForbidden, "an administrator gives cases to other people; you may take one yourself")
			return
		case body.To == "" && held && c.Assigned.To != s.User:
			writeError(w, http.StatusForbidden, "only "+c.Assigned.To+" or an administrator may hand it back")
			return
		case body.To != "" && held && c.Assigned.To != s.User:
			writeError(w, http.StatusConflict, c.Assigned.To+" has this case")
			return
		}
	}
	if body.To != "" {
		to, ok, err := cfg.Sessions.Account(body.To)
		if err != nil || !ok {
			writeError(w, http.StatusBadRequest, "no account named "+body.To)
			return
		}
		if !cfg.mayWork(to, c) {
			writeError(w, http.StatusBadRequest, body.To+" may not decide this case (a reviewer, and a clinician when it needs one)")
			return
		}
	}
	got, err := cfg.Cases.Assign(c.ID, s.User, body.To)
	if err != nil {
		workError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (cfg Config) escalateCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Note string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.openCase(w, r, s)
	if !ok {
		return
	}
	got, err := cfg.Cases.Escalate(c.ID, s.User, body.Note)
	if err != nil {
		workError(w, err)
		return
	}
	// A case sent to a physician leaves a reviewer who is not one.
	if got.Assigned != nil && got.Assigned.To != "" {
		if to, ok, _ := cfg.Sessions.Account(got.Assigned.To); !ok || !to.Clinician() {
			if again, err := cfg.Cases.Assign(c.ID, s.User, ""); err == nil {
				got = again
			}
		}
	}
	writeJSON(w, http.StatusOK, got)
}

func (cfg Config) peerToPeer(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body cases.PeerToPeer
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.openCase(w, r, s)
	if !ok {
		return
	}
	got, err := cfg.Cases.SchedulePeerToPeer(c.ID, s.User, cases.PeerToPeer{ID: body.ID, At: body.At, With: body.With,
		Status: body.Status, Note: body.Note})
	if err != nil {
		workError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// receiveAppeal takes in an appeal of a denied case. Its outcome is then recorded by someone other than the denier.
func (cfg Config) receiveAppeal(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body cases.AppealRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.openCase(w, r, s)
	if !ok {
		return
	}
	if body.Received == "" {
		body.Received = cfg.now().UTC().Format(time.RFC3339)
	}
	got, err := cfg.Cases.TakeAppeal(c.ID, s.User, cases.AppealRequest{From: body.From, Received: body.Received,
		Expedited: body.Expedited, Reason: body.Reason})
	if err != nil {
		workError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// Workload is one reviewer's open cases.
type Workload struct {
	Reviewer  string `json:"reviewer"`
	Open      int    `json:"open"`
	Overdue   int    `json:"overdue"`
	Physician int    `json:"physician"`
}

// workload counts the open cases each reviewer holds, and those nobody does (reviewer "").
func (cfg Config) workload(w http.ResponseWriter, r *http.Request, s auth.Session) {
	now := cfg.now()
	by := map[string]*Workload{}
	for _, c := range cfg.Cases.Select(func(c *cases.Case) bool { return c.Open() && s.Sees(c.Policy) }) {
		who := ""
		if c.Assigned != nil {
			who = c.Assigned.To
		}
		wl := by[who]
		if wl == nil {
			wl = &Workload{Reviewer: who}
			by[who] = wl
		}
		wl.Open++
		if !now.Before(c.WorkDue()) {
			wl.Overdue++
		}
		if cfg.needsClinician(&c) {
			wl.Physician++
		}
	}
	out := []Workload{}
	for _, wl := range by {
		out = append(out, *wl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reviewer < out[j].Reviewer })
	writeJSON(w, http.StatusOK, out)
}
