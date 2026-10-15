package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/reader"
)

// AutoRead reads a pended request's documents as soon as it is pended, so a reviewer opens a case with the model's
// proposals waiting rather than asking for them one document at a time. With Approve, it goes one step further, and
// only ever toward an approval: when the policy, given every quote-checked finding of those readings, approves, the case
// is approved without a person. A finding is quote-checked when its quote is word for word in the document (the reader
// refuses any other). A reading that would deny or pend changes nothing: the case waits for a reviewer, with the
// proposals ready to confirm. Of the automatic approvals, SamplePercent are set aside for a reviewer to check after the
// fact; the share they disagree with is what says whether to keep Approve on.
type AutoRead struct {
	Approve       bool
	SamplePercent int // 0 to 100
	Workers       int // documents read at once (default 2)

	once  sync.Once
	queue chan string
	mu    sync.Mutex
	stats AutoReadStats
}

// AutoReadStats is what the automatic reader has done since the server started.
type AutoReadStats struct {
	Queued    int    `json:"queued"`
	Read      int    `json:"read"`     // documents read
	Failed    int    `json:"failed"`   // documents the model did not answer for
	Approved  int    `json:"approved"` // cases approved on their documents
	Waiting   int    `json:"waiting"`  // in the queue now
	Dropped   int    `json:"dropped"`  // not queued because the queue was full: a reviewer reads them by hand
	Sampled   int    `json:"sampled"`  // automatic approvals set aside for a check
	LastError string `json:"lastError,omitempty"`
}

// queueSize bounds the cases waiting to be read. A burst beyond it is left to reviewers, never blocks a decision.
const queueSize = 512

func (a *AutoRead) start(cfg Config) {
	a.once.Do(func() {
		a.queue = make(chan string, queueSize)
		n := a.Workers
		if n <= 0 {
			n = 2
		}
		for i := 0; i < n; i++ {
			go func() {
				for id := range a.queue {
					a.mu.Lock()
					a.stats.Waiting--
					a.mu.Unlock()
					cfg.autoRead(id)
				}
			}()
		}
	})
}

// enqueue asks for a case to be read. It never blocks.
func (a *AutoRead) enqueue(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case a.queue <- id:
		a.stats.Queued++
		a.stats.Waiting++
	default:
		a.stats.Dropped++
	}
}

// Stats is a snapshot of what the reader has done.
func (a *AutoRead) Stats() AutoReadStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

func (a *AutoRead) note(f func(*AutoReadStats)) {
	a.mu.Lock()
	f(&a.stats)
	a.mu.Unlock()
}

// readSoon queues a pended case with documents for the automatic reader, when there is one.
func (cfg Config) readSoon(c *cases.Case) {
	if cfg.AutoRead == nil || cfg.Reader == nil || c.Status != cases.Pended {
		return
	}
	cfg.AutoRead.enqueue(c.ID)
}

