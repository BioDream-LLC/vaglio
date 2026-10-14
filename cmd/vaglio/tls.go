package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/tlsserve"
)

// tlsCmd shows Vaglio's own certificate authority (making it with init), and how to trust it on each kind of computer.
func tlsCmd(args []string, w io.Writer) error {
	if len(args) == 0 || (args[0] != "show" && args[0] != "init") {
		return errors.New("usage: vaglio tls show|init -data DIR [-addr ADDR] [-hostname NAME]")
	}
	fs := flag.NewFlagSet("tls", flag.ExitOnError)
	data := fs.String("data", "data", "the server's data directory")
	addr := fs.String("addr", "127.0.0.1:8740", "init: the address the server will listen on")
	hostname := fs.String("hostname", "", "init: the name browsers will reach the server by")
	fs.Parse(args[1:])
	dir := filepath.Join(*data, "tls")
	var a *tlsserve.Authority
	var err error
	if args[0] == "init" {
		names, err := tlsserve.NamesFor(*addr, *hostname)
		if err != nil {
			return err
		}
		if a, err = tlsserve.Open(dir, names); err != nil {
			return err
		}
		if _, err := a.EnsureServer(names); err != nil {
			return err
		}
	} else if a, err = tlsserve.Load(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no authority in %s yet: start vaglio serve once, or run vaglio tls init", dir)
		}
		return err
	}
	describeAuthority(w, a, *data)
	return nil
}

// describeAuthority says what the authority is, what it may vouch for, and how a reviewer's computer comes to trust it.
func describeAuthority(w io.Writer, a *tlsserve.Authority, data string) {
	ca := filepath.Join(data, "tls", tlsserve.CAFile)
	var names []string
	names = append(names, a.Cert.PermittedDNSDomains...)
	for _, r := range a.Cert.PermittedIPRanges {
		names = append(names, r.IP.String())
	}
	switch {
	case a.Replaced != "":
		fmt.Fprintf(w, "  The server's names changed, so Vaglio made a new certificate authority (the old one is kept as %s).\n"+
			"  Computers that trusted the old one must trust this one instead.\n", a.Replaced)
	case a.Created:
		fmt.Fprintf(w, "  Vaglio made its own certificate authority for this server's https.\n")
	}
	fmt.Fprintf(w, "  Certificate authority: %s\n", ca)
	fmt.Fprintf(w, "    SHA-256:  %s\n", a.Fingerprint())
	fmt.Fprintf(w, "    vouches only for: %s (until %s)\n", strings.Join(names, ", "), a.Cert.NotAfter.Format(time.DateOnly))
	fmt.Fprintf(w, "  Browsers warn until a computer trusts it once (compare the SHA-256 first). Or give -tls-cert and\n"+
		"  -tls-key from the plan's own authority and nobody has to.\n"+
		"    macOS:    security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db %[1]s\n"+
		"    Windows:  certutil -user -addstore Root %[1]s   (or IT, by group policy, for every computer)\n"+
		"    Linux:    copy it to /usr/local/share/ca-certificates/vaglio.crt, then: sudo update-ca-certificates\n"+
		"    Firefox:  Settings > Privacy & Security > Certificates > View Certificates > Authorities > Import\n", ca)
}
