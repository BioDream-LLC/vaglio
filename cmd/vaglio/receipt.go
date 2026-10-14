package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/receipt"
)

// receiptCmd checks a receipt against a copy of the audit log, with no server: what a regulator given the log runs.
func receiptCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("receipt", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	file := fs.String("log", "", "the audit log file itself, instead of -data (a copy someone was given)")
	asJSON := fs.Bool("json", false, "print the check as JSON")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: vaglio receipt [-data DIR | -log FILE] [-json] VG-XXXX-XXXX-XXXX-XXXX")
	}
	path := *file
	if path == "" {
		path = filepath.Join(*dir, "audit.jsonl")
	}
	records, err := audit.ReadFile(path)
	if err != nil {
		return err
	}
	c, err := receipt.Find(records, fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(c)
	}
	fmt.Fprintf(w, "%s is genuine: record %d of %d in a log whose chain verifies from its first record.\n",
		c.Receipt, c.Record.Seq, c.Chain.Records)
	fmt.Fprintf(w, "  decision  %s, by the %s, %s\n", c.Decision, c.By, c.Time)
	fmt.Fprintf(w, "  policy    %s v%s (%s)\n", c.PolicyTitle, c.PolicyVersion, c.Policy)
	if c.Clause != "" {
		fmt.Fprintf(w, "  clause    %q\n", c.Clause)
	}
	for _, l := range c.Later {
		fmt.Fprintf(w, "  later     %s by the %s, %s (receipt %s)\n", l.Decision, l.By, l.Time, l.Receipt)
	}
	fmt.Fprintf(w, "  record    %s\n  head      %s\n", c.Record.Hash, c.Chain.Head)
	return nil
}
