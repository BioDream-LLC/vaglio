package faxmodem

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const W, H = 1728, 240

// picture is a test page: bars, a checkerboard, a diagonal and a frame, one bit per pixel, 1 for black.
func picture() [][]bool {
	img := make([][]bool, H)
	for y := range img {
		img[y] = make([]bool, W)
		for x := range img[y] {
			switch {
			case y < 4 || y >= H-4 || x < 4 || x >= W-4:
				img[y][x] = true
			case y > 20 && y < 60 && (x/37)%2 == 0:
				img[y][x] = true
			case y >= 80 && y < 160 && x >= 100 && x < 900 && ((x/16)+(y/16))%2 == 0:
				img[y][x] = true
			case y >= 170 && y < 230 && (x-1000)/3 == y-170:
				img[y][x] = true
			}
		}
	}
	return img
}

// tiff is the picture as an uncompressed bilevel TIFF, one strip, for libtiff to encode.
func tiff(img [][]bool) []byte {
	row := (W + 7) / 8
	pix := make([]byte, row*H)
	for y := range img {
		for x, black := range img[y] {
			if black { // PhotometricInterpretation 0: 1 is black
				pix[y*row+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("II*\x00")
	binary.Write(&b, binary.LittleEndian, uint32(8))
	tags := [][3]uint32{{256, 4, W}, {257, 4, H}, {258, 3, 1}, {259, 3, 1}, {262, 3, 0}, {273, 4, 0}, {277, 3, 1}, {278, 4, H}, {279, 4, uint32(len(pix))}}
	dataAt := uint32(8 + 2 + len(tags)*12 + 4)
	binary.Write(&b, binary.LittleEndian, uint16(len(tags)))
	for _, t := range tags {
		v := t[2]
		if t[0] == 273 {
			v = dataAt
		}
		binary.Write(&b, binary.LittleEndian, uint16(t[0]))
		binary.Write(&b, binary.LittleEndian, uint16(t[1]))
		binary.Write(&b, binary.LittleEndian, uint32(1))
		binary.Write(&b, binary.LittleEndian, v)
	}
	binary.Write(&b, binary.LittleEndian, uint32(0))
	b.Write(pix)
	return b.Bytes()
}

// strip reads the one strip of a little- or big-endian TIFF.
func strip(t *testing.T, raw []byte) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if raw[0] == 'M' {
		bo = binary.BigEndian
	}
	ifd := bo.Uint32(raw[4:])
	n := int(bo.Uint16(raw[ifd:]))
	var off, count uint32
	for i := 0; i < n; i++ {
		e := raw[int(ifd)+2+12*i:]
		tag, typ := bo.Uint16(e), bo.Uint16(e[2:])
		v := bo.Uint32(e[8:])
		if typ == 3 {
			v = uint32(bo.Uint16(e[8:]))
		}
		switch tag {
		case 273:
			off = v
		case 279:
			count = v
		}
	}
	if count == 0 {
		t.Fatal("no strip")
	}
	return raw[off : off+count]
}

// encode has libtiff (tiffcp) code the picture as fax data, one- or two-dimensional, with an end-of-line code before
// every line, and adds the RTC a fax page ends with.
func encode(t *testing.T, twoD bool) []byte {
	tiffcp, err := exec.LookPath("tiffcp")
	if err != nil {
		t.Skip("libtiff's tiffcp is not installed: the page data it would encode is not made here")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.tif"), filepath.Join(dir, "out.tif")
	os.WriteFile(in, tiff(picture()), 0o600)
	scheme := "g3:1d"
	if twoD {
		scheme = "g3:2d"
	}
	if b, err := exec.Command(tiffcp, "-c", scheme, "-r", "100000", in, out).CombinedOutput(); err != nil {
		t.Fatalf("tiffcp: %v %s", err, b)
	}
	raw, _ := os.ReadFile(out)
	data := append([]byte(nil), strip(t, raw)...)
	// RTC: six EOLs (each followed by a 1 in two-dimensional coding), packed after the data.
	var bits []byte
	for i := 0; i < 6; i++ {
		bits = append(bits, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1)
		if twoD {
			bits = append(bits, 1)
		}
	}
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	for i := 0; i < len(bits); i += 8 {
		var c byte
		for _, x := range bits[i : i+8] {
			c = c<<1 | x
		}
		data = append(data, c)
	}
	return data
}

// standIn plays a Class 2.0 (or 2) fax modem receiving a call: it answers the host's commands, rings, and sends the
// pages with DLE shielding, in the bit order lsb says.
func standIn(t *testing.T, conn net.Conn, class string, pages [][]byte, twoD, lsb bool) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	// One stream, as a modem's serial line is: every write whole and in turn (the rings come from another goroutine,
	// and a second RING landing inside a page's data once corrupted it).
	var mu sync.Mutex
	write := func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		conn.Write(b)
	}
	say := func(lines ...string) {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("\r\n" + l + "\r\n")
		}
		write([]byte(b.String()))
	}
	prefix := func(two, one string) string {
		if class == "2.0" {
			return two
		}
		return one
	}
	df := "0"
	if twoD {
		df = "1"
	}
	next := 0
	rang := false
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		cmd, err := r.ReadString('\r')
		if err != nil {
			return
		}
		cmd = strings.TrimSpace(cmd)
		switch {
		case cmd == "AT+FCR=1":
			say("OK")
			if !rang { // set up: the line rings
				rang = true
				go func() { time.Sleep(50 * time.Millisecond); say("RING", "RING") }()
			}
		case cmd == "ATA":
			say(prefix("+FCO", "+FCON"), prefix(`+FCI:"+1 555 010 0199"`, `+FTSI:"+1 555 010 0199"`), prefix("+FCS:1,5,0,2,"+df+",0,0,0", "+FDCS:1,5,0,2,"+df+",0,0,0"), "OK")
		case cmd == "AT+FDR":
			if next == len(pages) {
				say(prefix("+FHS:00", "+FHNG:0"), "OK")
				return
			}
			say("CONNECT")
			c := make([]byte, 1)
			if _, err := io.ReadFull(r, c); err != nil || c[0] != 0x12 {
				t.Errorf("no DC2 before the page's data: %v %x", err, c)
				return
			}
			var w bytes.Buffer
			for _, b := range pages[next] {
				if lsb {
					b = reverse[b]
				}
				if b == 0x10 {
					w.WriteByte(0x10)
				}
				w.WriteByte(b)
			}
			w.Write([]byte{0x10, 0x03})
			write(w.Bytes())
			next++
			et := "0"
			if next == len(pages) {
				et = "2"
			}
			say(prefix("+FPS:1,0,0,0,0", "+FPTS:1"), "+FET:"+et, "OK")
		default:
			say("OK")
		}
	}
}

