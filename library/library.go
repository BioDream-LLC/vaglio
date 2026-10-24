// Package library carries the policy library inside the program, so a server decides with it as published (serve
// -library, on by default). A plan that reads a rule differently overrides the policy: its own signed version, of the
// same id, in its policy directory, decides instead, and removing that file puts the published one back.
package library

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"io/fs"
	"sort"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

//go:embed *.vaglio library.json sources/*.txt
var files embed.FS

// Policies parses every library policy. Each must pass its exhaustive check (TestLibrary also holds each to its source).
func Policies() (map[string]*policy.Policy, error) {
	names, err := fs.Glob(files, "*"+policy.Ext)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := map[string]*policy.Policy{}
	for _, n := range names {
		src, err := files.ReadFile(n)
		if err != nil {
			return nil, err
		}
		p, err := policy.Parse(n, string(src))
		if err != nil {
			return nil, fmt.Errorf("library %s: %w", n, err)
		}
		if p.Status == policy.Draft {
			return nil, fmt.Errorf("library %s: a draft; the library carries only policies in force", n)
		}
		if r := decide.Check(p); !r.OK() {
			return nil, fmt.Errorf("library %s: fails its check", n)
		}
		if _, dup := out[p.ID]; dup {
			return nil, fmt.Errorf("library %s: id %q twice", n, p.ID)
		}
		out[p.ID] = p
	}
	return out, nil
}

// Source is a library policy's file as published, header comments included.
func Source(id string) ([]byte, error) { return files.ReadFile(id + policy.Ext) }

// Document is the published coverage document a library policy was drafted from, as CMS numbers it.
type Document struct {
	Type    string `json:"type"`              // lcd or ncd
	ID      int    `json:"id"`                // the Coverage API's document id (lcdid, ncdid)
	Version int    `json:"version,omitempty"` // the version drafted from, when the copy says
}

// Display is the document as people cite it: L39240, or the NCD's id.
func (d Document) Display() string {
	if d.Type == "lcd" {
		return fmt.Sprintf("L%d", d.ID)
	}
	return fmt.Sprintf("NCD (id %d)", d.ID)
}

// Entry is what library.json says of one policy.
type Entry struct {
	File     string    `json:"file"`
	Source   string    `json:"source,omitempty"` // the source document's copy, sources/...
	Basis    string    `json:"basis"`
	Category string    `json:"category"`
	CMS      *Document `json:"cms,omitempty"`
}

// Entries are library.json's entries, by policy id.
func Entries() (map[string]Entry, error) {
	raw, err := files.ReadFile("library.json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Policies []Entry `json:"policies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]Entry{}
	for _, e := range doc.Policies {
		out[strings.TrimSuffix(e.File, policy.Ext)] = e
	}
	return out, nil
}

// SourceDocument is the copy of the document a library policy was drafted from, when the library carries one.
func SourceDocument(id string) ([]byte, error) {
	es, err := Entries()
	if err != nil {
		return nil, err
	}
	e, ok := es[id]
	if !ok || e.Source == "" {
		return nil, fmt.Errorf("library policy %s carries no copy of its source", id)
	}
	return files.ReadFile(e.Source)
}
