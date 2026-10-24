package anchor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Rekor records each checkpoint in Sigstore's public transparency log as a hashedrekord entry: the checkpoint's
// digest, signed. Rekor's hashedrekord checks a signature over a digest, which Ed25519 cannot make (only Ed25519ph),
// so this witness signs with an ECDSA P-256 key of its own, kept beside the checkpoint key (DATA/anchor/rekor.key);
// the entry names its public half. Rekor's answer (the entry, its log index, its signed entry timestamp and inclusion
// proof) is kept as a .json file. Only the digest and the signature leave.
type Rekor struct {
	URL    string // https://rekor.sigstore.dev
	Key    *ecdsa.PrivateKey
	Client *http.Client
	// LogKey, when set, is Rekor's public key: the signed entry timestamp in its answer is then checked.
	LogKey *ecdsa.PublicKey
}

// Name is the log's host.
func (r Rekor) Name() string { return "rekor " + strings.TrimPrefix(r.URL, "https://") }

// RekorKey reads the Rekor signing key from dir, making it the first time.
func RekorKey(dir string) (*ecdsa.PrivateKey, error) {
	file := filepath.Join(dir, "rekor.key")
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := fsx.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, fmt.Errorf("%s is not a PEM key", file)
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an ECDSA key", file)
	}
	return ek, nil
}

// PublicPEM is the public half, as Rekor and cosign take it.
func PublicPEM(k *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// RekorEntry is one log entry as Rekor answers it.
type RekorEntry struct {
	Body           string `json:"body"`
	IntegratedTime int64  `json:"integratedTime"`
	LogID          string `json:"logID"`
	LogIndex       int64  `json:"logIndex"`
	Verification   struct {
		SignedEntryTimestamp string          `json:"signedEntryTimestamp"`
		InclusionProof       json.RawMessage `json:"inclusionProof,omitempty"`
	} `json:"verification"`
}

// Witness signs the digest and submits it.
func (r Rekor) Witness(ctx context.Context, c Checkpoint) (WitnessProof, error) {
	p := WitnessProof{Kind: "rekor"}
	if r.Key == nil {
		return p, errors.New("no Rekor signing key")
	}
	d := c.Digest()
	sig, err := ecdsa.SignASN1(rand.Reader, r.Key, d[:])
	if err != nil {
		return p, err
	}
	pub, err := PublicPEM(&r.Key.PublicKey)
	if err != nil {
		return p, err
	}
	entry := map[string]any{"apiVersion": "0.0.1", "kind": "hashedrekord", "spec": map[string]any{
		"signature": map[string]any{"content": base64.StdEncoding.EncodeToString(sig),
			"publicKey": map[string]any{"content": base64.StdEncoding.EncodeToString(pub)}},
		"data": map[string]any{"hash": map[string]any{"algorithm": "sha256", "value": hex.EncodeToString(d[:])}}}}
	body, _ := json.Marshal(entry)
	ans, err := post(ctx, r.Client, strings.TrimRight(r.URL, "/")+"/api/v1/log/entries", "application/json",
		"application/json", body, nil)
	if err != nil {
		return p, err
	}
	e, err := ParseRekor(ans, d[:], r.LogKey)
	if err != nil {
		return p, err
	}
	p.Data, p.OK = ans, true
	p.Time = time.Unix(e.IntegratedTime, 0).UTC().Format(time.RFC3339)
	p.Note = fmt.Sprintf("entry %d in Rekor's public log, integrated %s", e.LogIndex, p.Time)
	if r.LogKey != nil {
		p.Note += "; its signed entry timestamp checked"
	}
	return p, nil
}

// ParseRekor reads Rekor's answer to a new entry: one entry, of this digest; with logKey, its signed entry timestamp
// (Rekor's signature over the entry's canonical JSON) is checked too.
func ParseRekor(ans, digest []byte, logKey *ecdsa.PublicKey) (*RekorEntry, error) {
	var m map[string]RekorEntry
	if err := json.Unmarshal(ans, &m); err != nil || len(m) != 1 {
		return nil, errors.New("not one Rekor entry")
	}
	var e RekorEntry
	for _, v := range m {
		e = v
	}
	raw, err := base64.StdEncoding.DecodeString(e.Body)
	if err != nil {
		return nil, fmt.Errorf("the entry's body: %w", err)
	}
	var got struct {
		Kind string `json:"kind"`
		Spec struct {
			Data struct {
				Hash struct {
					Algorithm string `json:"algorithm"`
					Value     string `json:"value"`
				} `json:"hash"`
			} `json:"data"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Kind != "hashedrekord" {
		return nil, errors.New("the entry is not a hashedrekord")
	}
	if got.Spec.Data.Hash.Algorithm != "sha256" || got.Spec.Data.Hash.Value != hex.EncodeToString(digest) {
		return nil, errors.New("the entry is over another digest than the checkpoint's")
	}
	if logKey != nil {
		set, err := base64.StdEncoding.DecodeString(e.Verification.SignedEntryTimestamp)
		if err != nil {
			return nil, errors.New("no signed entry timestamp")
		}
		// The canonical JSON Rekor signs: these four keys, sorted, without spaces (RFC 8785 for these values).
		canon, _ := json.Marshal(struct {
			Body           string `json:"body"`
			IntegratedTime int64  `json:"integratedTime"`
			LogID          string `json:"logID"`
			LogIndex       int64  `json:"logIndex"`
		}{e.Body, e.IntegratedTime, e.LogID, e.LogIndex})
		h := sha256.Sum256(canon)
		if !ecdsa.VerifyASN1(logKey, h[:], set) {
			return nil, errors.New("Rekor's signed entry timestamp does not verify with its key")
		}
	}
	return &e, nil
}

// RekorLogKey fetches a Rekor log's public key (GET /api/v1/log/publicKey), for ParseRekor.
func RekorLogKey(ctx context.Context, c *http.Client, url string) (*ecdsa.PublicKey, error) {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/api/v1/log/publicKey", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, _ := pem.Decode(buf.Bytes())
	if b == nil {
		return nil, errors.New("Rekor's key is not PEM")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("Rekor's key is not ECDSA")
	}
	return ek, nil
}

func parsePEMPublic(raw []byte) (*ecdsa.PublicKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, errors.New("not PEM")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not ECDSA")
	}
	return ek, nil
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }
