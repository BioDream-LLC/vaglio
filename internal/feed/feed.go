// Package feed sends what happens to cases out of Vaglio, for the plan's own systems (claims, care management, a data
// warehouse): every audit record about a case, in order, as JSON events. A webhook gets them pushed, signed, at least
// once and in order, from a cursor kept on disk; the same events can be pulled as NDJSON from the audit log.
//
// An event is the audit record itself, hash and previous hash included, so a receiver can check the chain for
// itself: nothing is summarized or recomputed on the way out.
package feed

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Event is one record about a case, as sent.
type Event struct {
	audit.Record
	// Type is the record's action, such as case.opened or case.reviewed.
	Type string `json:"type"`
	// Promise, on a decision when the log is anchored, is the server's signed promise of the record's place in the
	// log: a receiver that keeps it can later prove the record was changed or left out (internal/anchor/promise.go).
	Promise any `json:"promise,omitempty"`
}

// Wanted is whether a record goes out: records about a case, but not the reading of one (case.viewed), which would
// flood a receiver with every page view.
func Wanted(r audit.Record) bool {
	return r.Case != "" && strings.HasPrefix(r.Action, "case.") && r.Action != "case.viewed"
}

// Events are the wanted records after seq, at most limit.
func Events(l *audit.Log, after int64, limit int) []Event { return EventsWith(l, after, limit, nil) }

// EventsWith is Events with each record's promise from promise (nil, or one returning nil, for none).
func EventsWith(l *audit.Log, after int64, limit int, promise func(audit.Record) any) []Event {
	var out []Event
	for len(out) < limit || limit <= 0 {
		page := l.Records(after, 500)
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			after = r.Seq
			if Wanted(r) {
				ev := Event{Record: r, Type: r.Action}
				if promise != nil {
					ev.Promise = promise(r)
				}
				out = append(out, ev)
				if limit > 0 && len(out) == limit {
					break
				}
			}
		}
	}
	return out
}

// Sign is the signature header of a body sent at t: "t=UNIX,v1=HEX", HEX the HMAC-SHA256 under the secret of the
// decimal time, a dot and the body. A receiver recomputes it, compares in constant time, and refuses a t more than a
// few minutes old, which stops a captured delivery from being replayed later.
func Sign(secret []byte, t time.Time, body []byte) string {
	m := hmac.New(sha256.New, secret)
	ts := strconv.FormatInt(t.Unix(), 10)
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature header against a body, accepting it within skew of now.
func Verify(secret []byte, header string, body []byte, now time.Time, skew time.Duration) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return errors.New("no signature")
	}
	t := time.Unix(n, 0)
	if d := now.Sub(t); d > skew || d < -skew {
		return errors.New("the signature is too old or from the future")
	}
	want := Sign(secret, t, body)
	if !hmac.Equal([]byte(want), []byte("t="+ts+",v1="+sig)) {
		return errors.New("the signature does not match")
	}
	return nil
}

// Webhook pushes events to one URL.
type Webhook struct {
	URL    string
	Secret []byte
	Cursor string // file holding the last sequence delivered
	Log    *audit.Log
	Client *http.Client
	Batch  int // events per delivery, 100 when 0
	// Promise, when set, gives a record's promise (nil for none): set by the server when it anchors its log.
	Promise func(audit.Record) any

	mu        sync.Mutex
	done      chan struct{}
	delivered int64
	lastErr   string
	lastTry   time.Time
}

// Check refuses a webhook URL that is not https, or plain http to this machine (a receiver under test).
func Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && ((ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost") {
		return nil
	}
	return fmt.Errorf("%s must be https (plain http only to this machine): the events hold patient records", raw)
}

// Start reads the cursor and sends until ctx ends. A new webhook starts from now, not from the first record: the
// plan pulls history from the export.
func (w *Webhook) Start(ctx context.Context) error {
	if err := Check(w.URL); err != nil {
		return err
	}
	if len(w.Secret) < 16 {
		return errors.New("the webhook secret is at least 16 bytes")
	}
	raw, err := os.ReadFile(w.Cursor)
	switch {
	case errors.Is(err, os.ErrNotExist):
		n, _ := w.Log.Head()
		w.delivered = int64(n)
		if err := w.save(); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if w.delivered, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err != nil {
			return fmt.Errorf("%s: %w", w.Cursor, err)
		}
	}
	if w.Client == nil {
		w.Client = &http.Client{Timeout: 15 * time.Second}
	}
	w.done = make(chan struct{})
	go w.run(ctx)
	return nil
}

// Done is closed when the webhook has stopped after its context ended.
func (w *Webhook) Done() <-chan struct{} { return w.done }

func (w *Webhook) save() error {
	return fsx.WriteFile(w.Cursor, []byte(fmt.Sprintf("%d\n", w.delivered)), 0o600)
}

func (w *Webhook) run(ctx context.Context) {
	defer close(w.done)
	wait := time.Second
	for {
		n, err := w.Once(ctx)
		switch {
		case err != nil:
			wait = min(wait*2, 5*time.Minute)
		case n > 0:
			wait = 0
		default:
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Once delivers the next batch, if any, and returns how many events it delivered.
func (w *Webhook) Once(ctx context.Context) (int, error) {
	w.mu.Lock()
	after := w.delivered
	w.mu.Unlock()
	batch := w.Batch
	if batch <= 0 {
		batch = 100
	}
	evs := EventsWith(w.Log, after, batch, w.Promise)
	last := after
	if len(evs) > 0 {
		last = evs[len(evs)-1].Seq
	} else if n, _ := w.Log.Head(); int64(n) > after {
		last = int64(n) // only unwanted records since: move past them
	}
	if len(evs) > 0 {
		body, _ := json.Marshal(map[string]any{"events": evs})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Vaglio-Signature", Sign(w.Secret, time.Now(), body))
		req.Header.Set("Vaglio-Delivery", fmt.Sprintf("%d-%d", evs[0].Seq, last))
		resp, err := w.Client.Do(req)
		w.mu.Lock()
		w.lastTry = time.Now()
		w.mu.Unlock()
		if err != nil {
			w.fail(err.Error())
			return 0, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			err := fmt.Errorf("the receiver answered %s", resp.Status)
			w.fail(err.Error())
			return 0, err
		}
	}
	if last == after {
		return 0, nil
	}
	w.mu.Lock()
	w.delivered, w.lastErr = last, ""
	err := w.save()
	w.mu.Unlock()
	return len(evs), err
}

func (w *Webhook) fail(msg string) {
	w.mu.Lock()
	w.lastErr = msg
	w.mu.Unlock()
}

// Status is where the webhook stands.
type Status struct {
	URL       string `json:"url"`
	Delivered int64  `json:"delivered"` // the last sequence delivered
	Behind    int    `json:"behind"`    // records since, wanted or not
	LastError string `json:"lastError,omitempty"`
	LastTry   string `json:"lastTry,omitempty"`
}

// Status reports the webhook's place.
func (w *Webhook) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, _ := w.Log.Head()
	s := Status{URL: w.URL, Delivered: w.delivered, Behind: max(0, n-int(w.delivered)), LastError: w.lastErr}
	if !w.lastTry.IsZero() {
		s.LastTry = w.lastTry.UTC().Format(time.RFC3339)
	}
	return s
}
