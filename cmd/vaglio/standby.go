package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/seal"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
)

// standbyCmd keeps a copy of a primary's data directory, ready to take over: it copies the audit log record by record,
// each checked against the chain, with the request Bundles each names fetched (and checked by their hash) before the
// record that names them, and the accounts file. It never writes anything of its own. To take over, stop it and run
// vaglio serve -data on the same directory.
func standbyCmd(args []string) error {
	fs := flag.NewFlagSet("standby", flag.ExitOnError)
	from := fs.String("from", "", "the primary's address, https://HOST:PORT")
	tokenEnv := fs.String("token-env", "", "name of an environment variable holding the primary's replica token")
	dir := fs.String("data", "data", "this standby's data directory")
	ca := fs.String("ca", "", "the certificate authority that issued the primary's certificate (its DATA/tls/ca.crt)")
	every := fs.Duration("every", 2*time.Second, "how often to ask the primary for new records, and for its accounts")
	follow := fs.Bool("follow", true, "hold each request open until the primary has new records, so every decision is "+
		"copied at once (needed for serve -standby-sync); false: ask every -every")
	fs.Parse(args)
	if *from == "" || *tokenEnv == "" {
		return errors.New("usage: vaglio standby -from https://PRIMARY -token-env VAR -data DIR [-ca FILE]")
	}
	token, err := secretFrom(*tokenEnv)
	if err != nil || token == "" {
		return fmt.Errorf("-token-env: %v", err)
	}
	hc, err := tlsserve.Client(*ca, 60*time.Second)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sb, err := openStandby(*from, token, *dir, hc)
	if err != nil {
		return err
	}
	defer sb.log.Close()
	n, _ := sb.log.Head()
	log.Printf("standby of %s in %s: %d records here", *from, *dir, n)
	sb.follow, sb.usersEvery = *follow, *every
	copied, said := 0, time.Now()
	for {
		began := time.Now()
		got, behind, err := sb.Once(ctx)
		copied += got
		switch {
		case err != nil:
			log.Printf("standby: %v", err)
		case copied > 0 && (behind > 0 || time.Since(said) > 10*time.Second):
			log.Printf("standby: copied %d records, %d behind", copied, behind)
			copied, said = 0, time.Now()
		}
		wait := *every
		switch {
		case err == nil && behind > 0:
			wait = 0 // behind: the next page at once
		case err == nil && *follow:
			// The primary held the request until it had something; one that answers at once with nothing does not
			// hold requests (an older version), and is asked again a little later rather than in a tight loop.
			wait = 0
			if got == 0 && time.Since(began) < 50*time.Millisecond {
				wait = 50 * time.Millisecond
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

var bundleRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

type standby struct {
	from, token, dir string
	hc               *http.Client
	log              *audit.Log
	usersSeen        string
	follow           bool
	usersEvery       time.Duration
	usersAt          time.Time
}

func openStandby(from, token, dir string, hc *http.Client) (*standby, error) {
	if !strings.HasPrefix(from, "https://") {
		return nil, errors.New("-from is the primary's https address")
	}
	if err := os.MkdirAll(filepath.Join(dir, "bundles"), 0o700); err != nil {
		return nil, err
	}
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		return nil, err
	}
	return &standby{from: strings.TrimRight(from, "/"), token: token, dir: dir, hc: hc, log: l}, nil
}

func (s *standby) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.from+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// bundlesOf are the request Bundles a record names.
func bundlesOf(r audit.Record) []string {
	if r.Action != "case.opened" && r.Action != "case.received" {
		return nil
	}
	var d struct {
		Bundle string `json:"bundle"`
	}
	if json.Unmarshal(r.Data, &d) != nil || d.Bundle == "" {
		return nil
	}
	return []string{d.Bundle}
}

// Once copies what the primary has past this copy's head: it returns how many records it copied and how many remain.
func (s *standby) Once(ctx context.Context) (int, int, error) {
	if time.Since(s.usersAt) >= s.usersEvery {
		if err := s.copyUsers(ctx); err != nil {
			return 0, 0, err
		}
		s.usersAt = time.Now()
	}
	n, _ := s.log.Head()
	q := fmt.Sprintf("/api/replica/records?after=%d", n)
	if s.follow {
		q += "&wait=1"
	}
	resp, err := s.get(ctx, q)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var head int
	fmt.Sscan(resp.Header.Get("Vaglio-Head-Seq"), &head)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	// A page of records: their request Bundles first (a record never arrives before what it names), then the records,
	// checked and written together with one sync.
	var page []audit.Record
	for sc.Scan() {
		var rec audit.Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return 0, head - n, fmt.Errorf("an unreadable record: %w", err)
		}
		for _, b := range bundlesOf(rec) {
			if err := s.copyBundle(ctx, b); err != nil {
				return 0, head - n, err
			}
		}
		page = append(page, rec)
	}
	if err := sc.Err(); err != nil {
		return 0, head - n, err
	}
	if err := s.log.CopyAll(page); err != nil {
		return 0, head - n, fmt.Errorf("%w (the primary's log and this copy disagree: stop and look)", err)
	}
	return len(page), max(0, head-n-len(page)), nil
}

func (s *standby) copyBundle(ctx context.Context, name string) error {
	if !bundleRe.MatchString(name) {
		return fmt.Errorf("a record names bundle %q", name)
	}
	path := filepath.Join(s.dir, "bundles", name+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	resp, err := s.get(ctx, "/api/replica/bundles/"+name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != name {
		return fmt.Errorf("bundle %s does not match its hash", name)
	}
	tmp := path + ".new"
	if err := seal.WriteFileSync(tmp, "bundle", raw, 0o600); err != nil {
		return err
	}
	return fsx.Replace(tmp, path)
}

func (s *standby) copyUsers(ctx context.Context) error {
	resp, err := s.get(ctx, "/api/replica/users")
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || !json.Valid(raw) {
		return errors.New("the accounts file did not arrive whole")
	}
	sum := sha256.Sum256(raw)
	if h := hex.EncodeToString(sum[:]); h != s.usersSeen {
		path := filepath.Join(s.dir, "users.json")
		if err := os.WriteFile(path+".new", raw, 0o600); err != nil {
			return err
		}
		if err := fsx.Replace(path+".new", path); err != nil {
			return err
		}
		s.usersSeen = h
	}
	return nil
}
