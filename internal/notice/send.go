package notice

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// Folder writes each letter as a PDF into a directory a mail house (or the plan's own print room) collects from, with
// the member's address on its first page for a #10 double-window envelope, and a line in manifest.csv for each.
type Folder struct {
	Dir string
	mu  sync.Mutex
}

func (f *Folder) Name() string { return "folder" }
func (f *Folder) Window() bool { return true }

func (f *Folder) Send(_ context.Context, m Mail) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return "", false, err
	}
	key := strings.TrimPrefix(m.Key, "vaglio-")
	if len(key) > 12 {
		key = key[:12]
	}
	name := m.CaseID + "-" + m.Event + "-" + key + ".pdf" // the same record writes the same file: never two copies
	path := filepath.Join(f.Dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, m.PDF, 0o600); err != nil {
		return "", false, err
	}
	if err := fsx.Replace(tmp, path); err != nil {
		return "", false, err
	}
	mf, err := os.OpenFile(filepath.Join(f.Dir, "manifest.csv"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", false, err
	}
	defer mf.Close()
	if st, _ := mf.Stat(); st != nil && st.Size() == 0 {
		mf.WriteString("file,case,event,name,street,city,state,zip,written\n")
	}
	w := csv.NewWriter(mf)
	w.Write([]string{name, m.CaseID, m.Event, m.To.Name, strings.Join(m.To.Lines, " / "), m.To.City, m.To.State, m.To.Zip,
		time.Now().UTC().Format(time.RFC3339)})
	w.Flush()
	if err := w.Error(); err != nil {
		return "", false, err
	}
	return name, false, mf.Sync()
}

// Lob sends letters through Lob's Print & Mail API (POST /v1/letters): the PDF uploaded, first-class, black and
// white, double-sided, operational mail, with an address page Lob adds (address_placement insert_blank_page) so the
// letter's own layout never meets the envelope's window. Tested against a stand-in of the API built from Lob's own
// OpenAPI description, not against Lob itself.
type Lob struct {
	Key    string // secret API key; a test_ key mails nothing
	URL    string // https://api.lob.com/v1/letters by default
	Client *http.Client
}

func (l *Lob) Name() string { return "lob" }
func (l *Lob) Window() bool { return false }

func (l *Lob) Send(ctx context.Context, m Mail) (string, bool, error) {
	if l.Key == "" {
		return "", true, errors.New("no Lob API key")
	}
	u := l.URL
	if u == "" {
		u = "https://api.lob.com/v1/letters"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	field := func(k, v string) { mw.WriteField(k, v) }
	addr := func(prefix string, a Address) {
		field(prefix+"[name]", a.Name)
		if len(a.Lines) > 0 {
			field(prefix+"[address_line1]", a.Lines[0])
		}
		if len(a.Lines) > 1 {
			field(prefix+"[address_line2]", strings.Join(a.Lines[1:], ", "))
		}
		field(prefix+"[address_city]", a.City)
		field(prefix+"[address_state]", a.State)
		field(prefix+"[address_zip]", a.Zip)
		field(prefix+"[address_country]", "US")
	}
	field("description", "Prior authorization notice "+m.CaseID+" ("+m.Event+")")
	addr("to", m.To)
	addr("from", m.From)
	field("color", "false")
	field("double_sided", "true")
	field("address_placement", "insert_blank_page")
	field("mail_type", "usps_first_class")
	field("use_type", "operational")
	field("metadata[case]", m.CaseID)
	field("metadata[event]", m.Event)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="notice.pdf"`)
	h.Set("Content-Type", "application/pdf")
	fw, _ := mw.CreatePart(h)
	fw.Write(m.PDF)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return "", true, err
	}
	req.SetBasicAuth(l.Key, "")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", m.Key)
	c := l.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(raw, &e)
		err := fmt.Errorf("Lob answered %s: %s", resp.Status, e.Error.Message)
		// A refused letter (bad address, bad key) is refused again; too many requests and Lob's own failures are not.
		final := resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout
		return "", final, err
	}
	var ok struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil || ok.ID == "" {
		return "", false, errors.New("Lob's answer has no letter id")
	}
	return ok.ID, false, nil
}
