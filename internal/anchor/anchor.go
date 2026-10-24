// Package anchor gives the audit log witnesses outside the plan. The log is hash-chained already, but the plan holds
// the whole chain: whoever can write the file could rewrite every record from some point on and recompute every hash
// after it. Anchoring closes that gap.
//
//   - Checkpoints. Every so often the server signs a checkpoint of the log: its size, the root of a Merkle tree over
//     its records (RFC 6962), and the time, signed with the server's own Ed25519 key. Each checkpoint must extend the
//     one before it (a consistency proof shows it does), and on every start the server checks that the log still has
//     the roots it signed: a log rewritten after a checkpoint no longer does, and the server says so.
//   - Witnesses. Each checkpoint may be sent to witnesses the plan chooses: OpenTimestamps (anchored in Bitcoin, free),
//     an RFC 3161 time-stamping authority, or an endpoint of a regulator's or partner's. What they keep proves the
//     checkpoint existed at that time, so a log rewritten later cannot match it. Outbound network is off unless the plan
//     names a witness.
//   - Receipts. A decision's receipt answer carries an inclusion proof against the checkpoint that covers it, so whoever
//     holds the receipt can check, offline, that the record is in the tree the server signed and the witnesses saw.
//
// Only hashes leave the server. A record's leaf is the hash of a secret salt and the record's hash (a commitment): a
// proof shows its neighbours' leaves to the receipt holder, and with the salt unknown, nobody can test a guess at
// another patient's record against them. The salt of a record is shown only with that record's own receipt.
//
// What it proves: that the log's records up to a checkpoint have not changed since the checkpoint was made (and, with a
// witness, since the time the witness recorded). What it does not: that a decision was right, or that nothing was left
// out of the log before the checkpoint.
package anchor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/merkle"
)

// Audit actions.
const (
	ActCheckpoint = "log.checkpoint" // the server signed a checkpoint
	ActWitnessed  = "log.witnessed"  // a witness answered for one
)

// Checkpoint is a signed statement of the log at one size.
type Checkpoint struct {
	Body      string `json:"body"` // the signed text: see body
	Name      string `json:"name"`
	Size      int    `json:"size"`
	Root      string `json:"root"` // hex
	Time      string `json:"time"`
	Key       string `json:"key"`       // the server's Ed25519 public key, base64
	Signature string `json:"signature"` // over Body, base64
}

// body is the text a checkpoint signs, one field a line.
func body(name string, size int, root merkle.Hash, t string) string {
	return fmt.Sprintf("vaglio checkpoint v1\n%s\n%d\n%s\n%s\n", name, size, hex.EncodeToString(root[:]), t)
}

// Digest is what witnesses are given: the SHA-256 of the checkpoint's signed text.
func (c Checkpoint) Digest() [32]byte { return sha256.Sum256([]byte(c.Body)) }

// Verify checks the signature, and that the body says what the fields do. key, when not nil, is the key the checkpoint
// must be signed with (the plan's published one); otherwise the key it carries is used.
func (c Checkpoint) Verify(key ed25519.PublicKey) error {
	if key == nil {
		raw, err := base64.StdEncoding.DecodeString(c.Key)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return errors.New("the checkpoint's key is not an Ed25519 public key")
		}
		key = raw
	} else if c.Key != base64.StdEncoding.EncodeToString(key) {
		return errors.New("the checkpoint is signed with another key than the plan's")
	}
	sig, err := base64.StdEncoding.DecodeString(c.Signature)
	if err != nil || !ed25519.Verify(key, []byte(c.Body), sig) {
		return errors.New("the checkpoint's signature does not verify")
	}
	root, err := rootOf(c.Root)
	if err != nil {
		return err
	}
	if body(c.Name, c.Size, root, c.Time) != c.Body {
		return errors.New("the checkpoint's fields do not match the text it signs")
	}
	return nil
}

func rootOf(h string) (merkle.Hash, error) {
	var r merkle.Hash
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != len(r) {
		return r, fmt.Errorf("%q is not a SHA-256 hash", h)
	}
	copy(r[:], raw)
	return r, nil
}

// WitnessProof is what a witness answered for a checkpoint.
type WitnessProof struct {
	Size    int    `json:"size"`
	Witness string `json:"witness"`
	Kind    string `json:"kind"` // opentimestamps, rfc3161, http
	Time    string `json:"time"`
	OK      bool   `json:"ok"`
	Note    string `json:"note,omitempty"`
	// Data is the witness's own proof (an .ots file, a time-stamp token, the endpoint's answer), checked with its own
	// tools; File is its name under the anchor directory.
	Data []byte `json:"data,omitempty"`
	File string `json:"file,omitempty"`
}

