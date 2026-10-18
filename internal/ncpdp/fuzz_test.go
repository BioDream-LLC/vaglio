package ncpdp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FuzzReadMessage: no message, however malformed, makes the reader panic or loop; what it reads, it reads as a request.
func FuzzReadMessage(f *testing.F) {
	files, _ := filepath.Glob("../../testdata/ncpdp/*")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := ReadMessage(bytes.NewReader(raw))
		if err != nil {
			return
		}
		_ = ReadRequest(m)
	})
}

// An element's text in many pieces (CDATA sections) is read in linear time: it took seconds at the size limit when
// each piece was appended to a string.
func TestManyTextPieces(t *testing.T) {
	in := "<a>" + strings.Repeat("<![CDATA[x]]>", 600000) + "</a>"
	t0 := time.Now()
	n, err := Parse(strings.NewReader(in))
	if err != nil || len(n.Text) != 600000 {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("%v for %d bytes", d, len(in))
	}
}
