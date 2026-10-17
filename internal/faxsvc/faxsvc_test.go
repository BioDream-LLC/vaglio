package faxsvc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var pdf = []byte("%PDF-1.4\n% a stand-in fax\n%%EOF\n")

type kept struct {
	mu    sync.Mutex
	items []string
}

func (k *kept) store(_ context.Context, raw []byte, ct, from string, pages int, source string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.items = append(k.items, strings.Join([]string{ct, from, source, string(rune('0' + pages))}, " "))
	return nil
}

func TestSinch(t *testing.T) {
	var k kept
	h := &Sinch{Password: "pw", Store: k.store}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("event", "INCOMING_FAX")
	mw.WriteField("fax", `{"id":"f1","direction":"INBOUND","from":"+15550100","numberOfPages":2}`)
	fw, _ := mw.CreateFormFile("file", "fax.pdf")
	fw.Write(pdf)
	mw.Close()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body.Bytes()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("no credentials: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/", bytes.NewReader(body.Bytes()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("sinch", "pw")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	js, _ := json.Marshal(map[string]any{"event": "INCOMING_FAX", "fax": map[string]any{"from": "+15550101", "numberOfPages": 1},
		"file": base64.StdEncoding.EncodeToString(pdf)})
	r = httptest.NewRequest("POST", "/", bytes.NewReader(js))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("x", "pw")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(k.items) != 2 || k.items[0] != "application/pdf +15550100 fax-service:sinch 2" || !strings.Contains(k.items[1], "+15550101") {
		t.Errorf("%d %v", w.Code, k.items)
	}
}

func TestDocumo(t *testing.T) {
	var k kept
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fax/m-1/download" || r.Header.Get("Authorization") != "Basic key" {
			http.Error(w, "no", 404)
			return
		}
		w.Write(pdf)
	}))
	defer api.Close()
	h := &Documo{Password: "pw", APIKey: "key", API: api.URL, Store: k.store}
	send := func(event, body string) int {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.SetBasicAuth("d", "pw")
		r.Header.Set("x-webhook-event", event)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	send("fax.v1.inbound.page", `{"messageId":"m-1","direction":"inbound"}`)
	if c := send("fax.v1.inbound.complete", `{"messageId":"m-1","faxCallerId":"+15550102","pagesCount":3,"direction":"inbound"}`); c != 204 {
		t.Fatalf("%d", c)
	}
	if c := send("fax.v1.inbound.complete", `{"messageId":"m-2","direction":"inbound"}`); c != 502 {
		t.Errorf("a failed download: %d", c)
	}
	if len(k.items) != 1 || k.items[0] != "application/pdf +15550102 fax-service:documo 3" {
		t.Errorf("%v", k.items)
	}
}

func TestSRFax(t *testing.T) {
	var k kept
	viewed := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("access_id") != "123" || r.Form.Get("access_pwd") != "pw" {
			json.NewEncoder(w).Encode(map[string]any{"Status": "Failed", "Result": "Invalid access"})
			return
		}
		switch r.Form.Get("action") {
		case "Get_Fax_Inbox":
			var list []map[string]any
			for _, f := range []string{"20261015-1|111", "20261015-2|222"} {
				if !viewed[f] {
					list = append(list, map[string]any{"FileName": f, "CallerID": "5550103", "Pages": "1"})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"Status": "Success", "Result": list})
		case "Retrieve_Fax":
			json.NewEncoder(w).Encode(map[string]any{"Status": "Success", "Result": base64.StdEncoding.EncodeToString(append(pdf, r.Form.Get("sFaxFileName")...))})
		case "Update_Viewed_Status":
			viewed[r.Form.Get("sFaxFileName")] = true
			json.NewEncoder(w).Encode(map[string]any{"Status": "Success", "Result": "ok"})
		}
	}))
	defer srv.Close()
	s := &SRFax{AccessID: "123", Password: "pw", Endpoint: srv.URL, Seen: &Seen{Path: filepath.Join(t.TempDir(), "seen")}, Store: k.store}
	if n, err := s.Poll(context.Background()); n != 2 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := s.Poll(context.Background()); n != 0 {
		t.Errorf("polled again: %d", n)
	}
	s.Password = "bad"
	if _, err := s.Poll(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid access") {
		t.Errorf("bad password: %v", err)
	}
}

