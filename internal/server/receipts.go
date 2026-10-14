package server

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/receipt"
)

// Receipt is one decision on a case and the receipt that checks it.
type Receipt struct {
	Receipt string `json:"receipt"`
	Action  string `json:"action"`
	Time    string `json:"time"`
	Seq     int64  `json:"seq"`
}

// receiptsFor lists the receipts of a case's decisions, oldest first.
func receiptsFor(history []audit.Record) []Receipt {
	out := []Receipt{}
	for _, r := range history {
		if receipt.Decisions[r.Action] {
			out = append(out, Receipt{Receipt: receipt.Code(r.Hash), Action: r.Action, Time: r.Time, Seq: r.Seq})
		}
	}
	return out
}

// checkReceipt is the one endpoint anyone may call without signing in: a patient, a provider or a regulator holding a
// receipt checks it against the log. It tells nothing about the patient (see package receipt), and it is limited per
// address so it cannot be used to sweep the log for receipts.
func (cfg Config) checkReceipt(w http.ResponseWriter, r *http.Request) {
	if !cfg.receiptLimit.allow(clientIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many receipts checked from this address; try again in a minute")
		return
	}
	c, err := receipt.Find(cfg.Audit.Records(0, 0), r.PathValue("code"))
	switch {
	case errors.Is(err, receipt.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// clientIP is the address a request came from (not a header a client can set).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter allows each key a number of requests per minute.
type limiter struct {
	mu     sync.Mutex
	per    int
	window map[string][]time.Time
}

func newLimiter(perMinute int) *limiter {
	return &limiter{per: perMinute, window: map[string][]time.Time{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Minute)
	if len(l.window) > 10000 {
		// Forget addresses quiet for a minute, so the map cannot grow without bound.
		for k, ts := range l.window {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(l.window, k)
			}
		}
	}
	ts := l.window[key]
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.per {
		l.window[key] = ts
		return false
	}
	l.window[key] = append(ts, now)
	return true
}