// Witness is someone outside the plan who keeps a record of a checkpoint.
type Witness interface {
	Name() string
	Witness(ctx context.Context, c Checkpoint) (WitnessProof, error)
}

// Anchor keeps the checkpoints of one log.
type Anchor struct {
	Dir       string // DATA/anchor
	Log       *audit.Log
	Name      string // the log's name in checkpoints: the plan's hostname
	Every     time.Duration
	Witnesses []Witness

	mu       sync.Mutex
	key      ed25519.PrivateKey
	salt     []byte
	leaves   []merkle.Hash
	hashes   []string // the records' hashes, for the receipt proofs
	points   []Checkpoint
	proofs   []WitnessProof
	problem  string
	lastErr  string
	finished chan struct{}
}

// Open reads (or makes) the keys, reads the checkpoints and witness proofs, and checks that the log still has every
// root it signed. A log that does not is not refused: it is what the anchors exist to show, and Problem says it.
func (a *Anchor) Open() error {
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return err
	}
	if a.Name == "" {
		a.Name = "vaglio"
	}
	seed, err := secret(filepath.Join(a.Dir, "signing.key"))
	if err != nil {
		return err
	}
	a.key = ed25519.NewKeyFromSeed(seed)
	if a.salt, err = secret(filepath.Join(a.Dir, "salt.key")); err != nil {
		return err
	}
	if err := readLines(filepath.Join(a.Dir, "checkpoints.jsonl"), func(raw []byte) error {
		var c Checkpoint
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		a.points = append(a.points, c)
		return nil
	}); err != nil {
		return err
	}
	if err := readLines(filepath.Join(a.Dir, "witnesses.jsonl"), func(raw []byte) error {
		var p WitnessProof
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		a.proofs = append(a.proofs, p)
		return nil
	}); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extend()
	a.problem = a.check()
	return nil
}

// secret reads a 32-byte secret file, making it when there is none.
func secret(file string) ([]byte, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		raw = make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		if err := fsx.WriteFile(file, raw, 0o600); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("%s is not a 32-byte key", file)
	}
	return raw, nil
}

func readLines(file string, each func([]byte) error) error {
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	// A power cut while a line was being added leaves it incomplete: it was never synced, so never relied on. It is
	// cut off, so the next line starts clean.
	if raw, err := io.ReadAll(f); err == nil && len(raw) > 0 && raw[len(raw)-1] != '\n' {
		if err := f.Truncate(int64(bytes.LastIndexByte(raw, '\n') + 1)); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if err := each(sc.Bytes()); err != nil {
			return fmt.Errorf("%s line %d: %w", file, n, err)
		}
	}
	return sc.Err()
}

func appendLine(file string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(file)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && errors.Is(statErr, os.ErrNotExist) {
		err = fsx.SyncDir(filepath.Dir(file)) // a new file's name, once
	}
	return err
}

// Salt is the secret salt of the record with this sequence number and hash.
func (a *Anchor) Salt(seq int64, hash string) []byte {
	m := hmac.New(sha256.New, a.salt)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(seq))
	m.Write(n[:])
	m.Write([]byte(hash))
	return m.Sum(nil)
}

// LeafOf is a record's leaf: the Merkle leaf hash of its salt and its hash.
func LeafOf(salt []byte, recordHash string) (merkle.Hash, error) {
	h, err := hex.DecodeString(recordHash)
	if err != nil || len(h) != 32 {
		return merkle.Hash{}, fmt.Errorf("%q is not a record hash", recordHash)
	}
	return merkle.Leaf(append(append([]byte{}, salt...), h...)), nil
}

// extend reads the records appended since and adds their leaves. The caller holds mu.
func (a *Anchor) extend() {
	// Only records on disk: a checkpoint over records a power cut could still take would later look like a rewrite.
	synced := a.Log.Synced()
	for len(a.leaves) < synced {
		page := a.Log.Records(int64(len(a.leaves)), min(1000, synced-len(a.leaves)))
		if len(page) == 0 {
			return
		}
		for _, r := range page {
			l, err := LeafOf(a.Salt(r.Seq, r.Hash), r.Hash)
			if err != nil {
				return
			}
			a.leaves, a.hashes = append(a.leaves, l), append(a.hashes, r.Hash)
		}
	}
}

