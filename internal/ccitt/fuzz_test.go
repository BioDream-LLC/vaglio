package ccitt

import "testing"

// FuzzDecode: fax data from outside, however corrupt, never panics the decoder or makes it run away with memory.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x00, 0x01, 0x59, 0x00, 0x14}, 0, 64, 0, false, false)
	f.Add([]byte{0x26, 0xa0, 0x00, 0x00, 0x08, 0x00, 0x80}, -1, 128, 16, true, false)
	f.Add([]byte{0x00, 0x80, 0x02, 0x00, 0x1d}, 4, 1728, 0, false, true)
	f.Fuzz(func(t *testing.T, data []byte, k, columns, rows int, align, lsb bool) {
		if columns < 1 || columns > 4096 || rows < 0 || rows > 4096 {
			return
		}
		img, err := Decode(data, Params{K: k, Columns: columns, Rows: rows, EncodedByteAlign: align, LSBFirst: lsb})
		if err == nil && img.Bounds().Dx() != columns {
			t.Fatalf("%d columns asked, %d decoded", columns, img.Bounds().Dx())
		}
	})
}
