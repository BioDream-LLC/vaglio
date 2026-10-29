package anchor

// Promises: what makes leaving a decision out of the log provable. A checkpoint shows the records it covers have not
// changed, but on its own it cannot show a record is missing: a server could answer a request and never log it, or
// log it and later rebuild the log without it, before the next checkpoint. So every decision is answered with a
// promise, signed by the same key as the checkpoints: "the record of this decision is number N in the log, with this
// hash and this leaf". Whoever holds the answer (the PAS server, the provider, the patient, a regulator) can later ask
// for the receipt's proof and check that checkpoint N's tree holds that leaf at that place. If it does not, the two
// signed statements contradict each other, and the contradiction is proof, signed by the server itself, that the log
// was changed or the record left out. This is the signed promise certificate transparency logs give (RFC 6962's SCT).
//
// What is left: a decision nobody was ever told about carries no promise, so leaving it out stays invisible. A
// decision is told to the PAS server at least, so in practice that is a decision never answered at all.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Promise is the server's signed statement that a decision's record is at a place in the log.
type Promise struct {
	Body       string `json:"body"` // the signed text: see promiseBody
	Name       string `json:"name"`
	Seq        int64  `json:"seq"`
	RecordHash string `json:"recordHash"`
	Leaf       string `json:"leaf"` // hex: the record's leaf in the log's Merkle tree
	Time       string `json:"time"`
	Key        string `json:"key"`       // the server's Ed25519 public key, base64 (the checkpoints' key)
	Signature  string `json:"signature"` // over Body, base64
}

func promiseBody(name string, seq int64, recordHash, leaf, t string) string {
	return fmt.Sprintf("vaglio promise v1\n%s\n%d\n%s\n%s\n%s\n", name, seq, recordHash, leaf, t)
}

// Promise signs a promise for the record with this sequence number and hash. It needs no checkpoint and no read of
// the log: the leaf comes from the record's hash and its salt.
func (a *Anchor) Promise(seq int64, recordHash string) (*Promise, error) {
	leaf, err := LeafOf(a.Salt(seq, recordHash), recordHash)
	if err != nil {
		return nil, err
	}
	p := &Promise{Name: a.Name, Seq: seq, RecordHash: recordHash, Leaf: hex.EncodeToString(leaf[:]),
		Time: time.Now().UTC().Format(time.RFC3339), Key: base64.StdEncoding.EncodeToString(a.Public())}
	p.Body = promiseBody(p.Name, p.Seq, p.RecordHash, p.Leaf, p.Time)
	p.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(a.key, []byte(p.Body)))
	return p, nil
}

// Verify checks the promise's signature, and that its fields say what its text does. key, when not nil, is the
// plan's published key; otherwise the key the promise carries is used.
func (p Promise) Verify(key ed25519.PublicKey) error {
	if key == nil {
		raw, err := base64.StdEncoding.DecodeString(p.Key)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return errors.New("the promise's key is not an Ed25519 public key")
		}
		key = raw
	} else if p.Key != base64.StdEncoding.EncodeToString(key) {
		return errors.New("the promise is signed with another key than the plan's")
	}
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil || !ed25519.Verify(key, []byte(p.Body), sig) {
		return errors.New("the promise's signature does not verify")
	}
	if promiseBody(p.Name, p.Seq, p.RecordHash, p.Leaf, p.Time) != p.Body {
		return errors.New("the promise's fields do not match the text it signs")
	}
	return nil
}

// ErrBroken is a promise the server's own checkpoint contradicts.
var ErrBroken = errors.New("BROKEN PROMISE")

// KeptBy checks a promise against a receipt proof given later. It is nil when the proof's checkpoint holds the
// promised record at the promised place; ErrBroken (wrapped, with what differs) when the server signed both and they
// disagree, which is the proof that the log was changed or the record left out; another error when nothing can be
// said yet (no checkpoint covers the record) or the files themselves do not verify.
func (p Promise) KeptBy(r ReceiptProof, key ed25519.PublicKey) error {
	if err := p.Verify(key); err != nil {
		return err
	}
	if r.Seq != p.Seq {
		return fmt.Errorf("the proof is for record %d, the promise for record %d", r.Seq, p.Seq)
	}
	if r.Pending || r.Checkpoint == nil {
		return errors.New("no checkpoint covers the record yet: check again after the next one")
	}
	if err := r.Checkpoint.Verify(key); err != nil {
		return err
	}
	if r.Checkpoint.Key != p.Key {
		return errors.New("the checkpoint and the promise are signed with different keys")
	}
	if r.RecordHash != p.RecordHash || r.Leaf != p.Leaf {
		return fmt.Errorf("%w: the server promised record %d was %.16s… (leaf %.16s…), and its checkpoint of %s "+
			"holds %.16s… (leaf %.16s…) there", ErrBroken, p.Seq, p.RecordHash, p.Leaf, r.Checkpoint.Time, r.RecordHash,
			r.Leaf)
	}
	if err := VerifyReceipt(r, key); err != nil {
		return fmt.Errorf("%w: the promised record is not in the tree of the checkpoint of %s that covers it (%v)",
			ErrBroken, r.Checkpoint.Time, err)
	}
	return nil
}
