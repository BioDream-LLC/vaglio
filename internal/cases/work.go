package cases

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Work on a case: who it is assigned to, whether it waits for a physician, the peer-to-peer calls offered to the
// ordering provider, and an appeal taken in and worked to a decision. Each is an audit record first, like everything
// else a case holds, so the log alone rebuilds it.

// Audit actions of work on cases.
const (
	ActAssigned    = "case.assigned"
	ActEscalated   = "case.escalated"
	ActPeerToPeer  = "case.peer-to-peer"
	ActAppealTaken = "case.appeal-received"
)

// Assignment is who works a case. To is empty when it was handed back to the queue.
type Assignment struct {
	To   string `json:"to"`
	By   string `json:"by"`
	Time string `json:"time"`
}

// Escalation sends a case to a physician: only a reviewer whose account records an NPI and a credential may then
// decide it.
type Escalation struct {
	By   string `json:"by"`
	Time string `json:"time"`
	Note string `json:"note"`
}

// PeerToPeer is a call offered to the ordering provider to discuss the request with a physician reviewer, and how it
// went. Each change is a new record with the same ID; the case keeps the latest.
type PeerToPeer struct {
	ID     string `json:"id"`
	By     string `json:"by"`
	Time   string `json:"time"`
	At     string `json:"at"`     // when the call is set for, RFC 3339
	With   string `json:"with"`   // who at the provider, and how to reach them
	Status string `json:"status"` // scheduled, held, missed or cancelled
	Note   string `json:"note,omitempty"`
}

// Peer-to-peer statuses.
const (
	P2PScheduled = "scheduled"
	P2PHeld      = "held"
	P2PMissed    = "missed"
	P2PCancelled = "cancelled"
)

// AppealRequest is an appeal of a denial, taken in: who asked, when it was received and how fast it must be decided.
// Its decision is the case's Appeal.
type AppealRequest struct {
	By        string `json:"by"`
	Time      string `json:"time"`
	From      string `json:"from"`     // provider, member or representative
	Received  string `json:"received"` // RFC 3339; the deadline runs from it
	Expedited bool   `json:"expedited,omitempty"`
	Reason    string `json:"reason"` // why they say the denial is wrong
	Due       string `json:"due"`    // RFC 3339
	// Denier is who made the denial being appealed ("engine" for an automated one): the appeal is decided by someone else.
	Denier string `json:"denier"`
	// DenierNPI is the denying reviewer's NPI when they were a clinician: the same person under another account does
	// not decide the appeal either.
	DenierNPI string `json:"denierNpi,omitempty"`
}

