// Package versions keeps every version of every policy that has been in force, so a decision can be read against the
// rules that made it, and any version replayed over the cases since.
//
// A version is kept as its source in the standard layout (policy.Format), named by policy id and the source's SHA-256,
// with a line in an index saying when it was first in force. The server keeps each policy it loads at start and each it
// puts in force at sign-off. Files are written once and never changed; one edited after the fact no longer matches its
// name and is refused.
package versions

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Version is one version of one policy.
type Version struct {
	Policy  string `json:"policy"`
	Version string `json:"version"`
	Title   string `json:"title"`
	SHA256  string `json:"sha256"`
	Since   string `json:"since"` // when it was first in force, RFC 3339
}

// Store is the directory of kept versions.
type Store struct {
	mu    sync.Mutex
	dir   string
	index []Version
	now   func() time.Time
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Open opens (creating) the store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: time.Now}
	f, err := os.Open(filepath.Join(dir, "index.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var v Version
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("versions: %s: %w", f.Name(), err)
		}
		s.index = append(s.index, v)
	}
	return s, sc.Err()
}

// Keep records p as in force from now, unless this exact source is kept already.
func (s *Store) Keep(p *policy.Policy) (Version, error) {
	src := []byte(policy.Format(p))
	sum := sha256.Sum256(src)
	v := Version{Policy: p.ID, Version: p.Version, Title: p.Title, SHA256: hex.EncodeToString(sum[:]),
		Since: s.now().UTC().Format(time.RFC3339)}
	if !idRe.MatchString(p.ID) {
		return v, fmt.Errorf("versions: %q cannot name a file", p.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.index {
		if old.Policy == v.Policy && old.SHA256 == v.SHA256 {
			return old, nil
		}
	}
	path := s.path(v.Policy, v.SHA256)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return v, err
	}
	if err := fsx.WriteFile(path, src, 0o600); err != nil { // whole and synced before the index names it
		return v, err
	}
	line, _ := json.Marshal(v)
	f, err := os.OpenFile(filepath.Join(s.dir, "index.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return v, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return v, err
	}
	if err := f.Sync(); err != nil {
		return v, err
	}
	s.index = append(s.index, v)
	return v, nil
}

// List is every kept version of a policy, oldest first.
func (s *Store) List(id string) []Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Version{}
	for _, v := range s.index {
		if v.Policy == id {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

// Source is a kept version's source, checked against its hash.
func (s *Store) Source(id, sha string) ([]byte, error) {
	if !idRe.MatchString(id) || !shaRe.MatchString(sha) {
		return nil, os.ErrNotExist
	}
	src, err := os.ReadFile(s.path(id, sha))
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(src); hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("versions: %s %s has been changed since it was kept", id, sha[:12])
	}
	return src, nil
}

func (s *Store) path(id, sha string) string {
	return filepath.Join(s.dir, id, sha+policy.Ext)
}
