package server

import (
	"errors"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/letter"
	"net"
	"net/http"
	"sync"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
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
// ReceiptOf is the receipt of the decision that stands on a case, "" when it has none: what a mailed letter carries.
func ReceiptOf(cs *cases.Store, id string) string {
	if rs := receiptsFor(cs.History(id)); len(rs) > 0 {
		return rs[len(rs)-1].Receipt
	}
	return ""
}

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

// caseLetter is the letter for a case's current decision, in ?lang= (en by default), with the receipt of the decision
// that stands. Reading it is recorded like reading the case: it is the patient's.
func (cfg Config) caseLetter(w http.ResponseWriter, r *http.Request, s auth.Session) {
	c, ok := cfg.Cases.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such case")
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
	code := ""
	if rs := receiptsFor(cfg.Cases.History(c.ID)); len(rs) > 0 {
		code = rs[len(rs)-1].Receipt
	}
	out, err := letter.WriteFor(*c, l, code, time.Now(), cfg.Letters)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := cfg.Cases.Viewed(c.ID, s.User); err != nil {
		writeError(w, http.StatusInternalServerError, "reading the case could not be recorded: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// failures counts failed attempts per key over a window, for refusing a key that has failed too often.
type failures struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string][]time.Time
}

func newFailures(max int, window time.Duration) *failures {
	return &failures{max: max, window: window, seen: map[string][]time.Time{}}
}

func (f *failures) recent(key string, now time.Time) []time.Time {
	ts := f.seen[key]
	i := 0
	for i < len(ts) && ts[i].Before(now.Add(-f.window)) {
		i++
	}
	return ts[i:]
}

// over is whether key has failed max times within the window.
func (f *failures) over(key string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recent(key, now)) >= f.max
}

func (f *failures) add(key string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) > 10000 {
		for k := range f.seen {
			if len(f.recent(k, now)) == 0 {
				delete(f.seen, k)
			}
		}
	}
	f.seen[key] = append(f.recent(key, now), now)
}
