package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/biodream-llc/vaglio/internal/cmswatch"
)

// sourcesCmd compares the Medicare documents the library's policies were drafted from with their current versions in
// the CMS Coverage API (two public reports; nothing else is sent), and exits 1 when any has changed.
func sourcesCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("sources", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cur, err := cmswatch.Fetch(ctx, nil)
	if err != nil {
		return err
	}
	found, err := cmswatch.Compare(cur)
	if err != nil {
		return err
	}
	changed := 0
	for _, f := range found {
		mark := "same   "
		switch {
		case f.Missing:
			mark = "MISSING"
		case f.Changed:
			mark = "CHANGED"
		case f.Unknown:
			mark = "unknown"
		}
		if f.Changed || f.Missing {
			changed++
		}
		fmt.Fprintf(w, "%s  %-18s %s\n", mark, f.Policy, f.Note)
	}
	if changed > 0 {
		return fmt.Errorf("%d library policies' sources have changed: review them", changed)
	}
	return nil
}
