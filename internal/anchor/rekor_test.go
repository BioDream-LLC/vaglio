package anchor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestRekor: against a stand-in Rekor that checks the entry's signature as Rekor does (ECDSA over the digest) and signs
// its answer, the entry is accepted, kept, and its signed entry timestamp checked; a timestamp from another log key, or
// an entry over another digest, is refused. The key is made once and kept.
func TestRekor(t *testing.T) {
	logKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Spec struct {
				Signature struct {
					Content   string `json:"content"`
					PublicKey struct {
						Content string `json:"content"`
					} `json:"publicKey"`
				} `json:"signature"`
				Data struct {
					Hash struct{ Value string } `json:"hash"`
				} `json:"data"`
			} `json:"spec"`
		}
		json.Unmarshal(raw, &in)
		sig, _ := base64.StdEncoding.DecodeString(in.Spec.Signature.Content)
		pemKey, _ := base64.StdEncoding.DecodeString(in.Spec.Signature.PublicKey.Content)
		pub, err := parsePEMPublic(pemKey)
		d, _ := hexDecode(in.Spec.Data.Hash.Value)
		if err != nil || !ecdsa.VerifyASN1(pub, d, sig) {
			http.Error(w, "verifying signature", http.StatusBadRequest)
			return
		}
		body := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(
			`{"apiVersion":"0.0.1","kind":"hashedrekord","spec":{"data":{"hash":{"algorithm":"sha256","value":"%s"}}}}`,
			in.Spec.Data.Hash.Value)))
		canon := fmt.Sprintf(`{"body":"%s","integratedTime":1792800000,"logID":"c0ffee","logIndex":42}`, body)
		h := sha256.Sum256([]byte(canon))
		set, _ := ecdsa.SignASN1(rand.Reader, logKey, h[:])
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"abc":{"body":"%s","integratedTime":1792800000,"logID":"c0ffee","logIndex":42,
			"verification":{"signedEntryTimestamp":"%s"}}}`, body, base64.StdEncoding.EncodeToString(set))
	}))
	defer srv.Close()
	dir := t.TempDir()
	l := openLog(t, filepath.Join(dir, "audit.jsonl"), 3)
	defer l.Close()
	k, err := RekorKey(filepath.Join(dir, "anchor"))
	if err != nil {
		t.Fatal(err)
	}
	if k2, _ := RekorKey(filepath.Join(dir, "anchor")); !k2.Equal(k) {
		t.Fatal("the key was not kept")
	}
	a := &Anchor{Dir: filepath.Join(dir, "anchor"), Log: l, Witnesses: []Witness{Rekor{URL: srv.URL, Key: k, LogKey: &logKey.PublicKey}}}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	c, err := a.Checkpoint(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	ws := a.Checkpoints()[0].Witnesses
	if len(ws) != 1 || !ws[0].OK || !strings.Contains(ws[0].Note, "entry 42") || !strings.HasSuffix(ws[0].File, ".json") {
		t.Fatalf("%+v", ws)
	}
	kept, _ := a.File(ws[0].File)
	d := c.Digest()
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := ParseRekor(kept, d[:], &other.PublicKey); err == nil {
		t.Error("a timestamp checked with another log's key")
	}
	if _, err := ParseRekor(kept, make([]byte, 32), nil); err == nil {
		t.Error("an entry over another digest was taken")
	}
}
