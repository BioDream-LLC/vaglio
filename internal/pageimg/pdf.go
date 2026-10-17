package pageimg

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"regexp"
	"strconv"

	"github.com/biodream-llc/vaglio/internal/ccitt"
)

// A PDF's images, read without drawing its pages: each image XObject, in the order the file holds them, which for
// scans and faxes (one image a page) is the pages' order.

var objRe = regexp.MustCompile(`(\d+)\s+(\d+)\s+obj\b`)

type pdfDoc struct {
	raw  []byte
	objs map[int]int // object number: offset just after "obj"
}

func pdfPages(raw []byte) ([]Page, error) {
	if bytes.Contains(raw, []byte("/Encrypt")) {
		return nil, errors.New("an encrypted PDF is not read here")
	}
	d := &pdfDoc{raw: raw, objs: map[int]int{}}
	for _, m := range objRe.FindAllSubmatchIndex(raw, -1) {
		n, _ := strconv.Atoi(string(raw[m[2]:m[3]]))
		d.objs[n] = m[1] // a later copy (an incremental update) replaces an earlier one
	}
	var pages []Page
	var skipped []string
	seen := map[int]bool{}
	for _, m := range objRe.FindAllSubmatchIndex(raw, -1) {
		n, _ := strconv.Atoi(string(raw[m[2]:m[3]]))
		if seen[n] || d.objs[n] != m[1] {
			continue
		}
		seen[n] = true
		p := &parser{b: raw, i: m[1]}
		v := p.value()
		dict, ok := v.(map[string]any)
		if !ok || dict["Subtype"] != name("Image") {
			continue
		}
		data, ok := d.stream(p, dict)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("image %d: its data could not be found", n))
			continue
		}
		page, err := d.image(dict, data)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("image %d: %v", n, err))
			continue
		}
		if page.Data != nil {
			pages = append(pages, page)
		}
		if len(pages) == MaxPages {
			break
		}
	}
	if len(pages) == 0 {
		if len(skipped) > 0 {
			return nil, fmt.Errorf("%w (%s)", errNoImages, skipped[0])
		}
		return nil, errNoImages
	}
	return pages, nil
}

// stream is the stream data after a dictionary, by its Length (followed when it is a reference), else to endstream.
func (d *pdfDoc) stream(p *parser, dict map[string]any) ([]byte, bool) {
	p.space()
	if !bytes.HasPrefix(p.b[p.i:], []byte("stream")) {
		return nil, false
	}
	start := p.i + len("stream")
	if start < len(p.b) && p.b[start] == '\r' {
		start++
	}
	if start < len(p.b) && p.b[start] == '\n' {
		start++
	}
	n := -1
	switch l := d.resolve(dict["Length"]).(type) {
	case float64:
		n = int(l)
	}
	if n >= 0 && start+n <= len(p.b) {
		return p.b[start : start+n], true
	}
	end := bytes.Index(p.b[start:], []byte("endstream"))
	if end < 0 {
		return nil, false
	}
	return bytes.TrimRight(p.b[start:start+end], "\r\n"), true
}

func (d *pdfDoc) resolve(v any) any {
	r, ok := v.(ref)
	if !ok {
		return v
	}
	at, ok := d.objs[int(r)]
	if !ok {
		return nil
	}
	return (&parser{b: d.raw, i: at}).value()
}

