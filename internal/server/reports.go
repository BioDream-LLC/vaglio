package server

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/dtr"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/replay"
	"github.com/biodream-llc/vaglio/internal/report"
	"github.com/biodream-llc/vaglio/internal/versions"
)

// policyQuestionnaire is the DTR Questionnaire a policy reads, made from the policy, as FHIR JSON a PAS server can
// store and serve (PUT it to the server's Questionnaire/{id}).
func (cfg Config) policyQuestionnaire(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	p, ok := cfg.policy(w, r)
	if !ok {
		return
	}
	q, err := dtr.For(p)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+q.ID+`.questionnaire.json"`)
	writeJSON(w, http.StatusOK, q)
}

func period(w http.ResponseWriter, r *http.Request) (report.Period, bool) {
	p, err := report.ParsePeriod(r.URL.Query().Get("from"), r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return p, false
	}
	return p, true
}

// metricsReport is the CMS prior authorisation metrics for a period (?from=YYYY-MM-DD&to=YYYY-MM-DD; this year to date
// by default), as JSON, or ?format=csv in the shape of CMS's template.
func (cfg Config) metricsReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	p, ok := period(w, r)
	if !ok {
		return
	}
	m := report.Build(cfg.Cases.Select(nil), p)
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="prior-authorization-metrics-`+m.From+`-`+m.To+`.csv"`)
		_, _ = w.Write([]byte(m.CSV()))
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// pendsReport is which missing evidence pended requests most in a period.
func (cfg Config) pendsReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if p, ok := period(w, r); ok {
		writeJSON(w, http.StatusOK, report.BuildPends(cfg.Cases.Select(nil), p, cfg.set.snapshot()))
	}
}

// GoldCardView is the programs in force and every provider's standing under them.
type GoldCardView struct {
	Enabled   bool `json:"enabled"`
	Programs  any  `json:"programs"`
	Standings any  `json:"standings"`
}

func (cfg Config) goldCard(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	v := GoldCardView{Programs: []any{}, Standings: []any{}}
	if cfg.GoldCard != nil {
		v.Enabled, v.Programs = true, cfg.GoldCard.Snapshot()
		v.Standings = cfg.GoldCard.Standings(cfg.Cases.Select(nil), time.Now().UTC())
	}
	writeJSON(w, http.StatusOK, v)
}

// replay runs a policy written in the language over the past cases it covers. It reads every stored request it
// replays, so it is audited like reading a case; it changes no case.
func (cfg Config) replay(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := policy.Parse("replay"+policy.Ext, body.Source)
	if err != nil {
		var pe *policy.ParseError
		if errors.As(err, &pe) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "line": pe.Line, "col": pe.Col})
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep := replay.Run(cfg.Cases, p)
	if _, err := cfg.Audit.Append("user:"+s.User, "policy.replayed", "", map[string]any{"policy": p.ID,
		"version": p.Version, "cases": rep.Cases, "changed": rep.Changed}); err != nil {
		writeError(w, http.StatusInternalServerError, "the replay could not be recorded, so it is not shown: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// overridesReport is how often the engine's denials were reversed, ?by=clause|provider|state.
func (cfg Config) overridesReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	p, ok := period(w, r)
	if !ok {
		return
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "clause"
	}
	o, err := report.BuildOverrides(cfg.Cases.Select(nil), p, by, cfg.set.snapshot())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// appealCase records the outcome of an appeal of a denied case. Appeals are decided outside Vaglio (by the payer's
// appeals process); recording the outcome is what lets the reversal analytics and the metrics count it.
func (cfg Config) appealCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body cases.Appeal
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := cfg.Cases.Appealed(r.PathValue("id"), s.User, cases.Appeal{Outcome: body.Outcome, Reason: body.Reason,
		Decided: body.Decided})
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
	case errors.Is(err, cases.ErrNotDenied):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// goldCardCounting switches which decisions a program counts (admin). The switch is an audit record first, so it is
// replayed at the next start and nobody's record changes without a name beside it.
func (cfg Config) goldCardCounting(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if cfg.GoldCard == nil {
		writeError(w, http.StatusNotFound, "no gold card programs are configured")
		return
	}
	var body struct {
		Count string `json:"count"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	probe := &goldcard.Config{Programs: cfg.GoldCard.Snapshot()}
	if err := probe.SetCounting(name, body.Count); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := cfg.Audit.Append("user:"+s.User, goldcard.ActCounting, "", map[string]string{"program": name,
		"count": body.Count}); err != nil {
		writeError(w, http.StatusInternalServerError, "the change could not be recorded, so it is not made: "+err.Error())
		return
	}
	_ = cfg.GoldCard.SetCounting(name, body.Count)
	writeJSON(w, http.StatusOK, cfg.GoldCard.Snapshot())
}

// policyVersions lists every version of a policy kept since this server first ran it, oldest first.
func (cfg Config) policyVersions(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.Versions == nil {
		writeJSON(w, http.StatusOK, []versions.Version{})
		return
	}
	writeJSON(w, http.StatusOK, cfg.Versions.List(r.PathValue("id")))
}

// policyVersion is one kept version's source.
func (cfg Config) policyVersion(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.Versions == nil {
		writeError(w, http.StatusNotFound, "no such version")
		return
	}
	src, err := cfg.Versions.Source(r.PathValue("id"), r.PathValue("sha"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "no such version")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(src)
}

// ReplayedCase is one case decided again by another version of its policy, beside the decision it was given.
type ReplayedCase struct {
	Was *decide.Result `json:"was"`
	Now *decide.Result `json:"now"`
}

// replayCase decides one stored case again under a source, for a side-by-side proof. Nothing changes; it is audited like
// reading the case.
func (cfg Config) replayCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Source string `json:"source"`
		Case   string `json:"case"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := cfg.Cases.Get(body.Case)
	if !ok || !s.Sees(c.Policy) {
		writeError(w, http.StatusNotFound, "no such case")
		return
	}
	p, err := policy.Parse("replay"+policy.Ext, body.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := replay.Decide(cfg.Cases, p, c)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := cfg.Cases.Viewed(c.ID, s.User); err != nil {
		writeError(w, http.StatusInternalServerError, "reading the case could not be recorded: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ReplayedCase{Was: c.Result, Now: res})
}
