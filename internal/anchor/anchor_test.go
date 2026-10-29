package anchor

import (
	"context"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
)

func openLog(t *testing.T, path string, n int) *audit.Log {
	t.Helper()
	l, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := l.Append("system", "test.record", "", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// TestCheckpointsAndProofs: a checkpoint is signed and recorded; a receipt's proof verifies offline, and fails with
// another salt or another key; a later checkpoint extends the earlier; a log rewritten after a checkpoint, every hash
// recomputed, no longer matches it, and the anchor says so.
func TestCheckpointsAndProofs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.jsonl")
	l := openLog(t, logPath, 7)
	a := &Anchor{Dir: filepath.Join(dir, "anchor"), Log: l, Name: "plan.example"}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c1, err := a.Checkpoint(ctx, false)
	if err != nil || c1 == nil || c1.Size != 7 {
		t.Fatalf("%+v %v", c1, err)
	}
	if c, _ := a.Checkpoint(ctx, false); c != nil {
		t.Errorf("a second checkpoint of nothing new: %+v", c)
	}
	if err := c1.Verify(a.Public()); err != nil {
		t.Fatal(err)
	}
	p, err := a.Prove(3)
	if err != nil || p.Checkpoint == nil || p.Pending {
		t.Fatalf("%+v %v", p, err)
	}
	if err := VerifyReceipt(*p, a.Public()); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	bad := *p
	bad.Salt = strings.Repeat("00", 32)
	if VerifyReceipt(bad, nil) == nil {
		t.Error("a wrong salt verified")
	}
	other := &Anchor{Dir: filepath.Join(dir, "other"), Log: l}
	if err := other.Open(); err != nil {
		t.Fatal(err)
	}
	if VerifyReceipt(*p, other.Public()) == nil {
		t.Error("verified with another server's key")
	}
	// The checkpoint record itself (8) is not yet in a checkpoint.
	if p8, _ := a.Prove(8); !p8.Pending {
		t.Errorf("record 8: %+v", p8)
	}
	for i := 0; i < 3; i++ {
		l.Append("system", "test.record", "", map[string]int{"later": i})
	}
	c2, err := a.Checkpoint(ctx, false)
	if err != nil || c2.Size != 11 {
		t.Fatalf("%+v %v", c2, err)
	}
	cp, err := a.Consistency(7, 11)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyConsistency(*cp, a.Public()); err != nil {
		t.Fatalf("consistency: %v", err)
	}
	raw, _ := json.Marshal(cp)
	var forged ConsistencyProof
	json.Unmarshal(raw, &forged)
	forged.To.Root = strings.Repeat("ab", 32)
	if VerifyConsistency(forged, nil) == nil {
		t.Error("a changed checkpoint verified")
	}

	// Rewrite the log: the same records, one changed, every hash recomputed (a fresh chain).
	recs := l.Records(0, 0)
	l.Close()
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	re, err := audit.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	for i, r := range recs {
		data := json.RawMessage(r.Data)
		if i == 2 {
			data = json.RawMessage(`{"i":99}`)
		}
		if _, err := re.Append(r.Actor, r.Action, r.Case, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := audit.Verify(logPath); err != nil {
		t.Fatalf("the rewritten chain should verify on its own: %v", err)
	}
	again := &Anchor{Dir: filepath.Join(dir, "anchor"), Log: re, Name: "plan.example"}
	if err := again.Open(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.Problem(), "no longer matches") {
		t.Fatalf("a rewritten log went unnoticed: %q", again.Problem())
	}
	if _, err := again.Checkpoint(ctx, true); err == nil {
		t.Error("a checkpoint was signed over a rewritten log")
	}
}

// TestWitnesses: against stand-ins, an OpenTimestamps calendar gets the digest and its answer is kept as an .ots file;
// a time-stamping authority's token is read for its time and refused over another digest; an endpoint gets the signed
// checkpoint with its token. Each is recorded, its proof kept.
func TestWitnesses(t *testing.T) {
	var gotDigest []byte
	var gotCheckpoint Checkpoint
	var gotAuth string
	gen := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/digest":
			gotDigest = b
			w.Write([]byte{0xf0, 0x08, 1, 2, 3, 4, 5, 6, 7, 8, 0x08})
		case "/tsa":
			var req timeStampReq
			asn1.Unmarshal(b, &req)
			w.Write(fakeToken(t, req.MessageImprint.HashedMessage, gen))
		case "/witness":
			gotAuth = r.Header.Get("Authorization")
			json.Unmarshal(b, &gotCheckpoint)
			w.Write([]byte(`{"kept":true}`))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	l := openLog(t, filepath.Join(dir, "audit.jsonl"), 2)
	defer l.Close()
	a := &Anchor{Dir: filepath.Join(dir, "anchor"), Log: l, Witnesses: []Witness{
		OpenTimestamps{Calendar: srv.URL}, TSA{URL: srv.URL + "/tsa"},
		HTTPWitness{Label: "regulator", URL: srv.URL + "/witness", Token: "w-token"}}}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	c, err := a.Checkpoint(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	d := c.Digest()
	if hex.EncodeToString(gotDigest) != hex.EncodeToString(d[:]) {
		t.Error("the calendar did not get the digest")
	}
	if gotAuth != "Bearer w-token" || gotCheckpoint.Signature != c.Signature {
		t.Errorf("the endpoint got %q %+v", gotAuth, gotCheckpoint)
	}
	ws := a.Checkpoints()[0].Witnesses
	if len(ws) != 3 {
		t.Fatalf("%+v", ws)
	}
	for _, w := range ws {
		if !w.OK || w.File == "" {
			t.Errorf("%+v", w)
		}
		raw, err := a.File(w.File)
		if err != nil || len(raw) == 0 {
			t.Errorf("%s: %v", w.File, err)
		}
		if w.Kind == "opentimestamps" && !strings.HasPrefix(string(raw), string(otsMagic)) {
			t.Error("not an .ots file")
		}
		if w.Kind == "rfc3161" && w.Time != gen.Format(time.RFC3339) {
			t.Errorf("time %s", w.Time)
		}
	}
	other := sha256.Sum256([]byte("another"))
	if _, err := ParseTimeStamp(fakeToken(t, other[:], gen), d[:]); err == nil {
		t.Error("a token over another digest was taken")
	}
	if _, err := a.File("../signing.key"); err == nil {
		t.Error("a file outside the witness folder was served")
	}
	n := 0
	for _, r := range l.Records(0, 0) {
		if r.Action == ActWitnessed {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d witness records", n)
	}
}

// fakeToken is a granted time-stamp response over digest, unsigned: the parts ParseTimeStamp reads.
func fakeToken(t *testing.T, digest []byte, gen time.Time) []byte {
	t.Helper()
	info, err := asn1.Marshal(struct {
		Version        int
		Policy         asn1.ObjectIdentifier
		MessageImprint messageImprint
		Serial         *big.Int
		GenTime        time.Time `asn1:"generalized"`
	}{1, asn1.ObjectIdentifier{1, 2, 3}, messageImprint{algorithm{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}, digest},
		big.NewInt(7), gen})
	if err != nil {
		t.Fatal(err)
	}
	sd, _ := asn1.Marshal(struct {
		Version          int
		DigestAlgorithms []algorithm `asn1:"set"`
		Encap            struct {
			Type    asn1.ObjectIdentifier
			Content []byte `asn1:"explicit,tag:0"`
		}
		SignerInfos []int `asn1:"set"`
	}{Version: 3, DigestAlgorithms: []algorithm{{Algorithm: oidSHA256}}, Encap: struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}{oidTSTInfo, info}, SignerInfos: []int{}})
	ci, _ := asn1.Marshal(struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue
	}{oidSignedData, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}})
	resp, _ := asn1.Marshal(struct {
		Status pkiStatusInfo
		Token  asn1.RawValue
	}{pkiStatusInfo{Status: 0}, asn1.RawValue{FullBytes: ci}})
	return resp
}

// TestPromises: a promise is signed at once; once a checkpoint covers the record, the receipt's proof keeps it. A
// server that later rebuilds its log without the record (same key, the old checkpoints thrown away, every hash fresh)
// signs a checkpoint that contradicts its own promise, and the contradiction is caught offline.
func TestPromises(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, filepath.Join(dir, "audit.jsonl"), 4)
	a := &Anchor{Dir: filepath.Join(dir, "anchor"), Log: l, Name: "plan.example"}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	rec, err := l.Append("system", "case.decided", "C-1", map[string]string{"decision": "deny"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := a.Promise(rec.Seq, rec.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.Verify(a.Public()); err != nil {
		t.Fatalf("promise: %v", err)
	}
	early, _ := a.Prove(rec.Seq)
	if err := pr.KeptBy(*early, a.Public()); err == nil || errors.Is(err, ErrBroken) {
		t.Fatalf("before any checkpoint: %v", err)
	}
	ctx := context.Background()
	if _, err := a.Checkpoint(ctx, false); err != nil {
		t.Fatal(err)
	}
	proof, _ := a.Prove(rec.Seq)
	if err := pr.KeptBy(*proof, a.Public()); err != nil {
		t.Fatalf("an honest log broke its promise: %v", err)
	}
	forged := *pr
	forged.Seq = 3
	if forged.Verify(nil) == nil {
		t.Error("a promise with a changed field verified")
	}

	// The server rebuilds its log without the denial: a harmless record in its place, the anchor's checkpoints gone,
	// the same signing key and salt kept so its new checkpoints look like its old ones.
	dir2 := t.TempDir()
	l2 := openLog(t, filepath.Join(dir2, "audit.jsonl"), 4)
	l2.Append("system", "case.decided", "C-1", map[string]string{"decision": "approve"})
	os.MkdirAll(filepath.Join(dir2, "anchor"), 0o700)
	for _, f := range []string{"signing.key", "salt.key"} {
		raw, _ := os.ReadFile(filepath.Join(dir, "anchor", f))
		os.WriteFile(filepath.Join(dir2, "anchor", f), raw, 0o600)
	}
	b := &Anchor{Dir: filepath.Join(dir2, "anchor"), Log: l2, Name: "plan.example"}
	if err := b.Open(); err != nil {
		t.Fatal(err)
	}
	if b.Problem() != "" {
		t.Fatalf("the rebuilt log should look clean to its own server: %s", b.Problem())
	}
	if _, err := b.Checkpoint(ctx, false); err != nil {
		t.Fatal(err)
	}
	lie, _ := b.Prove(rec.Seq)
	if err := VerifyReceipt(*lie, a.Public()); err != nil {
		t.Fatalf("the rebuilt log's own proof should verify alone: %v", err)
	}
	if err := pr.KeptBy(*lie, a.Public()); !errors.Is(err, ErrBroken) {
		t.Fatalf("a rebuilt log kept the promise: %v", err)
	}
	// Handing over the old leaf with the new tree's path does not help.
	mixed := *lie
	mixed.RecordHash, mixed.Leaf, mixed.Salt = proof.RecordHash, proof.Leaf, proof.Salt
	if err := pr.KeptBy(mixed, a.Public()); !errors.Is(err, ErrBroken) {
		t.Fatalf("the old leaf on the new tree kept the promise: %v", err)
	}
}