// check is what is wrong with the checkpoints against the log, "" for nothing. The caller holds mu.
func (a *Anchor) check() string {
	pub := a.key.Public().(ed25519.PublicKey)
	for i, c := range a.points {
		if err := c.Verify(pub); err != nil {
			return fmt.Sprintf("checkpoint %d (size %d, %s): %v", i+1, c.Size, c.Time, err)
		}
		if c.Size > len(a.leaves) {
			return fmt.Sprintf("the log has %d records, fewer than the %d its checkpoint of %s covered: records were "+
				"removed after it", len(a.leaves), c.Size, c.Time)
		}
		root, _ := rootOf(c.Root)
		if merkle.Root(a.leaves[:c.Size]) != root {
			return fmt.Sprintf("the log no longer matches its checkpoint of %s (size %d): a record up to it was changed "+
				"after the checkpoint was made", c.Time, c.Size)
		}
		if i > 0 && c.Size < a.points[i-1].Size {
			return fmt.Sprintf("checkpoint %d is smaller than the one before it", i+1)
		}
	}
	return ""
}

// onlyOwnSince is whether every record after the first size ones is the anchor's own (a checkpoint, a witness's
// answer): nothing to sign a new checkpoint for. The caller holds mu.
func (a *Anchor) onlyOwnSince(size int) bool {
	if size >= len(a.leaves) {
		return true
	}
	for _, r := range a.Log.Records(int64(size), 0) {
		if r.Action != ActCheckpoint && r.Action != ActWitnessed {
			return false
		}
	}
	return true
}

// Problem is what is wrong between the log and its checkpoints, "" for nothing.
func (a *Anchor) Problem() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.problem
}

// Public is the server's checkpoint key, for the plan to publish.
func (a *Anchor) Public() ed25519.PublicKey { return a.key.Public().(ed25519.PublicKey) }

// Checkpoint signs a checkpoint of the log as it stands, unless the last one covers it already (force makes one
// anyway), records it, and sends it to the witnesses.
func (a *Anchor) Checkpoint(ctx context.Context, force bool) (*Checkpoint, error) {
	a.mu.Lock()
	a.extend()
	if a.problem != "" {
		a.mu.Unlock()
		return nil, errors.New("no checkpoint is signed over a log that does not match the last: " + a.problem)
	}
	n := len(a.leaves)
	if n == 0 || (!force && len(a.points) > 0 && a.onlyOwnSince(a.points[len(a.points)-1].Size)) {
		a.mu.Unlock()
		return nil, nil
	}
	root := merkle.Root(a.leaves)
	now := time.Now().UTC().Format(time.RFC3339)
	b := body(a.Name, n, root, now)
	c := Checkpoint{Body: b, Name: a.Name, Size: n, Root: hex.EncodeToString(root[:]), Time: now,
		Key:       base64.StdEncoding.EncodeToString(a.Public()),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(a.key, []byte(b)))}
	if k := len(a.points); k > 0 {
		// The new one must extend the last: it does, unless the records under the last changed in this process.
		prev := a.points[k-1]
		p, _ := merkle.Consistency(prev.Size, a.leaves)
		pr, _ := rootOf(prev.Root)
		if err := merkle.VerifyConsistency(prev.Size, n, pr, root, p); err != nil {
			a.problem = "the log does not extend its last checkpoint (" + prev.Time + ")"
			a.mu.Unlock()
			return nil, errors.New(a.problem)
		}
	}
	if err := appendLine(filepath.Join(a.Dir, "checkpoints.jsonl"), c); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	a.points = append(a.points, c)
	a.mu.Unlock()
	d := c.Digest()
	if _, err := a.Log.Append("system", ActCheckpoint, "", map[string]any{"size": n, "root": c.Root, "time": now,
		"digest": hex.EncodeToString(d[:])}); err != nil {
		return &c, err
	}
	for _, w := range a.Witnesses {
		p, err := w.Witness(ctx, c)
		p.Size, p.Witness = c.Size, w.Name()
		if p.Time == "" {
			p.Time = time.Now().UTC().Format(time.RFC3339)
		}
		if err != nil {
			p.OK, p.Note = false, err.Error()
		}
		a.keepProof(&p)
		_, _ = a.Log.Append("system", ActWitnessed, "", map[string]any{"size": c.Size, "witness": p.Witness,
			"kind": p.Kind, "ok": p.OK, "note": p.Note})
	}
	return &c, nil
}

