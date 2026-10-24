package anchor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// OpenTimestamps sends the checkpoint's digest to an OpenTimestamps calendar, which commits it, with others, to the
// Bitcoin blockchain within hours. What the calendar answers is kept as a standard .ots file: the OpenTimestamps
// client upgrades it once the block is mined (ots upgrade) and verifies it (ots verify). Only the 32-byte digest leaves.
type OpenTimestamps struct {
	Calendar string // https://a.pool.opentimestamps.org
	Client   *http.Client
}

// Name is the calendar's host.
func (o OpenTimestamps) Name() string {
	return "opentimestamps " + strings.TrimPrefix(strings.TrimPrefix(o.Calendar, "https://"), "http://")
}

// otsMagic starts every .ots file, then the major version (1).
var otsMagic = []byte("\x00OpenTimestamps\x00\x00Proof\x00\xbf\x89\xe2\xe8\x84\xe8\x92\x94\x01")

// Witness submits the digest.
func (o OpenTimestamps) Witness(ctx context.Context, c Checkpoint) (WitnessProof, error) {
	p := WitnessProof{Kind: "opentimestamps"}
	d := c.Digest()
	ans, err := post(ctx, o.Client, strings.TrimRight(o.Calendar, "/")+"/digest", "application/octet-stream",
		"application/vnd.opentimestamps.v1", d[:], nil)
	if err != nil {
		return p, err
	}
	if len(ans) == 0 {
		return p, errors.New("the calendar answered nothing")
	}
	// A detached timestamp: magic and version, the hash operation (SHA-256, 0x08), the digest, then the calendar's
	// timestamp of it.
	file := append(append(append([]byte{}, otsMagic...), 0x08), d[:]...)
	p.Data, p.OK = append(file, ans...), true
	p.Note = "pending: committed by the calendar; in Bitcoin within hours (upgrade and verify with the OpenTimestamps client)"
	return p, nil
}

// TSA asks an RFC 3161 time-stamping authority to sign the checkpoint's digest and the time. The token it returns is
// kept as a .tsr file, verified with the authority's certificate (openssl ts -verify). Only the digest leaves.
type TSA struct {
	URL    string
	Client *http.Client
}

// Name is the authority's address.
func (t TSA) Name() string { return "rfc3161 " + strings.TrimPrefix(t.URL, "https://") }

var (
	oidSHA256     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
)

type algorithm struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type messageImprint struct {
	HashAlgorithm algorithm
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	Nonce          *big.Int `asn1:"optional"`
	CertReq        bool     `asn1:"optional"`
}

type pkiStatusInfo struct {
	Status       int
	StatusString []asn1.RawValue `asn1:"optional"`
	FailInfo     asn1.BitString  `asn1:"optional"`
}

type timeStampResp struct {
	Status pkiStatusInfo
	Token  asn1.RawValue `asn1:"optional"`
}

// Witness asks for a token over the digest.
func (t TSA) Witness(ctx context.Context, c Checkpoint) (WitnessProof, error) {
	p := WitnessProof{Kind: "rfc3161"}
	d := c.Digest()
	nonce, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	req, err := asn1.Marshal(timeStampReq{Version: 1, MessageImprint: messageImprint{
		HashAlgorithm: algorithm{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}, HashedMessage: d[:]},
		Nonce: nonce, CertReq: true})
	if err != nil {
		return p, err
	}
	ans, err := post(ctx, t.Client, t.URL, "application/timestamp-query", "application/timestamp-reply", req, nil)
	if err != nil {
		return p, err
	}
	gen, err := ParseTimeStamp(ans, d[:])
	if err != nil {
		return p, err
	}
	p.Data, p.OK, p.Time = ans, true, gen.UTC().Format(time.RFC3339)
	p.Note = "time-stamped by the authority at " + p.Time + " (verify the token with its certificate)"
	return p, nil
}

// ParseTimeStamp reads a time-stamp response: granted, over this digest, and when. It does not check the authority's
// signature, which needs its certificate (openssl ts -verify does).
func ParseTimeStamp(resp, digest []byte) (time.Time, error) {
	var r timeStampResp
	if _, err := asn1.Unmarshal(resp, &r); err != nil {
		return time.Time{}, fmt.Errorf("not a time-stamp response: %w", err)
	}
	if r.Status.Status > 1 {
		return time.Time{}, fmt.Errorf("the authority refused it (status %d)", r.Status.Status)
	}
	var ci struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(r.Token.FullBytes, &ci); err != nil || !ci.ContentType.Equal(oidSignedData) {
		return time.Time{}, errors.New("the token is not CMS signed data")
	}
	var sd struct {
		Version          int
		DigestAlgorithms asn1.RawValue
		Encap            struct {
			Type    asn1.ObjectIdentifier
			Content []byte `asn1:"explicit,tag:0"`
		}
		Rest asn1.RawValue `asn1:"optional"`
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		// Certificates and signer infos follow; only what comes before them is read here.
		var lenient struct {
			Version          int
			DigestAlgorithms asn1.RawValue
			Encap            asn1.RawValue
		}
		if _, err2 := asn1.Unmarshal(ci.Content.Bytes, &lenient); err2 != nil {
			return time.Time{}, fmt.Errorf("the token's signed data: %w", err)
		}
		if _, err := asn1.Unmarshal(lenient.Encap.FullBytes, &sd.Encap); err != nil {
			return time.Time{}, fmt.Errorf("the token's content: %w", err)
		}
	}
	if !sd.Encap.Type.Equal(oidTSTInfo) {
		return time.Time{}, errors.New("the token does not hold time-stamp information")
	}
	var info struct {
		Version        int
		Policy         asn1.ObjectIdentifier
		MessageImprint messageImprint
		Serial         *big.Int
		GenTime        time.Time `asn1:"generalized"`
	}
	if _, err := asn1.Unmarshal(sd.Encap.Content, &info); err != nil {
		return time.Time{}, fmt.Errorf("the time-stamp information: %w", err)
	}
	if !info.MessageImprint.HashAlgorithm.Algorithm.Equal(oidSHA256) || !bytes.Equal(info.MessageImprint.HashedMessage, digest) {
		return time.Time{}, errors.New("the token is over another digest than the checkpoint's")
	}
	return info.GenTime, nil
}

// HTTPWitness posts the checkpoint itself (signed, hashes only) to an endpoint a regulator or partner runs, which keeps
// it; its answer is kept as its proof.
type HTTPWitness struct {
	Label  string
	URL    string
	Token  string
	Client *http.Client
}

// Name is the label given.
func (h HTTPWitness) Name() string { return h.Label }

// Witness posts the checkpoint.
func (h HTTPWitness) Witness(ctx context.Context, c Checkpoint) (WitnessProof, error) {
	p := WitnessProof{Kind: "http"}
	body, _ := json.Marshal(c)
	hdr := http.Header{}
	if h.Token != "" {
		hdr.Set("Authorization", "Bearer "+h.Token)
	}
	ans, err := post(ctx, h.Client, h.URL, "application/json", "application/json", body, hdr)
	if err != nil {
		return p, err
	}
	p.Data, p.OK, p.Note = ans, true, "received by the endpoint"
	return p, nil
}

func post(ctx context.Context, c *http.Client, url, ctype, accept string, body []byte, hdr http.Header) ([]byte, error) {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "vaglio-anchor")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return raw, nil
}
