// Package library carries the policy library inside the program, so a server decides with it as published (serve
// -library, on by default). A plan that reads a rule differently overrides the policy: its own signed version, of the
// same id, in its policy directory, decides instead, and removing that file puts the published one back.
package library

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

//go:embed *.vaglio
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
