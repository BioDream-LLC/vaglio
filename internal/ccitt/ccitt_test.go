package ccitt

import (
	"bytes"
	"encoding/binary"
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

// libtiff codes the picture each way a fax can be coded; each decodes back to the picture, pixel for pixel.
func TestDecodeLibtiffsCodings(t *testing.T) {
	tiffcp, err := exec.LookPath("tiffcp")
	if err != nil {
		t.Skip("libtiff's tiffcp is not installed: there is nothing here to code the pictures with")
	}
	for _, c := range []struct {
		scheme string
		k      int
		lsb    bool
	}{{"g3:1d", 0, false}, {"g3:2d", 1, false}, {"g4", -1, false}, {"g3:1d", 0, true}, {"g4", -1, true}} {
		dir := t.TempDir()
		in, out := filepath.Join(dir, "in.tif"), filepath.Join(dir, "out.tif")
		os.WriteFile(in, tiff(picture()), 0o600)
		args := []string{"-c", c.scheme, "-r", "100000"}
		if c.lsb {
			args = append(args, "-f", "lsb2msb")
		}
		if b, err := exec.Command(tiffcp, append(args, in, out)...).CombinedOutput(); err != nil {
			t.Fatalf("tiffcp: %v %s", err, b)
		}
		raw, _ := os.ReadFile(out)
		img, err := Decode(strip(t, raw), Params{K: c.k, Columns: W, Rows: H, LSBFirst: c.lsb})
		if err != nil {
			t.Fatalf("%s: %v", c.scheme, err)
		}
		want := picture()
		wrong := 0
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				if (img.GrayAt(x, y).Y == 0) != want[y][x] {
					wrong++
				}
			}
		}
		if wrong != 0 {
			t.Errorf("%s lsb %v: %d pixels differ", c.scheme, c.lsb, wrong)
		}
	}
}

// Without the row count, a T.4 page ends at its RTC.
func TestDecodeToRTC(t *testing.T) {
	// One all-white line of 8 pixels (EOL, white 8 = 10011), then RTC.
	bits := "000000000001" + "10011" + "000000000001000000000001000000000001000000000001000000000001000000000001"
	for len(bits)%8 != 0 {
		bits += "0"
	}
	var data []byte
	for i := 0; i < len(bits); i += 8 {
		var c byte
		for _, r := range bits[i : i+8] {
			c = c<<1 | byte(r-'0')
		}
		data = append(data, c)
	}
	img, err := Decode(data, Params{K: 0, Columns: 8})
	if err != nil || img.Bounds().Dy() != 1 {
		t.Fatalf("%v %v", img.Bounds(), err)
	}
	if _, err := Decode([]byte{0, 0, 0}, Params{K: 0, Columns: 8, Rows: 1}); err == nil {
		t.Error("garbage decoded")
	}
}
