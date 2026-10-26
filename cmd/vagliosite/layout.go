package main

import (
	"fmt"
	"html"
	"strings"
)

// icons are the site's own line drawings, 24 by 24, drawn with the current text colour.
var icons = map[string]string{
	"clipboard": `<rect x="5" y="4" width="14" height="17" rx="2"/><path d="M9 4h6v3H9zM8.5 12h7M8.5 16h5"/>`,
	"tree":      `<circle cx="12" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="19" r="2"/><circle cx="12" cy="13" r="2"/><path d="M12 7v4M10.6 14.4l-3.2 3.2M13.4 14.4l3.2 3.2"/>`,
	"search":    `<circle cx="10.5" cy="10.5" r="6"/><path d="M15 15l5 5M8.8 9a1.8 1.8 0 1 1 2.5 1.7c-.5.2-.8.6-.8 1.1M10.5 13.6v.1"/>`,
	"grid":      `<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M4 9.3h16M4 14.7h16M9.3 4v16M14.7 4v16"/>`,
	"receipt":   `<path d="M6 3h12v18l-2-1.5-2 1.5-2-1.5-2 1.5-2-1.5L6 21z"/><path d="M9 8h6M9 12h6M9 16h3"/>`,
	"person":    `<circle cx="10" cy="8" r="3.5"/><path d="M3.5 20c.6-3.6 3.2-5.5 6.5-5.5 1.4 0 2.7.3 3.7 1M15 18l2 2 4-4.5"/>`,
	"library":   `<path d="M4 20V5M8 20V5M12 20V7M15.2 7.6l3.8-1.1 3 13.4-3.8 1.1zM3 20h11"/>`,
	"pen":       `<path d="M4 20l1-4L16.5 4.5a2.1 2.1 0 0 1 3 3L8 19z"/><path d="M14.5 6.5l3 3M4 20h7"/>`,
	"plug":      `<path d="M9 3v5M15 3v5M6.5 8h11v3a5.5 5.5 0 0 1-11 0zM12 16.5V21"/>`,
	"server":    `<rect x="4" y="4" width="16" height="7" rx="1.5"/><rect x="4" y="13" width="16" height="7" rx="1.5"/><path d="M8 7.5h.01M8 16.5h.01M12 7.5h4M12 16.5h4"/>`,
	"chart":     `<path d="M4 20h16M6.5 17v-5M11 17V7M15.5 17v-7M20 17V4"/>`,
	"flag":      `<path d="M5 21V4M5 4h11l-2 4 2 4H5"/>`,
	"check":     `<path d="M5 12.5l4.5 4.5L19 7.5"/>`,
	"arrow":     `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"download":  `<path d="M12 4v11M7 10.5l5 5 5-5M5 20h14"/>`,
	"github":    `<path d="M8.5 7.5L4 12l4.5 4.5M15.5 7.5L20 12l-4.5 4.5M13.5 5l-3 14"/>`,
	"play":      `<circle cx="12" cy="12" r="9"/><path d="M10 8.5l5.5 3.5-5.5 3.5z"/>`,
	"menu":      `<path d="M4 7h16M4 12h16M4 17h16"/>`,
}

func icon(name string) string {
	return `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">` + icons[name] + `</svg>`
}

// menu is the top menu: one dropdown per area of articles, then the pages of their own.
func (s *site) menu(current string) string {
	var b strings.Builder
	b.WriteString(`<nav class="menu" aria-label="Main">`)
	for _, ar := range areas[1:] { // "Start here" goes under Learn, last
		s.drop(&b, ar.menu, "/learn/#"+ar.id, s.byArea(ar.name), current, "")
	}
	s.drop(&b, "Learn", "/learn/", s.byArea("Start here"), current, `<a class="drop-all" href="/learn/">Every article `+icon("arrow")+`</a>`)
	cls := ""
	if current == "/download/" {
		cls = ` class="here"`
	}
	b.WriteString(`<a href="/download/"` + cls + `>Download</a></nav>`)
	return b.String()
}

func (s *site) drop(b *strings.Builder, label, href string, in []article, current, extra string) {
	here := ""
	for _, a := range in {
		if current == "/learn/"+a.slug+"/" {
			here = " here"
		}
	}
	fmt.Fprintf(b, `<div class="drop%s"><a class="drop-label" href="%s">%s</a><div class="drop-panel">`, here, href, html.EscapeString(label))
	for _, a := range in {
		fmt.Fprintf(b, `<a href="/learn/%s/"><span class="tile-icon">%s</span><span><b>%s</b><small>%s</small></span></a>`,
			a.slug, icon(a.icon), html.EscapeString(a.short), html.EscapeString(a.tile))
	}
	b.WriteString(extra + `</div></div>`)
}