func (d *pdfDoc) image(dict map[string]any, data []byte) (Page, error) {
	w, _ := d.resolve(dict["Width"]).(float64)
	h, _ := d.resolve(dict["Height"]).(float64)
	if w < 16 || h < 16 {
		return Page{}, nil // a logo, a rule, a mask too small to be a page: not one
	}
	filters := list(d.resolve(dict["Filter"]))
	parms := list(d.resolve(dict["DecodeParms"]))
	for i, f := range filters {
		var pm map[string]any
		if i < len(parms) {
			pm, _ = d.resolve(parms[i]).(map[string]any)
		}
		last := i == len(filters)-1
		switch f {
		case name("FlateDecode"), name("Fl"):
			z, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				return Page{}, err
			}
			out, err := io.ReadAll(io.LimitReader(z, 256<<20))
			if err != nil && len(out) == 0 {
				return Page{}, err
			}
			if out, err = unpredict(out, pm); err != nil {
				return Page{}, err
			}
			data = out
		case name("ASCIIHexDecode"), name("AHx"):
			clean := bytes.Map(func(r rune) rune {
				if r == '>' || r == ' ' || r == '\n' || r == '\r' || r == '\t' {
					return -1
				}
				return r
			}, data)
			if len(clean)%2 == 1 {
				clean = append(clean, '0')
			}
			out := make([]byte, len(clean)/2)
			if _, err := hex.Decode(out, clean); err != nil {
				return Page{}, err
			}
			data = out
		case name("ASCII85Decode"), name("A85"):
			data = bytes.TrimSuffix(bytes.TrimSpace(data), []byte("~>"))
			out := make([]byte, len(data))
			n, _, err := ascii85.Decode(out, data, true)
			if err != nil {
				return Page{}, err
			}
			data = out[:n]
		case name("DCTDecode"), name("DCT"):
			if !last {
				return Page{}, errors.New("a filter after DCTDecode")
			}
			return Page{"image/jpeg", data}, nil
		case name("CCITTFaxDecode"), name("CCF"):
			p := ccitt.Params{Columns: 1728}
			if v, ok := pm["K"].(float64); ok {
				p.K = int(v)
			}
			if v, ok := pm["Columns"].(float64); ok {
				p.Columns = int(v)
			}
			if v, ok := pm["Rows"].(float64); ok {
				p.Rows = int(v)
			} else {
				p.Rows = int(h)
			}
			p.EncodedByteAlign = pm["EncodedByteAlign"] == true
			img, err := ccitt.Decode(data, p)
			if err != nil {
				return Page{}, err
			}
			if invert(d.resolve(dict["Decode"])) {
				for i := range img.Pix {
					img.Pix[i] = 255 - img.Pix[i]
				}
			}
			return encodePNG(img)
		default:
			return Page{}, fmt.Errorf("the %v filter is not read here", f)
		}
	}
	return d.raster(dict, data, int(w), int(h))
}

// raster is unfiltered image samples: 1-bit (an image mask or grey), 8-bit grey, RGB or CMYK.
func (d *pdfDoc) raster(dict map[string]any, data []byte, w, h int) (Page, error) {
	bpc, _ := d.resolve(dict["BitsPerComponent"]).(float64)
	mask := dict["ImageMask"] == true
	if mask {
		bpc = 1
	}
	comps := 1
	switch cs := d.resolve(dict["ColorSpace"]).(type) {
	case name:
		switch cs {
		case "DeviceRGB", "RGB", "CalRGB":
			comps = 3
		case "DeviceCMYK", "CMYK":
			comps = 4
		}
	case []any:
		if len(cs) > 0 && cs[0] == name("ICCBased") {
			if s, ok := d.resolve(cs[1]).(map[string]any); ok {
				if n, ok := s["N"].(float64); ok {
					comps = int(n)
				}
			}
		} else if len(cs) > 0 && cs[0] == name("Indexed") {
			return Page{}, errors.New("an indexed-colour image is not read here")
		}
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	inv := invert(d.resolve(dict["Decode"]))
	switch {
	case bpc == 1 && comps == 1:
		row := (w + 7) / 8
		if len(data) < row*h {
			return Page{}, errors.New("short image data")
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				on := data[y*row+x/8]>>(7-x%8)&1 == 1 // 1 is white in DeviceGray; for a mask 0 paints, so the same
				if on != inv {
					img.Pix[y*img.Stride+x] = 255
				}
			}
		}
	case bpc == 8:
		if len(data) < w*h*comps {
			return Page{}, errors.New("short image data")
		}
		for i := 0; i < w*h; i++ {
			s := data[i*comps : i*comps+comps]
			var g uint8
			switch comps {
			case 1:
				g = s[0]
			case 3:
				g = uint8((299*int(s[0]) + 587*int(s[1]) + 114*int(s[2])) / 1000)
			case 4:
				g = uint8(255 - min(255, (299*int(s[0])+587*int(s[1])+114*int(s[2]))/1000+int(s[3])))
			default:
				return Page{}, fmt.Errorf("%d colour components", comps)
			}
			if inv {
				g = 255 - g
			}
			img.Pix[i] = g
		}
	default:
		return Page{}, fmt.Errorf("%v bits a component is not read here", bpc)
	}
	return encodePNG(img)
}

func invert(v any) bool {
	a, ok := v.([]any)
	return ok && len(a) >= 2 && a[0] == float64(1) && a[1] == float64(0)
}

func list(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	default:
		return []any{x}
	}
}

