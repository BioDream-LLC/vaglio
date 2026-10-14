package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Extractor turns a document that is not text - a PDF, a scanned image - into text, by a program on this machine:
// pdftotext (Poppler) for PDFs, tesseract for images, or anything else that reads the document on standard input and
// writes its text on standard output. None is built in, and none is needed: without one, such documents are listed for
// the reviewer to read, as before.
//
// The program runs with the document on stdin, no shell, an empty environment beyond PATH, and a time limit, and its
// output is bounded. Patient data never leaves the machine for it. Text it produces goes through the same quote gate as
// any other, but an OCR'd quote is only as good as the OCR: the case says which documents were extracted, and by what,
// so the reviewer confirms against the original.
type Extractor struct {
	Name    string   // as the case shows it: "pdftotext", "tesseract"
	Path    string   // the program, resolved when the server starts
	Args    []string // its arguments; the document is on stdin
	Types   []string // the content types it reads: application/pdf, image/png ...
	Timeout time.Duration
}

// Extractors are the programs a server has, tried in order for a document's content type.
type Extractors []Extractor

// MaxInput bounds a document handed to an extractor.
const MaxInput = 20 << 20

// For is the extractor for a content type, if any.
func (xs Extractors) For(contentType string) *Extractor {
	for i := range xs {
		for _, t := range xs[i].Types {
			if t == contentType {
				return &xs[i]
			}
		}
	}
	return nil
}

// Extract runs the extractor over a document.
func (x *Extractor) Extract(ctx context.Context, raw []byte) (string, error) {
	if len(raw) > MaxInput {
		return "", fmt.Errorf("the document is over %d MB", MaxInput>>20)
	}
	t := x.Timeout
	if t <= 0 {
		t = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, x.Path, x.Args...)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "OMP_THREAD_LIMIT=1"}
	var out, errb limited
	out.max, errb.max = 4*MaxText, 4096
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s took longer than %s", x.Name, t)
		}
		return "", fmt.Errorf("%s failed: %v %s", x.Name, err, strings.TrimSpace(errb.String()))
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", errors.New(x.Name + " found no text in it")
	}
	return text, nil
}

// limited is a buffer that keeps at most max bytes and drops the rest.
type limited struct {
	bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		if len(p) > room {
			l.Buffer.Write(p[:room])
		} else {
			l.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// Known are the extractors a server can be told to use by name, with the arguments that make them read stdin and write
// stdout.
var Known = map[string]Extractor{
	"pdftotext": {Name: "pdftotext", Args: []string{"-layout", "-enc", "UTF-8", "-", "-"}, Types: []string{"application/pdf"}},
	"tesseract": {Name: "tesseract", Args: []string{"stdin", "stdout", "-l", "eng"},
		Types: []string{"image/png", "image/jpeg", "image/tiff", "image/gif", "image/bmp"}},
}

// Lookup resolves a known extractor's program on this machine.
func Lookup(name string) (Extractor, error) {
	x, ok := Known[name]
	if !ok {
		return Extractor{}, fmt.Errorf("%q is not an extractor this server knows: pdftotext or tesseract", name)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return Extractor{}, fmt.Errorf("%s is not installed here: %w", name, err)
	}
	x.Path = p
	return x, nil
}
