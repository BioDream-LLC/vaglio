// Package um links Vaglio to a plan's own case-review (utilization management) system, where its nurses and
// physicians already work cases, so they need not move to Vaglio's screens.
//
// Out: every case Vaglio pends (missing evidence, a policy that cannot settle it, a denial only a clinician may make)
// opens a case in that system, carrying the proof, the missing evidence, the citations, the receipt, a link back to the
// case in Vaglio and the proof as a PDF. In: the reviewer's decision there comes back, pushed to /api/feeds/um or polled,
// and closes Vaglio's case as a review recorded under the reviewer's name, NPI and credential, so the audit log, the
// letters, the CMS metrics and the fairness report stay whole.
//
// The case-review system stays where the human decision is made. Vaglio never decides a case there, never closes one
// there, and never writes a decision back into it: it only opens cases and reads what people decided.
package um

import (
	"context"
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
)

// Opened is the case the system opened: its id, and where it is when the system says.
type Opened struct {
	Ref string
	URL string
}

// Decision is a reviewer's decision in the case-review system, as read from it.
type Decision struct {
	Case       string `json:"case,omitempty"` // Vaglio's case id, when the system kept it
	Ref        string `json:"ref,omitempty"`  // the system's id for the case
	Decision   string `json:"decision"`       // approve or deny
	Reason     string `json:"reason"`
	Reviewer   string `json:"reviewer"` // the reviewer's name or account in the system
	NPI        string `json:"npi,omitempty"`
	Credential string `json:"credential,omitempty"`
	Decided    string `json:"decided,omitempty"` // when, as the system says
}

// ErrNoPoll is a connector whose system pushes its decisions (to /api/feeds/um) and cannot be asked for them.
var ErrNoPoll = errors.New("this case-review system pushes its decisions; it is not polled")

// Connector is one kind of case-review system.
type Connector interface {
	// Open opens a case for a packet.
	Open(ctx context.Context, p Packet) (Opened, error)
	// Poll asks for the decision on the case ref; decided is false while nobody has decided it.
	Poll(ctx context.Context, ref string) (d Decision, decided bool, err error)
	// Parse reads the decisions the system pushed in one delivery.
	Parse(body []byte) ([]Decision, error)
}

