// Package faxmodem receives faxes on a fax modem: a phone line plugged into a Class 2 or Class 2.0 (EIA-592) fax
// modem, such as the USB ones still sold for exactly this. The modem does the fax protocol (T.30) itself; Vaglio
// answers the call, takes each page's image (ITU T.4, one- or two-dimensional), and keeps the fax as a PDF whose
// pages are those images, decoded by any PDF reader (CCITTFaxDecode), for the fax inbox.
//
// It asks the modem for T.4 only (no T.6, no error correction), so every page's lines can be counted from their
// end-of-line codes without decoding the image. Written from the Class 2 and Class 2.0 command sets; tested against a
// stand-in modem that sends pages libtiff encoded, read back through Poppler. Not tested with a modem on a line.
package faxmodem

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Config is how to talk to the modem.
type Config struct {
	Class     string // "2.0" (default) or "2"
	ID        string // this fax's number, as the caller's machine shows it (20 characters at most)
	Rings     int    // rings before answering, 1 by default
	FillOrder string // the bit order of the modem's page data: "lsb" (the first bit sent in each byte's low bit), "msb", or "auto"
}

// Page is one received page.
type Page struct {
	Data       []byte // T.4 data, first bit in each byte's high bit
	Fine       bool   // 196 lines per inch (else 98)
	Width      int    // pixels: 1728, 2048 or 2432
	TwoD       bool   // T.4 two-dimensional (MR) coding
	Lines      int    // scan lines, counted from the end-of-line codes
	BadQuality bool   // the modem reported the page received with errors
}

// Fax is one call's pages and who sent them.
type Fax struct {
	From  string // the sending machine's ID (its fax number, usually)
	Pages []Page
}

// Session talks to one modem over rw (a serial port, or a stand-in in a test).
type Session struct {
	cfg Config
	rw  io.ReadWriter
	in  *bufio.Reader
	two bool // Class 2.0
}

// NewSession wraps the modem's port.
func NewSession(rw io.ReadWriter, cfg Config) *Session {
	if cfg.Class == "" {
		cfg.Class = "2.0"
	}
	if cfg.Rings <= 0 {
		cfg.Rings = 1
	}
	if cfg.FillOrder == "" {
		cfg.FillOrder = "auto"
	}
	return &Session{cfg: cfg, rw: rw, in: bufio.NewReader(rw), two: cfg.Class == "2.0"}
}

func (s *Session) send(cmd string) error {
	_, err := io.WriteString(s.rw, cmd+"\r")
	return err
}

// line reads one response line, skipping blank ones and the command's echo.
func (s *Session) line(ctx context.Context) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		l, err := s.in.ReadString('\n')
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "AT") {
			return l, nil
		}
		if err != nil {
			return "", err
		}
	}
}

// command sends a command and reads to its OK, returning the lines before it. ERROR is an error.
func (s *Session) command(ctx context.Context, cmd string) ([]string, error) {
	if err := s.send(cmd); err != nil {
		return nil, err
	}
	var got []string
	for {
		l, err := s.line(ctx)
		if err != nil {
			return got, err
		}
		switch l {
		case "OK":
			return got, nil
		case "ERROR":
			return got, fmt.Errorf("the modem refused %s", cmd)
		}
		got = append(got, l)
	}
}

// Init puts the modem into fax mode and says what it may receive: fine resolution, up to 14,400 bit/s, A4/Letter width,
// any length, T.4 one- or two-dimensional, no error correction.
func (s *Session) Init(ctx context.Context) error {
	if _, err := s.command(ctx, "ATZ"); err != nil {
		return err
	}
	s.command(ctx, "ATE0")
	class := "AT+FCLASS=2.0"
	caps := "AT+FCC=1,5,0,2,1,0,0,0"
	id := "AT+FLI=\"" + quote(s.cfg.ID) + "\""
	if !s.two {
		class, caps, id = "AT+FCLASS=2", "AT+FDCC=1,5,0,2,1,0,0,0", "AT+FLID=\""+quote(s.cfg.ID)+"\""
	}
	if _, err := s.command(ctx, class); err != nil {
		return fmt.Errorf("not a Class %s fax modem: %w", s.cfg.Class, err)
	}
	if _, err := s.command(ctx, caps); err != nil {
		return err
	}
	if _, err := s.command(ctx, "AT+FCR=1"); err != nil {
		return err
	}
	if s.cfg.ID != "" {
		s.command(ctx, id)
	}
	if s.two {
		s.command(ctx, "AT+FNR=1,1,1,0") // report the negotiation; older modems refuse it, which costs nothing
	}
	return nil
}

func quote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' {
			return -1
		}
		return r
	}, s)
	if len(s) > 20 {
		s = s[:20]
	}
	return s
}

// Wait waits for a call and answers it after the configured rings, then receives the fax.
func (s *Session) Wait(ctx context.Context) (*Fax, error) {
	rings := 0
	for {
		l, err := s.line(ctx)
		if err != nil {
			return nil, err
		}
		if l == "RING" || strings.HasPrefix(l, "RING ") {
			if rings++; rings >= s.cfg.Rings {
				return s.receive(ctx)
			}
		}
	}
}

// params are the session's negotiated parameters (+FCS, or +FDCS in Class 2): vr, br, wd, ln, df, ...
type params struct{ vr, wd, df int }

