package fsx

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "users.json")
	for _, body := range []string{"first\n", "second, longer\n"} {
		if err := WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Fatalf("read %q, want %q", got, body)
		}
	}
	if info, _ := os.Stat(p); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}
