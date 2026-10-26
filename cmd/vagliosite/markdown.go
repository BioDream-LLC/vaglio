package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// A small Markdown renderer for the site's articles (copied from BitVoice's site builder): headings (## and ###), paragraphs, lists,
// tables, fenced code, quotes, and inline code, bold, italic and links. Everything else is text,
// escaped. The articles are ours, so this covers what they use and nothing more.

var inlineRE = regexp.MustCompile("(`[^`]+`)|(\\*\\*[^*]+\\*\\*)|(\\*[^*\\s][^*]*\\*)|(\\[[^\\]]+\\]\\([^)\\s]+\\))")

func mdInline(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range inlineRE.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		t := s[m[0]:m[1]]
		switch {
		case strings.HasPrefix(t, "`"):
			b.WriteString("<code>" + html.EscapeString(t[1:len(t)-1]) + "</code>")
		case strings.HasPrefix(t, "**"):
			b.WriteString("<strong>" + mdInline(t[2:len(t)-2]) + "</strong>")
		case strings.HasPrefix(t, "*"):
			b.WriteString("<em>" + mdInline(t[1:len(t)-1]) + "</em>")
		default:
			i := strings.Index(t, "](")
			text, href := t[1:i], t[i+2:len(t)-1]
			ext := ""
			if strings.HasPrefix(href, "http") {
				ext = ` rel="noopener"`
			}
			fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, html.EscapeString(href), ext, mdInline(text))
		}
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}

var (
	listRE  = regexp.MustCompile(`^(\s*)([-*]|\d+\.)\s+`)
	imageRE = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)\s]+)\)\s*$`)
	alignRE = regexp.MustCompile(`^:?-+:?$`)
)

// slug makes a heading's id: lower case, letters and digits joined by hyphens.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// heading is one ## heading of an article, for its table of contents.
type heading struct{ id, text string }

// markdown renders an article's body, and lists its ## headings.
func markdown(src string) (string, []heading) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out strings.Builder
	var toc []heading
	special := func(l string) bool {
		return imageRE.MatchString(l) || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "```") || strings.HasPrefix(l, "> ") || strings.HasPrefix(l, "|") || listRE.MatchString(l)
	}
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case strings.TrimSpace(l) == "":
			i++
		case strings.HasPrefix(l, "```"):
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
				code = append(code, lines[i])
			}
			i++
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case strings.HasPrefix(l, "### "):
			t := strings.TrimSpace(l[4:])
			fmt.Fprintf(&out, "<h3 id=\"%s\">%s</h3>\n", slug(t), mdInline(t))
			i++
		case strings.HasPrefix(l, "## "):
			t := strings.TrimSpace(l[3:])
			toc = append(toc, heading{slug(t), t})
			fmt.Fprintf(&out, "<h2 id=\"%s\">%s</h2>\n", slug(t), mdInline(t))
			i++
		case strings.HasPrefix(l, "#"):
			t := strings.TrimSpace(strings.TrimLeft(l, "#"))
			fmt.Fprintf(&out, "<h3>%s</h3>\n", mdInline(t))
			i++
		case imageRE.MatchString(l):
			// A picture on a line of its own: ![caption](/img/file.png).
			m := imageRE.FindStringSubmatch(l)
			fmt.Fprintf(&out, "<figure><img src=\"%s\" alt=\"%s\" loading=\"lazy\"><figcaption>%s</figcaption></figure>\n",
				html.EscapeString(m[2]), html.EscapeString(m[1]), mdInline(m[1]))
			i++
		case strings.HasPrefix(l, "> "):
			var q []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "> "); i++ {
				q = append(q, lines[i][2:])
			}
			out.WriteString("<blockquote><p>" + mdInline(strings.Join(q, " ")) + "</p></blockquote>\n")
		case strings.HasPrefix(l, "|"):
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
				cells := strings.Split(strings.Trim(strings.TrimSpace(lines[i]), "|"), "|")
				for k := range cells {
					cells[k] = strings.TrimSpace(cells[k])
				}
				sep := true
				for _, c := range cells {
					sep = sep && alignRE.MatchString(c)
				}
				if !sep {
					rows = append(rows, cells)
				}
			}
			if len(rows) == 0 {
				continue
			}
			out.WriteString("<div class=\"table\"><table><thead><tr>")
			for _, c := range rows[0] {
				out.WriteString("<th>" + mdInline(c) + "</th>")
			}
			out.WriteString("</tr></thead><tbody>")
			for _, r := range rows[1:] {
				out.WriteString("<tr>")
				for _, c := range r {
					out.WriteString("<td>" + mdInline(c) + "</td>")
				}
				out.WriteString("</tr>")
			}
			out.WriteString("</tbody></table></div>\n")
		case listRE.MatchString(l):
			i = list(lines, i, &out)
		default:
			var p []string
			for ; i < len(lines) && strings.TrimSpace(lines[i]) != "" && (len(p) == 0 || !special(lines[i])); i++ {
				p = append(p, strings.TrimSpace(lines[i]))
			}
			out.WriteString("<p>" + mdInline(strings.Join(p, " ")) + "</p>\n")
		}
	}
	return out.String(), toc
}

// list renders a list starting at lines[i], with deeper-indented items as nested lists and other
// indented lines continuing the item above, and returns the line after it.
func list(lines []string, i int, out *strings.Builder) int {
	m := listRE.FindStringSubmatch(lines[i])
	indent := len(m[1])
	tag := "ul"
	if m[2] != "-" && m[2] != "*" {
		tag = "ol"
	}
	out.WriteString("<" + tag + ">")
	for i < len(lines) {
		m := listRE.FindStringSubmatch(lines[i])
		if m == nil || len(m[1]) < indent {
			break
		}
		if len(m[1]) > indent {
			i = list(lines, i, out)
			continue
		}
		item := []string{strings.TrimSpace(lines[i][len(m[0]):])}
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !listRE.MatchString(lines[i]) && strings.HasPrefix(lines[i], " ") {
			item = append(item, strings.TrimSpace(lines[i]))
			i++
		}
		out.WriteString("<li>" + mdInline(strings.Join(item, " ")))
		if i < len(lines) {
			if n := listRE.FindStringSubmatch(lines[i]); n != nil && len(n[1]) > indent {
				i = list(lines, i, out)
			}
		}
		out.WriteString("</li>")
		// A blank line between items keeps the list going; anything else after one ends it.
		if i < len(lines) && strings.TrimSpace(lines[i]) == "" && i+1 < len(lines) {
			if n := listRE.FindStringSubmatch(lines[i+1]); n != nil && len(n[1]) == indent {
				i++
			}
		}
	}
	out.WriteString("</" + tag + ">\n")
	return i
}
