// Package audit is Vaglio's audit trail: an append-only log in which every record carries the hash of the one before.
//
// Everything that happens to a case is a record here - the engine's decision, a reviewer's, its delivery to the PAS
// server - and so is every sign-in and every change to who may sign in. The case list is rebuilt from this log on start
// rather than kept beside it, so there is one account of what happened, not a log and a database that might disagree.
//
// The chain makes the log tamper-evident, not tamper-proof: whoever can write the file can rewrite all of it. What it
// does guarantee is that editing or deleting any record, short of rewriting every record after it, is detected, and the
// server refuses to start on a log whose chain is broken. Keeping a copy of the latest hash somewhere else (Verify prints
// it) turns that into a guarantee about the whole log up to that point.
package audit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Record is one entry in the log.
type Record struct {
	Seq    int64           `json:"seq"`
	Time   string          `json:"time"`  // RFC 3339, UTC
	Actor  string          `json:"actor"` // who did it: "user:<name>", "pas" (the PAS server), "system"
	Action string          `json:"action"`
	Case   string          `json:"case,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Prev   string          `json:"prev"` // the hash of the record before, "" for the first
	Hash   string          `json:"hash,omitempty"`
}

// hash is the record's hash: SHA-256 of its JSON with the hash itself left out.
func (r Record) hash() (string, error) {
	r.Hash = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Log is an open audit log. It is safe for concurrent use, and for use by several processes at once - the server and the
// vaglio user command both write it: each append holds a lock on the file, first reads whatever another process appended
// (verifying it as Open does), and only then numbers and chains its own record.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	size    int64 // bytes of the file read into records
	records []Record
	now     func() time.Time
}

// maxLine bounds one record. Records carry decisions and citations, not documents.
const maxLine = 4 << 20

// Open opens the log at path, creating it if need be, and verifies its whole chain. A broken chain is an error: the
// server must not carry on writing to a log it cannot vouch for.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, now: time.Now}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	err = l.catchUp()
	unlock(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

// catchUp reads and verifies whatever the file holds past what this Log has read. The caller holds the file lock.
func (l *Log) catchUp() error {
	info, err := l.f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == l.size {
		return nil
	}
	if info.Size() < l.size {
		return fmt.Errorf("%w: the file is shorter than the records already read from it", ErrBroken)
	}
	prev := ""
	if n := len(l.records); n > 0 {
		prev = l.records[n-1].Hash
	}
	more, err := read(io.NewSectionReader(l.f, l.size, info.Size()-l.size), int64(len(l.records))+1, prev)
	if err != nil {
		return err
	}
	l.records = append(l.records, more...)
	l.size = info.Size()
	return nil
}

// sync catches up with other processes' appends, for the readers.
func (l *Log) sync() {
	if lock(l.f) == nil {
		_ = l.catchUp() // a reader shows what verified; Append and Open refuse what did not
		unlock(l.f)
	}
}

// Verify reads the log at path and checks its chain without opening it for writing. It returns the number of records and
// the latest hash.
func Verify(path string) (int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	records, err := read(f, 1, "")
	if err != nil {
		return 0, "", err
	}
	if len(records) == 0 {
		return 0, "", nil
	}
	return len(records), records[len(records)-1].Hash, nil
}

// ErrBroken is a chain that does not verify.
var ErrBroken = errors.New("the audit log's chain is broken")

// read reads and verifies records, the first of which must be number first and follow the record with hash prev.
func read(r io.Reader, first int64, prev string) ([]Record, error) {
	var out []Record
	br := bufio.NewReaderSize(r, 64<<10)
	if _, err := br.Peek(1); err == nil {
		// Every record ends its line; a file that does not end in one was cut off mid-write.
		raw, _ := io.ReadAll(io.LimitReader(br, 1<<40))
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			return nil, fmt.Errorf("%w: the last record is incomplete", ErrBroken)
		}
		br = bufio.NewReaderSize(bytes.NewReader(raw), 64<<10)
	}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for n := first; sc.Scan(); n++ {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf("%w: line %d is blank", ErrBroken, n)
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("%w: line %d is not a record: %v", ErrBroken, n, err)
		}
		want, err := rec.hash()
		if err != nil {
			return nil, err
		}
		switch {
		case rec.Seq != n:
			return nil, fmt.Errorf("%w: line %d has sequence number %d", ErrBroken, n, rec.Seq)
		case rec.Prev != prev:
			return nil, fmt.Errorf("%w: record %d does not follow record %d (a record was removed, reordered or inserted)",
				ErrBroken, n, n-1)
		case rec.Hash != want:
			return nil, fmt.Errorf("%w: record %d has been altered", ErrBroken, n)
		}
		prev = rec.Hash
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Append writes a record and syncs it to disk before returning: a decision is not made until it is written down.
func (l *Log) Append(actor, action, caseID string, data any) (Record, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Record{}, err
		}
		raw = b
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := lock(l.f); err != nil {
		return Record{}, err
	}
	defer unlock(l.f)
	if err := l.catchUp(); err != nil {
		return Record{}, err // another writer broke the chain: refuse to write after it
	}
	rec := Record{Seq: int64(len(l.records)) + 1, Time: l.now().UTC().Format(time.RFC3339Nano), Actor: actor,
		Action: action, Case: caseID, Data: raw}
	if n := len(l.records); n > 0 {
		rec.Prev = l.records[n-1].Hash
	}
	h, err := rec.hash()
	if err != nil {
		return Record{}, err
	}
	rec.Hash = h
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	if len(line) >= maxLine {
		return Record{}, fmt.Errorf("audit: a record of %d bytes is too large", len(line))
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return Record{}, err
	}
	if err := l.f.Sync(); err != nil {
		return Record{}, err
	}
	l.records = append(l.records, rec)
	l.size += int64(len(line)) + 1
	return rec, nil
}

// Records returns the records after seq, at most limit of them (0: all).
func (l *Log) Records(after int64, limit int) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sync()
	if after < 0 {
		after = 0
	}
	if after >= int64(len(l.records)) {
		return []Record{}
	}
	out := l.records[after:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return append([]Record(nil), out...)
}

// ForCase returns every record about one case, oldest first.
func (l *Log) ForCase(id string) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sync()
	out := []Record{}
	for _, r := range l.records {
		if r.Case == id {
			out = append(out, r)
		}
	}
	return out
}

// Head is the number of records and the latest hash.
func (l *Log) Head() (int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sync()
	if len(l.records) == 0 {
		return 0, ""
	}
	return len(l.records), l.records[len(l.records)-1].Hash
}

// Close closes the log.
func (l *Log) Close() error { return l.f.Close() }

// ReadFile reads and verifies the whole log at path without opening it for writing: a copy someone was given, say.
func ReadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return read(f, 1, "")
}
