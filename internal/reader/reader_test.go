package reader

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	stand := helper("stand-in-ocr", "echo", "{file}")
	stand.Types = []string{"image/png"}
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
	if len(xs) == 1 {
		skipped = skipped[1:] // the PDF, which nothing here reads
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "image/gif") {
		t.Errorf("skipped %v", skipped)
	}
	// A failing or slow program is a skipped document, never text.
	broken, slow := helper("broken", "fail"), helper("slow", "sleep")
	broken.Types, slow.Types, slow.Timeout = []string{"image/png"}, []string{"application/pdf"}, 200*time.Millisecond
	bad := Extractors{broken, slow}
	docs, skipped = DocumentsWith(context.Background(), req, bad, true)
	if len(docs) != 0 || len(skipped) != 3 || !strings.Contains(strings.Join(skipped, " "), "took longer") {
		t.Errorf("%v %v", docs, skipped)
	}
}

// helper is an extractor that is this test binary run again (TestHelperExtractor), so the test needs no shell: echo
// prints a sentence after reading the file it is given, fail exits 3, sleep outlasts any time limit.
func helper(name string, mode ...string) Extractor {
	return Extractor{Name: name, Path: os.Args[0], Args: append([]string{"-test.run=^TestHelperExtractor$", "--"}, mode...)}
}

func TestHelperExtractor(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	switch os.Args[i+1] {
	case "echo":
		if _, err := os.ReadFile(os.Args[i+2]); err != nil {
			os.Exit(2)
		}
		fmt.Println("She completed eight weeks of physical therapy without relief.")
		os.Exit(0)
	case "fail":
		os.Exit(3)
	case "sleep":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func docWith(id, ct string, raw []byte) map[string]any {
	return map[string]any{"fullUrl": pastest.Base + "/DocumentReference/" + id, "resource": map[string]any{
		"resourceType": "DocumentReference", "id": id, "status": "current", "description": "a scanned note",
		"content": []any{map[string]any{"attachment": map[string]any{"contentType": ct,
			"data": base64.StdEncoding.EncodeToString(raw)}}}}}
}

// A two-page fax as TIFF (Group 4, written here without libtiff: PackBits pages are enough for the page split), read
// page by page by a stand-in tesseract and by a stand-in model; and readers tried in turn when the first finds nothing.
func TestPageReaders(t *testing.T) {
	page := func() []byte {
		// A tiny uncompressed bilevel TIFF page, 16x16, white.
		var b bytes.Buffer
		b.WriteString("II*\x00\x08\x00\x00\x00")
		tags := [][3]uint32{{256, 4, 16}, {257, 4, 16}, {258, 3, 1}, {259, 3, 1}, {262, 3, 0}, {273, 4, 8 + 2 + 9*12 + 4}, {277, 3, 1}, {278, 4, 16}, {279, 4, 32}}
		binary.Write(&b, binary.LittleEndian, uint16(len(tags)))
		for _, tg := range tags {
			binary.Write(&b, binary.LittleEndian, uint16(tg[0]))
			binary.Write(&b, binary.LittleEndian, uint16(tg[1]))
			binary.Write(&b, binary.LittleEndian, uint32(1))
			binary.Write(&b, binary.LittleEndian, tg[2])
		}
		binary.Write(&b, binary.LittleEndian, uint32(0))
		b.Write(make([]byte, 32))
		return b.Bytes()
	}()
	// Two pages: the second directory is the first's copy, after it.
	two := append([]byte(nil), page...)
	second := append([]byte(nil), page[8:]...)
	off := uint32(len(two))
	binary.LittleEndian.PutUint32(two[8+2+9*12:], off)                     // the first page's next directory
	binary.LittleEndian.PutUint32(second[2+5*12+8:], off+uint32(2+9*12+4)) // the second page's strip
	two = append(two, second...)

	tess := tesseractPages(helper("tesseract", "echo", "{file}"))
	text, err := tess.ExtractType(context.Background(), "image/tiff", two)
	if err != nil || strings.Count(text, "eight weeks") != 2 || !strings.Contains(text, "--- page 2 ---") {
		t.Fatalf("tesseract by pages: %q %v", text, err)
	}

	var images []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string
			Messages []struct {
				Content []struct {
					Type     string
					Text     string
					ImageURL struct{ URL string } `json:"image_url"`
				}
			}
		}
		json.NewDecoder(r.Body).Decode(&req)
		c := req.Messages[0].Content
		if r.Header.Get("Authorization") != "Bearer k" || req.Model != "vision-1" || len(c) != 2 || !strings.Contains(c[0].Text, "Transcribe") {
			w.WriteHeader(400)
			return
		}
		images = append(images, c[1].ImageURL.URL[:22])
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"Page %d: no saddle anaesthesia."}}]}`, len(images))
	}))
	defer model.Close()
	v := Vision(model.URL, "k", "vision-1")
	text, err = v.ExtractType(context.Background(), "image/tiff", two)
	if err != nil || !strings.Contains(text, "Page 2: no saddle") || len(images) != 2 || images[0] != "data:image/png;base64," {
		t.Fatalf("model by pages: %q %v %v", text, err, images)
	}

	broken := helper("pdftotext", "fail")
	broken.Types = []string{"image/tiff"}
	text, by, err := Extractors{broken, v}.Extract(context.Background(), "image/tiff", two)
	if err != nil || by != "model:vision-1" || text == "" {
		t.Errorf("in turn: %q %s %v", text, by, err)
	}
}

// The real thing, where the programs are installed (CI installs them on Linux): a typed note drawn by Poppler, made a
// Group 4 fax by libtiff, and read back by tesseract page by page.
func TestTesseractReadsAFax(t *testing.T) {
	var paths []string
	for _, p := range []string{"tesseract", "pdftoppm", "ppm2tiff", "tiffcp"} {
		path, err := exec.LookPath(p)
		if err != nil {
			t.Skipf("%s is not installed", p)
		}
		paths = append(paths, path)
	}
	dir := t.TempDir()
	content := "BT /F1 24 Tf 72 640 Td (She completed eight weeks of therapy.) Tj ET"
	pdf := fmt.Sprintf("%%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n"+
		"3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >> endobj\n"+
		"4 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n5 0 obj << /Length %d >> stream\n%s\nendstream endobj\n"+
		"trailer << /Root 1 0 R >>\n%%%%EOF\n", len(content), content)
	os.WriteFile(filepath.Join(dir, "note.pdf"), []byte(pdf), 0o600)
	run := func(args ...string) {
		if b, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, b)
		}
	}
	run(paths[1], "-mono", "-r", "200", "-singlefile", filepath.Join(dir, "note.pdf"), filepath.Join(dir, "note"))
	run(paths[2], filepath.Join(dir, "note.pbm"), filepath.Join(dir, "note.tif"))
	run(paths[3], "-c", "g4", filepath.Join(dir, "note.tif"), filepath.Join(dir, "fax.tif"))
	fax, _ := os.ReadFile(filepath.Join(dir, "fax.tif"))
	x, err := Lookup("tesseract")
	if err != nil {
		t.Fatal(err)
	}
	text, err := x.ExtractType(context.Background(), "image/tiff", fax)
	if err != nil || !strings.Contains(strings.Join(strings.Fields(text), " "), "eight weeks of therapy") {
		t.Fatalf("%q %v", text, err)
	}
}
