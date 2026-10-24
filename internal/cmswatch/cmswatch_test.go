package cmswatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCompare: against a stand-in of the Coverage API's two reports, a source at the version drafted from is
// unchanged, one CMS has moved on is changed, one whose copy states no version is unknown, one to be retired is changed,
// and one CMS no longer lists is missing.
func TestCompare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "lcds"):
			w.Write([]byte(`{"data":[
{"document_id":39240,"document_version":16,"document_display_id":"L39240","updated_on":"11/01/2026","retirement_date":"N/A"},
{"document_id":39240,"document_version":15,"document_display_id":"L39240","retirement_date":"N/A"},
{"document_id":33718,"document_version":52,"document_display_id":"L33718","retirement_date":"N/A"},
{"document_id":36575,"document_version":21,"document_display_id":"L36575","retirement_date":"N/A"},
{"document_id":38773,"document_version":32,"document_display_id":"L38773","retirement_date":"12/31/2026"}]}`))
		default:
			w.Write([]byte(`{"data":[{"document_id":57,"document_version":5,"document_display_id":"100.1"}]}`))
		}
	}))
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	cur, err := Fetch(context.Background(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	fs, err := Compare(cur)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Finding{}
	for _, f := range fs {
		got[f.Policy] = f
		if f.Unknown {
			t.Errorf("%s: the library's copy must say which version of %s it was drafted from (cms.version in "+
				"library.json)", f.Policy, f.Document.Display())
		}
	}
	for id, want := range map[string]string{"esi": "changed", "cpap_osa": "same", "tka": "same", "facet_rfa": "changed",
		"bariatric_ncd": "same", "hbo": "missing"} {
		f := got[id]
		state := map[bool]string{true: "changed"}[f.Changed]
		switch {
		case f.Missing:
			state = "missing"
		case f.Unknown:
			state = "unknown"
		case state == "":
			state = "same"
		}
		if state != want {
			t.Errorf("%s: %s, not %s (%s)", id, state, want, f.Note)
		}
	}
	if !strings.Contains(got["esi"].Note, "L39240 is at version 16") || !strings.Contains(got["bariatric_ncd"].Note, "NCD 100.1") {
		t.Errorf("%q / %q", got["esi"].Note, got["bariatric_ncd"].Note)
	}
}
