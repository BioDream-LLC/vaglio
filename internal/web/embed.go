// Package web holds the built console, embedded so that Vaglio deploys as one file.
//
// dist is committed so that `go build` works on a checkout without Node. Rebuild it with `make web`. A pattern that
// matches nothing is a compile error, which is wanted: a server without its own interface should not build.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist/index.html dist/assets
var embedded embed.FS

// Assets returns the built console.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // "dist" is a literal that the embed directive above guarantees exists
	}
	return sub
}
