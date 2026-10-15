package feed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
)

func TestWebhook(t *testing.T) {
	dir := t.TempDir()
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Append("pas", "case.opened", "old", map[string]string{"x": "before the webhook"})
	secret := []byte("0123456789abcdef0123")
	var mu sync.Mutex
	var got []Event
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := Verify(secret, r.Header.Get("Vaglio-Signature"), body, time.Now(), 5*time.Minute); err != nil {
			t.Errorf("signature: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if fail {
			fail = false
			w.WriteHeader(503)
			return
		}
		var b struct{ Events []Event }
		json.Unmarshal(body, &b)
		got = append(got, b.Events...)
	}))
	defer srv.Close()
	cursor := filepath.Join(dir, "webhook.cursor")
	w := &Webhook{URL: srv.URL, Secret: secret, Cursor: cursor, Log: l}
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	l.Append("pas", "case.opened", "c1", nil)
	l.Append("user:kim", "case.viewed", "c1", nil)
	l.Append("system", "user.created", "", nil)
	l.Append("user:kim", "case.reviewed", "c1", map[string]string{"decision": "approve"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 2 || got[0].Type != "case.opened" || got[0].Case != "c1" || got[1].Type != "case.reviewed" || got[1].Hash == "" {
		t.Fatalf("got %+v", got)
	}
	mu.Unlock()
	if s := w.Status(); s.Delivered != 5 || s.Behind != 0 {
		t.Errorf("status %+v", s)
	}
	cancel()
	<-w.Done()
	// A restart resumes at the cursor: nothing is sent twice.
	w2 := &Webhook{URL: srv.URL, Secret: secret, Cursor: cursor, Log: l}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer func() { cancel2(); <-w2.Done() }()
	w2.Start(ctx2)
	if n, err := w2.Once(context.Background()); n != 0 || err != nil {
		t.Errorf("after restart: %d %v", n, err)
	}
	if ev := Events(l, 0, 0); len(ev) != 3 {
		t.Errorf("export: %d", len(ev))
	}
}

func TestChecks(t *testing.T) {
	for _, u := range []string{"http://example.com/hook", "ftp://x", "nonsense"} {
		if Check(u) == nil {
			t.Errorf("%s accepted", u)
		}
	}
	for _, u := range []string{"https://example.com/hook", "http://127.0.0.1:9/x"} {
		if err := Check(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	secret := []byte("0123456789abcdef")
	now := time.Now()
	h := Sign(secret, now.Add(-10*time.Minute), []byte("b"))
	if Verify(secret, h, []byte("b"), now, 5*time.Minute) == nil {
		t.Error("an old signature passed")
	}
	if Verify(secret, Sign(secret, now, []byte("b")), []byte("c"), now, time.Minute) == nil {
		t.Error("a changed body passed")
	}
}
