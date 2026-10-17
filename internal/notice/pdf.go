package notice

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/biodream-llc/vaglio/internal/letter"
)

// A letter as a PDF for the mail: US Letter pages in the PDF standard fonts (Helvetica, which every PDF reader and
// printer has, so nothing is embedded), text laid out and wrapped here. Nothing but the standard library: a mail house
// or a print-and-mail service takes the file as it is.

const (
	pageW, pageH = 612.0, 792.0 // US Letter, in points
	margin       = 66.0
	bodySize     = 10.5
	leading      = 14.5
)

// Address is a postal address, as the envelope's window shows it.
type Address struct {
	Name    string   `json:"name"`
	Lines   []string `json:"lines"` // street lines
	City    string   `json:"city"`
	State   string   `json:"state"`
	Zip     string   `json:"zip"`
	Country string   `json:"country,omitempty"` // US when empty
}

func (a Address) block() []string {
	out := append([]string{a.Name}, a.Lines...)
	return append(out, strings.TrimSpace(a.City+", "+a.State+" "+a.Zip))
}

// Layout says what goes on the first page besides the letter.
type Layout struct {
	// Window prints the return address and the member's address where a #10 double-window envelope shows them. Off
	// when the mail service prints its own address page (Lob's insert_blank_page).
	Window   bool
	From, To Address
}

// font is one of the standard fonts used.
type font struct {
	res  string // its name in the page resources
	bold bool
}

var (
	regular = font{"F1", false}
	bold    = font{"F2", true}
	oblique = font{"F3", false}
)

// line is one line of text placed on a page.
type line struct {
	f    font
	size float64
	x, y float64
	text string
}

// PDF lays a letter out and writes it.
func PDF(l *letter.Letter, lay Layout) []byte {
	var pages [][]line
	var cur []line
	y := pageH - margin
	newPage := func() {
		pages = append(pages, cur)
		cur, y = nil, pageH-margin
	}
	room := func(h float64) {
		if y-h < margin+24 {
			newPage()
		}
	}
	put := func(f font, size, x float64, text string) {
		cur = append(cur, line{f, size, x, y, text})
	}
	para := func(f font, size, indent float64, text, bullet string) {
		for i, ln := range wrap(text, f, size, pageW-2*margin-indent) {
			room(leading)
			if i == 0 && bullet != "" {
				put(f, size, margin+indent-11, bullet)
			}
			put(f, size, margin+indent, ln)
			y -= size * 1.38
		}
	}

	if lay.Window {
		// The return address in the upper window, the member's in the lower, as #10 double-window envelopes place them.
		y = pageH - 45
		for _, s := range lay.From.block() {
			put(regular, 9, 50, s)
			y -= 11
		}
		y = pageH - 150
		for _, s := range lay.To.block() {
			put(regular, 10.5, 72, s)
			y -= 13
		}
		y = pageH - 270
	}
	para(bold, 15, 0, l.Title, "")
	y -= 2
	para(regular, bodySize, 0, l.Date, "")
	y -= 8
	for _, s := range l.Sections {
		if s.Heading != "" {
			room(40)
			y -= 4
			para(bold, 12, 0, s.Heading, "")
			y -= 1
		}
		for _, p := range s.Paragraphs {
			para(regular, bodySize, 0, p, "")
			y -= 4
		}
		if s.Quote != "" {
			para(oblique, bodySize, 18, "\u201c"+s.Quote+"\u201d", "")
			y -= 4
		}
		for _, it := range s.Items {
			para(regular, bodySize, 18, it, "\u2022")
		}
		if len(s.Items) > 0 {
			y -= 4
		}
		for _, p := range s.After {
			para(regular, bodySize, 0, p, "")
			y -= 4
		}
		y -= 4
	}
	if l.Quoted != "" {
		para(oblique, 9, 0, l.Quoted, "")
	}
	pages = append(pages, cur)

	// The receipt and the page number on every page: a page that comes apart from the rest still says what it is.
	for i := range pages {
		foot := fmt.Sprintf("%d / %d", i+1, len(pages))
		if l.Receipt != "" {
			foot = l.Receipt + "    " + foot
		}
		pages[i] = append(pages[i], line{regular, 8, pageW - margin - width(foot, regular, 8), margin - 20, foot})
	}
	return write(pages)
}