// layout wraps a page's body: head, top bar with the menu, and footer.
func (s *site) layout(path, title, desc, body string) string {
	var b strings.Builder
	canonical := s.base + path
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<meta name="description" content="%s">
<link rel="canonical" href="%s">
<link rel="icon" href="/logo.svg" type="image/svg+xml">
<link rel="stylesheet" href="/site.css?v=%s">
<meta property="og:title" content="%s">
<meta property="og:description" content="%s">
<meta property="og:type" content="website">
<meta property="og:url" content="%s">
<meta property="og:site_name" content="Vaglio">
<meta name="theme-color" content="#0f1729">
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="top"><div class="wrap bar">
<a class="brand" href="/"><img src="/logo.svg" alt="" width="34" height="34"><span>Vaglio</span></a>
%s
<div class="bar-end"><a class="bar-link" href="%s">%sGitHub</a><a class="btn btn-solid btn-small" href="%s">Try the demo</a></div>
<details class="mobile"><summary aria-label="Menu">%s</summary><div class="mobile-panel">%s</div></details>
</div></header>
<main id="main">
%s
</main>
%s
</body>
</html>
`, html.EscapeString(title), html.EscapeString(desc), canonical, s.cssVersion, html.EscapeString(title), html.EscapeString(desc), canonical,
		s.menu(path), repo, icon("github"), demo, icon("menu"), s.mobileMenu(), body, s.footer())
	return b.String()
}

func (s *site) mobileMenu() string {
	var b strings.Builder
	for _, ar := range areas {
		fmt.Fprintf(&b, `<b>%s</b>`, html.EscapeString(ar.menu))
		for _, a := range s.byArea(ar.name) {
			fmt.Fprintf(&b, `<a href="/learn/%s/">%s</a>`, a.slug, html.EscapeString(a.short))
		}
	}
	b.WriteString(`<b>Get it</b><a href="/download/">Download</a><a href="` + repo + `">GitHub</a><a href="` + demo + `">Try the demo</a>`)
	return b.String()
}

func (s *site) footer() string {
	var b strings.Builder
	b.WriteString(`<footer class="foot"><div class="wrap foot-grid"><div class="foot-brand"><a class="brand" href="/"><img src="/logo.svg" alt="" width="30" height="30"><span>Vaglio</span></a><p>A prior authorization decision engine that cannot deny care without proving why.</p><p class="foot-small">Source-available under the PolyForm Shield License 1.0.0. Pull requests are not accepted; bug reports and private security reports are welcome.</p></div>`)
	for _, ar := range areas {
		fmt.Fprintf(&b, `<div><b>%s</b>`, html.EscapeString(ar.menu))
		for _, a := range s.byArea(ar.name) {
			fmt.Fprintf(&b, `<a href="/learn/%s/">%s</a>`, a.slug, html.EscapeString(a.short))
		}
		b.WriteString(`</div>`)
	}
	fmt.Fprintf(&b, `<div><b>Get it</b><a href="/download/">Download %s</a><a href="%s">Source on GitHub</a><a href="%s/releases">Releases</a><a href="%s">The demonstration</a><a href="https://perfuse.health">Perfuse, the PAS server in front</a><a href="%s/blob/main/LICENSE.md">The licence</a><a href="%s/security">Report a security problem</a></div></div>`,
		s.version, repo, repo, demo, repo, repo)
	b.WriteString(`<div class="wrap maker"><a href="https://biodream.io" aria-label="BioDream LLC"><img src="/biodream-wordmark.png" alt="BioDream" width="88" height="60"></a><p>Vaglio is a product of <a href="https://biodream.io">BioDream LLC</a>, which builds free and source-available software for healthcare, telecom and 911, and the support to run it. Commercial licences: <a href="mailto:firstmind@biodream.ai">firstmind@biodream.ai</a>. <a href="https://biodream.io/support/">Support from BioDream</a></p></div>`)
	b.WriteString(`<div class="wrap foot-legal"><p>© 2026 BioDream LLC. Vaglio is not affiliated with or endorsed by CMS, HL7, the Da Vinci Project, AHIP, BCBSA or any company named on this site. HL7 and FHIR are registered trademarks of Health Level Seven International. Nothing on this site is medical or legal advice; the example policies are illustrative and are not any plan's coverage policy.</p></div></footer>`)
	return b.String()
}