func TestRingCentral(t *testing.T) {
	var k kept
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/restapi/oauth/token":
			id, sec, _ := r.BasicAuth()
			r.ParseForm()
			if id != "cid" || sec != "csec" || r.Form.Get("assertion") != "jwt" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(401)
		case strings.HasSuffix(r.URL.Path, "/message-store"):
			json.NewEncoder(w).Encode(map[string]any{"records": []any{
				map[string]any{"id": 9, "messageStatus": "Received", "faxPageCount": 2, "creationTime": "2026-10-15T10:00:00Z",
					"from": map[string]any{"phoneNumber": "+15550104"},
					"attachments": []any{map[string]any{"uri": srv.URL + "/restapi/v1.0/account/1/extension/1/message-store/9/content/9", "contentType": "application/pdf"},
						map[string]any{"uri": "https://elsewhere.example/steal", "contentType": "application/pdf"}}},
				map[string]any{"id": 10, "messageStatus": "Queued"}}})
		default:
			w.Write(pdf)
		}
	}))
	defer srv.Close()
	rc := &RingCentral{Server: srv.URL, ClientID: "cid", ClientSecret: "csec", JWT: "jwt", Seen: &Seen{Path: filepath.Join(t.TempDir(), "seen")}, Store: k.store}
	if n, err := rc.Poll(context.Background()); n != 1 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := rc.Poll(context.Background()); n != 0 || len(k.items) != 1 || k.items[0] != "application/pdf +15550104 fax-service:ringcentral 2" {
		t.Errorf("%v", k.items)
	}
}

func TestFolder(t *testing.T) {
	var k kept
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "scan1.pdf"), pdf, 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600)
	os.WriteFile(filepath.Join(dir, "fresh.pdf"), pdf, 0o600)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(dir, "scan1.pdf"), old, old)
	os.Chtimes(filepath.Join(dir, "notes.txt"), old, old)
	f := &Folder{Dir: dir, Store: k.store}
	if n, err := f.Poll(context.Background()); n != 1 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	for _, p := range []string{"kept/scan1.pdf", "refused/notes.txt", "refused/notes.txt.why", "fresh.pdf"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	_ = url.Values{}
}

// eFax Developer's inbound post: the XML in the form's "xml" field, answered "Post Successful".
func TestEFax(t *testing.T) {
	var k kept
	h := &EFax{User: "vaglio", Password: "posting-pw", Store: k.store}
	post := func(user, pw, kind, contents string) *httptest.ResponseRecorder {
		x := `<?xml version="1.0"?><InboundPostRequest><AccessControl><UserName>` + user + `</UserName><Password>` + pw +
			`</Password></AccessControl><RequestControl><RequestDate>08/18/2026 12:02:25</RequestDate><RequestType>` + kind +
			`</RequestType></RequestControl><FaxControl><AccountID>1234567890</AccountID><DateReceived>08/18/2026 12:02:13</DateReceived>` +
			`<FaxName>SampleOut</FaxName><FileType>pdf</FileType><PageCount>3</PageCount><CSID>8587123600</CSID><ANI>8587123600</ANI>` +
			`<Status>0</Status><MCFID>12345678</MCFID><FileContents>` + contents + `</FileContents></FaxControl></InboundPostRequest>`
		r := httptest.NewRequest("POST", "/api/feeds/fax/efax", strings.NewReader(url.Values{"xml": {x}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	b64 := base64.StdEncoding.EncodeToString(pdf)
	if w := post("vaglio", "posting-pw", "New Inbound", b64[:10]+"\n"+b64[10:]); w.Code != 200 || w.Body.String() != "Post Successful" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(k.items) != 1 || k.items[0] != "application/pdf 8587123600 fax-service:efax 3" {
		t.Fatalf("%v", k.items)
	}
	if w := post("vaglio", "wrong", "New Inbound", b64); w.Code != 401 {
		t.Errorf("a wrong password: %d", w.Code)
	}
	if w := post("vaglio", "posting-pw", "New Inbound", "not base64!"); w.Code != 400 {
		t.Errorf("bad contents: %d", w.Code)
	}
	if w := post("vaglio", "posting-pw", "Status Disposition", ""); w.Code != 200 || len(k.items) != 1 {
		t.Errorf("another kind of post: %d %v", w.Code, k.items)
	}
}