// Result is what became of one decision from the system.
type Result struct {
	Case   string `json:"case,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Status string `json:"status"` // recorded, already, refused
	Error  string `json:"error,omitempty"`
}

// Link follows the audit log and opens a case in the system for every case Vaglio pends.
type Link struct {
	Name   string // the system's name, as recorded on the cases
	Conn   Connector
	Log    *audit.Log
	Cases  *cases.Store
	Cursor string // file holding the last audit record handled
	// Packet builds what a case is opened with (the server adds the receipt, the link back and the proof).
	Packet func(c cases.Case) (Packet, error)
	// Record records a decision from the system with the server's rules (who may deny, physician-only cases).
	Record func(d Decision) Result
	// PollEvery is how often open cases are asked about, for a system that is polled; 0 is never.
	PollEvery time.Duration
	// ReviewHere lets reviewers decide a linked case in Vaglio's console too. Off, the console refuses: the case is
	// worked in the case-review system, and two places to decide one case is how decisions are lost.
	ReviewHere bool

	mu       sync.Mutex
	done     int64
	finished chan struct{}
	opened   int
	recorded int
	lastErr  string
	lastTry  time.Time
	lastPoll time.Time
}

// Start reads the cursor and runs until ctx ends. With no cursor it starts at the beginning, so the cases already
// waiting when the link is first set up are opened too; anything no longer waiting is passed over.
func (l *Link) Start(ctx context.Context) error {
	if err := l.Load(); err != nil {
		return err
	}
	l.finished = make(chan struct{})
	go func() {
		defer close(l.finished)
		wait := time.Second
		for {
			n, err := l.Once(ctx)
			switch {
			case err != nil:
				wait = min(wait*2, 5*time.Minute)
			case n > 0:
				wait = 0
			default:
				wait = 2 * time.Second
			}
			if l.PollEvery > 0 && time.Since(l.lastPoll) >= l.PollEvery {
				l.PollOnce(ctx)
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

// Done is closed when the link has stopped.
func (l *Link) Done() <-chan struct{} { return l.finished }

// Load reads the cursor.
func (l *Link) Load() error {
	raw, err := os.ReadFile(l.Cursor)
	switch {
	case errors.Is(err, os.ErrNotExist):
		l.done = 0
		return nil
	case err != nil:
		return err
	}
	if l.done, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err != nil {
		return fmt.Errorf("%s: %w", l.Cursor, err)
	}
	return nil
}

func (l *Link) save() error {
	tmp := l.Cursor + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(l.done, 10)+"\n"), 0o600); err != nil {
		return err
	}
	return fsx.Replace(tmp, l.Cursor)
}

// Once opens cases for the pended cases after the cursor, in order, stopping at the first the system refuses (so none
// is skipped), and returns how many it opened.
func (l *Link) Once(ctx context.Context) (int, error) {
	n := 0
	for {
		page := l.Log.Records(l.done, 200)
		if len(page) == 0 {
			return n, nil
		}
		for _, r := range page {
			if r.Action == cases.ActOpened || r.Action == cases.ActEscalated {
				opened, err := l.open(ctx, r.Case)
				if err != nil {
					l.fail(err)
					return n, err
				}
				if opened {
					n++
				}
			}
			l.mu.Lock()
			l.done = r.Seq
			err := l.save()
			l.mu.Unlock()
			if err != nil {
				return n, err
			}
		}
	}
}

// open opens a case in the system for one Vaglio case, if it still waits and has none.
func (l *Link) open(ctx context.Context, id string) (bool, error) {
	c, ok := l.Cases.Get(id)
	if !ok || c.Status != cases.Pended || c.UM != nil {
		return false, nil
	}
	p, err := l.Packet(*c)
	if err != nil {
		return false, fmt.Errorf("case %s: %w", id, err)
	}
	l.mu.Lock()
	l.lastTry = time.Now()
	l.mu.Unlock()
	o, err := l.Conn.Open(ctx, p)
	if err != nil {
		return false, fmt.Errorf("opening case %s in %s: %w", id, l.Name, err)
	}
	if _, err := l.Cases.LinkUM(id, cases.UMLink{System: l.Name, Ref: o.Ref, URL: o.URL}); err != nil {
		if errors.Is(err, cases.ErrLinked) {
			return false, nil
		}
		return false, err
	}
	l.mu.Lock()
	l.opened++
	l.lastErr = ""
	l.mu.Unlock()
	return true, nil
}

// PollOnce asks the system about every case still open there, and records the decisions it has. It returns how many
// it recorded.
func (l *Link) PollOnce(ctx context.Context) int {
	l.mu.Lock()
	l.lastPoll = time.Now()
	l.mu.Unlock()
	n := 0
	for _, c := range l.Cases.Select(func(c *cases.Case) bool {
		return c.Status == cases.Pended && c.UM != nil && c.UM.System == l.Name
	}) {
		d, decided, err := l.Conn.Poll(ctx, c.UM.Ref)
		if errors.Is(err, ErrNoPoll) {
			return n
		}
		if err != nil {
			l.fail(fmt.Errorf("asking %s about %s: %w", l.Name, c.UM.Ref, err))
			continue
		}
		if !decided {
			continue
		}
		d.Ref, d.Case = c.UM.Ref, c.ID
		if r := l.Take(d); r.Status == "recorded" {
			n++
		}
	}
	return n
}

// Take records one decision from the system, pushed or polled: matched to its case by the system's id for it, or by
// Vaglio's id when the system kept that instead.
func (l *Link) Take(d Decision) Result {
	if d.Ref != "" {
		if c, ok := l.Cases.ByUMRef(l.Name, d.Ref); ok {
			if d.Case != "" && d.Case != c.ID {
				return Result{Case: d.Case, Ref: d.Ref, Status: "refused",
					Error: fmt.Sprintf("%s's case %s is Vaglio's case %s, not %s", l.Name, d.Ref, c.ID, d.Case)}
			}
			d.Case = c.ID
		}
	}
	if d.Case == "" {
		return Result{Ref: d.Ref, Status: "refused", Error: "no case here is linked to that case-review case"}
	}
	c, ok := l.Cases.Get(d.Case)
	switch {
	case !ok:
		return Result{Case: d.Case, Ref: d.Ref, Status: "refused", Error: "no such case"}
	case c.UM == nil || c.UM.System != l.Name:
		return Result{Case: d.Case, Ref: d.Ref, Status: "refused", Error: "that case was not opened in " + l.Name}
	case d.Ref != "" && d.Ref != c.UM.Ref:
		return Result{Case: d.Case, Ref: d.Ref, Status: "refused",
			Error: fmt.Sprintf("case %s is %s's case %s, not %s", c.ID, l.Name, c.UM.Ref, d.Ref)}
	}
	d.Ref = c.UM.Ref
	if c.Review != nil && c.Review.Via == l.Name && c.Review.Reference == d.Ref && c.Review.Decision == d.Decision {
		return Result{Case: d.Case, Ref: d.Ref, Status: "already"}
	}
	r := l.Record(d)
	r.Case, r.Ref = d.Case, d.Ref
	if r.Status == "recorded" {
		l.mu.Lock()
		l.recorded++
		l.mu.Unlock()
	}
	return r
}

func (l *Link) fail(err error) {
	l.mu.Lock()
	l.lastErr = err.Error()
	l.mu.Unlock()
}

// Status is where the link stands.
type Status struct {
	System    string `json:"system"`
	Handled   int64  `json:"handled"`  // the last audit record looked at
	Opened    int    `json:"opened"`   // cases opened there since the server started
	Recorded  int    `json:"recorded"` // decisions recorded from there since the server started
	Waiting   int    `json:"waiting"`  // cases open there that nobody has decided yet
	Polled    bool   `json:"polled"`
	LastError string `json:"lastError,omitempty"`
	LastTry   string `json:"lastTry,omitempty"`
}

// Status reports the link's place.
func (l *Link) Status() Status {
	waiting := len(l.Cases.Select(func(c *cases.Case) bool {
		return c.Status == cases.Pended && c.UM != nil && c.UM.System == l.Name
	}))
	l.mu.Lock()
	defer l.mu.Unlock()
	s := Status{System: l.Name, Handled: l.done, Opened: l.opened, Recorded: l.recorded, Waiting: waiting,
		Polled: l.PollEvery > 0, LastError: l.lastErr}
	if !l.lastTry.IsZero() {
		s.LastTry = l.lastTry.UTC().Format(time.RFC3339)
	}
	return s
}
