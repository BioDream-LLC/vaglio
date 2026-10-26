// Command vagliosite builds the vaglio.health website into a directory of static files.
//
// The pages are HTML fragments in site/pages and plain-words articles in site/articles (Markdown with a little front
// matter), wrapped in one layout here. The version comes from docs/release-notes.md and the screenshots from
// docs/assets/screens, so the site says what the repository says. The site carries no JavaScript: its pages are
// served under a policy that forbids scripts, and the menus open with CSS alone.
//
//	go run ./cmd/vagliosite -out dist-site
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	repo = "https://github.com/BioDream-LLC/vaglio"
	demo = "https://vaglio.perfuse.health:8443"
)

func main() {
	out := flag.String("out", "dist-site", "directory to write the site into (emptied first)")
	root := flag.String("root", ".", "the repository's root")
	base := flag.String("base", "https://vaglio.health", "the site's address, for the sitemap and canonical links")
	flag.Parse()
	n, err := build(*root, *out, *base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vagliosite:", err)
		os.Exit(1)
	}
	fmt.Printf("vagliosite: %d pages in %s\n", n, *out)
}

// article is one plain-words article: a tile on the home page and a page at /learn/SLUG/.
type article struct {
	slug, title, short, tile, area, icon, description string
	order                                             int
	body                                              string
	words                                             int
}

// areas groups the articles, in order, each with its menu label and a line under its heading.
var areas = []struct{ name, menu, id, intro string }{
	{"Start here", "Learn", "start", "What prior authorization is, why it is in the news, and what Vaglio has not done yet."},
	{"Deciding", "How it decides", "deciding", "How a request becomes an answer, and why that answer can always be shown."},
	{"Oversight", "Oversight", "oversight", "People stay in charge, and anyone can check what was decided."},
	{"Running it", "For health plans", "running", "How it fits a plan's systems, its servers and its policies."},
}

func build(root, out, base string) (int, error) {
	version, err := releaseVersion(root)
	if err != nil {
		return 0, err
	}
	arts, err := loadArticles(root)
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(out); err != nil {
		return 0, err
	}
	css, err := os.ReadFile(filepath.Join(root, "site/static/site.css"))
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(css)
	site := &site{root: root, out: out, base: base, version: version, arts: arts, cssVersion: hex.EncodeToString(sum[:4])}
	for _, p := range []struct{ src, path, title, desc string }{
		{"home.html", "/", "Vaglio: prior authorization decisions that show their work",
			"Vaglio is a source-available prior authorization decision engine by BioDream LLC. Every answer is a proof, and missing evidence never denies care."},
		{"download.html", "/download/", "Download Vaglio " + version,
			"Vaglio " + version + " for Linux, macOS and Windows: one program, checked before it runs, reproducible and attested."},
	} {
		frag, err := os.ReadFile(filepath.Join(root, "site/pages", p.src))
		if err != nil {
			return 0, err
		}
		if err := site.page(p.path, p.title, p.desc, site.fill(string(frag))); err != nil {
			return 0, err
		}
	}
	if err := site.page("/learn/", "Learn: Vaglio in plain words",
		"Long, plain-words articles on prior authorization and on how Vaglio decides, proves and records every answer.", site.learnIndex()); err != nil {
		return 0, err
	}
	for _, a := range arts {
		if err := site.page("/learn/"+a.slug+"/", a.title, a.description, site.articlePage(a)); err != nil {
			return 0, err
		}
	}
	nf, err := os.ReadFile(filepath.Join(root, "site/pages/404.html"))
	if err != nil {
		return 0, err
	}
	if err := site.write("404.html", site.layout("/404", "Page not found", "This page is not on vaglio.health.", site.fill(string(nf)))); err != nil {
		return 0, err
	}
	if err := site.assets(); err != nil {
		return 0, err
	}
	if err := site.sitemap(); err != nil {
		return 0, err
	}
	if err := site.check(); err != nil {
		return 0, err
	}
	return len(site.paths), nil
}

type site struct {
	root, out, base, version string
	cssVersion               string // the start of the stylesheet's SHA-256, so a changed one is never served from cache
	arts                     []article
	paths                    []string
}

var versionRE = regexp.MustCompile(`(?m)^# Vaglio (v\d+\.\d+\.\d+)\s*$`)

// releaseVersion is the version the release notes describe: the latest release.
func releaseVersion(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "docs/release-notes.md"))
	if err != nil {
		return "", err
	}
	m := versionRE.FindSubmatch(b)
	if m == nil {
		return "", errors.New("docs/release-notes.md: no \"# Vaglio vX.Y.Z\" heading")
	}
	return string(m[1]), nil
}

