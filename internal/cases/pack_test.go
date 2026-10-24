package cases

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/vaglio/internal/seal"
)

func bundleN(i int) []byte {
	return []byte(fmt.Sprintf(`{"resourceType":"Bundle","id":"b%d","note":%q}`, i, strings.Repeat("x", i%300)))
}

// Many requests at once: every Bundle reads back as written, and a second store over the same directory (a restart)
// finds them all.
func TestPacksConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := openPacks(dir)
	var wg sync.WaitGroup
	sums := make([]string, 400)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(sums); i += 16 {
				s, err := p.put(bundleN(i))
				if err != nil {
					t.Error(err)
					return
				}
				sums[i] = s
			}
		}(w)
	}
	wg.Wait()
	// The same Bundle again is kept once.
	if _, err := p.put(bundleN(7)); err != nil {
		t.Fatal(err)
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	again := openPacks(dir)
	for i, s := range sums {
		got, err := again.get(s)
		if err != nil || !bytes.Equal(got, bundleN(i)) {
			t.Fatalf("bundle %d: %v", i, err)
		}
	}
	if len(again.index) != len(sums) {
		t.Errorf("%d entries, want %d (one per Bundle)", len(again.index), len(sums))
	}
}

// A power cut in the middle of the last entry: the entries before it read back, the torn one is cut off when the
// server next writes, and the next Bundle goes after the last whole entry.
func TestPacksTornTail(t *testing.T) {
	dir := t.TempDir()
	p := openPacks(dir)
	a, _ := p.put(bundleN(1))
	b, _ := p.put(bundleN(2))
	p.close()
	path := packName(dir, 1)
	info, _ := os.Stat(path)
	whole := info.Size()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(packMagic + strings.Repeat("a", 64) + " 500\n{\"resourceType\":\"Bun")
	f.Close()

	q := openPacks(dir)
	for _, s := range []string{a, b} {
		if _, err := q.get(s); err != nil {
			t.Fatalf("a whole entry before the torn one: %v", err)
		}
	}
	c, err := q.put(bundleN(3))
	if err != nil {
		t.Fatal(err)
	}
	q.close()
	if got, err := openPacks(dir).get(c); err != nil || !bytes.Equal(got, bundleN(3)) {
		t.Fatalf("the Bundle after the cut: %v", err)
	}
	info, _ = os.Stat(path)
	if want := whole + int64(len(fmt.Sprintf("%s%s %d\n", packMagic, c, len(bundleN(3))))+len(bundleN(3))+1); info.Size() != want {
		t.Errorf("pack is %d bytes, want %d (the torn entry cut off)", info.Size(), want)
	}
}

// A changed byte is caught: what is read must hash to its name.
func TestPacksChangedByte(t *testing.T) {
	dir := t.TempDir()
	p := openPacks(dir)
	s, _ := p.put([]byte(`{"resourceType":"Bundle","id":"tamper"}`))
	p.close()
	raw, _ := os.ReadFile(packName(dir, 1))
	os.WriteFile(packName(dir, 1), bytes.Replace(raw, []byte("tamper"), []byte("tamped"), 1), 0o600)
	if _, err := openPacks(dir).get(s); !errors.Is(err, ErrBundleChanged) {
		t.Errorf("a changed pack read without complaint: %v", err)
	}
}

// A full pack is closed and the next started; Bundles in both read back.
func TestPacksRotate(t *testing.T) {
	old := packLimit
	packLimit = 2000
	defer func() { packLimit = old }()
	dir := t.TempDir()
	p := openPacks(dir)
	var sums []string
	for i := 0; i < 60; i++ {
		s, err := p.put(bundleN(i))
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, s)
	}
	p.close()
	if n := len(segments(dir)); n < 3 {
		t.Fatalf("%d packs, want several", n)
	}
	q := openPacks(dir)
	for i, s := range sums {
		if got, err := q.get(s); err != nil || !bytes.Equal(got, bundleN(i)) {
			t.Fatalf("bundle %d: %v", i, err)
		}
	}
}

// Bundles kept as single files before packs still read, and are not written again into a pack.
func TestPacksReadOldFiles(t *testing.T) {
	dir := t.TempDir()
	raw := bundleN(9)
	h := sha256.Sum256(raw)
	sum := hex.EncodeToString(h[:])
	os.WriteFile(filepath.Join(dir, sum+".json"), raw, 0o600)
	p := openPacks(dir)
	if got, err := p.get(sum); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("old file: %v", err)
	}
	if s, err := p.put(raw); err != nil || s != sum {
		t.Fatal(err)
	}
	if len(segments(dir)) != 0 {
		t.Error("an old file's Bundle was written into a pack too")
	}
}

// Sealing rewrites plain entries; they read back with the key, and not without.
func TestSealPacks(t *testing.T) {
	seal.SetDefault(nil)
	defer seal.SetDefault(nil)
	dir := t.TempDir()
	p := openPacks(dir)
	s, _ := p.put([]byte(`{"resourceType":"Bundle","name":"Rivera"}`))
	p.close()
	k, _ := seal.Parse("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	seal.SetDefault(k)
	if n, err := SealPacks(dir); err != nil || n != 1 {
		t.Fatalf("sealed %d: %v", n, err)
	}
	if raw, _ := os.ReadFile(packName(dir, 1)); bytes.Contains(raw, []byte("Rivera")) {
		t.Error("the pack still holds the name")
	}
	if got, err := openPacks(dir).get(s); err != nil || !bytes.Contains(got, []byte("Rivera")) {
		t.Fatalf("reading back sealed: %v", err)
	}
	if sealed, plain, _ := PackEntries(dir); sealed != 1 || plain != 0 {
		t.Errorf("entries: %d sealed, %d plain", sealed, plain)
	}
	if n, _ := SealPacks(dir); n != 0 {
		t.Errorf("sealed again: %d", n)
	}
	seal.SetDefault(nil)
	if _, err := openPacks(dir).get(s); err == nil {
		t.Error("a sealed entry opened without the key")
	}
}