// keepProof writes a witness's proof beside the checkpoints, its data as a file of its own.
func (a *Anchor) keepProof(p *WitnessProof) {
	if len(p.Data) > 0 {
		ext := map[string]string{"opentimestamps": ".ots", "rfc3161": ".tsr"}[p.Kind]
		if ext == "" {
			ext = ".json"
		}
		p.File = fmt.Sprintf("witness/%d.%s%s", p.Size, strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return '-'
		}, strings.ToLower(p.Witness)), ext)
		_ = os.MkdirAll(filepath.Join(a.Dir, "witness"), 0o700)
		if err := fsx.WriteFile(filepath.Join(a.Dir, p.File), p.Data, 0o600); err != nil {
			p.OK, p.Note, p.File = false, "the proof could not be kept: "+err.Error(), ""
		}
	}
	kept := *p
	kept.Data = nil
	_ = appendLine(filepath.Join(a.Dir, "witnesses.jsonl"), kept)
	a.mu.Lock()
	a.proofs = append(a.proofs, kept)
	a.mu.Unlock()
}

// Start makes a checkpoint at the interval until ctx ends.
func (a *Anchor) Start(ctx context.Context) {
	a.finished = make(chan struct{})
	go func() {
		defer close(a.finished)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.Every):
			}
			_, err := a.Checkpoint(ctx, false)
			a.mu.Lock()
			if err != nil {
				a.lastErr = err.Error()
			} else {
				a.lastErr = ""
			}
			a.mu.Unlock()
		}
	}()
}

// Done is closed when the anchor has stopped.
func (a *Anchor) Done() <-chan struct{} { return a.finished }

// Status is the anchor's state.
type Status struct {
	Name        string         `json:"name"`
	Key         string         `json:"key"`
	Records     int            `json:"records"`
	Checkpoints int            `json:"checkpoints"`
	Last        *Checkpoint    `json:"last,omitempty"`
	Witnesses   []WitnessProof `json:"witnesses"` // the last checkpoint's
	Every       string         `json:"every,omitempty"`
	Problem     string         `json:"problem,omitempty"`
	LastError   string         `json:"lastError,omitempty"`
}

// Status reports where the anchor stands.
func (a *Anchor) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extend()
	s := Status{Name: a.Name, Key: base64.StdEncoding.EncodeToString(a.Public()), Records: len(a.leaves),
		Checkpoints: len(a.points), Problem: a.problem, LastError: a.lastErr, Witnesses: []WitnessProof{}}
	if a.Every > 0 {
		s.Every = a.Every.String()
	}
	if n := len(a.points); n > 0 {
		last := a.points[n-1]
		s.Last = &last
		s.Witnesses = append(s.Witnesses, a.witnessesOf(last.Size)...)
	}
	return s
}

// witnessesOf are the witness proofs for the checkpoint of this size. The caller holds mu.
func (a *Anchor) witnessesOf(size int) []WitnessProof {
	var out []WitnessProof
	for _, p := range a.proofs {
		if p.Size == size {
			out = append(out, p)
		}
	}
	return out
}

// Checkpoints are every checkpoint signed, oldest first, each with what its witnesses answered.
func (a *Anchor) Checkpoints() []Witnessed {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Witnessed, 0, len(a.points))
	for _, c := range a.points {
		out = append(out, Witnessed{Checkpoint: c, Witnesses: append([]WitnessProof{}, a.witnessesOf(c.Size)...)})
	}
	return out
}

// Witnessed is a checkpoint and its witnesses' answers.
type Witnessed struct {
	Checkpoint
	Witnesses []WitnessProof `json:"witnesses"`
}

