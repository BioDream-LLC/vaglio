package faxmodem

import (
	"bytes"
	"fmt"
)

// PDF is the fax as a PDF: each page one image, its T.4 data as received, which PDF readers decode themselves
// (CCITTFaxDecode, K 0 for one-dimensional coding, 1 for two). The page is sized from the image: 1728 pixels across
// 8.5 inches' width as fax machines scan it, 98 or 196 lines an inch down.
func PDF(f *Fax) []byte {
	var b bytes.Buffer
	var offsets []int
	start := func() int {
		offsets = append(offsets, b.Len())
		return len(offsets)
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(f.Pages)
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+3*i)
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n", start())
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", start(), kids, n)
	for i, p := range f.Pages {
		lpi := 98.0
		if p.Fine {
			lpi = 196
		}
		w := 8.5 * 72 * float64(p.Width) / 1728
		h := float64(p.Lines) / lpi * 72
		if h < 1 {
			h = 1
		}
		content, img := 4+3*i, 5+3*i
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im%d %d 0 R >> >> /Contents %d 0 R >>\nendobj\n",
			start(), w, h, i, img, content)
		cs := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im%d Do Q\n", w, h, i)
		fmt.Fprintf(&b, "%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", start(), len(cs), cs)
		k := 0
		if p.TwoD {
			k = 1
		}
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 1 "+
			"/Filter /CCITTFaxDecode /DecodeParms << /K %d /Columns %d /Rows %d /EndOfBlock false >> /Length %d >>\nstream\n",
			start(), p.Width, max(p.Lines, 1), k, p.Width, max(p.Lines, 1), len(p.Data))
		b.Write(p.Data)
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
