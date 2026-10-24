package cases

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
)

func open(t *testing.T, dir string) (*Store, *audit.Log) {
	t.Helper()
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() }) // before the directory is removed: Windows cannot remove an open file
	s, err := Open(l, filepath.Join(dir, "bundles"))
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}

func opening(decision, claim, item string) Opening {
	return Opening{Policy: "p", PolicyVersion: "1", Item: item, Claim: claim, Request: "req-1", Answer: Answer{Decision: decision}}
}

func TestLifecycleSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	s, l := open(t, dir)
	approved, err := s.Open(opening("approve", "sys|1", "1"), []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	pended, _ := s.Open(opening("pend", "sys|2", "1"), []byte(`{"b":1}`))
	if approved.Status != Decided || pended.Status != Pended {
		t.Fatalf("statuses %s %s", approved.Status, pended.Status)
	}
	if _, err := s.Review(approved.ID, "rev", Review{Decision: "deny", Reason: "x"}); !errors.Is(err, ErrNotPended) {
		t.Errorf("reviewing a decided case: %v", err)
	}
	if _, err := s.Review(pended.ID, "rev", Review{Decision: "deny", Reason: " "}); err == nil {
		t.Error("a review without a reason was accepted")
	}
	if _, err := s.Review(pended.ID, "rev", Review{Decision: "maybe", Reason: "x"}); err == nil {
		t.Error("a review that neither approves nor denies was accepted")
	}
	got, err := s.Review(pended.ID, "rev", Review{Decision: "deny", Reason: "Neurological exam documented normal.", ReviewerNPI: "1234567893"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Reviewed || got.Review.By != "rev" || got.Review.Time == "" {
		t.Fatalf("reviewed %+v", got.Review)
	}
	if u := s.Undelivered(); len(u) != 1 || u[0].ID != pended.ID {
		t.Fatalf("undelivered %+v", u)
	}
	s.Delivered(pended.ID, errors.New("connection refused"), false)
	if len(s.Undelivered()) != 1 {
		t.Error("a failed delivery counts as delivered")
	}
	s.Delivered(pended.ID, nil, false)
	if len(s.Undelivered()) != 0 {
		t.Error("a delivered review is still waiting")
	}
	other, _ := s.Open(opening("pend", "sys|3", "1"), []byte(`{"c":1}`))
	s.Review(other.ID, "rev", Review{Decision: "approve", Reason: "ok"})
	s.Delivered(other.ID, errors.New("already decided there"), true)
	if len(s.Undelivered()) != 0 {
		t.Error("a refused delivery is retried")
	}
	l.Close()

	s2, l2 := open(t, dir)
	defer l2.Close()
	c, ok := s2.Get(pended.ID)
	if !ok || c.Status != Reviewed || c.Review.Reason != "Neurological exam documented normal." || !c.Delivery.OK {
		t.Fatalf("after a restart: %+v", c)
	}
	if h := s2.History(pended.ID); len(h) != 4 || h[1].Actor != "user:rev" {
		t.Errorf("history %+v", h)
	}
}

func TestAResubmissionSupersedesTheWaitingCase(t *testing.T) {
	s, l := open(t, t.TempDir())
	defer l.Close()
	first, _ := s.Open(opening("pend", "sys|1", "1"), []byte(`{"v":1}`))
	other, _ := s.Open(opening("pend", "sys|1", "2"), []byte(`{"v":1}`)) // another item of the same Claim
	second, _ := s.Open(opening("pend", "sys|1", "1"), []byte(`{"v":2}`))
	if c, _ := s.Get(first.ID); c.Status != Superseded || c.SupersededBy != second.ID {
		t.Errorf("first %+v", c)
	}
	if c, _ := s.Get(other.ID); c.Status != Pended {
		t.Errorf("another item was superseded: %s", c.Status)
	}
	list := s.List(Pended)
	if len(list) != 2 || list[0].ID != other.ID {
		t.Errorf("pended, oldest first: %+v", list)
	}
	if all := s.List(""); len(all) != 3 || all[0].Status != Pended || all[2].ID != first.ID {
		t.Errorf("all: %+v", all)
	}
}

func TestTheBundleMustMatch(t *testing.T) {
	dir := t.TempDir()
	s, l := open(t, dir)
	defer l.Close()
	c, _ := s.Open(opening("pend", "", "1"), []byte(`{"resourceType":"Bundle"}`))
	if raw, err := s.Bundle(c); err != nil || string(raw) != `{"resourceType":"Bundle"}` {
		t.Fatalf("%s %v", raw, err)
	}
	pack := filepath.Join(dir, "bundles", "pack-000001.vgp")
	info, _ := os.Stat(pack)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows keeps no Unix modes
		t.Errorf("pack mode %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(pack)
	os.WriteFile(pack, bytes.Replace(raw, []byte(`"Bundle"`), []byte(`"Bundlf"`), 1), 0o600)
	if _, err := s.Bundle(c); !errors.Is(err, ErrBundleChanged) {
		t.Errorf("an edited bundle: %v", err)
	}
	if err := s.Viewed(c.ID, "rev"); err != nil {
		t.Fatal(err)
	}
	if h := s.History(c.ID); h[len(h)-1].Action != ActViewed {
		t.Error("viewing was not audited")
	}
}

func TestTheSameRequestAtOnce(t *testing.T) {
	s, _ := open(t, t.TempDir())
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			_, err := s.Open(opening("approve", "sys|"+string(rune('a'+i)), "1"), []byte(`{"same":1}`))
			errs <- err
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