// receiveVia runs a session against the stand-in and returns the fax.
func receiveVia(t *testing.T, class string, pages [][]byte, twoD, lsb bool, fill string) *Fax {
	a, b := net.Pipe()
	go standIn(t, b, class, pages, twoD, lsb)
	s := NewSession(a, Config{Class: class, ID: "+1 555 010 0100", FillOrder: fill})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fax, err := s.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return fax
}

// The received pages, laid out as a PDF, render through Poppler to the picture libtiff encoded, pixel for pixel or
// near it: one- and two-dimensional coding, Class 2.0 and Class 2, bit order told and found.
func TestReceive(t *testing.T) {
	for _, c := range []struct {
		class     string
		twoD, lsb bool
		fill      string
	}{{"2.0", false, false, "auto"}, {"2.0", true, true, "auto"}, {"2", false, true, "lsb"}, {"2", true, false, "msb"}} {
		t.Run(fmt.Sprintf("class %s 2-D %v lsb %v %s", c.class, c.twoD, c.lsb, c.fill), func(t *testing.T) {
			page := encode(t, c.twoD)
			fax := receiveVia(t, c.class, [][]byte{page, page}, c.twoD, c.lsb, c.fill)
			if fax.From != "+1 555 010 0199" || len(fax.Pages) != 2 {
				t.Fatalf("from %q, %d pages", fax.From, len(fax.Pages))
			}
			p := fax.Pages[0]
			if p.Lines != H || p.Width != W || !p.Fine || p.TwoD != c.twoD || p.BadQuality {
				t.Fatalf("%d lines, %d wide, fine %v, 2-D %v", p.Lines, p.Width, p.Fine, p.TwoD)
			}
			if !bytes.Equal(p.Data, page) {
				t.Fatal("the page's data is not what was sent")
			}
			render(t, PDF(fax))
		})
	}
}

// render has Poppler draw the first page at the image's own size and compares it with the picture.
func render(t *testing.T, pdf []byte) {
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Log("Poppler's pdftoppm is not installed: the PDF is not drawn here")
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fax.pdf")
	os.WriteFile(path, pdf, 0o600)
	if b, err := exec.Command(pdftoppm, "-f", "1", "-l", "1", "-mono", "-scale-to-x", fmt.Sprint(W), "-scale-to-y", fmt.Sprint(H), path, filepath.Join(dir, "p")).CombinedOutput(); err != nil {
		t.Fatalf("pdftoppm: %v %s", err, b)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "p*.pbm"))
	if len(files) != 1 {
		t.Fatalf("%v", files)
	}
	raw, _ := os.ReadFile(files[0])
	// P4 header: "P4\n1728 240\n", then the bits, 1 for black.
	parts := bytes.SplitN(raw, []byte("\n"), 3)
	if len(parts) != 3 || string(parts[0]) != "P4" || string(parts[1]) != fmt.Sprintf("%d %d", W, H) {
		t.Fatalf("header %q", raw[:20])
	}
	bits, img, row := parts[2], picture(), (W+7)/8
	// Scaled to the image's own size, a drawn pixel may land a pixel away from its source, so each is compared with
	// its source and the pixels around it: a decoding fault garbles whole runs, a resampling shift moves edges by one.
	at := func(x, y int) (bool, bool) {
		if x < 0 || y < 0 || x >= W || y >= H {
			return false, false
		}
		return img[y][x], true
	}
	wrong := 0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			drawn := bits[y*row+x/8]>>(7-x%8)&1 == 1
			near := false
			for dy := -1; dy <= 1 && !near; dy++ {
				for dx := -1; dx <= 1 && !near; dx++ {
					if v, ok := at(x+dx, y+dy); ok && v == drawn {
						near = true
					}
				}
			}
			if !near {
				wrong++
			}
		}
	}
	if wrong > 0 {
		t.Errorf("%d drawn pixels match nothing near them in the picture sent", wrong)
	}
}

func TestLines(t *testing.T) {
	if n := lines4([]byte{0x00, 0x10, 0x00, 0x01}); n != 2 { // two EOLs and no RTC
		t.Errorf("%d", n)
	}
}
