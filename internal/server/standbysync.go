package server

import (
	"context"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
)

// StandbySync makes a decision wait, before it is answered, until a standby (vaglio standby) has the log up to and
// including it (serve -standby-sync). A standby asking for the records after n has written records 1..n to its own
// disk with a sync, so its position is the n it asks after. A decision the standby has not confirmed within Wait is
// answered anyway and the gap recorded (standby.unconfirmed): a stopped standby must not stop decisions. While the
// standby keeps up, a takeover loses no decision the primary answered.
type StandbySync struct {
	Wait time.Duration
	Log  *audit.Log

	mu      sync.Mutex
	acked   int64
	changed chan struct{} // closed and replaced whenever acked moves
	noted   time.Time     // when standby.unconfirmed was last recorded
}

// NewStandbySync waits up to wait for the standby on log.
func NewStandbySync(log *audit.Log, wait time.Duration) *StandbySync {
	return &StandbySync{Wait: wait, Log: log, changed: make(chan struct{})}
}

// Ack records that the standby holds the log up to seq.
func (s *StandbySync) Ack(seq int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > s.acked {
		s.acked = seq
		close(s.changed)
		s.changed = make(chan struct{})
	}
}

// Acked is the standby's last confirmed position.
func (s *StandbySync) Acked() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acked
}

// Confirm waits until the standby holds the log up to its present head. It reports whether it did within Wait.
func (s *StandbySync) Confirm(ctx context.Context) bool {
	if s == nil {
		return true
	}
	n, _ := s.Log.Head()
	return s.ConfirmUpTo(ctx, int64(n))
}

// ConfirmUpTo waits until the standby holds the log up to record want: an answer waits for its own records, not for
// those other requests wrote after them.
func (s *StandbySync) ConfirmUpTo(ctx context.Context, want int64) bool {
	if s == nil {
		return true
	}
	deadline := time.NewTimer(s.Wait)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		acked, changed := s.acked, s.changed
		s.mu.Unlock()
		if acked >= want {
			return true
		}
		select {
		case <-changed:
		case <-deadline.C:
			s.unconfirmed(want, acked)
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// unconfirmed records, at most once a minute, that decisions are being answered without the standby holding them.
func (s *StandbySync) unconfirmed(want, acked int64) {
	s.mu.Lock()
	if time.Since(s.noted) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.noted = time.Now()
	s.mu.Unlock()
	_, _ = s.Log.Append("system", "standby.unconfirmed", "", map[string]any{
		"head": want, "standby": acked, "waited": s.Wait.String(),
		"note": "answered without the standby holding this decision; a takeover now could lose it",
	})
}

// waitForRecords holds a standby's request for the records after n until there are some, ctx ends or max passes, so
// a standby in step hears of a decision at once instead of at its next poll.
func waitForRecords(ctx context.Context, log *audit.Log, after int64, max time.Duration) {
	stop := time.NewTimer(max)
	defer stop.Stop()
	for {
		changed := log.Changed() // asked for before the head, so a record kept in between is not missed
		if n, _ := log.Head(); int64(n) > after {
			// A moment for the records written with it to arrive too: under load the standby then copies, and syncs,
			// several at once rather than one each.
			time.Sleep(time.Millisecond)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-stop.C:
			return
		case <-changed:
		}
	}
}

// lastOfCase is the number of a case's last record so far, 0 when it has none (then Confirm's head is waited for).
func lastOfCase(log *audit.Log, id string) int64 {
	recs := log.ForCase(id)
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

// confirmCase waits for the standby to hold every record of a case so far.
func (cfg Config) confirmCase(ctx context.Context, id string) {
	if cfg.StandbySync == nil {
		return
	}
	if n := lastOfCase(cfg.Audit, id); n > 0 {
		cfg.StandbySync.ConfirmUpTo(ctx, n)
		return
	}
	cfg.StandbySync.Confirm(ctx)
}
