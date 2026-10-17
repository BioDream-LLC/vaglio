package pageimg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"

	"github.com/biodream-llc/vaglio/internal/ccitt"
)

type ifd struct {
	tags map[uint16][]uint32
	next uint32
}

func readIFD(raw []byte, bo binary.ByteOrder, off uint32) (*ifd, error) {
	if int(off)+2 > len(raw) {
		return nil, errors.New("a TIFF directory past the file's end")
	}
	n := int(bo.Uint16(raw[off:]))
	if int(off)+2+12*n+4 > len(raw) {
		return nil, errors.New("a TIFF directory past the file's end")
	}
	d := &ifd{tags: map[uint16][]uint32{}}
	size := map[uint16]int{1: 1, 3: 2, 4: 4, 16: 8}
	for i := 0; i < n; i++ {
		e := raw[int(off)+2+12*i:]
		tag, typ, count := bo.Uint16(e), bo.Uint16(e[2:]), bo.Uint32(e[4:])
		sz := size[typ]
		if sz == 0 || count > 1<<20 {
			continue // rationals, strings and the like: nothing read here needs them
		}
		at := e[8:12]
		if sz*int(count) > 4 {
			p := bo.Uint32(e[8:])
			if int(p)+sz*int(count) > len(raw) {
				return nil, errors.New("a TIFF value past the file's end")
			}
			at = raw[p : int(p)+sz*int(count)]
		}
		vals := make([]uint32, count)
		for j := range vals {
			switch sz {
			case 1:
				vals[j] = uint32(at[j])
			case 2:
				vals[j] = uint32(bo.Uint16(at[2*j:]))
			case 4:
				vals[j] = bo.Uint32(at[4*j:])
			case 8:
				vals[j] = uint32(bo.Uint64(at[8*j:]))
			}
		}
		d.tags[tag] = vals
	}
	d.next = bo.Uint32(raw[int(off)+2+12*n:])
	return d, nil
}

func (d *ifd) one(tag uint16, def uint32) uint32 {
	if v := d.tags[tag]; len(v) > 0 {
		return v[0]
	}
	return def
}

// tiffPages decodes each page (directory) of a TIFF.
func tiffPages(raw []byte) ([]Page, error) {
	if len(raw) < 8 {
		return nil, errors.New("not a TIFF")
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if raw[0] == 'M' {
		bo = binary.BigEndian
	}
	var pages []Page
	seen := map[uint32]bool{}
	for off := bo.Uint32(raw[4:]); off != 0 && !seen[off] && len(pages) < MaxPages; {
		seen[off] = true
		d, err := readIFD(raw, bo, off)
		if err != nil {
			return pages, err
		}
		img, err := tiffImage(raw, d)
		if err != nil {
			return pages, fmt.Errorf("page %d: %w", len(pages)+1, err)
		}
		p, err := encodePNG(img)
		if err != nil {
			return pages, err
		}
		pages = append(pages, p)
		off = d.next
	}
	if len(pages) == 0 {
		return nil, errNoImages
	}
	return pages, nil
}

func tiffImage(raw []byte, d *ifd) (image.Image, error) {
	w, h := int(d.one(256, 0)), int(d.one(257, 0))
	if w <= 0 || h <= 0 || w*h > 64<<20 {
		return nil, fmt.Errorf("a %dx%d page", w, h)
	}
	comp, photo, bps := d.one(259, 1), d.one(262, 0), d.one(258, 1)
	rowsPer := int(d.one(278, uint32(h)))
	offs, counts := d.tags[273], d.tags[279]
	if len(offs) == 0 || len(offs) != len(counts) {
		return nil, errors.New("no strips")
	}
	lsb := d.one(266, 1) == 2
	out := image.NewGray(image.Rect(0, 0, w, h))
	y := 0
	for i, o := range offs {
		if int(o)+int(counts[i]) > len(raw) {
			return nil, errors.New("a strip past the file's end")
		}
		data := raw[o : o+counts[i]]
		rows := min(rowsPer, h-y)
		switch comp {
		case 3, 4:
			p := ccitt.Params{Columns: w, Rows: rows, LSBFirst: lsb}
			if comp == 4 {
				p.K = -1
			} else {
				opts := d.one(292, 0)
				if opts&1 != 0 {
					p.K = 1
				}
				p.EncodedByteAlign = opts&4 != 0
			}
			img, err := ccitt.Decode(data, p)
			if err != nil {
				return nil, err
			}
			// CCITT codes black and white runs whatever the photometric says: copy as decoded.
			for yy := 0; yy < img.Bounds().Dy() && y+yy < h; yy++ {
				copy(out.Pix[(y+yy)*out.Stride:(y+yy)*out.Stride+w], img.Pix[yy*img.Stride:yy*img.Stride+w])
			}
		case 1, 32773:
			if comp == 32773 {
				data = unpackBits(data)
			}
			if err := uncompressed(out, data, y, rows, w, int(bps), photo, lsb); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("TIFF compression %d is not read here", comp)
		}
		y += rows
	}
	return out, nil
}

func uncompressed(out *image.Gray, data []byte, y0, rows, w, bps int, photo uint32, lsb bool) error {
	switch bps {
	case 1:
		row := (w + 7) / 8
		for r := 0; r < rows; r++ {
			for x := 0; x < w; x++ {
				i := r*row + x/8
				if i >= len(data) {
					return nil
				}
				b := data[i]
				var bit byte
				if lsb {
					bit = b >> (x % 8) & 1
				} else {
					bit = b >> (7 - x%8) & 1
				}
				black := (bit == 1) == (photo == 0) // 0 WhiteIsZero: 1 is black; 1 BlackIsZero: 0 is black
				if !black {
					out.SetGray(x, y0+r, color.Gray{255})
				}
			}
		}
	case 8:
		for r := 0; r < rows; r++ {
			for x := 0; x < w; x++ {
				i := r*w + x
				if i >= len(data) {
					return nil
				}
				v := data[i]
				if photo == 0 {
					v = 255 - v
				}
				out.SetGray(x, y0+r, color.Gray{v})
			}
		}
	default:
		return fmt.Errorf("%d bits a sample is not read here", bps)
	}
	return nil
}

// unpackBits undoes PackBits (TIFF compression 32773).
func unpackBits(in []byte) []byte {
	var out []byte
	for i := 0; i < len(in); {
		n := int(int8(in[i]))
		i++
		switch {
		case n >= 0:
			end := min(i+n+1, len(in))
			out = append(out, in[i:end]...)
			i = end
		case n != -128:
			if i < len(in) {
				for j := 0; j < 1-n; j++ {
					out = append(out, in[i])
				}
				i++
			}
		}
	}
	return out
}
