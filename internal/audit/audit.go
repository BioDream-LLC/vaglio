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
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/seal"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	byCase  map[string][]int // each case's records, by place in records: ForCase is asked on every answer
	ends    []int64          // where each record's line ends in the file
	now     func() time.Time

	// Group commit: an append writes its line under mu, then waits, outside it, until the file is synced past that
	// line. One waiter syncs for everyone written by then, so appends arriving together share one sync.
	sy struct {
		mu      sync.Mutex
		cond    *sync.Cond
		synced  int64 // the file is durable up to here
		running bool
		err     error // a failed sync: nothing more is written to this log
	}
}

// durable waits until the file is synced past end.
func (l *Log) durable(end int64) error {
	s := &l.sy
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.synced < end {
		if s.err != nil {
			return s.err
		}
		if s.running {
			s.cond.Wait()
			continue
		}
		s.running = true
		s.mu.Unlock()
		l.mu.Lock()
		target := l.size // everything written so far, by this process or (synced by them) others
		l.mu.Unlock()
		err := l.f.Sync()
		s.mu.Lock()
		s.running = false
		if err != nil {
			// After a failed fsync the file's state on disk is unknown; writing on would build on it.
			s.err = fmt.Errorf("audit: syncing the log failed, so nothing more is written to it until a restart: %w", err)
		} else if target > s.synced {
			s.synced = target
		}
		s.cond.Broadcast()
	}
	return nil
}

// maxLine bounds one record. Records carry decisions and citations, not documents.
const maxLine = 4 << 20

