// Package ccitt decodes fax images: ITU-T T.4 (Group 3, one- and two-dimensional) and T.6 (Group 4), as fax services
// deliver them in TIFF and PDF files, into bitmaps another program (a model reading the page, the console) can use.
// Written from the recommendations' code tables and coding rules; tested against libtiff's own encodings.
package ccitt

import (
	"errors"
	"fmt"
	"image"
	"image/color"
)

// Params say how the data is coded, with the names PDF's CCITTFaxDecode gives them.
type Params struct {
	K                int  // < 0: T.6; 0: T.4 one-dimensional; > 0: T.4 two-dimensional (each line tagged)
	Columns          int  // pixels per line, 1728 by default
	Rows             int  // lines, 0 when not known: decoding stops at the data's end or its end-of-page codes
	EncodedByteAlign bool // each line (T.6) or each end-of-line code (T.4) starts on a byte
	BlackIs1         bool // decoded 1 bits are black; PDF's default is false (0 is black) - only for the caller
	LSBFirst         bool // the first bit is in each byte's low bit (TIFF FillOrder 2)
}

// maxPixels bounds one decoded image.
const maxPixels = 64 << 20

type bits struct {
	data []byte
	pos  int // in bits
	lsb  bool
}

func (b *bits) left() int { return len(b.data)*8 - b.pos }

func (b *bits) peek(n int) (int, bool) {
	if b.left() < n {
		return 0, false
	}
	v := 0
	for i := 0; i < n; i++ {
		p := b.pos + i
		by := b.data[p/8]
		var bit byte
		if b.lsb {
			bit = by >> (p % 8) & 1
		} else {
			bit = by >> (7 - p%8) & 1
		}
		v = v<<1 | int(bit)
	}
	return v, true
}

func (b *bits) align() {
	if b.pos%8 != 0 {
		b.pos += 8 - b.pos%8
	}
}

// code reads the next code of a table, as a string of bits matched against it.
func (b *bits) code(tables ...map[string]int) (int, error) {
	s := make([]byte, 0, 13)
	for len(s) < 13 {
		v, ok := b.peek(1)
		if !ok {
			return 0, errEnd
		}
		b.pos++
		s = append(s, byte('0'+v))
		for _, t := range tables {
			if r, ok := t[string(s)]; ok {
				return r, nil
			}
		}
	}
	return 0, fmt.Errorf("no code %s at bit %d", s, b.pos-len(s))
}

var errEnd = errors.New("the data ended")

// run reads one colour's run: make-up codes then a terminating code.
func (b *bits) run(white bool) (int, error) {
	total := 0
	t := blackCodes
	if white {
		t = whiteCodes
	}
	for {
		n, err := b.code(t, extended)
		if err != nil {
			return 0, err
		}
		total += n
		if n < 64 {
			return total, nil
		}
		if total > maxPixels {
			return 0, errors.New("a run past any line")
		}
	}
}

// eol skips fill bits and end-of-line codes, and says how many it skipped.
func (b *bits) eol(align bool) int {
	n := 0
	for {
		save := b.pos
		if align {
			b.align()
		}
		zeros := 0
		for {
			v, ok := b.peek(1)
			if !ok || v != 0 {
				break
			}
			b.pos++
			zeros++
		}
		if v, ok := b.peek(1); ok && v == 1 && zeros >= 11 {
			b.pos++
			n++
			continue
		}
		b.pos = save
		return n
	}
}

