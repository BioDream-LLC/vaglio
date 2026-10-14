package versions

import (
	"os"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/policy"
)

func TestKeep(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.Keep(p)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if again, _ := s.Keep(p); again != v1 {
		t.Errorf("the same source kept twice: %+v %+v", again, v1)
	}
	p2 := *p
	p2.Version = "9.9.9"
	v2, _ := s.Keep(&p2)
	if list := s.List(p.ID); len(list) != 2 || list[0] != v1 || list[1] != v2 {
		t.Fatalf("%+v", list)
	}
	src, err := s.Source(p.ID, v2.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	back, err := policy.Parse("x"+policy.Ext, string(src))
	if err != nil || back.Version != "9.9.9" {
		t.Fatalf("%v %v", back, err)
	}
	// Reopened, it remembers; edited, a version is refused.
	s2, _ := Open(dir)
	if len(s2.List(p.ID)) != 2 {
		t.Error("the index was not kept")
	}
	os.WriteFile(s.path(p.ID, v1.SHA256), []byte("policy x"), 0o600)
	if _, err := s2.Source(p.ID, v1.SHA256); err == nil {
		t.Error("an edited version was served")
	}
	if _, err := s2.Source("../etc", v1.SHA256); err == nil {
		t.Error("a path outside the store")
	}
}
