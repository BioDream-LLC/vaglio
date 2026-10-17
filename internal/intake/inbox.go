package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/seal"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Fax is one fax received, waiting to be keyed in or set aside.
type Fax struct {
	ID          string `json:"id"`
	Received    string `json:"received"`
	From        string `json:"from,omitempty"` // the sending number, as the fax service reports it
	Pages       int    `json:"pages,omitempty"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
	// Text is what the extractors made of it, to read beside the image while keying it in.
	Text string `json:"text,omitempty"`
	// Done is set once it is keyed in (Case names the case) or set aside (Reason says why).
	Done   string `json:"done,omitempty"`
	Case   string `json:"case,omitempty"`
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Inbox keeps faxes on disk: DIR/<id>.pdf (or .tif) and DIR/<id>.json.
type Inbox struct {
	Dir string
	mu  sync.Mutex
}

var faxIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (b *Inbox) path(id, ext string) string { return filepath.Join(b.Dir, id+ext) }

func ext(contentType string) string {
	switch contentType {
	case "application/pdf":
		return ".pdf"
	case "image/tiff":
		return ".tif"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	}
	return ".bin"
}

// Add keeps a fax. A fax whose bytes match one kept already (a service delivering twice) is that one.
func (b *Inbox) Add(raw []byte, contentType, from string, pages int, text string, now time.Time) (Fax, error) {
	f, _, err := b.AddNew(raw, contentType, from, pages, text, now)
	return f, err
}

// AddNew is Add, saying whether the fax is new.
func (b *Inbox) AddNew(raw []byte, contentType, from string, pages int, text string, now time.Time) (Fax, bool, error) {
	if len(raw) == 0 || len(raw) > MaxDocument {
		return Fax{}, false, fmt.Errorf("a fax is 1 byte to %d MB", MaxDocument>>20)
	}
	switch contentType {
	case "application/pdf", "image/tiff", "image/png", "image/jpeg":
	default:
		return Fax{}, false, fmt.Errorf("a fax is PDF, TIFF, PNG or JPEG, not %q", contentType)
	}
	sum := sha256.Sum256(raw)
	h := hex.EncodeToString(sum[:])
	id := h[:16]
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, err := b.get(id); err == nil {
		return f, false, nil
	}
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return Fax{}, false, err
	}
	f := Fax{ID: id, Received: now.UTC().Format(time.RFC3339), From: from, Pages: pages, ContentType: contentType,
		Size: len(raw), SHA256: h, Text: text}
	if err := seal.WriteFile(b.path(id, ext(contentType)), "fax", raw, 0o600); err != nil {
		return Fax{}, false, err
	}
	return f, true, b.save(f)
}

func (b *Inbox) save(f Fax) error {
	raw, _ := json.MarshalIndent(f, "", "  ")
	tmp := b.path(f.ID, ".json.new")
	if err := seal.WriteFile(tmp, "fax", raw, 0o600); err != nil {
		return err
	}
	return fsx.Replace(tmp, b.path(f.ID, ".json"))
}

func (b *Inbox) get(id string) (Fax, error) {
	if !faxIDRe.MatchString(id) {
		return Fax{}, os.ErrNotExist
	}
	raw, err := seal.ReadFile(b.path(id, ".json"), "fax")
	if err != nil {
		return Fax{}, err
	}
	var f Fax
	return f, json.Unmarshal(raw, &f)
}

// Get is one fax.
func (b *Inbox) Get(id string) (Fax, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := b.get(id)
	return f, err == nil
}

// Content is a fax's bytes, refused when they no longer match the hash it was received with.
func (b *Inbox) Content(f Fax) ([]byte, error) {
	raw, err := seal.ReadFile(b.path(f.ID, ext(f.ContentType)), "fax")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return nil, errors.New("the stored fax does not match the one received")
	}
	return raw, nil
}

// List is the faxes, the waiting ones first, newest first within each.
func (b *Inbox) List() ([]Fax, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Fax{}
	for _, file := range files {
		id := filepath.Base(file)
		id = id[:len(id)-len(".json")]
		if f, err := b.get(id); err == nil {
			f.Text = "" // the list is light; Get has the text
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Done == "") != (out[j].Done == "") {
			return out[i].Done == ""
		}
		return out[i].Received > out[j].Received
	})
	return out, nil
}

// ErrDone is a fax already keyed in or set aside.
var ErrDone = errors.New("this fax has been handled already")

// Close marks a fax keyed in as a case, or set aside for a reason (a duplicate, not a prior authorization request).
func (b *Inbox) Close(id, by, caseID, reason string, now time.Time) (Fax, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := b.get(id)
	if err != nil {
		return Fax{}, err
	}
	if f.Done != "" {
		return f, ErrDone
	}
	if caseID == "" && reason == "" {
		return f, errors.New("a fax set aside says why")
	}
	f.Done, f.By, f.Case, f.Reason = now.UTC().Format(time.RFC3339), by, caseID, reason
	return f, b.save(f)
}
