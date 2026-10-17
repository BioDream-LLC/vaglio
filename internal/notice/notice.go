// Package notice mails the member their letter: a denial, an approval, a timeframe extension, an appeal's outcome, as
// the plan chooses. It follows the audit log from a cursor kept on disk, as the outbound feed does, and for each record
// that owes the member a notice it writes the case's letter (internal/letter, the same words the console shows), lays
// it out as a PDF, and hands it to a sender: a folder a mail house collects from, or a print-and-mail service (Lob).
// Each attempt is recorded on the case (case.noticed): what was sent, by which service, under what id, or why not.
//
// Mail is sent at most once per record as far as Vaglio can make it: the case remembers which records it has sent
// notices for, and the print service is given an idempotency key from the record's hash, so a crash between sending and
// recording does not mail it twice (Lob keeps keys for 24 hours). A letter says what stands when it is written, a
// moment after the record: if a reviewer approves a case in that moment, the denial's notice says approved.
package notice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/letter"
)

// The events a plan may mail notices for.
const (
	Denied   = "denied"   // a denial, by the engine or a person
	Approved = "approved" // an approval by a person (the engine's own approvals are many; add "approved-all" for those)
	AllOK    = "approved-all"
	Extended = "extended" // the decision timeframe extended
	Appeal   = "appeal"   // an appeal's outcome
)

// Mail is one letter to send.
type Mail struct {
	Key      string // idempotency key, unique per record
	CaseID   string
	Event    string
	To, From Address
	PDF      []byte
}

// Sender sends a letter. final says an error is not worth retrying (a refused address, a bad key); otherwise the
// mailer stops and tries again later, from the same record.
type Sender interface {
	Name() string
	Send(ctx context.Context, m Mail) (id string, final bool, err error)
	// Window is whether the letter's first page carries the addresses for a window envelope.
	Window() bool
}

// Mailer follows the log and mails the notices owed.
type Mailer struct {
	Log     *audit.Log
	Cases   *cases.Store
	Plan    *letter.Plan
	Sender  Sender
	From    Address
	Events  []string
	Cursor  string                     // file holding the last record handled
	Receipt func(caseID string) string // the receipt of the decision that stands
	Now     func() time.Time           // the letter's date

	mu       sync.Mutex
	done     int64
	lastErr  string
	lastTry  time.Time
	finished chan struct{}
}

// Status is how the mailer stands, for the console.
type Status struct {
	Service string    `json:"service"`
	Through int64     `json:"through"` // the last record handled
	Error   string    `json:"error,omitempty"`
	LastTry time.Time `json:"lastTry,omitempty"`
}

func (m *Mailer) wants(event string) bool {
	for _, e := range m.Events {
		if e == event || (e == AllOK && event == Approved) {
			return true
		}
	}
	return false
}

func (m *Mailer) wantsAllApprovals() bool {
	for _, e := range m.Events {
		if e == AllOK {
			return true
		}
	}
	return false
}

// owed is the notice a record owes the member, or "".
func (m *Mailer) owed(r audit.Record) string {
	var ev string
	switch r.Action {
	case cases.ActOpened:
		var o struct {
			Answer cases.Answer `json:"answer"`
		}
		if json.Unmarshal(r.Data, &o) != nil {
			return ""
		}
		switch {
		case o.Answer.Decision == "deny":
			ev = Denied
		case o.Answer.Decision == "approve" && m.wantsAllApprovals():
			ev = Approved
		}
	case cases.ActReviewed:
		var rv struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(r.Data, &rv) != nil {
			return ""
		}
		switch rv.Decision {
		case "deny":
			ev = Denied
		case "approve":
			ev = Approved
		}
	case cases.ActExtended:
		ev = Extended
	case cases.ActAppealed:
		ev = Appeal
	}
	if ev == "" || !m.wants(ev) {
		return ""
	}
	return ev
}

