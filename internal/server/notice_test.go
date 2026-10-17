package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/vaglio/internal/notice"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

// withAddress gives the request's Patient a name and a mailing address.
func withAddress(b map[string]any) map[string]any {
	p := pastest.Map(pastest.Entries(b)[1])
	p["name"] = []any{map[string]any{"given": []any{"Ana"}, "family": "Reyes"}}
	p["address"] = []any{map[string]any{"use": "home", "line": []any{"12 Elm St"}, "city": "Springfield", "state": "IL", "postalCode": "62702"}}
	return b
}

var planAddress = notice.Address{Name: "Example Health Plan", Lines: []string{"PO Box 1000"}, City: "Springfield", State: "IL", Zip: "62701"}

// Notices into a folder: a denial is mailed once, with its receipt, even when the cursor is lost; an approval is not
// (not asked for); a request with no address is recorded as not mailed.
func TestNoticesToAFolder(t *testing.T) {
	e := newEnv(t, "")
	dir := t.TempDir()
	m := &notice.Mailer{Log: e.log, Cases: e.cases, Sender: &notice.Folder{Dir: filepath.Join(dir, "out")}, From: planAddress,
		Events: []string{notice.Denied, notice.Extended}, Cursor: filepath.Join(dir, "cursor"),
		Receipt: func(id string) string { return ReceiptOf(e.cases, id) }}
	if err := m.Load(); err != nil { // starts at the head: nothing before is mailed
		t.Fatal(err)
	}
	d := submit(t, e, withAddress(request("", "", "", denies...)))
	submit(t, e, withAddress(request("", "", "", approves...)))
	none := submit(t, e, request("", "", "", denies...)) // no address
	if _, err := m.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.cases.Get(d.Case)
	if len(c.Notices) != 1 || c.Notices[0].Error != "" || c.Notices[0].Receipt != d.Receipt || c.Notices[0].Event != "denied" {
		t.Fatalf("%+v", c.Notices)
	}
	pdf, err := os.ReadFile(filepath.Join(dir, "out", c.Notices[0].ID))
	if err != nil || !strings.HasPrefix(string(pdf), "%PDF") {
		t.Fatalf("%v", err)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "out", "manifest.csv"))
	if !strings.Contains(string(manifest), "Ana Reyes,12 Elm St,Springfield,IL,62702") {
		t.Errorf("manifest:\n%s", manifest)
	}
	if n, _ := e.cases.Get(none.Case); len(n.Notices) != 1 || !strings.HasPrefix(n.Notices[0].Error, "not mailed") {
		t.Errorf("%+v", n.Notices)
	}
	// The cursor lost: the records are read again, and nothing is mailed twice.
	os.WriteFile(filepath.Join(dir, "cursor"), []byte("0\n"), 0o600)
	m2 := &notice.Mailer{Log: e.log, Cases: e.cases, Sender: &notice.Folder{Dir: filepath.Join(dir, "out")}, From: planAddress,
		Events: []string{notice.Denied}, Cursor: filepath.Join(dir, "cursor")}
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	m2.Once(context.Background())
	if c, _ := e.cases.Get(d.Case); len(c.Notices) != 1 {
		t.Errorf("mailed twice: %+v", c.Notices)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "out", "*.pdf"))
	if len(files) != 1 {
		t.Errorf("%d letters written", len(files))
	}
}

// Through a stand-in of Lob's API: the key, the idempotency key, the addresses and the PDF arrive as Lob's OpenAPI
// description says; Lob's own failure is tried again from the same record, a refusal is recorded and not.
func TestNoticesThroughLob(t *testing.T) {
	var mu sync.Mutex
	status, got := http.StatusOK, []*http.Request{}
	forms := []map[string]string{}
	lob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if user, pass, ok := r.BasicAuth(); !ok || user != "test_key" || pass != "" || r.URL.Path != "/v1/letters" {
			w.WriteHeader(401)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			w.WriteHeader(400)
			return
		}
		f := map[string]string{}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			f[p.FormName()] = string(b)
		}
		got, forms = append(got, r), append(forms, f)
		w.WriteHeader(status)
		if status == 200 {
			io.WriteString(w, `{"id":"ltr_4868c3b754655f90","expected_delivery_date":"2026-10-23"}`)
		} else {
			io.WriteString(w, `{"error":{"message":"address_zip is invalid","status_code":422}}`)
		}
	}))
	defer lob.Close()
	e := newEnv(t, "")
	dir := t.TempDir()
	m := &notice.Mailer{Log: e.log, Cases: e.cases, Sender: &notice.Lob{Key: "test_key", URL: lob.URL + "/v1/letters"},
		From: planAddress, Events: []string{notice.Denied}, Cursor: filepath.Join(dir, "cursor")}
	m.Load()

	status = http.StatusServiceUnavailable
	d := submit(t, e, withAddress(request("", "", "", denies...)))
	if _, err := m.Once(context.Background()); err == nil {
		t.Fatal("Lob's failure was not reported")
	}
	if c, _ := e.cases.Get(d.Case); len(c.Notices) != 0 {
		t.Fatalf("recorded before it was sent: %+v", c.Notices)
	}
	status = http.StatusOK
	if _, err := m.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.cases.Get(d.Case)
	if len(c.Notices) != 1 || c.Notices[0].ID != "ltr_4868c3b754655f90" || c.Notices[0].Service != "lob" {
		t.Fatalf("%+v", c.Notices)
	}
	f := forms[len(forms)-1]
	for k, want := range map[string]string{"to[name]": "Ana Reyes", "to[address_line1]": "12 Elm St", "to[address_zip]": "62702",
		"from[name]": "Example Health Plan", "use_type": "operational", "address_placement": "insert_blank_page", "color": "false"} {
		if f[k] != want {
			t.Errorf("%s = %q, want %q", k, f[k], want)
		}
	}
	if !strings.HasPrefix(f["file"], "%PDF") {
		t.Error("no PDF")
	}
	if k1, k2 := got[0].Header.Get("Idempotency-Key"), got[1].Header.Get("Idempotency-Key"); k1 == "" || k1 != k2 {
		t.Errorf("the retry's idempotency key %q is not the first's %q", k2, k1)
	}

	status = http.StatusUnprocessableEntity
	d2 := submit(t, e, withAddress(request("", "", "", denies...)))
	if _, err := m.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.cases.Get(d2.Case); len(c.Notices) != 1 || !strings.Contains(c.Notices[0].Error, "address_zip is invalid") {
		t.Errorf("%+v", c.Notices)
	}
}
