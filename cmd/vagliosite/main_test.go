package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The site builds from the repository, every internal link resolves, and no page says what the site must not
// (check runs inside build).
func TestBuild(t *testing.T) {
	out := t.TempDir()
	n, err := build("../..", out, "https://vaglio.health")
	if err != nil {
		t.Fatal(err)
	}
	arts, _ := filepath.Glob("../../site/articles/*.md")
	if want := len(arts) + 3; n != want {
		t.Errorf("%d pages, want %d (home, download, learn and every article)", n, want)
	}
	home, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(home), "{{") || strings.Contains(string(home), "<script") {
		t.Error("the home page has a placeholder left, or a script")
	}
}
