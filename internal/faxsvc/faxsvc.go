// Package faxsvc brings faxes into Vaglio's fax inbox from where they arrive: the webhooks of Sinch (formerly Phaxio)
// and Documo, polling SRFax's and RingCentral's APIs, and a folder a scanner or a fax server writes to.
//
// Each connector is written from its service's public developer documentation (October 2026), and is tested against a
// stand-in for the service built from that documentation, not against the service itself. Where the documentation was
// not specific, the code says what it assumes. eFax Corporate is not here: its inbound API is documented only to
// customers.
package faxsvc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Store keeps one fax in the inbox; source names where it came from in the audit log.
type Store func(ctx context.Context, raw []byte, contentType, from string, pages int, source string) error

// MaxFax bounds one fax.
const MaxFax = 8 << 20

// sniff is a document's type from its first bytes, for services that do not say: PDF, TIFF, PNG or JPEG.
func sniff(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("%PDF")):
		return "application/pdf"
	case bytes.HasPrefix(raw, []byte("II*\x00")), bytes.HasPrefix(raw, []byte("MM\x00*")):
		return "image/tiff"
	case bytes.HasPrefix(raw, []byte("\x89PNG")):
		return "image/png"
	case bytes.HasPrefix(raw, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	}
	return ""
}

// basicOK checks a webhook's HTTP Basic credentials: the password must be the configured one (the user name is not
// checked; services set it as they like).
func basicOK(r *http.Request, password string) bool {
	_, pw, ok := r.BasicAuth()
	return ok && password != "" && subtle.ConstantTimeCompare([]byte(pw), []byte(password)) == 1
}

// Sinch receives Sinch Fax API v3 incoming-fax callbacks (event INCOMING_FAX), configured on the fax service with HTTP
// Basic credentials. Both of its formats are read: multipart/form-data with the fax as the "file" part and the event's
// JSON in the other parts, or one JSON object with the file base64 in "file".
type Sinch struct {
	Password string
	Store    Store
}