// unpredict undoes a PNG predictor (Predictor 10 to 15), the one Flate images use.
func unpredict(data []byte, pm map[string]any) ([]byte, error) {
	pred, _ := pm["Predictor"].(float64)
	if pred < 10 {
		if pred == 2 {
			return nil, errors.New("TIFF predictor 2 is not read here")
		}
		return data, nil
	}
	colors, bpc, cols := 1.0, 8.0, 1.0
	if v, ok := pm["Colors"].(float64); ok {
		colors = v
	}
	if v, ok := pm["BitsPerComponent"].(float64); ok {
		bpc = v
	}
	if v, ok := pm["Columns"].(float64); ok {
		cols = v
	}
	bpp := max(int(colors*bpc+7)/8, 1)
	row := int(colors*bpc*cols+7) / 8
	var out []byte
	prev := make([]byte, row)
	for i := 0; i+row+1 <= len(data); i += row + 1 {
		ft, cur := data[i], append([]byte(nil), data[i+1:i+1+row]...)
		for x := range cur {
			var a, b, c byte
			if x >= bpp {
				a, c = cur[x-bpp], prev[x-bpp]
			}
			b = prev[x]
			switch ft {
			case 1:
				cur[x] += a
			case 2:
				cur[x] += b
			case 3:
				cur[x] += byte((int(a) + int(b)) / 2)
			case 4:
				cur[x] += paeth(a, b, c)
			}
		}
		out = append(out, cur...)
		prev = cur
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// The values of a PDF file, as far as image dictionaries need them.
type (
	name string
	ref  int
)

type parser struct {
	b []byte
	i int
}

func (p *parser) space() {
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '%' {
			for p.i < len(p.b) && p.b[p.i] != '\n' && p.b[p.i] != '\r' {
				p.i++
			}
			continue
		}
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' && c != '\f' && c != 0 {
			return
		}
		p.i++
	}
}

func delim(c byte) bool {
	return bytes.IndexByte([]byte(" \t\r\n\f\x00/<>[]()%{}"), c) >= 0
}

func (p *parser) value() any {
	return p.valueDepth(0)
}

func (p *parser) valueDepth(depth int) any {
	p.space()
	if p.i >= len(p.b) || depth > 32 {
		return nil
	}
	switch c := p.b[p.i]; {
	case c == '<' && p.i+1 < len(p.b) && p.b[p.i+1] == '<':
		p.i += 2
		d := map[string]any{}
		for {
			p.space()
			if p.i+1 >= len(p.b) {
				return d
			}
			if p.b[p.i] == '>' && p.b[p.i+1] == '>' {
				p.i += 2
				return d
			}
			k, ok := p.valueDepth(depth + 1).(name)
			if !ok {
				return d
			}
			d[string(k)] = p.valueDepth(depth + 1)
		}
	case c == '[':
		p.i++
		var a []any
		for {
			p.space()
			if p.i >= len(p.b) {
				return a
			}
			if p.b[p.i] == ']' {
				p.i++
				return a
			}
			a = append(a, p.valueDepth(depth+1))
		}
	case c == '/':
		p.i++
		s := p.i
		for p.i < len(p.b) && !delim(p.b[p.i]) {
			p.i++
		}
		return name(p.b[s:p.i])
	case c == '(':
		depthParen := 0
		for p.i < len(p.b) {
			switch p.b[p.i] {
			case '\\':
				p.i++
			case '(':
				depthParen++
			case ')':
				depthParen--
			}
			p.i++
			if depthParen == 0 {
				return "string"
			}
		}
		return "string"
	case c == '<':
		e := bytes.IndexByte(p.b[p.i:], '>')
		if e < 0 {
			p.i = len(p.b)
		} else {
			p.i += e + 1
		}
		return "string"
	default:
		s := p.i
		for p.i < len(p.b) && !delim(p.b[p.i]) {
			p.i++
		}
		tok := string(p.b[s:p.i])
		if p.i == s {
			p.i++
			return nil
		}
		switch tok {
		case "true":
			return true
		case "false":
			return false
		case "null":
			return nil
		}
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return tok
		}
		// "n g R" is a reference.
		save := p.i
		p.space()
		s2 := p.i
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
		if p.i > s2 {
			p.space()
			if p.i < len(p.b) && p.b[p.i] == 'R' && (p.i+1 == len(p.b) || delim(p.b[p.i+1])) {
				p.i++
				return ref(int(f))
			}
		}
		p.i = save
		return f
	}
}