// AppealTimeframe is how long an appeal (a Medicare Advantage reconsideration, or a Part D redetermination for a drug)
// may take from receipt: 30 days for a standard pre-service one (42 CFR 422.590(a)), 7 days for a drug (423.590(a)),
// 72 hours when expedited (422.590(e), 423.590(d)).
func AppealTimeframe(drug, expedited bool) time.Duration {
	switch {
	case expedited:
		return 72 * time.Hour
	case drug:
		return 7 * 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// ErrNotOpen is work asked for a case nobody is waiting on.
var ErrNotOpen = errors.New("this case is not waiting for a decision or an appeal")

// Open is whether someone must still act: the request waits for a decision, or an appeal for its outcome.
func (c *Case) Open() bool {
	return c.Status == Pended || (c.AppealTaken != nil && c.Appeal == nil)
}

// TookPart is whether the account (by name, or by NPI) made the denial being appealed.
func (a *AppealRequest) TookPart(user, npi string) bool {
	return a != nil && (strings.EqualFold(user, a.Denier) || (npi != "" && npi == a.DenierNPI))
}

// Denier is who made the case's denial: the reviewer, or "engine" when the engine denied it.
func (c *Case) Denier() string {
	if c.Status == Reviewed && c.Review != nil {
		return c.Review.By
	}
	return "engine"
}

// Assign gives an open case to a reviewer, or back to the queue (to empty).
func (s *Store) Assign(id, user, to string) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if !c.Open() {
		return nil, ErrNotOpen
	}
	if c.Assigned != nil && c.Assigned.To == to {
		out := *c
		return &out, nil
	}
	if _, err := s.write("user:"+user, ActAssigned, id, Assignment{To: to, By: user}); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// TakeNext assigns the first unassigned case of the queue that may accept to user, in the queue's order.
func (s *Store) TakeNext(user string, now time.Time, may func(*Case) bool) (*Case, error) {
	s.mu.Lock()
	var open []Case
	for _, id := range s.order {
		c := s.cases[id]
		if c.Open() && (c.Assigned == nil || c.Assigned.To == "") && (may == nil || may(c)) {
			open = append(open, *c)
		}
	}
	s.mu.Unlock()
	if len(open) == 0 {
		return nil, os.ErrNotExist
	}
	SortQueue(open, now)
	return s.Assign(open[0].ID, user, user)
}

// Escalate sends an open case to a physician.
func (s *Store) Escalate(id, user, note string) (*Case, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, errors.New("an escalation says what the physician is asked to look at")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if !c.Open() {
		return nil, ErrNotOpen
	}
	if c.Escalated != nil {
		return nil, errors.New("this case waits for a physician already")
	}
	if _, err := s.write("user:"+user, ActEscalated, id, Escalation{By: user, Note: note}); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// SchedulePeerToPeer offers the ordering provider a call (p.ID empty), or records how one went (p.ID set).
func (s *Store) SchedulePeerToPeer(id, user string, p PeerToPeer) (*Case, error) {
	p.With, p.Note = strings.TrimSpace(p.With), strings.TrimSpace(p.Note)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if p.ID == "" {
		if !c.Open() {
			return nil, ErrNotOpen
		}
		if _, err := time.Parse(time.RFC3339, p.At); err != nil {
			return nil, fmt.Errorf("the call's time %q is not RFC 3339", p.At)
		}
		if p.With == "" {
			return nil, errors.New("a call says who at the provider it is with, and how to reach them")
		}
		p.ID, p.Status = newID()[:8], P2PScheduled
	} else {
		var was *PeerToPeer
		for i := range c.PeerToPeer {
			if c.PeerToPeer[i].ID == p.ID {
				was = &c.PeerToPeer[i]
			}
		}
		switch {
		case was == nil:
			return nil, os.ErrNotExist
		case was.Status != P2PScheduled:
			return nil, fmt.Errorf("that call is %s already", was.Status)
		case p.Status != P2PHeld && p.Status != P2PMissed && p.Status != P2PCancelled:
			return nil, fmt.Errorf("a call was held, missed or cancelled, not %q", p.Status)
		case p.Status == P2PHeld && p.Note == "":
			return nil, errors.New("a held call records what was said")
		}
		p.At, p.With = was.At, was.With
	}
	p.By = user
	if _, err := s.write("user:"+user, ActPeerToPeer, id, p); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// TakeAppeal records an appeal of a denied case, received at received.
func (s *Store) TakeAppeal(id, user string, a AppealRequest) (*Case, error) {
	switch a.From {
	case "provider", "member", "representative":
	default:
		return nil, fmt.Errorf("an appeal is from the provider, the member or a representative, not %q", a.From)
	}
	a.Reason = strings.TrimSpace(a.Reason)
	if a.Reason == "" {
		return nil, errors.New("an appeal records what it says is wrong with the denial")
	}
	got, err := time.Parse(time.RFC3339, a.Received)
	if err != nil {
		return nil, fmt.Errorf("received %q is not RFC 3339", a.Received)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	switch {
	case c.Final() != "deny":
		return nil, ErrNotDenied
	case c.AppealTaken != nil || c.Appeal != nil:
		return nil, errors.New("this case's appeal is recorded already")
	}
	a.By, a.Denier = user, c.Denier()
	if c.Status == Reviewed && c.Review != nil {
		a.DenierNPI = c.Review.ReviewerNPI
	}
	a.Due = got.Add(AppealTimeframe(c.Drug, a.Expedited)).UTC().Format(time.RFC3339)
	if _, err := s.write("user:"+user, ActAppealTaken, id, a); err != nil {
		return nil, err
	}
	out := *c
	return &out, nil
}

// applyWork applies the records of work on cases; ok is false for an action it does not know.
func (s *Store) applyWork(action string, c *Case, t string, data func(any) error) (bool, error) {
	switch action {
	case ActAssigned:
		var a Assignment
		if err := data(&a); err != nil {
			return true, err
		}
		a.Time = t
		c.Assigned = &a
	case ActEscalated:
		var e Escalation
		if err := data(&e); err != nil {
			return true, err
		}
		e.Time = t
		c.Escalated = &e
	case ActPeerToPeer:
		var p PeerToPeer
		if err := data(&p); err != nil {
			return true, err
		}
		p.Time = t
		for i := range c.PeerToPeer {
			if c.PeerToPeer[i].ID == p.ID {
				// A new slice: copies handed out earlier keep what they read.
				calls := append([]PeerToPeer(nil), c.PeerToPeer...)
				calls[i] = p
				c.PeerToPeer = calls
				return true, nil
			}
		}
		c.PeerToPeer = append(c.PeerToPeer, p)
	case ActAppealTaken:
		var a AppealRequest
		if err := data(&a); err != nil {
			return true, err
		}
		a.Time = t
		c.AppealTaken = &a
	default:
		return false, nil
	}
	return true, nil
}

// Rank is where a case stands in the queue: a score, higher first, and the reasons for it. It only orders the work;
// it decides nothing and is never shown to the provider.
type Rank struct {
	Score int      `json:"score"`
	Why   []string `json:"why"`
}

// WorkDue is when the work waiting on an open case is due: the appeal's deadline, or the decision's.
func (c *Case) WorkDue() time.Time {
	if c.AppealTaken != nil && c.Appeal == nil {
		if t, err := time.Parse(time.RFC3339, c.AppealTaken.Due); err == nil {
			return t
		}
	}
	return c.Due()
}

// RankAt ranks an open case at now, by stated rules: past its deadline first, then expedited, then by hours left, with
// a nudge for a case whose documents arrived since it pended (it may be decided at once) and for one a clinician must
// see (fewer people can take it).
func (c *Case) RankAt(now time.Time) Rank {
	var r Rank
	left := c.WorkDue().Sub(now)
	switch {
	case left <= 0:
		r.Score += 10000
		r.Why = append(r.Why, "past its deadline")
	case left <= 24*time.Hour:
		r.Score += 2000
		r.Why = append(r.Why, "due within a day")
	}
	if c.Expedited() || (c.AppealTaken != nil && c.AppealTaken.Expedited) {
		r.Score += 3000
		r.Why = append(r.Why, "expedited")
	}
	if left > 0 {
		hours := int(left.Hours())
		if hours < 1000 {
			r.Score += 1000 - hours
		}
		r.Why = append(r.Why, fmt.Sprintf("%d hours left", hours))
	}
	if len(c.Received) > 0 {
		r.Score += 200
		r.Why = append(r.Why, "documents arrived since it pended")
	}
	if c.Proposed != nil || c.Escalated != nil {
		r.Score += 100
		r.Why = append(r.Why, "waits for a clinician")
	}
	if c.AppealTaken != nil && c.Appeal == nil {
		r.Why = append(r.Why, "an appeal")
	}
	return r
}

// SortQueue orders open cases as the queue is worked: highest rank first, then the earliest deadline, then the oldest.
func SortQueue(cs []Case, now time.Time) {
	rank := make(map[string]int, len(cs))
	for i := range cs {
		rank[cs[i].ID] = cs[i].RankAt(now).Score
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := &cs[i], &cs[j]
		if rank[a.ID] != rank[b.ID] {
			return rank[a.ID] > rank[b.ID]
		}
		if da, db := a.WorkDue(), b.WorkDue(); !da.Equal(db) {
			return da.Before(db)
		}
		return a.Created < b.Created
	})
}