func loadArticles(root string) ([]article, error) {
	files, err := filepath.Glob(filepath.Join(root, "site/articles/*.md"))
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, a := range areas {
		known[a.name] = true
	}
	var arts []article
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if !strings.HasPrefix(s, "---\n") {
			return nil, fmt.Errorf("%s: no front matter", f)
		}
		end := strings.Index(s[4:], "\n---\n")
		if end < 0 {
			return nil, fmt.Errorf("%s: front matter not closed", f)
		}
		a := article{slug: strings.TrimSuffix(filepath.Base(f), ".md"), body: s[4+end+5:]}
		for _, l := range strings.Split(s[4:4+end], "\n") {
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"`)
			switch strings.TrimSpace(k) {
			case "title":
				a.title = v
			case "short":
				a.short = v
			case "tile":
				a.tile = v
			case "area":
				a.area = v
			case "icon":
				a.icon = v
			case "description":
				a.description = v
			case "order":
				fmt.Sscan(v, &a.order)
			}
		}
		if a.title == "" || a.short == "" || a.tile == "" || a.description == "" || a.order == 0 || !known[a.area] {
			return nil, fmt.Errorf("%s: front matter needs title, short, tile, description, order and a known area", f)
		}
		if _, ok := icons[a.icon]; !ok {
			return nil, fmt.Errorf("%s: no icon %q", f, a.icon)
		}
		a.words = len(strings.Fields(a.body))
		arts = append(arts, a)
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].order < arts[j].order })
	return arts, nil
}

// fill replaces a fragment's placeholders.
func (s *site) fill(frag string) string {
	r := strings.NewReplacer(
		"{{VERSION}}", s.version,
		"{{REPO}}", repo,
		"{{DEMO}}", demo,
		"{{TILES}}", s.tiles(),
		"{{ICON:check}}", icon("check"),
		"{{ICON:arrow}}", icon("arrow"),
		"{{ICON:download}}", icon("download"),
		"{{ICON:github}}", icon("github"),
		"{{ICON:play}}", icon("play"),
	)
	return r.Replace(frag)
}

func (s *site) byArea(name string) []article {
	var in []article
	for _, a := range s.arts {
		if a.area == name {
			in = append(in, a)
		}
	}
	return in
}

// tiles is the grid of squares, one per article, under a heading for each area.
func (s *site) tiles() string {
	var b strings.Builder
	n := 0
	for _, ar := range areas {
		in := s.byArea(ar.name)
		if len(in) == 0 {
			continue
		}
		fmt.Fprintf(&b, `<div class="area" id="%s"><div class="area-head"><h3>%s</h3><p>%s</p></div><div class="tiles">`+"\n",
			ar.id, html.EscapeString(ar.name), html.EscapeString(ar.intro))
		for _, a := range in {
			n++
			fmt.Fprintf(&b, `<a class="tile tile-%s" href="/learn/%s/"><span class="tile-top"><span class="tile-icon">%s</span><span class="tile-num">%02d</span></span><span class="tile-title">%s</span><span class="tile-text">%s</span><span class="tile-more">Read the article · %d min %s</span></a>`+"\n",
				ar.id, a.slug, icon(a.icon), n, html.EscapeString(a.short), html.EscapeString(a.tile), minutes(a.words), icon("arrow"))
		}
		b.WriteString("</div></div>\n")
	}
	return b.String()
}

func minutes(words int) int { return (words + 219) / 220 }

func (s *site) learnIndex() string {
	var b strings.Builder
	total := 0
	for _, a := range s.arts {
		total += a.words
	}
	fmt.Fprintf(&b, `<section class="page-hero"><div class="wrap narrow"><p class="eyebrow">Learn</p><h1>Vaglio in plain words</h1><p class="lede">%d articles, about %d minutes of reading in all. Each starts from nothing, explains one idea, and says plainly what is not done yet. No software background needed.</p></div></section>`,
		len(s.arts), minutes(total))
	b.WriteString(`<section class="band"><div class="wrap">` + s.tiles() + `</div></section>`)
	return b.String()
}