// Start loads the cursor and runs until ctx ends.
func (m *Mailer) Start(ctx context.Context) error {
	if err := m.Load(); err != nil {
		return err
	}
	m.finished = make(chan struct{})
	go func() {
		defer close(m.finished)
		wait := time.Second
		for {
			n, err := m.Once(ctx)
			switch {
			case err != nil:
				wait = min(wait*2, 10*time.Minute)
			case n > 0:
				wait = 0
			default:
				wait = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
	return nil
}

// Load reads the cursor; a mailer with none starts at the log's head, so it never mails the past.
func (m *Mailer) Load() error {
	if m.Now == nil {
		m.Now = time.Now
	}
	raw, err := os.ReadFile(m.Cursor)
	switch {
	case errors.Is(err, os.ErrNotExist):
		n, _ := m.Log.Head()
		m.done = int64(n)
		if err := m.save(); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if m.done, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err != nil {
			return fmt.Errorf("%s: %w", m.Cursor, err)
		}
	}
	return nil
}

// Done is closed when the mailer has stopped.
func (m *Mailer) Done() <-chan struct{} { return m.finished }

func (m *Mailer) save() error {
	tmp := m.Cursor + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(m.done, 10)+"\n"), 0o600); err != nil {
		return err
	}
	return fsx.Replace(tmp, m.Cursor)
}

// Once handles the records since the cursor, and returns how many notices it recorded. A retryable failure stops it at
// that record, to be tried again.
func (m *Mailer) Once(ctx context.Context) (int, error) {
	m.mu.Lock()
	after := m.done
	m.mu.Unlock()
	sent := 0
	for {
		page := m.Log.Records(after, 200)
		if len(page) == 0 {
			return sent, nil
		}
		for _, r := range page {
			if ev := m.owed(r); ev != "" && r.Case != "" {
				err := m.notify(ctx, r, ev)
				m.mu.Lock()
				m.lastTry = time.Now()
				if err != nil {
					m.lastErr = err.Error()
					m.mu.Unlock()
					return sent, err
				}
				m.lastErr = ""
				m.mu.Unlock()
				sent++
			}
			after = r.Seq
			m.mu.Lock()
			m.done = after
			err := m.save()
			m.mu.Unlock()
			if err != nil {
				return sent, err
			}
		}
	}
}

// notify mails one notice and records it; it returns an error only when the notice should be tried again.
func (m *Mailer) notify(ctx context.Context, r audit.Record, ev string) error {
	c, ok := m.Cases.Get(r.Case)
	if !ok {
		return nil
	}
	for _, n := range c.Notices {
		if n.For == r.Seq {
			return nil // sent before a crash lost the cursor
		}
	}
	n := cases.Notice{Event: ev, For: r.Seq, Service: m.Sender.Name()}
	record := func(n cases.Notice) error { return m.Cases.Noticed(c.ID, n) }
	b, err := m.Cases.Entries(c)
	if err != nil {
		n.Error = "the request could not be read: " + err.Error()
		return record(n)
	}
	to, lang, err := MemberAddress(b, c.Patient)
	if err != nil {
		n.Error = err.Error()
		return record(n)
	}
	n.Lang = lang
	if m.Receipt != nil {
		n.Receipt = m.Receipt(c.ID)
	}
	l, err := letter.WriteFor(*c, lang, n.Receipt, m.Now(), m.Plan)
	if err != nil {
		n.Error = err.Error()
		return record(n)
	}
	pdf := PDF(l, Layout{Window: m.Sender.Window(), From: m.From, To: to})
	key := "vaglio-" + r.Hash
	if len(key) > 64 {
		key = key[:64]
	}
	id, final, err := m.Sender.Send(ctx, Mail{Key: key, CaseID: c.ID, Event: ev, To: to, From: m.From, PDF: pdf})
	if err != nil {
		if !final {
			return fmt.Errorf("mailing the %s notice of case %s: %w", ev, c.ID, err)
		}
		n.Error = err.Error()
		return record(n)
	}
	n.ID = id
	return record(n)
}

// Status is the mailer's state.
func (m *Mailer) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Service: m.Sender.Name(), Through: m.done, Error: m.lastErr, LastTry: m.lastTry}
}
