package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/seal"
)

// dataCmd: vaglio data seal|status -data DIR. Seal encrypts, under VAGLIO_DATA_KEY, what was written before the key was
// set: the audit log's plain records, the request Bundles and documents, the fax inbox. Run it with the server stopped.
// The log is rewritten to a new file and checked, record for record and hash for hash, before it replaces the old.
func dataCmd(args []string, w io.Writer) error {
	if len(args) == 0 || (args[0] != "seal" && args[0] != "status") {
		return errors.New("usage: vaglio data seal|status -data DIR (with VAGLIO_DATA_KEY set for seal)")
	}
	fs := flag.NewFlagSet("data", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	fs.Parse(args[1:])
	if args[0] == "seal" && !seal.On() {
		return errors.New("set " + seal.EnvKey + " to the data key (openssl rand -base64 32), kept outside the data directory")
	}
	logPath := filepath.Join(*dir, "audit.jsonl")
	plainLines, sealedLines, err := countLines(logPath)
	if err != nil {
		return err
	}
	var plainFiles, sealedFiles int
	var toSeal []string
	for _, pattern := range []string{"bundles/*.json", "faxes/*"} {
		files, _ := filepath.Glob(filepath.Join(*dir, pattern))
		for _, f := range files {
			if info, err := os.Stat(f); err != nil || info.IsDir() || strings.HasSuffix(f, ".seen") || strings.HasSuffix(f, ".new") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if seal.Sealed(raw) {
				sealedFiles++
			} else {
				plainFiles++
				toSeal = append(toSeal, f)
			}
		}
	}
	packSealed, packPlain, err := cases.PackEntries(filepath.Join(*dir, "bundles"))
	if err != nil {
		return err
	}
	if args[0] == "status" {
		fmt.Fprintf(w, "audit log: %d records sealed, %d plain\nfiles: %d sealed, %d plain\n"+
			"request Bundles in packs: %d sealed, %d plain\nkey set: %v\n",
			sealedLines, plainLines, sealedFiles, plainFiles, packSealed, packPlain, seal.On())
		return nil
	}
	for _, f := range toSeal {
		purpose := "bundle"
		if filepath.Base(filepath.Dir(f)) == "faxes" {
			purpose = "fax"
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		tmp := f + ".sealing"
		if err := seal.WriteFile(tmp, purpose, raw, 0o600); err != nil {
			return err
		}
		if back, err := seal.ReadFile(tmp, purpose); err != nil || !bytes.Equal(back, raw) {
			os.Remove(tmp)
			return fmt.Errorf("%s: sealed copy does not read back; nothing changed", f)
		}
		if err := fsx.Replace(tmp, f); err != nil {
			return err
		}
	}
	packs, err := cases.SealPacks(filepath.Join(*dir, "bundles"))
	if err != nil {
		return err
	}
	if plainLines > 0 {
		if err := sealLog(logPath); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "sealed %d audit records, %d files and %d request Bundles in packs\n", plainLines, len(toSeal), packs)
	return nil
}

func countLines(path string) (plain, sealed int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		if seal.SealedLine(sc.Bytes()) {
			sealed++
		} else if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			plain++
		}
	}
	return plain, sealed, sc.Err()
}

// sealLog rewrites the log with every line sealed, then checks the new file has the same records and head.
func sealLog(path string) error {
	n, head, err := audit.Verify(path)
	if err != nil {
		return fmt.Errorf("the log does not verify, so it is not rewritten: %w", err)
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close() // and closed before the rename below: Windows will not replace a file that is open
	tmp := path + ".sealing"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	bw := bufio.NewWriter(out)
	for sc.Scan() {
		line := sc.Bytes()
		if !seal.SealedLine(line) {
			line = seal.Line(line)
		}
		bw.Write(line)
		bw.WriteByte('\n')
	}
	if err := errors.Join(sc.Err(), bw.Flush(), out.Sync(), out.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	n2, head2, err := audit.Verify(tmp)
	if err != nil || n2 != n || head2 != head {
		os.Remove(tmp)
		return fmt.Errorf("the sealed log does not match the plain one (%d %s / %d %s, %v); nothing changed", n2, head2, n, head, err)
	}
	in.Close()
	if err := fsx.Replace(tmp, path); err != nil {
		return err
	}
	return fsx.SyncDir(filepath.Dir(path))
}