// Decode decodes the data into a bitmap, black on white.
func Decode(data []byte, p Params) (*image.Gray, error) {
	if p.Columns <= 0 {
		p.Columns = 1728
	}
	if p.Columns > 1<<15 {
		return nil, errors.New("wider than any fax")
	}
	b := &bits{data: data, lsb: p.LSBFirst}
	w := p.Columns
	ref := make([]bool, w) // the line above: all white before the first
	var rows [][]bool
	for p.Rows == 0 || len(rows) < p.Rows {
		if len(rows)*w > maxPixels {
			return nil, errors.New("an image past any fax")
		}
		twoD := p.K < 0
		if p.K >= 0 {
			if n := b.eol(p.EncodedByteAlign); n >= 2 && len(rows) > 0 {
				break // RTC: the page's end (six EOLs; two are enough to know)
			}
			if p.K > 0 {
				tag, ok := b.peek(1)
				if !ok {
					break
				}
				b.pos++
				twoD = tag == 0
			}
		} else {
			if p.EncodedByteAlign {
				b.align()
			}
			if v, ok := b.peek(24); ok && v == 0x001001 {
				break // EOFB
			}
		}
		if b.left() < 2 {
			break
		}
		line := make([]bool, w)
		var err error
		if twoD {
			err = decode2D(b, ref, line)
		} else {
			err = decode1D(b, line)
		}
		if err != nil {
			if errors.Is(err, errEnd) && len(rows) > 0 && p.Rows == 0 {
				break
			}
			return nil, fmt.Errorf("line %d: %w", len(rows)+1, err)
		}
		rows = append(rows, line)
		ref = line
	}
	if len(rows) == 0 {
		return nil, errors.New("no lines")
	}
	img := image.NewGray(image.Rect(0, 0, w, len(rows)))
	for y, line := range rows {
		for x, black := range line {
			if !black {
				img.SetGray(x, y, color.Gray{255})
			}
		}
	}
	return img, nil
}

func fill(line []bool, from, to int, black bool) {
	from, to = max(from, 0), min(to, len(line))
	for i := from; i < to; i++ {
		line[i] = black
	}
}

func decode1D(b *bits, line []bool) error {
	a, white := 0, true
	for a < len(line) {
		n, err := b.run(white)
		if err != nil {
			return err
		}
		fill(line, a, a+n, !white)
		a += n
		white = !white
	}
	return nil
}

// changes is where the reference line's colour changes, after a0, to the colour wanted (b1), and the change after it (b2).
func changes(ref []bool, a0 int, black bool) (int, int) {
	w := len(ref)
	b1 := w
	for i := max(a0+1, 0); i < w; i++ {
		prev := false
		if i > 0 {
			prev = ref[i-1]
		}
		if ref[i] != prev && ref[i] == black {
			b1 = i
			break
		}
	}
	b2 := w
	for i := b1 + 1; i < w; i++ {
		if ref[i] != ref[i-1] {
			b2 = i
			break
		}
	}
	return b1, b2
}

func decode2D(b *bits, ref, line []bool) error {
	w := len(line)
	a0, black := -1, false
	for a0 < w {
		mode, err := b.code(modeCodes)
		if err != nil {
			return err
		}
		start := max(a0, 0)
		b1, b2 := changes(ref, a0, !black)
		if a0 < 0 {
			// At the line's start, a0 sits before the first pixel: b1 may be the first pixel itself.
			b1, b2 = changesFrom(ref, !black)
		}
		switch mode {
		case modePass:
			fill(line, start, b2, black)
			a0 = b2
		case modeHorizontal:
			r1, err := b.run(!black)
			if err != nil {
				return err
			}
			r2, err := b.run(black)
			if err != nil {
				return err
			}
			fill(line, start, start+r1, black)
			fill(line, start+r1, start+r1+r2, !black)
			a0 = start + r1 + r2
		case modeExt:
			return errors.New("an extension (uncompressed) mode, which fax services do not send")
		default:
			d := map[int]int{modeV0: 0, modeVR1: 1, modeVR2: 2, modeVR3: 3, modeVL1: -1, modeVL2: -2, modeVL3: -3}[mode]
			a1 := b1 + d
			if a1 < start || a1 > w {
				return fmt.Errorf("a vertical code past the line (%d)", a1)
			}
			fill(line, start, a1, black)
			a0 = a1
			black = !black
		}
	}
	return nil
}

// changesFrom is changes for a0 before the line's first pixel.
func changesFrom(ref []bool, black bool) (int, int) {
	w := len(ref)
	b1 := w
	for i := 0; i < w; i++ {
		prev := false
		if i > 0 {
			prev = ref[i-1]
		}
		if ref[i] != prev && ref[i] == black {
			b1 = i
			break
		}
	}
	b2 := w
	for i := b1 + 1; i < w; i++ {
		if ref[i] != ref[i-1] {
			b2 = i
			break
		}
	}
	return b1, b2
}
