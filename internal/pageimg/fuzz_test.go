package pageimg

import "testing"

// FuzzPages: a document from outside (a fax, an upload), however malformed, never panics the page splitter; what it
// gives is at most MaxPages pages.
func FuzzPages(f *testing.F) {
	f.Add("image/tiff", tiff(picture()))
	f.Add("application/pdf", []byte("%PDF-1.4\n1 0 obj << /Type /XObject /Subtype /Image /Width 8 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 8 >> stream\n\x00\x10\x20\x30\x40\x50\x60\x70\nendstream endobj\n%%EOF"))
	f.Add("image/gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
	f.Fuzz(func(t *testing.T, ct string, raw []byte) {
		pages, err := Pages(ct, raw)
		if err == nil && len(pages) > MaxPages {
			t.Fatalf("%d pages", len(pages))
		}
	})
}
