package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/feed"
)

// exportEvents streams every record about a case after a sequence number as NDJSON, one event a line, the same events
// the webhook pushes: for a plan's nightly load, or to fill in history before a webhook starts. Reading it is in the
// audit log (the events hold patient records); that record is not an event itself.
func (cfg Config) exportEvents(w http.ResponseWriter, r *http.Request, s auth.Session) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	evs := feed.EventsWith(cfg.Audit, after, limit, cfg.PromiseOf)
	if _, err := cfg.Audit.Append("user:"+s.User, "export.read", "", map[string]any{"after": after, "events": len(evs)}); err != nil {
		writeError(w, http.StatusInternalServerError, "the export could not be recorded, so it is not given: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if len(evs) > 0 {
		w.Header().Set("Vaglio-Last-Seq", strconv.FormatInt(evs[len(evs)-1].Seq, 10))
	}
	enc := json.NewEncoder(w)
	for _, e := range evs {
		_ = enc.Encode(e)
	}
}

func (cfg Config) exportStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if cfg.Webhook == nil {
		writeJSON(w, http.StatusOK, map[string]any{"webhook": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhook": cfg.Webhook.Status()})
}