// autoRead reads every document of a case no reading covers yet, then, with Approve, approves when the readings do.
func (cfg Config) autoRead(id string) {
	a := cfg.AutoRead
	c, ok := cfg.Cases.Get(id)
	if !ok || c.Status != cases.Pended {
		return
	}
	p := cfg.set.get(c.Policy)
	if p == nil {
		return
	}
	req, err := cfg.request(c)
	if err != nil {
		a.note(func(s *AutoReadStats) { s.Failed++; s.LastError = err.Error() })
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docs, _ := reader.DocumentsWith(ctx, req, cfg.Extractors, true)
	done := map[string]bool{}
	for _, rd := range c.Readings {
		done[rd.Document] = true
	}
	for _, d := range docs {
		if done[d.Reference] {
			continue
		}
		rd, err := reader.Read(ctx, cfg.Reader.Model, cfg.Reader.Name, p, d)
		if err != nil {
			a.note(func(s *AutoReadStats) { s.Failed++; s.LastError = err.Error() })
			log.Printf("reader: case %s, %s: %v", id, d.Reference, err)
			continue
		}
		if _, err := cfg.Cases.Read(id, "", *rd); err != nil {
			log.Printf("reader: case %s: the reading could not be recorded: %v", id, err)
			return
		}
		a.note(func(s *AutoReadStats) { s.Read++ })
	}
	if !a.Approve {
		return
	}
	if c, ok = cfg.Cases.Get(id); !ok || c.Status != cases.Pended || c.Proposed != nil {
		return // a proposed denial waits for a clinician, whatever the documents say
	}
	why, approves, err := cfg.approvesOnDocuments(c)
	if err != nil || !approves {
		return
	}
	sampled := sample(id, a.SamplePercent)
	got, err := cfg.Cases.AutoApprove(id, why, sampled)
	if err != nil {
		return // reviewed meanwhile, or not recorded: either way a person has it
	}
	a.note(func(s *AutoReadStats) {
		s.Approved++
		if sampled {
			s.Sampled++
		}
	})
	if cfg.Deliver != nil && got.Request != "" {
		cfg.Deliver(*got)
	}
}

// approvesOnDocuments decides a case again with every quote-checked finding of its readings, and says whether that
// approves, with the reason the approval records: each finding it rests on, quoted.
func (cfg Config) approvesOnDocuments(c *cases.Case) (string, bool, error) {
	p := cfg.set.get(c.Policy)
	if p == nil || len(c.Readings) == 0 {
		return "", false, nil
	}
	req, err := cfg.request(c)
	if err != nil {
		return "", false, err
	}
	x := evidence.ExtractWith(p, req, c.Evidence.FromOutside()) // what outside sources said then
	r := x.Request(p.ID)
	var used []reader.Proposal
	for _, rd := range c.Readings {
		for _, pr := range rd.Proposals {
			r = merge(r, decide.Assertion{Argument: pr.Argument, Finding: pr.Finding,
				Citation: decide.Citation{Reference: pr.Document, Quote: pr.Quote}})
			used = append(used, pr)
		}
	}
	res, err := decide.Decide(p, r)
	if err != nil || res.Outcome != decide.Approved {
		return "", false, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Approved under %s v%s on what the documents sent with the request show", p.Title, p.Version)
	for i, pr := range used {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		state := "shown"
		if pr.Finding == decide.Absent {
			state = "shown not to be the case"
		}
		fmt.Fprintf(&b, "%s, %s (%q)", pr.Text, state, pr.Quote)
	}
	b.WriteString(". Read by a document model; each finding is quoted word for word from the document.")
	return b.String(), true, nil
}

// sample picks percent of case ids, the same way every time for a case.
func sample(id string, percent int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	sum := sha256.Sum256([]byte(id))
	return int(binary.BigEndian.Uint32(sum[:4])%100) < percent
}

// AutomaticReport is what automatic reading and approval have done: the reader's counts since the start, every automatic
// approval in the log, and how the after-the-fact checks came out.
type AutomaticReport struct {
	On       bool           `json:"on"`
	Approve  bool           `json:"approve"`
	Sample   int            `json:"samplePercent"`
	Reader   *AutoReadStats `json:"reader,omitempty"`
	Approved int            `json:"approved"` // automatic approvals in the log, all time
	Sampled  int            `json:"sampled"`
	Checked  int            `json:"checked"`
	Disagree int            `json:"disagree"`
	// DisagreePercent is of the checked: the rate at which a reviewer found the documents did not show what an
	// automatic approval rested on.
	DisagreePercent *float64   `json:"disagreePercent"`
	ToCheck         []AutoCase `json:"toCheck"`  // sampled, not yet checked, oldest first
	Disputed        []AutoCase `json:"disputed"` // checked, and the reviewer disagreed
}

// AutoCase is one automatic approval.
type AutoCase struct {
	ID     string `json:"id"`
	Policy string `json:"policy"`
	Time   string `json:"time"`
	Note   string `json:"note,omitempty"`
}

func (cfg Config) automaticReport(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	out := AutomaticReport{ToCheck: []AutoCase{}, Disputed: []AutoCase{}}
	if a := cfg.AutoRead; a != nil && cfg.Reader != nil {
		st := a.Stats()
		out.On, out.Approve, out.Sample, out.Reader = true, a.Approve, a.SamplePercent, &st
	}
	for _, c := range cfg.Cases.Select(func(c *cases.Case) bool { return c.Review != nil && c.Review.Automatic }) {
		out.Approved++
		ac := AutoCase{ID: c.ID, Policy: c.Policy, Time: c.Review.Time}
		if c.Review.Sampled {
			out.Sampled++
		}
		switch {
		case c.QA != nil:
			out.Checked++
			if !c.QA.Agree {
				out.Disagree++
				ac.Note = c.QA.Note
				out.Disputed = append(out.Disputed, ac)
			}
		case c.Review.Sampled:
			out.ToCheck = append(out.ToCheck, ac)
		}
	}
	if out.Checked > 0 {
		v := 100 * float64(out.Disagree) / float64(out.Checked)
		out.DisagreePercent = &v
	}
	writeJSON(w, http.StatusOK, out)
}

// checkAutomatic records a reviewer's after-the-fact check of an automatic approval: agree, or disagree with what the
// documents do not show.
func (cfg Config) checkAutomatic(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var body struct {
		Agree bool   `json:"agree"`
		Note  string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := cfg.Cases.CheckQA(r.PathValue("id"), s.User, body.Agree, body.Note)
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such case")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c)
	}
}