// articlePage is an article's own page: title, a contents list beside the text, and the next article.
func (s *site) articlePage(a article) string {
	body, toc := markdown(a.body)
	var b strings.Builder
	areaID := ""
	for _, ar := range areas {
		if ar.name == a.area {
			areaID = ar.id
		}
	}
	fmt.Fprintf(&b, `<section class="page-hero article-hero hero-%s"><div class="wrap narrow"><p class="crumbs"><a href="/learn/">Learn</a> <span>/</span> <a href="/learn/#%s">%s</a></p><h1>%s</h1><p class="lede">%s</p><p class="meta">%d min read · %s words</p></div></section>`,
		areaID, areaID, html.EscapeString(a.area), html.EscapeString(a.title), html.EscapeString(a.description), minutes(a.words), thousands(a.words))
	b.WriteString(`<section class="band"><div class="wrap article-layout"><article class="prose">` + "\n" + body + "</article>\n<aside class=\"article-side\">")
	if len(toc) > 1 {
		b.WriteString(`<nav class="side-box toc" aria-label="On this page"><b>On this page</b><ol>`)
		for _, h := range toc {
			fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`, h.id, mdInline(h.text))
		}
		b.WriteString("</ol></nav>")
	}
	b.WriteString(`<div class="side-box try"><b>See it decide</b><p>The public demonstration runs on invented requests. Sign in as <code>demo</code>, password <code>try-vaglio-demo</code>.</p><a class="btn btn-solid" href="` + demo + `">` + icon("play") + `Try the demo</a></div></aside></div></section>` + "\n")
	// The next article, in reading order, and the one before.
	for i, o := range s.arts {
		if o.slug != a.slug {
			continue
		}
		b.WriteString(`<section class="band band-tint"><div class="wrap next">`)
		if i > 0 {
			p := s.arts[i-1]
			fmt.Fprintf(&b, `<a class="next-card prev" href="/learn/%s/"><span>Previous</span><b>%s</b></a>`, p.slug, html.EscapeString(p.short))
		} else {
			b.WriteString(`<span></span>`)
		}
		if i+1 < len(s.arts) {
			n := s.arts[i+1]
			fmt.Fprintf(&b, `<a class="next-card" href="/learn/%s/"><span>Next</span><b>%s</b></a>`, n.slug, html.EscapeString(n.short))
		}
		b.WriteString(`</div></section>`)
	}
	return b.String()
}

func thousands(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%d,%03d", n/1000, n%1000)
}

// page writes one page at path (ending in /) as path/index.html.
func (s *site) page(path, title, desc, body string) error {
	s.paths = append(s.paths, path)
	return s.write(strings.TrimPrefix(path, "/")+"index.html", s.layout(path, title, desc, body))
}

func (s *site) write(rel, content string) error {
	f := filepath.Join(s.out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(content), 0o644)
}

// assets copies the stylesheet, the logo and the screenshots the pages use.
func (s *site) assets() error {
	copies := map[string]string{
		"site/static/site.css":              "site.css",
		"docs/assets/vaglio-logo.svg":       "logo.svg",
		"docs/assets/biodream-wordmark.png": "biodream-wordmark.png",
	}
	shots, err := filepath.Glob(filepath.Join(s.root, "docs/assets/screens/*.png"))
	if err != nil {
		return err
	}
	for _, f := range shots {
		rel, _ := filepath.Rel(s.root, f)
		copies[filepath.ToSlash(rel)] = "img/" + filepath.Base(f)
	}
	for src, dst := range copies {
		if err := copyFile(filepath.Join(s.root, src), filepath.Join(s.out, dst)); err != nil {
			return err
		}
	}
	return s.write("robots.txt", "User-agent: *\nAllow: /\nSitemap: "+s.base+"/sitemap.xml\n")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (s *site) sitemap() error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range s.paths {
		fmt.Fprintf(&b, "  <url><loc>%s%s</loc></url>\n", s.base, p)
	}
	b.WriteString("</urlset>\n")
	return s.write("sitemap.xml", b.String())
}

var (
	hrefRE = regexp.MustCompile(`(?:href|src)="(/[^"#?]*)`)
	// Words the site must never carry: Vaglio is source-available, never described as open source.
	forbidden = regexp.MustCompile(`(?i)open[ -]source`)
)

// check refuses a site with a link to a page or file it does not have, or a forbidden word.
func (s *site) check() error {
	var problems []string
	err := filepath.WalkDir(s.out, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.out, p)
		if m := forbidden.Find(b); m != nil {
			problems = append(problems, fmt.Sprintf("%s: says %q", rel, m))
		}
		for _, m := range hrefRE.FindAllSubmatch(b, -1) {
			target := filepath.Join(s.out, filepath.FromSlash(string(m[1])))
			if strings.HasSuffix(string(m[1]), "/") {
				target = filepath.Join(target, "index.html")
			}
			if _, err := os.Stat(target); err != nil {
				problems = append(problems, fmt.Sprintf("%s: links to %s, which the site does not have", rel, m[1]))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}