func (s *Sinch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !basicOK(r, s.Password) {
		w.Header().Set("WWW-Authenticate", `Basic realm="vaglio fax"`)
		http.Error(w, "the fax service's credentials are needed", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFax*2)
	var event struct {
		Event string `json:"event"`
		Fax   struct {
			ID            string `json:"id"`
			Direction     string `json:"direction"`
			From          string `json:"from"`
			NumberOfPages int    `json:"numberOfPages"`
			Status        string `json:"status"`
		} `json:"fax"`
		File string `json:"file"`
	}
	var raw []byte
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "multipart/form-data":
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "unreadable multipart body", http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(io.LimitReader(p, MaxFax+1))
			switch name := p.FormName(); {
			case name == "file":
				raw = b
			case name == "fax":
				_ = json.Unmarshal(b, &event.Fax)
			case name == "event":
				event.Event = strings.TrimSpace(string(b))
			case json.Valid(b) && bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")):
				_ = json.Unmarshal(b, &event) // the event's JSON as one part
			}
		}
	case "application/json":
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "not JSON", http.StatusBadRequest)
			return
		}
		b, err := base64.StdEncoding.DecodeString(event.File)
		if err != nil {
			http.Error(w, "file is not base64", http.StatusBadRequest)
			return
		}
		raw = b
	default:
		http.Error(w, "multipart/form-data or application/json", http.StatusUnsupportedMediaType)
		return
	}
	if event.Event != "" && event.Event != "INCOMING_FAX" {
		w.WriteHeader(http.StatusNoContent) // another event: acknowledged, nothing to keep
		return
	}
	if len(raw) == 0 || len(raw) > MaxFax {
		http.Error(w, "no fax file, or one over 8 MB", http.StatusBadRequest)
		return
	}
	if err := s.Store(r.Context(), raw, sniff(raw), event.Fax.From, event.Fax.NumberOfPages, "fax-service:sinch"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Documo receives Documo (mFax) webhooks, configured with HTTP Basic credentials. Its inbound events carry no file
// ("use the API download endpoint"), so on fax.v1.inbound.complete the fax is downloaded with the account's API key.
// API is the API's base URL; the download path, /v1/fax/{messageId}/download, is our reading of Documo's API
// reference, which links the endpoint without spelling it in the webhook page.
type Documo struct {
	Password string
	APIKey   string
	API      string // default https://api.documo.com
	Client   *http.Client
	Store    Store
}

func (d *Documo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !basicOK(r, d.Password) {
		w.Header().Set("WWW-Authenticate", `Basic realm="vaglio fax"`)
		http.Error(w, "the fax service's credentials are needed", http.StatusUnauthorized)
		return
	}
	var ev struct {
		MessageID   string `json:"messageId"`
		FaxCallerID string `json:"faxCallerId"`
		FaxCsid     string `json:"faxCsid"`
		PagesCount  int    `json:"pagesCount"`
		Status      string `json:"status"`
		Direction   string `json:"direction"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ev); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	if r.Header.Get("x-webhook-event") != "fax.v1.inbound.complete" || !strings.EqualFold(ev.Direction, "inbound") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if ev.MessageID == "" || strings.ContainsAny(ev.MessageID, "/?#") {
		http.Error(w, "no usable messageId", http.StatusBadRequest)
		return
	}
	api := strings.TrimRight(d.API, "/")
	if api == "" {
		api = "https://api.documo.com"
	}
	raw, err := get(r.Context(), d.client(), api+"/v1/fax/"+url.PathEscape(ev.MessageID)+"/download", "Basic "+d.APIKey)
	if err != nil {
		// 502: the service retries its webhook, and the download is tried again then.
		http.Error(w, "downloading the fax: "+err.Error(), http.StatusBadGateway)
		return
	}
	from := ev.FaxCallerID
	if from == "" {
		from = ev.FaxCsid
	}
	if err := d.Store(r.Context(), raw, sniff(raw), from, ev.PagesCount, "fax-service:documo"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Documo) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func get(ctx context.Context, c *http.Client, u, auth string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxFax+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	if len(raw) > MaxFax {
		return nil, errors.New("over 8 MB")
	}
	return raw, nil
}

// Seen is a set of the service's fax ids already kept, on disk, so a poller does not fetch one twice.
type Seen struct {
	Path string
	mu   sync.Mutex
	m    map[string]bool
}

func (s *Seen) load() {
	if s.m != nil {
		return
	}
	s.m = map[string]bool{}
	raw, _ := os.ReadFile(s.Path)
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			s.m[l] = true
		}
	}
}

// Has is whether an id was kept.
func (s *Seen) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.m[id]
}

// Add records an id as kept.
func (s *Seen) Add(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.m[id] {
		return nil
	}
	f, err := os.OpenFile(s.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(id + "\n"); err != nil {
		return err
	}
	s.m[id] = true
	return f.Sync()
}

// Poller is a connector that asks its service for new faxes now and then.
type Poller interface {
	Name() string
	Poll(ctx context.Context) (int, error)
}

// Run polls p every interval until ctx ends, logging failures (a service that is down is tried again next time).
func Run(ctx context.Context, p Poller, every time.Duration) {
	for {
		if n, err := p.Poll(ctx); err != nil {
			log.Printf("fax %s: %v", p.Name(), err)
		} else if n > 0 {
			log.Printf("fax %s: %d received", p.Name(), n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// SRFax polls SRFax's API (it pushes nothing for received faxes): Get_Fax_Inbox for the unread ones, Retrieve_Fax for
// each (base64 PDF), then Update_Viewed_Status so it is not listed again. Seen guards against one fetched but not
// marked when the connection broke in between.
type SRFax struct {
	AccessID string
	Password string
	Endpoint string // default https://secure.srfax.com/SRF_SecWebSvc.php
	Client   *http.Client
	Seen     *Seen
	Store    Store
}

func (s *SRFax) Name() string { return "srfax" }

func (s *SRFax) call(ctx context.Context, action string, form url.Values) (json.RawMessage, error) {
	form.Set("action", action)
	form.Set("access_id", s.AccessID)
	form.Set("access_pwd", s.Password)
	form.Set("sResponseFormat", "JSON")
	ep := s.Endpoint
	if ep == "" {
		ep = "https://secure.srfax.com/SRF_SecWebSvc.php"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Status string          `json:"Status"`
		Result json.RawMessage `json:"Result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxFax*2)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer: %w", action, err)
	}
	if out.Status != "Success" {
		var msg string
		_ = json.Unmarshal(out.Result, &msg)
		return nil, fmt.Errorf("%s: %s", action, msg)
	}
	return out.Result, nil
}