func parseParams(l string) params {
	_, v, _ := strings.Cut(l, ":")
	f := strings.Split(v, ",")
	n := func(i int) int {
		if i >= len(f) {
			return 0
		}
		x, _ := strconv.ParseInt(strings.TrimSpace(f[i]), 16, 0) // Class 2.0 reports in hexadecimal; the small values read the same
		return int(x)
	}
	return params{vr: n(0), wd: n(2), df: n(4)}
}

func hangup(l string) bool {
	return strings.HasPrefix(l, "+FHS") || strings.HasPrefix(l, "+FHNG") || l == "NO CARRIER" || l == "ERROR"
}

// receive answers and reads every page.
func (s *Session) receive(ctx context.Context) (*Fax, error) {
	fax := &Fax{}
	var p params
	session := func(lines []string) {
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "+FCI:"), strings.HasPrefix(l, "+FTSI:"):
				_, id, _ := strings.Cut(l, ":")
				fax.From = strings.TrimSpace(strings.Trim(strings.TrimSpace(id), `"`))
			case strings.HasPrefix(l, "+FCS:"), strings.HasPrefix(l, "+FDCS:"):
				p = parseParams(l)
			}
		}
	}
	lines, err := s.command(ctx, "ATA")
	session(lines)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if hangup(l) {
			return nil, fmt.Errorf("the call ended before a page: %s", l)
		}
	}
	for {
		if err := s.send("AT+FDR"); err != nil {
			return fax, err
		}
		var pre []string
		connected := false
		for !connected {
			l, err := s.line(ctx)
			if err != nil {
				return fax, err
			}
			switch {
			case l == "CONNECT":
				connected = true
			case l == "OK" || hangup(l):
				session(pre)
				if len(fax.Pages) == 0 {
					return nil, fmt.Errorf("the call ended with no page: %s", l)
				}
				return fax, nil // the sender is done
			default:
				pre = append(pre, l)
			}
		}
		session(pre)
		if p.df > 1 {
			return fax, errors.New("the sender chose a coding other than T.4, which this modem was not offered")
		}
		s.rw.Write([]byte{0x12}) // DC2: ready for the page's data
		data, err := s.pageData(ctx)
		if err != nil {
			return fax, err
		}
		pg := Page{Data: s.order(data), Fine: p.vr == 1, Width: width(p.wd), TwoD: p.df == 1}
		pg.Lines = lines4(pg.Data)
		// After the data: the page's status, whether more follow, OK.
		more := true
		for {
			l, err := s.line(ctx)
			if err != nil {
				return fax, err
			}
			if l == "OK" {
				break
			}
			switch {
			case strings.HasPrefix(l, "+FPS:"), strings.HasPrefix(l, "+FPTS:"):
				_, v, _ := strings.Cut(l, ":")
				ppr, _ := strconv.Atoi(strings.TrimSpace(strings.Split(v, ",")[0]))
				pg.BadQuality = ppr != 1
			case strings.HasPrefix(l, "+FET:"):
				_, v, _ := strings.Cut(l, ":")
				more = strings.TrimSpace(v) != "2"
			}
		}
		fax.Pages = append(fax.Pages, pg)
		if !more {
			// Past the last page: one more AT+FDR lets the modem end the call (+FHS / +FHNG, then OK).
			s.command(ctx, "AT+FDR")
			return fax, nil
		}
	}
}

func width(wd int) int {
	switch wd {
	case 1:
		return 2048
	case 2:
		return 2432
	}
	return 1728
}

// pageData reads a page's data to DLE ETX, undoing DLE shielding: DLE DLE is one DLE, and in Class 2.0 DLE SUB is two.
func (s *Session) pageData(ctx context.Context) ([]byte, error) {
	var out bytes.Buffer
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return nil, errors.New("the page did not end")
		}
		b, err := s.in.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0x10 {
			out.WriteByte(b)
			if out.Len() > 16<<20 {
				return nil, errors.New("a page over 16 MB")
			}
			continue
		}
		c, err := s.in.ReadByte()
		if err != nil {
			return nil, err
		}
		switch c {
		case 0x10:
			out.WriteByte(0x10)
		case 0x1a:
			out.Write([]byte{0x10, 0x10})
		case 0x03:
			return out.Bytes(), nil
		}
	}
}

// order puts the page's bits first-bit-high, the order PDF and TIFF (FillOrder 1) expect. Modems disagree on the order
// they send, so "auto" reads the data both ways and keeps the one with more end-of-line codes: every line starts with
// one, and read backwards they all but vanish.
func (s *Session) order(data []byte) []byte {
	rev := make([]byte, len(data))
	for i, b := range data {
		rev[i] = reverse[b]
	}
	switch s.cfg.FillOrder {
	case "lsb":
		return rev
	case "msb":
		return data
	}
	if lines4(rev) > lines4(data) {
		return rev
	}
	return data
}

var reverse = func() (t [256]byte) {
	for i := range t {
		b := byte(i)
		var r byte
		for j := 0; j < 8; j++ {
			r = r<<1 | b&1
			b >>= 1
		}
		t[i] = r
	}
	return
}()

// lines4 counts a T.4 page's lines: its end-of-line codes (eleven or more zeros then a one), less the six that end the
// page (RTC). In two-dimensional coding each EOL has a tag bit after it, which does not change the count.
func lines4(data []byte) int {
	zeros, eols := 0, 0
	for _, b := range data {
		for bit := 7; bit >= 0; bit-- {
			if b>>bit&1 == 0 {
				zeros++
				continue
			}
			if zeros >= 11 {
				eols++
			}
			zeros = 0
		}
	}
	// Each line begins with an EOL, and RTC is six more at the end.
	if eols > 6 {
		return eols - 6
	}
	return eols
}
