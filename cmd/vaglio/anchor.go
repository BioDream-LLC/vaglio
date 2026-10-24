package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/biodream-llc/vaglio/internal/anchor"
)

// anchorCmd checks the log's anchors offline, with nothing but files someone was given: a receipt's answer (from
// /api/receipts/CODE: its record is in a signed checkpoint) or a consistency proof (from /api/anchor/consistency: a
// later checkpoint extends an earlier one). -key pins the plan's published checkpoint key.
func anchorCmd(args []string, w io.Writer) error {
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: vaglio anchor verify [-key BASE64] FILE.json...")
	}
	fs := flag.NewFlagSet("anchor verify", flag.ContinueOnError)
	keyText := fs.String("key", "", "the plan's published checkpoint key (base64); without it, the key each checkpoint carries")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var key ed25519.PublicKey
	if *keyText != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*keyText))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return errors.New("-key is an Ed25519 public key in base64")
		}
		key = raw
	}
	if fs.NArg() == 0 {
		return errors.New("name the files to check")
	}
	bad := 0
	for _, f := range fs.Args() {
		msg, err := verifyAnchorFile(f, key)
		if err != nil {
			bad++
			fmt.Fprintf(w, "FAILS  %s: %v\n", f, err)
			continue
		}
		fmt.Fprintf(w, "OK     %s: %s\n", f, msg)
	}
	if key == nil {
		fmt.Fprintln(w, "(checked with the key each checkpoint carries: give -key, the plan's published key, to tie them to the plan)")
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d do not verify", bad, fs.NArg())
	}
	return nil
}

func verifyAnchorFile(file string, key ed25519.PublicKey) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var probe struct {
		Anchor *anchor.ReceiptProof `json:"anchor"`
		From   *anchor.Checkpoint   `json:"from"`
		Seq    int64                `json:"seq"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("not JSON: %w", err)
	}
	switch {
	case probe.From != nil:
		var p anchor.ConsistencyProof
		json.Unmarshal(raw, &p)
		if err := anchor.VerifyConsistency(p, key); err != nil {
			return "", err
		}
		return fmt.Sprintf("the checkpoint of %s (%d records) extends the one of %s (%d records): none of the first %d "+
			"changed between them", p.To.Time, p.To.Size, p.From.Time, p.From.Size, p.From.Size), nil
	case probe.Anchor != nil || probe.Seq > 0:
		p := probe.Anchor
		if p == nil {
			var bare anchor.ReceiptProof
			json.Unmarshal(raw, &bare)
			p = &bare
		}
		if p.Pending {
			return "", errors.New("no checkpoint covered this record when the answer was given: ask again after the next one")
		}
		if err := anchor.VerifyReceipt(*p, key); err != nil {
			return "", err
		}
		var ws []string
		for _, x := range p.Witnesses {
			ws = append(ws, fmt.Sprintf("%s (%s, %s)", x.Witness, map[bool]string{true: "answered", false: "failed"}[x.OK], x.Time))
		}
		msg := fmt.Sprintf("record %d is in the checkpoint of %s (%d records, root %.16s…), signed by %s", p.Seq,
			p.Checkpoint.Time, p.Checkpoint.Size, p.Checkpoint.Root, p.Checkpoint.Name)
		if len(ws) > 0 {
			msg += "; witnessed by " + strings.Join(ws, ", ") + " (check their proofs with their own tools)"
		}
		return msg, nil
	}
	return "", errors.New("neither a receipt's answer nor a consistency proof")
}