func (s *SRFax) Poll(ctx context.Context) (int, error) {
	res, err := s.call(ctx, "Get_Fax_Inbox", url.Values{"sPeriod": {"ALL"}, "sViewedStatus": {"UNREAD"}})
	if err != nil {
		return 0, err
	}
	var list []struct {
		FileName string `json:"FileName"`
		CallerID string `json:"CallerID"`
		Pages    any    `json:"Pages"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return 0, fmt.Errorf("Get_Fax_Inbox: %w", err)
	}
	n := 0
	for _, f := range list {
		if f.FileName == "" || s.Seen.Has(f.FileName) {
			continue
		}
		res, err := s.call(ctx, "Retrieve_Fax", url.Values{"sFaxFileName": {f.FileName}, "sDirection": {"IN"}, "sFaxFormat": {"PDF"}})
		if err != nil {
			return n, err
		}
		var b64 string
		if err := json.Unmarshal(res, &b64); err != nil {
			return n, fmt.Errorf("Retrieve_Fax: %w", err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return n, fmt.Errorf("Retrieve_Fax: not base64: %w", err)
		}
		pages, _ := strconv.Atoi(fmt.Sprint(f.Pages))
		if err := s.Store(ctx, raw, sniff(raw), f.CallerID, pages, "fax-service:srfax"); err != nil {
			return n, err
		}
		if err := s.Seen.Add(f.FileName); err != nil {
			return n, err
		}
		n++
		if _, err := s.call(ctx, "Update_Viewed_Status", url.Values{"sFaxFileName": {f.FileName}, "sDirection": {"IN"},
			"sMarkasViewed": {"Y"}}); err != nil {
			log.Printf("fax srfax: kept %s but could not mark it viewed: %v", f.FileName, err)
		}
	}
	return n, nil
}

// RingCentral polls the message store of a RingCentral extension for inbound faxes, signed in with the JWT grant
// (a JWT credential made in RingCentral's developer console, for an app with the ReadMessages permission), and
// downloads each fax's PDF attachment.
type RingCentral struct {
	Server       string // default https://platform.ringcentral.com
	ClientID     string
	ClientSecret string
	JWT          string
	Client       *http.Client
	Seen         *Seen
	Store        Store

	token   string
	expires time.Time
}

func (rc *RingCentral) Name() string { return "ringcentral" }

func (rc *RingCentral) server() string {
	if rc.Server == "" {
		return "https://platform.ringcentral.com"
	}
	return strings.TrimRight(rc.Server, "/")
}

func (rc *RingCentral) client() *http.Client {
	if rc.Client != nil {
		return rc.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (rc *RingCentral) bearer(ctx context.Context) (string, error) {
	if rc.token != "" && time.Now().Before(rc.expires) {
		return rc.token, nil
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {rc.JWT}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.server()+"/restapi/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(rc.ClientID, rc.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := rc.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&t); err != nil || resp.StatusCode != 200 || t.AccessToken == "" {
		return "", fmt.Errorf("signing in to RingCentral: %s", resp.Status)
	}
	rc.token, rc.expires = t.AccessToken, time.Now().Add(time.Duration(max(t.ExpiresIn-60, 30))*time.Second)
	return rc.token, nil
}

func (rc *RingCentral) Poll(ctx context.Context) (int, error) {
	tok, err := rc.bearer(ctx)
	if err != nil {
		return 0, err
	}
	since := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	raw, err := get(ctx, rc.client(), rc.server()+"/restapi/v1.0/account/~/extension/~/message-store?messageType=Fax&direction=Inbound&perPage=100&dateFrom="+url.QueryEscape(since), "Bearer "+tok)
	if err != nil {
		return 0, err
	}
	var page struct {
		Records []struct {
			ID            any    `json:"id"`
			MessageStatus string `json:"messageStatus"`
			CreationTime  string `json:"creationTime"`
			FaxPageCount  int    `json:"faxPageCount"`
			From          struct {
				PhoneNumber string `json:"phoneNumber"`
			} `json:"from"`
			Attachments []struct {
				URI         string `json:"uri"`
				ContentType string `json:"contentType"`
			} `json:"attachments"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return 0, fmt.Errorf("message store: %w", err)
	}
	sort.SliceStable(page.Records, func(i, j int) bool { return page.Records[i].CreationTime < page.Records[j].CreationTime })
	n := 0
	for _, m := range page.Records {
		id := fmt.Sprint(m.ID)
		if m.MessageStatus != "Received" || rc.Seen.Has(id) {
			continue
		}
		for _, a := range m.Attachments {
			if a.ContentType != "application/pdf" || !strings.HasPrefix(a.URI, rc.server()+"/") {
				continue // only a PDF, and only from the server signed in to: the bearer token goes nowhere else
			}
			doc, err := get(ctx, rc.client(), a.URI, "Bearer "+tok)
			if err != nil {
				return n, err
			}
			if err := rc.Store(ctx, doc, "application/pdf", m.From.PhoneNumber, m.FaxPageCount, "fax-service:ringcentral"); err != nil {
				return n, err
			}
		}
		if err := rc.Seen.Add(id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Folder takes in the files a scanner or a fax server writes to a directory: each PDF, TIFF, PNG or JPEG goes to the
// inbox and then to DIR/kept (or DIR/refused, with a .why beside it). A file is taken once it has not changed for a few
// seconds, so one still being written is left for the next look.
type Folder struct {
	Dir   string
	Store Store
	Quiet time.Duration // default 5 seconds
}

func (f *Folder) Name() string { return "folder " + f.Dir }

func (f *Folder) Poll(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return 0, err
	}
	quiet := f.Quiet
	if quiet == 0 {
		quiet = 5 * time.Second
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < quiet {
			continue
		}
		path := filepath.Join(f.Dir, e.Name())
		move := func(to, why string) error {
			dir := filepath.Join(f.Dir, to)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if why != "" {
				_ = os.WriteFile(filepath.Join(dir, e.Name()+".why"), []byte(why+"\n"), 0o600)
			}
			return fsx.Replace(path, filepath.Join(dir, e.Name()))
		}
		if info.Size() > MaxFax {
			if err := move("refused", "over 8 MB"); err != nil {
				return n, err
			}
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return n, err
		}
		ct := sniff(raw)
		if ct == "" {
			if err := move("refused", "not a PDF, TIFF, PNG or JPEG"); err != nil {
				return n, err
			}
			continue
		}
		if err := f.Store(ctx, raw, ct, "", 0, "scan-folder"); err != nil {
			if err := move("refused", err.Error()); err != nil {
				return n, err
			}
			continue
		}
		if err := move("kept", ""); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
