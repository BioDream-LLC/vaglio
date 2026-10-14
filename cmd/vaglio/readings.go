package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/readings"
)

// readingsCmd compares two readings of one coverage document and prints where they decide the same record differently.
func readingsCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("readings", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the comparison as JSON")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("usage: vaglio readings [-json] A.vaglio B.vaglio   (two policies drafted from one document)")
	}
	a, err := policy.LoadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	b, err := policy.LoadFile(fs.Arg(1))
	if err != nil {
		return err
	}
	c, err := readings.Compare(a, b)
	if err != nil {
		return err
	}
	if *asJSON {
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(c)
	}
	fmt.Fprintf(w, "A %s, B %s: %d pieces of evidence quote the same clause.\n", fs.Arg(0), fs.Arg(1), len(c.Matched))
	for _, it := range c.OnlyA {
		fmt.Fprintf(w, "  only A decides on: %s\n", it.Text)
	}
	for _, it := range c.OnlyB {
		fmt.Fprintf(w, "  only B decides on: %s\n", it.Text)
	}
	if c.Differ == 0 {
		fmt.Fprintf(w, "They agree on all %d records.\n", c.Records)
		return nil
	}
	fmt.Fprintf(w, "They disagree on %d of %d records (%.1f%%):\n", c.Differ, c.Records, 100*float64(c.Differ)/float64(c.Records))
	for _, g := range c.Groups {
		fmt.Fprintf(w, "\n  %d records: A %s, B %s\n", g.Records, g.A, g.B)
		if g.ADecides != "" {
			fmt.Fprintf(w, "    A rests on  %q\n", g.ADecides)
		}
		if g.BDecides != "" {
			fmt.Fprintf(w, "    B rests on  %q\n", g.BDecides)
		}
		ex := "nothing (every piece of evidence documented absent)"
		if len(g.Example) > 0 {
			ex = strings.Join(g.Example, "; ")
		}
		fmt.Fprintf(w, "    for example, documented present: %s\n", ex)
	}
	return nil
}