// write is the PDF file: catalog, page tree, the three fonts, then each page and its content stream.
func write(pages [][]line) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(pages)
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 6+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	for _, name := range []string{"Helvetica", "Helvetica-Bold", "Helvetica-Oblique"} {
		obj("<< /Type /Font /Subtype /Type1 /BaseFont /" + name + " /Encoding /WinAnsiEncoding >>")
	}
	for i, p := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Resources << /Font << /F1 3 0 R /F2 4 0 R /F3 5 0 R >> >> /Contents %d 0 R >>",
			pageW, pageH, 7+2*i))
		var cs bytes.Buffer
		for _, ln := range p {
			fmt.Fprintf(&cs, "BT /%s %g Tf %.2f %.2f Td (%s) Tj ET\n", ln.f.res, ln.size, ln.x, ln.y, pdfString(ln.text))
		}
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(cs.Bytes())
		zw.Close()
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", len(offsets), z.Len())
		b.Write(z.Bytes())
		b.WriteString("\nendstream\nendobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

// winAnsi maps the characters outside Latin-1 that letters use to their WinAnsiEncoding bytes.
var winAnsi = map[rune]byte{'\u2018': 0x91, '\u2019': 0x92, '\u201c': 0x93, '\u201d': 0x94, '\u2022': 0x95, '\u2013': 0x96,
	'\u2014': 0x97, '\u2026': 0x85, '\u20ac': 0x80}

// encode is text as WinAnsiEncoding bytes; anything it cannot show becomes "?".
func encode(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 0x20 && r < 0x7f, r >= 0xa0 && r <= 0xff:
			out = append(out, byte(r))
		case winAnsi[r] != 0:
			out = append(out, winAnsi[r])
		case r == '\t' || r == '\n':
			out = append(out, ' ')
		default:
			out = append(out, '?')
		}
	}
	return out
}

func pdfString(s string) string {
	var b strings.Builder
	for _, c := range encode(s) {
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 0x20 || c > 0x7e {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// helvetica are Helvetica's advance widths for ASCII 32 to 126, in thousandths of the font size (its AFM file).
var helvetica = [95]int{278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, 556, 556, 556,
	556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, 1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500,
	667, 556, 833, 722, 778, 667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, 333, 556, 556,
	500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, 556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500,
	334, 260, 334, 584}

// width is a string's width at a size. Bold is reckoned a tenth wider than regular, which never underestimates it, so a
// wrapped bold line never runs past the margin.
func width(s string, f font, size float64) float64 {
	w := 0
	for _, c := range encode(s) {
		switch {
		case c >= 32 && c <= 126:
			w += helvetica[c-32]
		case c >= 0xc0:
			w += 667 // accented capitals and lower case are no wider than this
		default:
			w += 556
		}
	}
	if f.bold {
		w = w * 11 / 10
	}
	return float64(w) * size / 1000
}

// wrap breaks text into lines no wider than max.
func wrap(text string, f font, size, max float64) []string {
	var out []string
	cur := ""
	for _, word := range strings.Fields(text) {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if width(try, f, size) <= max || cur == "" {
			cur = try
			for width(cur, f, size) > max && utf8.RuneCountInString(cur) > 1 { // one word wider than the line
				i := len(cur) - 1
				for i > 0 && (!utf8.RuneStart(cur[i]) || width(cur[:i], f, size) > max) {
					i--
				}
				out = append(out, cur[:i])
				cur = cur[i:]
			}
			continue
		}
		out = append(out, cur)
		cur = word
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
