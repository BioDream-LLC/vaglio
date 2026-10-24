// Package cmswatch checks the Medicare coverage documents library policies were drafted from against their current
// versions in the CMS Coverage API, so a policy whose source has changed is reviewed again before anyone relies on it.
// It reads two public reports (every final LCD, every NCD, with their current versions); nothing about the plan or its
// patients is sent. A changed source changes nothing by itself: the policy goes on deciding as adopted, and its screen
// says the source moved on.
package cmswatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/library"
)

// API is the Coverage API's address.
var API = "https://api.coverage.cms.gov/v1"

// Current is a document's current version, as CMS reports it.
type Current struct {
	Version int    `json:"version"`
	Updated string `json:"updated,omitempty"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Retired string `json:"retired,omitempty"`
	Display string `json:"display,omitempty"` // as people cite it: L39240, NCD 100.1
}

// Finding is one library policy's source compared.
type Finding struct {
	Policy   string           `json:"policy"`
	Document library.Document `json:"document"`
	Current  *Current         `json:"current,omitempty"` // nil when CMS no longer lists it
	// Changed: CMS has a newer version than the one drafted from. Unknown: the copy does not say which version it
	// was drafted from, so only a person comparing the texts can tell.
	Changed bool   `json:"changed"`
	Unknown bool   `json:"unknown,omitempty"`
	Missing bool   `json:"missing,omitempty"` // CMS no longer lists the document (retired, or renumbered)
	Note    string `json:"note"`
}

// Fetch reads both reports: current version by document type and id.
func Fetch(ctx context.Context, c *http.Client) (map[string]Current, error) {
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	out := map[string]Current{}
	for kind, path := range map[string]string{"lcd": "/reports/local-coverage-final-lcds/", "ncd": "/reports/national-coverage-ncd/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the Coverage API answered %s for %s", resp.Status, path)
		}
		var rep struct {
			Data []struct {
				ID          int    `json:"document_id"`
				DisplayID   string `json:"document_display_id"`
				Version     int    `json:"document_version"`
				Title       string `json:"title"`
				Updated     string `json:"updated_on"`
				LastUpdated string `json:"last_updated"`
				Retired     string `json:"retirement_date"`
				URL         string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &rep); err != nil {
			return nil, fmt.Errorf("the Coverage API's %s report: %w", kind, err)
		}
		for _, d := range rep.Data {
			cur := Current{Version: d.Version, Updated: d.Updated, URL: d.URL, Title: d.Title, Display: d.DisplayID}
			if kind == "ncd" && d.DisplayID != "" {
				cur.Display = "NCD " + d.DisplayID
			}
			if cur.Updated == "" {
				cur.Updated = d.LastUpdated
			}
			if d.Retired != "" && d.Retired != "N/A" {
				cur.Retired = d.Retired
			}
			k := key(kind, d.ID)
			if prev, ok := out[k]; !ok || d.Version > prev.Version {
				out[k] = cur
			}
		}
	}
	return out, nil
}

func key(kind string, id int) string { return fmt.Sprintf("%s:%d", kind, id) }

// Compare holds every library policy drafted from a CMS document against the current versions.
func Compare(current map[string]Current) ([]Finding, error) {
	es, err := library.Entries()
	if err != nil {
		return nil, err
	}
	var out []Finding
	for id, e := range es {
		if e.CMS == nil {
			continue
		}
		f := Finding{Policy: id, Document: *e.CMS}
		cur, ok := current[key(e.CMS.Type, e.CMS.ID)]
		name := e.CMS.Display()
		if cur.Display != "" {
			name = cur.Display
		}
		switch {
		case !ok:
			f.Missing = true
			f.Note = name + " is no longer in CMS's list of current documents: retired or renumbered. " +
				"Review the policy before relying on it."
		case e.CMS.Version == 0:
			f.Current, f.Unknown = &cur, true
			f.Note = fmt.Sprintf("%s is at version %d; the library's copy does not say which version it was drafted "+
				"from. Compare the texts.", name, cur.Version)
		case cur.Version > e.CMS.Version:
			f.Current, f.Changed = &cur, true
			f.Note = fmt.Sprintf("%s is at version %d (updated %s); the policy was drafted from version %d. Review it "+
				"against the new version before relying on it.", name, cur.Version, cur.Updated, e.CMS.Version)
		default:
			f.Current = &cur
			f.Note = fmt.Sprintf("%s is still at version %d, the version the policy was drafted from.", name,
				e.CMS.Version)
		}
		if ok && cur.Retired != "" {
			f.Changed = true
			f.Note = fmt.Sprintf("%s is to be retired on %s. Review the policy.", name, cur.Retired)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Policy < out[j].Policy })
	return out, nil
}

// Watcher checks once a day and keeps the last findings.
type Watcher struct {
	Client  *http.Client
	Every   time.Duration // a day when 0
	OnFound func([]Finding)

	mu       sync.Mutex
	findings map[string]Finding
	checked  time.Time
	lastErr  string
}

// Start checks now, then at the interval, until ctx ends.
func (w *Watcher) Start(ctx context.Context) {
	every := w.Every
	if every <= 0 {
		every = 24 * time.Hour
	}
	go func() {
		for {
			w.Once(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
	}()
}

// Once checks now.
func (w *Watcher) Once(ctx context.Context) error {
	cur, err := Fetch(ctx, w.Client)
	var fs []Finding
	if err == nil {
		fs, err = Compare(cur)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.checked = time.Now()
	if err != nil {
		w.lastErr = err.Error()
		return err
	}
	w.lastErr = ""
	w.findings = map[string]Finding{}
	for _, f := range fs {
		w.findings[f.Policy] = f
	}
	if w.OnFound != nil {
		w.OnFound(fs)
	}
	return nil
}

// For is the last finding for a policy.
func (w *Watcher) For(policy string) (Finding, bool) {
	if w == nil {
		return Finding{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, ok := w.findings[policy]
	return f, ok
}

// Checked is when the watcher last checked, and its last error.
func (w *Watcher) Checked() (time.Time, string) {
	if w == nil {
		return time.Time{}, ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.checked, w.lastErr
}
