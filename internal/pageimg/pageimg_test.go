package pageimg

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

func tool(t *testing.T, name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed: the file it would make is not made here", name)
	}
	return p
}

func run(t *testing.T, args ...string) {
	if b, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %v %s", args, err, b)
	}
}

// same checks a PNG page against the picture, pixel for pixel.
func same(t *testing.T, p Page) {
	t.Helper()
	if p.ContentType != "image/png" {
		t.Fatalf("%s", p.ContentType)
	}
	img, err := png.Decode(bytes.NewReader(p.Data))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := img.(*image.Gray)
	if !ok || g.Bounds().Dx() != W || g.Bounds().Dy() != H {
		t.Fatalf("%T %v", img, img.Bounds())
	}
	want, wrong := picture(), 0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if (g.GrayAt(x, y).Y < 128) != want[y][x] {
				wrong++
			}
		}
	}
	if wrong != 0 {
		t.Errorf("%d pixels differ", wrong)
	}
}

// A two-page TIFF libtiff writes in Group 4, Group 3 two-dimensional and PackBits: every page decodes to the picture.
func TestTIFF(t *testing.T) {
	tiffcp := tool(t, "tiffcp")
	dir := t.TempDir()
	in := filepath.Join(dir, "in.tif")
	os.WriteFile(in, tiff(picture()), 0o600)
	for _, c := range []string{"g4", "g3:2d", "packbits", "none"} {
		out := filepath.Join(dir, c+".tif")
		run(t, tiffcp, "-c", c, in, in, out) // two pages
		raw, _ := os.ReadFile(out)
		pages, err := Pages("image/tiff", raw)
		if err != nil || len(pages) != 2 {
			t.Fatalf("%s: %d pages, %v", c, len(pages), err)
		}
		same(t, pages[1])
	}
}

// A PDF libtiff's tiff2pdf makes of a Group 4 fax (its CCITT data copied in as it is) gives back the picture, as does
// one it makes with the image Flate-compressed.
func TestPDFFromTiff2pdf(t *testing.T) {
	tiffcp, tiff2pdf := tool(t, "tiffcp"), tool(t, "tiff2pdf")
	dir := t.TempDir()
	in, g4 := filepath.Join(dir, "in.tif"), filepath.Join(dir, "g4.tif")
	os.WriteFile(in, tiff(picture()), 0o600)
	run(t, tiffcp, "-c", "g4", in, in, g4)
	for _, flag := range []string{"", "-z"} {
		out := filepath.Join(dir, "out"+flag+".pdf")
		args := []string{tiff2pdf, "-o", out}
		if flag != "" {
			args = append(args, flag)
		}
		run(t, append(args, g4)...)
		raw, _ := os.ReadFile(out)
		pages, err := Pages("application/pdf", raw)
		if err != nil || len(pages) != 2 {
			t.Fatalf("tiff2pdf %s: %d pages, %v", flag, len(pages), err)
		}
		same(t, pages[0])
	}
}

func TestNotAPicture(t *testing.T) {
	if _, err := Pages("application/pdf", []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n%%EOF")); err == nil {
		t.Error("a PDF with no images gave pages")
	}
	if p, err := Pages("image/jpeg", []byte("\xff\xd8\xff\xe0")); err != nil || p[0].ContentType != "image/jpeg" {
		t.Error("a JPEG is not passed through")
	}
}
