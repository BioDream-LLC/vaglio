package server

import (
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/report"
)

// A shadow pilot (serve -shadow) runs beside the plan's own prior authorization process and changes nothing in it.
// The PAS server sends it every request as it would a live decision service; Vaglio decides and records each, and
// answers 404, which the decision service protocol defines as "decides nothing for this item", so the PAS server
// answers as it would without Vaglio. Nothing else reaches anyone: advice, eligibility, NCPDP, the provider portal
// and keyed-in intake answer 404 too, and serve refuses letters to members, webhooks and exports beside -shadow.
// What the plan decided comes in on /api/feeds/outcomes, and clinicians rule on the disagreements.

// shadowOff are the paths a shadow server does not serve: each would answer someone outside the plan's process.
func shadowOff(path string) bool {
	for _, p := range []string{"/api/pas/advise", "/api/pas/eligibility", "/api/pas/ncpdp", "/api/portal/", "/api/intake"} {
		if path == p || strings.HasPrefix(path, p+"/") || (strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
}

const shadowWhy = "this is a shadow pilot's server: it decides beside the plan's own process and answers no one"

// ShadowAnswer is a shadow server's 404 to the PAS server: decided and recorded, and given to no one.
type ShadowAnswer struct {
	Shadow   bool   `json:"shadow"`
	Error    string `json:"error"`
	Case     string `json:"case,omitempty"`
	Decision string `json:"decision,omitempty"` // what it would have answered, for the PAS server's own log if it keeps one
}

// PlanOutcomeIn is one of the plan's decisions on the outcomes feed.
type PlanOutcomeIn struct {
	// Claim and Item find the case, as the appeals feed does (the Claim identifier system|value, or its value alone,
	// and the item's sequence); Case may be given instead.
	Claim     string `json:"claim,omitempty"`
	Item      string `json:"item,omitempty"`
	Case      string `json:"case,omitempty"`
	Decision  string `json:"decision"`
	By        string `json:"by,omitempty"`
	Decided   string `json:"decided,omitempty"`
	Appeal    string `json:"appeal,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// OutcomeResult is what became of one outcome.
type OutcomeResult struct {
	Claim  string `json:"claim,omitempty"`
	Item   string `json:"item,omitempty"`
	Case   string `json:"case,omitempty"`
	Status string `json:"status"` // recorded, already-recorded, refused
	Error  string `json:"error,omitempty"`
}

// outcomesFeed takes the plan's own decisions, by bearer token (-outcomes-token-env): a JSON array, or CSV with a
// header row (claim, item, decision, by, decided, appeal, reference). Each is answered on its own.
func (cfg Config) outcomesFeed(w http.ResponseWriter, r *http.Request) {
	if cfg.OutcomesToken == "" {
		writeError(w, http.StatusNotFound, "the outcomes feed is off: start a shadow server with -outcomes-token-env")
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.OutcomesToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "a bearer token is required")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxBody)
	var batch []PlanOutcomeIn
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		batch, err = outcomesCSV(body)
	} else {
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		err = dec.Decode(&batch)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "the feed sends a JSON array of outcomes, or CSV: "+err.Error())
		return
	}
	if len(batch) > maxFeed {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d outcomes per delivery", maxFeed))
		return
	}
	out := make([]OutcomeResult, 0, len(batch))
	for _, o := range batch {
		out = append(out, cfg.RecordOutcome(o))
	}
	writeJSON(w, http.StatusOK, out)
}

func outcomesCSV(r io.Reader) ([]PlanOutcomeIn, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["decision"]; !ok {
		return nil, errors.New("no decision column")
	}
	var out []PlanOutcomeIn
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		out = append(out, PlanOutcomeIn{Claim: get("claim"), Item: get("item"), Case: get("case"), Decision: get("decision"),
			By: get("by"), Decided: get("decided"), Appeal: get("appeal"), Reference: get("reference")})
	}
}

// RecordOutcome records one of the plan's decisions against the shadow case it decides.
func (cfg Config) RecordOutcome(o PlanOutcomeIn) OutcomeResult {
	res := OutcomeResult{Claim: o.Claim, Item: o.Item, Status: "refused"}
	id, err := cfg.shadowCase(o)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Case = id
	_, err = cfg.Cases.PlanDecided(id, cases.PlanOutcome{Decision: o.Decision, By: o.By, Decided: o.Decided,
		Appeal: o.Appeal, Reference: o.Reference})
	switch {
	case errors.Is(err, cases.ErrSameOutcome):
		res.Status = "already-recorded"
	case err != nil:
		res.Error = err.Error()
	default:
		res.Status = "recorded"
	}
	return res
}

// shadowCase is the case an outcome is for: by id, or the one current case for the Claim and item.
func (cfg Config) shadowCase(o PlanOutcomeIn) (string, error) {
	if o.Case != "" {
		if _, ok := cfg.Cases.Get(o.Case); !ok {
			return "", errors.New("no case " + o.Case)
		}
		return o.Case, nil
	}
	if o.Claim == "" {
		return "", errors.New("an outcome names its case, or the Claim (and item) it decides")
	}
	item := o.Item
	if item == "" {
		item = "1"
	}
	matches := cfg.Cases.Select(func(c *cases.Case) bool {
		if c.SupersededBy != "" || c.Item != item {
			return false
		}
		if c.Claim == o.Claim {
			return true
		}
		_, value, ok := strings.Cut(c.Claim, "|")
		return ok && !strings.Contains(o.Claim, "|") && value == o.Claim
	})
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no request for Claim %s item %s", o.Claim, item)
	case 1:
		return matches[0].ID, nil
	}
	return "", fmt.Errorf("Claim %s item %s matches %d requests; give the Claim's system too, or the case", o.Claim, item, len(matches))
}

// ruleCase records a clinician's ruling on a disagreement.
func (cfg Config) ruleCase(w http.ResponseWriter, r *http.Request, s auth.Session) {
	u, ok, err := cfg.Sessions.Account(s.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || !u.Clinician() {
		writeError(w, http.StatusForbidden, "only a clinician rules on a disagreement: an account with an NPI and a "+
			"credential (vaglio user clinician)")
		return
	}
	var body struct {
		Supports string `json:"supports"`
		Note     string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := cfg.Cases.Rule(r.PathValue("id"), s.User, cases.Ruling{Supports: body.Supports, Note: body.Note,
		Credential: u.Credential, NPI: u.NPI})
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
	case err != nil:
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// shadowReport is the shadow pilot's report, as JSON or ?format=csv (the disagreements).
func (cfg Config) shadowReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	rep := report.BuildShadow(cfg.Cases.Select(nil))
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="shadow-pilot-disagreements.csv"`)
		_, _ = w.Write([]byte(rep.CSV()))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