// Open opens the log at path, creating it if need be, and verifies its whole chain. A broken chain is an error: the
// server must not carry on writing to a log it cannot vouch for.
func Open(path string) (*Log, error) {
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if errors.Is(statErr, os.ErrNotExist) {
		// A new log: its name made durable, so its first synced records are never in a file a power cut forgets.
		if err := fsx.SyncDir(filepath.Dir(path)); err != nil {
			f.Close()
			return nil, err
		}
	}
	l := &Log{f: f, now: time.Now}
	l.sy.cond = sync.NewCond(&l.sy.mu)
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	err = l.catchUp()
	var torn *TornTail
	if errors.Is(err, errTorn) {
		torn, err = setAsideTorn(f, path)
		if err == nil {
			l.records, l.byCase, l.ends, l.size = nil, nil, nil, 0
			err = l.catchUp()
		}
	}
	unlock(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err == nil {
		// What was read is made durable now (a previous process may have been killed before its sync), so it all
		// counts as synced.
		if err = f.Sync(); err == nil {
			l.sy.synced = l.size
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if torn != nil {
		// Said in the log itself, so whoever reads it knows a write was cut short and where its bytes are.
		if _, err := l.Append("system", "log.torn_tail_set_aside", "", torn); err != nil {
			l.Close()
			return nil, err
		}
	}
	return l, nil
}

// errTorn is a log whose last line was cut off mid-write.
var errTorn = errors.New("the last record is incomplete (cut off mid-write, as by a power cut; the server sets it " +
	"aside when it next starts)")

// TornTail is what a power cut left: the bytes after the last whole record. They cannot be a record anyone was
// answered on (an answer waits until its whole line is synced), so the log is cut back to its last whole record and
// the bytes are kept beside it, never thrown away.
type TornTail struct {
	Bytes int    `json:"bytes"`
	Kept  string `json:"kept"`  // the file they are kept in, beside the log
	Whole int64  `json:"whole"` // the log's length after, its last whole record's end
}

// setAsideTorn copies the torn bytes to PATH.torn-TIME, synced, then cuts the log back to its last whole line.
func setAsideTorn(_ *os.File, path string) (*TornTail, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	good := bytes.LastIndexByte(raw, '\n') + 1
	kept := fmt.Sprintf("%s.torn-%d", path, time.Now().UnixNano())
	k, err := os.OpenFile(kept, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = k.Write(raw[good:])
	if err == nil {
		err = k.Sync()
	}
	if cerr := k.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsx.SyncDir(filepath.Dir(path))
	}
	if err == nil {
		// Through a handle of its own: the log's is opened for appending, which Windows does not let truncate.
		var t *os.File
		if t, err = os.OpenFile(path, os.O_RDWR, 0); err == nil {
			err = t.Truncate(int64(good))
			if err == nil {
				err = t.Sync()
			}
			if cerr := t.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("setting aside a torn last record: %w", err)
	}
	return &TornTail{Bytes: len(raw) - good, Kept: filepath.Base(kept), Whole: int64(good)}, nil
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
	l.keep(info.Size(), more...) // read together: durable once the file is synced past all of them
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
			return nil, fmt.Errorf("%w: %w", ErrBroken, errTorn)
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
		line, err := seal.Unline(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
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
	l.sy.mu.Lock()
	failed := l.sy.err
	l.sy.mu.Unlock()
	if failed != nil {
		return Record{}, failed
	}
	rec, end, err := l.write(actor, action, caseID, raw)
	if err != nil {
		return Record{}, err
	}
	// A decision is not made until it is written down: the record is answered only once it is on disk.
	if err := l.durable(end); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// AppendLater writes a record like Append but returns before it is synced, with a function that waits until it is:
// for a caller holding its own lock, which releases it before waiting so that appends arriving together share one sync.
// Nothing may be answered on the record until wait has returned nil.
func (l *Log) AppendLater(actor, action, caseID string, data any) (Record, func() error, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Record{}, nil, err
		}
		raw = b
	}
	l.sy.mu.Lock()
	failed := l.sy.err
	l.sy.mu.Unlock()
	if failed != nil {
		return Record{}, nil, failed
	}
	rec, end, err := l.write(actor, action, caseID, raw)
	if err != nil {
		return Record{}, nil, err
	}
	return rec, func() error { return l.durable(end) }, nil
}

// write numbers, chains and writes one record, and returns it with the file offset its line ends at. Readers in this
// process may see it before it is synced; Append returns it to the caller only after.
func (l *Log) write(actor, action, caseID string, raw json.RawMessage) (Record, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := lock(l.f); err != nil {
		return Record{}, 0, err
	}
	defer unlock(l.f)
	if err := l.catchUp(); err != nil {
		return Record{}, 0, err // another writer broke the chain: refuse to write after it
	}
	rec := Record{Seq: int64(len(l.records)) + 1, Time: l.now().UTC().Format(time.RFC3339Nano), Actor: actor,
		Action: action, Case: caseID, Data: raw}
	if n := len(l.records); n > 0 {
		rec.Prev = l.records[n-1].Hash
	}
	h, err := rec.hash()
	if err != nil {
		return Record{}, 0, err
	}
	rec.Hash = h
	end, err := l.put(rec)
	return rec, end, err
}

// put writes a numbered, chained record's line. The caller holds mu and the file lock.
func (l *Log) put(rec Record) (int64, error) {
	line, err := json.Marshal(rec)
	if err != nil {
		return 0, err
	}
	line = seal.Line(line)
	if len(line) >= maxLine {
		return 0, fmt.Errorf("audit: a record of %d bytes is too large", len(line))
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	l.keep(l.size+int64(len(line))+1, rec)
	l.size += int64(len(line)) + 1
	return l.size, nil
}

// Copy appends a record made by another log - a standby's copy of its primary's - keeping its number, time and hash:
// it must be the next record of this log's chain, and its hash must be its own.
func (l *Log) Copy(rec Record) error { return l.CopyAll([]Record{rec}) }

// CopyAll appends records copied from another log, in order, each checked to follow the one before it and to be
// unaltered, and syncs once for all of them: a standby catching up writes a page of records with one sync, not one each.
// Nothing is written unless every record checks.
func (l *Log) CopyAll(recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	end, err := func() (int64, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if err := lock(l.f); err != nil {
			return 0, err
		}
		defer unlock(l.f)
		if err := l.catchUp(); err != nil {
			return 0, err
		}
		prev := ""
		if n := len(l.records); n > 0 {
			prev = l.records[n-1].Hash
		}
		next := int64(len(l.records)) + 1
		for _, rec := range recs {
			want, err := rec.hash()
			switch {
			case err != nil:
				return 0, err
			case rec.Seq != next:
				return 0, fmt.Errorf("%w: record %d arrived after record %d", ErrBroken, rec.Seq, next-1)
			case rec.Prev != prev:
				return 0, fmt.Errorf("%w: record %d does not follow this log's last record", ErrBroken, rec.Seq)
			case rec.Hash != want:
				return 0, fmt.Errorf("%w: record %d has been altered", ErrBroken, rec.Seq)
			}
			prev, next = rec.Hash, next+1
		}
		var end int64
		for _, rec := range recs {
			e, err := l.put(rec)
			if err != nil {
				return 0, err
			}
			end = e
		}
		return end, nil
	}()
	if err != nil {
		return err
	}
	return l.durable(end)
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
	for _, i := range l.byCase[id] {
		out = append(out, l.records[i])
	}
	return out
}

// keep adds records read or written to the ones in memory, and to their cases' lists; end is where in the file they
// are all on disk once it is synced that far. The caller holds mu.
func (l *Log) keep(end int64, recs ...Record) {
	if l.byCase == nil {
		l.byCase = map[string][]int{}
	}
	for _, r := range recs {
		if r.Case != "" {
			l.byCase[r.Case] = append(l.byCase[r.Case], len(l.records))
		}
		l.records = append(l.records, r)
		l.ends = append(l.ends, end)
	}
}

// Synced is how many records are certainly on disk: readers in this process see records as soon as they are
// written, but anything signed outside the process (an anchor's checkpoint) must cover only these, or a power cut
// could leave a checkpoint over records the disk never kept.
func (l *Log) Synced() int {
	l.sy.mu.Lock()
	synced := l.sy.synced
	l.sy.mu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return sort.Search(len(l.ends), func(i int) bool { return l.ends[i] > synced })
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
