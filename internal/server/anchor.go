package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/anchor"
	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/receipt"
)

// The log's anchors (internal/anchor): signed checkpoints, their witnesses, and the proofs a receipt carries. All of it
// is hashes and signatures, so it is public like the receipt check, and limited per address the same way.

// ReceiptAnswer is a receipt's check, and its record's proof against the log's anchors when the server keeps them.
type ReceiptAnswer struct {
	*receipt.Check
	Anchor *anchor.ReceiptProof `json:"anchor,omitempty"`
}

func (cfg Config) anchorPublic(w http.ResponseWriter, r *http.Request) bool {
	if cfg.Anchor == nil {
		writeError(w, http.StatusNotFound, "this server keeps no anchors of its log (serve -anchor-every)")
		return false
	}
	if !cfg.receiptLimit.allow(clientIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many requests from this address; try again in a minute")
		return false
	}
	return true
}

func (cfg Config) anchorStatus(w http.ResponseWriter, r *http.Request) {
	if cfg.anchorPublic(w, r) {
		writeJSON(w, http.StatusOK, cfg.Anchor.Status())
	}
}

func (cfg Config) anchorCheckpoints(w http.ResponseWriter, r *http.Request) {
	if cfg.anchorPublic(w, r) {
		writeJSON(w, http.StatusOK, cfg.Anchor.Checkpoints())
	}
}

// anchorConsistency proves a later checkpoint extends an earlier one: ?from=SIZE&to=SIZE.
func (cfg Config) anchorConsistency(w http.ResponseWriter, r *http.Request) {
	if !cfg.anchorPublic(w, r) {
		return
	}
	from, err1 := strconv.Atoi(r.URL.Query().Get("from"))
	to, err2 := strconv.Atoi(r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "give ?from=SIZE&to=SIZE, the sizes of two checkpoints")
		return
	}
	p, err := cfg.Anchor.Consistency(from, to)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// anchorWitness serves a witness's kept proof (an .ots file, a time-stamp token), for its own tools to check.
func (cfg Config) anchorWitness(w http.ResponseWriter, r *http.Request) {
	if !cfg.anchorPublic(w, r) {
		return
	}
	name := "witness/" + r.PathValue("file")
	raw, err := cfg.Anchor.File(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such proof")
		return
	}
	ctype := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".tsr"):
		ctype = "application/timestamp-reply"
	case strings.HasSuffix(name, ".json"):
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimPrefix(name, "witness/")+`"`)
	_, _ = w.Write(raw)
}

// anchorNow signs a checkpoint now (an administrator's), and sends it to the witnesses.
func (cfg Config) anchorNow(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.Anchor == nil {
		writeError(w, http.StatusNotFound, "this server keeps no anchors of its log (serve -anchor-every)")
		return
	}
	c, err := cfg.Anchor.Checkpoint(r.Context(), false)
	switch {
	case err != nil:
		writeError(w, http.StatusConflict, err.Error())
	case c == nil:
		writeJSON(w, http.StatusOK, map[string]any{"checkpoint": nil, "note": "the last checkpoint covers the log already"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"checkpoint": c})
	}
}

// receiptProof adds the anchor proof to a receipt's check.
func (cfg Config) receiptProof(c *receipt.Check) ReceiptAnswer {
	a := ReceiptAnswer{Check: c}
	if cfg.Anchor != nil {
		if p, err := cfg.Anchor.Prove(c.Record.Seq); err == nil {
			a.Anchor = p
		} else {
			a.Anchor = &anchor.ReceiptProof{Seq: c.Record.Seq, Problem: err.Error()}
		}
	}
	return a
}

// receiptAndPromise is the receipt of a case's first decision and, when the log is anchored, the server's signed
// promise of where that decision sits in the log.
func (cfg Config) receiptAndPromise(caseID string) (string, *anchor.Promise) {
	for _, r := range cfg.Audit.ForCase(caseID) {
		if !receipt.Decisions[r.Action] {
			continue
		}
		var p *anchor.Promise
		if cfg.Anchor != nil {
			p, _ = cfg.Anchor.Promise(r.Seq, r.Hash) // a hash that is not one would be a broken log: no promise then
		}
		return receipt.Code(r.Hash), p
	}
	return "", nil
}

// PromiseOf is a decision record's promise for the outbound feeds, nil for any other record or when the log is not
// anchored: a receiver that keeps it holds the same proof as whoever got the decision's answer.
func (cfg Config) PromiseOf(r audit.Record) any {
	if cfg.Anchor == nil || !receipt.Decisions[r.Action] {
		return nil
	}
	p, err := cfg.Anchor.Promise(r.Seq, r.Hash)
	if err != nil {
		return nil
	}
	return p
}
