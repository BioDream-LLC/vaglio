package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/server"
)

func TestStandby(t *testing.T) {
	pdir := t.TempDir()
	os.Chmod(pdir, 0o700)
	st, err := openData(pdir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.log.Close()
	st.users.Set("kim", auth.Reviewer)
	h := server.New(server.Config{Sessions: auth.NewSessions(st.users), Audit: st.log, AuditPath: st.auditPath, Cases: st.cases,
		ReplicaToken: "replica-token-0123", BundleDir: filepath.Join(pdir, "bundles"), UsersPath: filepath.Join(pdir, "users.json")})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	c, _ := st.cases.Open(cases.Opening{Policy: "p", PolicyVersion: "1", Item: "1", Claim: "x|1", Answer: cases.Answer{Decision: "pend"}},
		[]byte(`{"resourceType":"Bundle","id":"r1"}`))

	sdir := t.TempDir()
	os.Chmod(sdir, 0o700)
	bad, _ := openStandby(srv.URL, "wrong-token-000000", filepath.Join(sdir, "x"), srv.Client())
	if _, _, err := bad.Once(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a wrong token: %v", err)
	}
	bad.log.Close()
	sb, err := openStandby(srv.URL, "replica-token-0123", sdir, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got, behind, err := sb.Once(context.Background()); err != nil || got != 1 || behind != 0 {
		t.Fatalf("%d %d %v", got, behind, err)
	}
	st.cases.Review(c.ID, "kim", cases.Review{Decision: "approve", Reason: "fine"})
	if got, _, err := sb.Once(context.Background()); err != nil || got != 1 {
		t.Fatalf("next: %d %v", got, err)
	}
	sb.log.Close()
	// Taking over: the standby's directory opens as the primary's did, with the case, its request and the accounts.
	n1, h1 := st.log.Head()
	n2, h2, err := audit.Verify(filepath.Join(sdir, "audit.jsonl"))
	if err != nil || n1 != n2 || h1 != h2 {
		t.Fatalf("%d %s / %d %s %v", n1, h1, n2, h2, err)
	}
	copy, err := openData(sdir)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.log.Close()
	got, ok := copy.cases.Get(c.ID)
	if raw, err := copy.cases.Bundle(got); !ok || err != nil || !strings.Contains(string(raw), `"r1"`) || got.Status != cases.Reviewed {
		t.Errorf("%+v %v", got, err)
	}
	if _, ok, _ := copy.users.Get("kim"); !ok {
		t.Error("accounts not copied")
	}

	// A copy that has diverged from its primary is refused, not overwritten.
	other := t.TempDir()
	l, _ := audit.Open(filepath.Join(other, "audit.jsonl"))
	l.Append("system", "something.else", "", nil)
	l.Close()
	os.MkdirAll(filepath.Join(other, "bundles"), 0o700)
	sb2, _ := openStandby(srv.URL, "replica-token-0123", other, srv.Client())
	defer sb2.log.Close()
	if _, _, err := sb2.Once(context.Background()); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("a diverged copy: %v", err)
	}
}
