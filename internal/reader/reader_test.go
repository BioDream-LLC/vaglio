package reader

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

const note = "Neurology consult.\nThe neurological examination is normal, with no saddle anaesthesia.\n" +
	"She completed eight weeks of physical therapy without relief."

func lumbar(t *testing.T) *policy.Policy {
	p, err := policy.LoadFile("../../policies/lumbar_mri.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGate(t *testing.T) {
	p := lumbar(t)
	d := Document{Reference: "DocumentReference/n1", Text: note}
	r := Gate(p, d, "Here you are:\n```json\n"+`{"findings":[
		{"evidence":"red_flag","finding":"absent","quote":"The neurological examination is normal"},
		{"evidence":"conservative_therapy_6wk","finding":"present","quote":"She completed eight weeks of physical therapy without relief."},
		{"evidence":"conservative_therapy_6wk","finding":"present","quote":"PT failed after six weeks"},
		{"evidence":"surgical_planning","finding":"present","quote":"normal"},
		{"evidence":"knee_pain","finding":"present","quote":"The neurological examination is normal"},
		{"evidence":"red_flag","finding":"maybe","quote":"The neurological examination is normal"}]}`+"\n```")
	if len(r.Proposals) != 2 || r.Proposals[0].Argument != "red_flag" || r.Proposals[0].Finding != "absent" {
		t.Fatalf("%+v", r.Proposals)
	}
	want := []string{"not in the document word for word", "too short", "not evidence this policy declares", "not present or absent"}
	if len(r.Refused) != len(want) {
		t.Fatalf("%q", r.Refused)
	}
	for i, w := range want {
		if !strings.Contains(r.Refused[i], w) {
			t.Errorf("refusal %d %q, want %q", i, r.Refused[i], w)
		}
	}
	if r := Gate(p, d, "I cannot help with that."); len(r.Proposals) != 0 || len(r.Refused) != 1 {
		t.Errorf("%+v", r)
	}
}

func TestDocuments(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	doc := pastest.Document("n1", "Neurology consult")
	pastest.Map(doc)["content"] = []any{map[string]any{"attachment": map[string]any{"contentType": "text/plain", "data": enc(note)}}}
	html := pastest.Document("h1", "Discharge summary")
	pastest.Map(html)["content"] = []any{map[string]any{"attachment": map[string]any{"contentType": "text/html; charset=utf-8",
		"data": enc("<html><style>p{}</style><p>Pain &amp; numbness.</p></html>")}}}
	pdf := pastest.Document("p1", "Scan")
	pastest.Map(pdf)["content"] = []any{map[string]any{"attachment": map[string]any{"contentType": "application/pdf", "data": enc("%PDF")}}}
	b := pastest.Bundle([]string{"72148"}, doc, html, pdf)
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	docs, skipped := Documents(req)
	if len(docs) != 2 || docs[0].Text != note || strings.TrimSpace(docs[1].Text) != "Pain & numbness." {
		t.Fatalf("%+v", docs)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "application/pdf") {
		t.Errorf("%q", skipped)
	}
}

type fake string

func (f fake) Complete(context.Context, []draft.Message) (string, error) { return string(f), nil }

func TestRead(t *testing.T) {
	r, err := Read(context.Background(), fake(`{"findings":[{"evidence":"red_flag","finding":"absent","quote":"with no saddle anaesthesia"}]}`),
		"test-model", lumbar(t), Document{Reference: "d", Text: note})
	if err != nil || r.Model != "test-model" || len(r.Proposals) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

// TestExtractors runs pdftotext over a real PDF when it is installed, and a stand-in program for images (tesseract is
// not needed to test the path a document takes through an extractor).
func TestExtractors(t *testing.T) {
	pdf, err := os.ReadFile("testdata/note.pdf")
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nnot really an image")
	b := pastest.Bundle([]string{"72148"},
		docWith("p1", "application/pdf", pdf), docWith("i1", "image/png", png), docWith("g1", "image/gif", png))
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	// Without extractors, both are skipped, as before.
	if docs, skipped := Documents(req); len(docs) != 0 || len(skipped) != 3 {
		t.Fatalf("%v %v", docs, skipped)
	}
	stand := Extractor{Name: "stand-in-ocr", Path: "/bin/sh", Args: []string{"-c", "cat >/dev/null; echo 'She completed eight weeks of physical therapy without relief.'"},
		Types: []string{"image/png"}}
	xs := Extractors{stand}
	if x, err := Lookup("pdftotext"); err == nil {
		xs = append(xs, x)
	} else {
		t.Log("pdftotext is not installed; its PDF is not read here:", err)
	}
	// Listed, not run: the list must be quick.
	docs, _ := DocumentsWith(context.Background(), req, xs, false)
	for _, d := range docs {
		if d.Text != "" || d.ExtractedBy == "" {
			t.Errorf("listing ran %s", d.ExtractedBy)
		}
	}
	docs, skipped := DocumentsWith(context.Background(), req, xs, true)
	byRef := map[string]Document{}
	for _, d := range docs {
		byRef[d.Reference] = d
	}
	img := byRef[pastest.Base+"/DocumentReference/i1"]
	if img.ExtractedBy != "stand-in-ocr" || !strings.Contains(img.Text, "eight weeks") || string(img.Raw) != string(png) {
		t.Errorf("image: %+v", img)
	}
	if len(xs) == 2 {
		d := byRef[pastest.Base+"/DocumentReference/p1"]
		if d.ExtractedBy != "pdftotext" || !strings.Contains(d.Text, "The neurological examination is normal") {
			t.Errorf("pdf: %+v", d)
		}
		// Through the quote gate like any text.
		r := Gate(lumbar(t), d, `{"findings":[{"evidence":"red_flag","finding":"absent","quote":"The neurological examination is normal"}]}`)
		if len(r.Proposals) != 1 {
			t.Errorf("%+v", r)
		}
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "image/gif") {
		t.Errorf("skipped %v", skipped)
	}
	// A failing or slow program is a skipped document, never text.
	bad := Extractors{{Name: "broken", Path: "/bin/sh", Args: []string{"-c", "exit 3"}, Types: []string{"image/png"}},
		{Name: "slow", Path: "/bin/sh", Args: []string{"-c", "sleep 5"}, Types: []string{"application/pdf"}, Timeout: 200 * time.Millisecond}}
	docs, skipped = DocumentsWith(context.Background(), req, bad, true)
	if len(docs) != 0 || len(skipped) != 3 || !strings.Contains(strings.Join(skipped, " "), "took longer") {
		t.Errorf("%v %v", docs, skipped)
	}
}

func docWith(id, ct string, raw []byte) map[string]any {
	return map[string]any{"fullUrl": pastest.Base + "/DocumentReference/" + id, "resource": map[string]any{
		"resourceType": "DocumentReference", "id": id, "status": "current", "description": "a scanned note",
		"content": []any{map[string]any{"attachment": map[string]any{"contentType": ct,
			"data": base64.StdEncoding.EncodeToString(raw)}}}}}
}
