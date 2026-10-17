package audit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fill(t *testing.T, path string, n int) {
	t.Helper()
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < n; i++ {
		if _, err := l.Append("user:a", "case.reviewed", "c1", map[string]any{"i": i, "note": "<b>&"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppendAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fill(t, path, 3)
	fill(t, path, 2) // reopened: the chain continues
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	n, head := l.Head()
	if n != 5 || head == "" {
		t.Fatalf("head %d %q", n, head)
	}
	if got := l.Records(3, 0); len(got) != 2 || got[0].Seq != 4 || got[0].Prev != l.Records(2, 1)[0].Hash {
		t.Fatalf("records after 3: %+v", got)
	}
	if got := l.Records(0, 2); len(got) != 2 {
		t.Errorf("limit: %d", len(got))
	}
	if len(l.ForCase("c1")) != 5 || len(l.ForCase("c2")) != 0 {
		t.Error("ForCase")
	}
	vn, vh, err := Verify(path)
	if err != nil || vn != 5 || vh != head {
		t.Fatalf("verify: %d %q %v", vn, vh, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v: the log holds patient data and is the owner's alone", info.Mode().Perm())
	}
}

func TestTamperingIsDetected(t *testing.T) {
	cases := map[string]struct {
		edit func(lines []string) []string
		want string
	}{
		"altered": {func(l []string) []string {
			l[1] = strings.Replace(l[1], `"actor":"user:a"`, `"actor":"user:b"`, 1)
			return l
		}, "record 2 has been altered"},
		"removed":        {func(l []string) []string { return append(l[:1], l[2:]...) }, "sequence number 3"},
		"reordered":      {func(l []string) []string { l[1], l[2] = l[2], l[1]; return l }, "sequence number 3"},
		"truncated head": {func(l []string) []string { return l[1:] }, "sequence number 2"},
	}
	for name, c := range cases {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		fill(t, path, 4)
		raw, _ := os.ReadFile(path)
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if err := os.WriteFile(path, []byte(strings.Join(c.edit(lines), "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		if !errors.Is(err, ErrBroken) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}

	// Rewriting a record and renumbering is still caught by the chain.
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fill(t, path, 3)
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	lines[2] = strings.Replace(lines[2], `"prev":"`, `"prev":"0`, 1)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if _, err := Open(path); !errors.Is(err, ErrBroken) {
		t.Errorf("a re-pointed record: %v", err)
	}
}

// TestTwoWriters is the server and the vaglio user command writing one log: each sees the other's records, and the
// chain stays whole.
func TestTwoWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	done := make(chan error, 2)
	for _, l := range []*Log{a, b} {
		go func(l *Log) {
			for i := 0; i < 50; i++ {
				if _, err := l.Append("x", "y", "", i); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(l)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n, _, err := Verify(path); err != nil || n != 100 {
		t.Fatalf("%d records: %v", n, err)
	}
	if n, h := a.Head(); n != 100 {
		t.Errorf("a sees %d records", n)
	} else if _, hb := b.Head(); hb != h {
		t.Error("the two writers disagree on the head")
	}
}

func TestACutOffRecordIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	fill(t, path, 2)
	raw, _ := os.ReadFile(path)
	os.WriteFile(path, raw[:len(raw)-5], 0o600)
	if _, err := Open(path); !errors.Is(err, ErrBroken) || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("%v", err)
	}
}

func TestGroupCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Append("t", "x", "", map[string]int{"i": i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	l.Close()
	if n, _, err := Verify(path); n != 200 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
}

func TestCopy(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(filepath.Join(dir, "a.jsonl"))
	b, _ := Open(filepath.Join(dir, "b.jsonl"))
	defer a.Close()
	defer b.Close()
	for i := 0; i < 3; i++ {
		a.Append("t", "x", "c", map[string]int{"i": i})
	}
	recs := a.Records(0, 0)
	if err := b.Copy(recs[1]); err == nil {
		t.Error("copied out of order")
	}
	altered := recs[0]
	altered.Actor = "someone else"
	if err := b.Copy(altered); err == nil {
		t.Error("copied an altered record")
	}
	for _, r := range recs {
		if err := b.Copy(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, ha := a.Head(); func() string { _, hb := b.Head(); return hb }() != ha {
		t.Error("heads differ")
	}
}

// Two processes appending to one log at once: the file lock keeps every record numbered once and the chain whole. The
// second process is this test binary run again (VAGLIO_TEST_LOG_WRITER names the log).
func TestTwoProcessesAppend(t *testing.T) {
	if path := os.Getenv("VAGLIO_TEST_LOG_WRITER"); path != "" {
		l, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 200; i++ {
			if _, err := l.Append("child", "test.append", "", nil); err != nil {
				t.Fatal(err)
			}
		}
		l.Close()
		return
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTwoProcessesAppend$")
	child.Env = append(os.Environ(), "VAGLIO_TEST_LOG_WRITER="+path)
	out := &strings.Builder{}
	child.Stdout, child.Stderr = out, out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if _, err := l.Append("parent", "test.append", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("the second writer: %v\n%s", err, out)
	}
	l.Close()
	n, _, err := Verify(path)
	if err != nil || n != 400 {
		t.Fatalf("%d records, %v; want 400 in one unbroken chain", n, err)
	}
}
