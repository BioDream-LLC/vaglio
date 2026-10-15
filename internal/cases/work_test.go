package cases

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWorkSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	s, l := open(t, dir)
	a, _ := s.Open(opening("pend", "sys|1", "1"), []byte(`{"a":1}`))
	b, _ := s.Open(opening("pend", "sys|2", "1"), []byte(`{"b":1}`))
	done, _ := s.Open(opening("approve", "sys|3", "1"), []byte(`{"c":1}`))
	if _, err := s.Assign(done.ID, "lead", "kim"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("assigning a decided case: %v", err)
	}
	if _, err := s.Assign(a.ID, "lead", "kim"); err != nil {
		t.Fatal(err)
	}
	// TakeNext skips the assigned case and the ones the reviewer may not take.
	got, err := s.TakeNext("lee", time.Now(), func(c *Case) bool { return c.Escalated == nil })
	if err != nil || got.ID != b.ID || got.Assigned.To != "lee" {
		t.Fatalf("next: %+v %v", got, err)
	}
	if _, err := s.TakeNext("lee", time.Now(), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an empty queue: %v", err)
	}
	if _, err := s.Escalate(a.ID, "kim", " "); err == nil {
		t.Error("an escalation without a note")
	}
	if _, err := s.Escalate(a.ID, "kim", "Imaging findings need a radiologist's read."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePeerToPeer(a.ID, "kim", PeerToPeer{At: "tomorrow", With: "Dr. A"}); err == nil {
		t.Error("a call at no time")
	}
	c, err := s.SchedulePeerToPeer(a.ID, "kim", PeerToPeer{At: "2026-10-16T15:00:00Z", With: "Dr. A, 555-0100"})
	if err != nil || len(c.PeerToPeer) != 1 || c.PeerToPeer[0].Status != P2PScheduled {
		t.Fatalf("%+v %v", c, err)
	}
	id := c.PeerToPeer[0].ID
	if _, err := s.SchedulePeerToPeer(a.ID, "kim", PeerToPeer{ID: id, Status: P2PHeld}); err == nil {
		t.Error("a held call with no note")
	}
	if _, err := s.SchedulePeerToPeer(a.ID, "kim", PeerToPeer{ID: id, Status: P2PHeld, Note: "Sent the MRI report."}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePeerToPeer(a.ID, "kim", PeerToPeer{ID: id, Status: P2PMissed}); err == nil {
		t.Error("a held call changed again")
	}
	l.Close()

	s, _ = open(t, dir)
	c, _ = s.Get(a.ID)
	if c.Assigned.To != "kim" || c.Escalated == nil || len(c.PeerToPeer) != 1 || c.PeerToPeer[0].Status != P2PHeld ||
		c.PeerToPeer[0].At != "2026-10-16T15:00:00Z" {
		t.Fatalf("after restart: %+v %+v %+v", c.Assigned, c.Escalated, c.PeerToPeer)
	}
}

func TestAppealWorkflow(t *testing.T) {
	s, _ := open(t, t.TempDir())
	p, _ := s.Open(opening("pend", "sys|1", "1"), []byte(`{"a":1}`))
	if _, err := s.TakeAppeal(p.ID, "intake", AppealRequest{From: "provider", Received: "2026-10-15T10:00:00Z", Reason: "x"}); !errors.Is(err, ErrNotDenied) {
		t.Errorf("an appeal of a waiting case: %v", err)
	}
	s.Review(p.ID, "kim", Review{Decision: "deny", Reason: "No conservative care recorded."})
	if _, err := s.TakeAppeal(p.ID, "intake", AppealRequest{From: "neighbour", Received: "2026-10-15T10:00:00Z", Reason: "x"}); err == nil {
		t.Error("an appeal from a neighbour")
	}
	c, err := s.TakeAppeal(p.ID, "intake", AppealRequest{From: "provider", Received: "2026-10-15T10:00:00Z",
		Reason: "Six weeks of physical therapy, notes attached."})
	if err != nil {
		t.Fatal(err)
	}
	if c.AppealTaken.Due != "2026-11-14T10:00:00Z" || c.AppealTaken.Denier != "kim" || !c.Open() {
		t.Fatalf("%+v open=%v", c.AppealTaken, c.Open())
	}
	if _, err := s.Appealed(p.ID, "KIM", Appeal{Outcome: "overturned", Reason: "x"}); err == nil ||
		!strings.Contains(err.Error(), "someone else") {
		t.Errorf("the denier decided the appeal: %v", err)
	}
	c, err = s.Appealed(p.ID, "lee", Appeal{Outcome: "overturned", Reason: "Therapy documented."})
	if err != nil || c.Open() {
		t.Fatalf("%v open=%v", err, c.Open())
	}
	if d := AppealTimeframe(true, false); d != 7*24*time.Hour {
		t.Errorf("drug appeal %v", d)
	}
}

func TestQueueOrder(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	at := func(h int) string { return now.Add(time.Duration(h) * time.Hour).Format(time.RFC3339Nano) }
	cs := []Case{
		{ID: "old", Created: at(-24 * 6), Status: Pended},                                  // 24h left
		{ID: "late", Created: at(-24 * 8), Status: Pended},                                 // past due
		{ID: "new", Created: at(-1), Status: Pended},                                       // 167h left
		{ID: "stat", Created: at(-1), Status: Pended, Opening: Opening{Priority: "stat"}},  // 71h left, expedited
		{ID: "docs", Created: at(-1), Status: Pended, Received: []Received{{Time: at(0)}}}, // 167h left, documents in
	}
	SortQueue(cs, now)
	var order []string
	for _, c := range cs {
		order = append(order, c.ID)
	}
	if strings.Join(order, " ") != "late stat old docs new" {
		t.Errorf("order %v", order)
	}
	if r := cs[0].RankAt(now); r.Why[0] != "past its deadline" {
		t.Errorf("why %v", r.Why)
	}
}
