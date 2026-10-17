package notice

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/letter"
)

func patient(addr map[string]any, lang string) map[string]any {
	p := map[string]any{"resourceType": "Patient", "id": "p1",
		"name": []any{map[string]any{"use": "official", "given": []any{"Ana", "M."}, "family": "Reyes"}}}
	if addr != nil {
		p["address"] = []any{map[string]any{"use": "old", "line": []any{"1 Old Rd"}, "city": "X", "state": "IL", "postalCode": "60601"}, addr}
	}
	if lang != "" {
		p["communication"] = []any{map[string]any{"language": map[string]any{"coding": []any{map[string]any{"code": lang}}}, "preferred": true}}
	}
	return map[string]any{"entry": []any{map[string]any{"fullUrl": "http://x/Patient/p1", "resource": p}}}
}

func TestMemberAddress(t *testing.T) {
	home := map[string]any{"use": "home", "line": []any{"12 Elm St", "Apt 3"}, "city": "Springfield", "state": "il", "postalCode": "62701"}
	a, lang, err := MemberAddress(patient(home, "es-MX"), "Patient/p1")
	if err != nil || a.Name != "Ana M. Reyes" || a.State != "IL" || len(a.Lines) != 2 || lang != "es" {
		t.Fatalf("%+v %s %v", a, lang, err)
	}
	for name, addr := range map[string]map[string]any{
		"no zip":      {"line": []any{"12 Elm St"}, "city": "Springfield", "state": "IL"},
		"abroad":      {"line": []any{"1 Rue"}, "city": "Paris", "state": "75", "postalCode": "75001", "country": "FR"},
		"ended":       {"line": []any{"12 Elm St"}, "city": "S", "state": "IL", "postalCode": "62701", "period": map[string]any{"end": "2001-01-01"}},
		"no street":   {"city": "Springfield", "state": "IL", "postalCode": "62701"},
		"state spelt": {"line": []any{"12 Elm St"}, "city": "S", "state": "Illinois", "postalCode": "62701"},
	} {
		if _, _, err := MemberAddress(patient(addr, ""), "Patient/p1"); err == nil || !strings.HasPrefix(err.Error(), "not mailed") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := MemberAddress(map[string]any{}, "Patient/p1"); err == nil {
		t.Error("no Patient at all")
	}
}

// The PDF reads back, through Poppler's pdftotext when it is here, with the address in the window, every paragraph,
// the Spanish letters' accents and the receipt on the page.
func TestPDF(t *testing.T) {
	long := strings.Repeat("Una revisión hecha por personas que no tomaron esta decisión. ", 60)
	l := &letter.Letter{Title: "Decisión de autorización previa: No aprobada", Date: "17 de octubre de 2026", Receipt: "VG-AAAA-BBBB-CCCC-DDDD",
		Sections: []letter.Section{{Heading: "Por qué", Paragraphs: []string{long}, Quote: "MRI is covered (when) one \\ is met."},
			{Heading: "Cómo pedirla", Items: []string{"Por fax: 1-800-555-0101", "En línea: https://plan.example/appeals"}, After: []string{"¿Preguntas?"}}}}
	from := Address{Name: "Example Health Plan", Lines: []string{"PO Box 1000"}, City: "Springfield", State: "IL", Zip: "62701"}
	to := Address{Name: "Ana Reyes", Lines: []string{"12 Elm St"}, City: "Springfield", State: "IL", Zip: "62702"}
	pdf := PDF(l, Layout{Window: true, From: from, To: to})
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("not a PDF")
	}
	if n := bytes.Count(pdf, []byte("/Type /Page ")); n < 2 {
		t.Errorf("%d pages for a long letter", n)
	}
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext is not installed: the PDF's text is not read back here")
	}
	path := filepath.Join(t.TempDir(), "n.pdf")
	os.WriteFile(path, pdf, 0o600)
	out, err := exec.Command(bin, "-enc", "UTF-8", path, "-").CombinedOutput()
	if err != nil {
		t.Fatalf("pdftotext: %v %s", err, out)
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	for _, want := range []string{"Ana Reyes", "12 Elm St", "Springfield, IL 62702", "PO Box 1000", "No aprobada", "Cómo pedirla",
		"Por fax: 1-800-555-0101", "¿Preguntas?", "MRI is covered (when) one \\ is met.", "VG-AAAA-BBBB-CCCC-DDDD",
		"personas que no tomaron esta decisión"} {
		if !strings.Contains(text, want) {
			t.Errorf("the PDF's text lacks %q", want)
		}
	}
	if !strings.Contains(strings.ReplaceAll(text, " ", ""), "1/2") {
		t.Error("no page number")
	}
}

func TestLoadConfig(t *testing.T) {
	none := func(string) (string, error) { return "", nil }
	if c, s, err := LoadConfig("../../examples/notices.json", none); err != nil || s.Name() != "folder" || len(c.Events) != 4 {
		t.Fatalf("the example: %v", err)
	}
	from := `"from":{"name":"P","lines":["PO Box 1"],"city":"C","state":"IL","zip":"62701"}`
	for _, bad := range []string{
		`{"events":[],"service":"folder","folder":"x",` + from + `}`,
		`{"events":["denied"],"service":"fax",` + from + `}`,
		`{"events":["refused"],"service":"folder","folder":"x",` + from + `}`,
		`{"events":["denied"],"service":"folder","folder":"x","from":{"name":"P"}}`,
		`{"events":["denied"],"service":"lob","lobKeyEnv":"NONE",` + from + `}`,
		`{"events":["denied"],"service":"folder","folder":"x","colour":true,` + from + `}`,
	} {
		p := filepath.Join(t.TempDir(), "n.json")
		os.WriteFile(p, []byte(bad), 0o600)
		if _, _, err := LoadConfig(p, none); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