// File is a kept witness proof, by the name its proof gives.
func (a *Anchor) File(name string) ([]byte, error) {
	if !strings.HasPrefix(name, "witness/") || strings.Contains(name, "..") || strings.ContainsAny(name[8:], `/\`) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(a.Dir, name))
}

// ReceiptProof is what a receipt's holder needs to check, offline, that its record is in a checkpoint.
type ReceiptProof struct {
	Seq        int64  `json:"seq"`
	RecordHash string `json:"recordHash"`
	Salt       string `json:"salt"` // hex: this record's own, shown only with its receipt
	Leaf       string `json:"leaf"`
	// Checkpoint is the first checkpoint covering the record; nil while none does yet (Pending).
	Checkpoint *Checkpoint    `json:"checkpoint,omitempty"`
	Inclusion  []string       `json:"inclusion,omitempty"` // hex, the audit path to the checkpoint's root
	Witnesses  []WitnessProof `json:"witnesses,omitempty"`
	Pending    bool           `json:"pending,omitempty"`
	Problem    string         `json:"problem,omitempty"`
}

// Prove is the proof for the record with this sequence number.
func (a *Anchor) Prove(seq int64) (*ReceiptProof, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extend()
	i := int(seq) - 1
	if i < 0 || i >= len(a.leaves) {
		return nil, fmt.Errorf("no record %d", seq)
	}
	salt := a.Salt(seq, a.hashes[i])
	p := &ReceiptProof{Seq: seq, RecordHash: a.hashes[i], Salt: hex.EncodeToString(salt),
		Leaf: hex.EncodeToString(a.leaves[i][:]), Problem: a.problem}
	for _, c := range a.points {
		if c.Size > i {
			cc := c
			path, err := merkle.Inclusion(i, a.leaves[:c.Size])
			if err != nil {
				return nil, err
			}
			for _, h := range path {
				p.Inclusion = append(p.Inclusion, hex.EncodeToString(h[:]))
			}
			p.Checkpoint, p.Witnesses = &cc, a.witnessesOf(c.Size)
			return p, nil
		}
	}
	p.Pending = true
	return p, nil
}

// VerifyReceipt checks a receipt proof offline: the leaf is the record's (with its salt), the path leads from it to
// the checkpoint's root, and the checkpoint is signed (with key, when given). It says nothing of the witnesses, whose
// proofs are checked with their own tools.
func VerifyReceipt(p ReceiptProof, key ed25519.PublicKey) error {
	salt, err := hex.DecodeString(p.Salt)
	if err != nil {
		return errors.New("the salt is not hex")
	}
	leaf, err := LeafOf(salt, p.RecordHash)
	if err != nil {
		return err
	}
	if hex.EncodeToString(leaf[:]) != p.Leaf {
		return errors.New("the leaf is not the record's")
	}
	if p.Checkpoint == nil {
		return errors.New("no checkpoint covers this record yet")
	}
	if err := p.Checkpoint.Verify(key); err != nil {
		return err
	}
	root, err := rootOf(p.Checkpoint.Root)
	if err != nil {
		return err
	}
	path, err := hashes(p.Inclusion)
	if err != nil {
		return err
	}
	if err := merkle.VerifyInclusion(int(p.Seq)-1, p.Checkpoint.Size, leaf, path, root); err != nil {
		return errors.New("the record is not in the checkpoint's tree")
	}
	return nil
}

func hashes(hs []string) ([]merkle.Hash, error) {
	out := make([]merkle.Hash, 0, len(hs))
	for _, h := range hs {
		r, err := rootOf(h)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ConsistencyProof shows a later checkpoint extends an earlier one.
type ConsistencyProof struct {
	From  Checkpoint `json:"from"`
	To    Checkpoint `json:"to"`
	Proof []string   `json:"proof"`
}

// Consistency is the proof between the checkpoints of these sizes.
func (a *Anchor) Consistency(from, to int) (*ConsistencyProof, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var f, t *Checkpoint
	for i := range a.points {
		c := a.points[i]
		if c.Size == from && f == nil {
			f = &c
		}
		if c.Size == to {
			t = &c
		}
	}
	if f == nil || t == nil {
		return nil, errors.New("there is no checkpoint of size " + strconv.Itoa(map[bool]int{true: from, false: to}[f == nil]))
	}
	if from > to || to > len(a.leaves) {
		return nil, errors.New("the earlier checkpoint comes first")
	}
	p, err := merkle.Consistency(from, a.leaves[:to])
	if err != nil {
		return nil, err
	}
	out := &ConsistencyProof{From: *f, To: *t, Proof: []string{}}
	for _, h := range p {
		out.Proof = append(out.Proof, hex.EncodeToString(h[:]))
	}
	return out, nil
}

// VerifyConsistency checks a consistency proof offline: both checkpoints signed (with key, when given), the later one
// extending the earlier.
func VerifyConsistency(p ConsistencyProof, key ed25519.PublicKey) error {
	if err := p.From.Verify(key); err != nil {
		return fmt.Errorf("the earlier checkpoint: %w", err)
	}
	if err := p.To.Verify(key); err != nil {
		return fmt.Errorf("the later checkpoint: %w", err)
	}
	r1, _ := rootOf(p.From.Root)
	r2, _ := rootOf(p.To.Root)
	path, err := hashes(p.Proof)
	if err != nil {
		return err
	}
	if err := merkle.VerifyConsistency(p.From.Size, p.To.Size, r1, r2, path); err != nil {
		return errors.New("the later checkpoint does not extend the earlier: the log was changed between them")
	}
	return nil
}
