// Package cql runs Clinical Quality Language (CQL 1.5) libraries over a patient's FHIR R4 data: the language Da Vinci
// DTR questionnaires use to fill in their answers from the record.
//
// It implements the part of CQL that payer DTR libraries are written in, not the whole language: libraries and their
// includes, code systems, value sets, codes and concepts, parameters, the Patient context, defines and functions
// (no overloading by type), retrieves by value set or code, queries (several sources, let, with/without ... such that,
// where, return, sort), lists, intervals and tuples, the logical, comparison, arithmetic, membership and interval
// operators, date and time arithmetic with precision, the aggregate and list functions, and FHIRHelpers as built-in
// functions. The FHIR model is read from the resources' JSON: a choice element (Observation.value[x]) carries its type,
// so `O.value as FHIR.Quantity` works; another primitive's `.value` is read as a date or a dateTime when its text is one.
//
// What it does not do: ELM, the whole operator set (time-of-day arithmetic, ratios beyond reading them, unit
// conversion between quantities of different units, uncertainty intervals), overloads, and any terminology service:
// value sets are given as FHIR ValueSet resources with an expansion (or a compose listing concepts), never fetched.
// An expression using something it does not do is an error for that define alone, never a guessed value.
package cql

import (
	"fmt"
	"strings"
	"unicode"
)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tQuoted // "quoted identifier"
	tString // 'string'
	tNumber
	tDate // @2020-01-01T10:00:00
	tTime // @T10:00
	tPunct
)

type token struct {
	kind tokKind
	text string
	line int
}

func (t token) String() string {
	switch t.kind {
	case tEOF:
		return "the end"
	case tString:
		return "'" + t.text + "'"
	case tQuoted:
		return `"` + t.text + `"`
	}
	return t.text
}

// Error is a problem in a library's text, with its line.
type Error struct {
	Library string
	Line    int
	Msg     string
}

func (e *Error) Error() string {
	if e.Library == "" {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return fmt.Sprintf("%s line %d: %s", e.Library, e.Line, e.Msg)
}

func lex(src string) ([]token, error) {
	var out []token
	line := 1
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '\n':
			line++
			i++
		case unicode.IsSpace(c):
			i++
		case c == '/' && i+1 < len(r) && r[i+1] == '/':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			i += 2
			for i+1 < len(r) && !(r[i] == '*' && r[i+1] == '/') {
				if r[i] == '\n' {
					line++
				}
				i++
			}
			if i+1 >= len(r) {
				return nil, &Error{Line: line, Msg: "a comment that never ends"}
			}
			i += 2
		case c == '"' || c == '\'' || c == '`':
			q := c
			start := line
			var b strings.Builder
			i++
			for ; i < len(r) && r[i] != q; i++ {
				if r[i] == '\n' {
					line++
				}
				if r[i] == '\\' && i+1 < len(r) {
					i++
					switch r[i] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					case 'r':
						b.WriteRune('\r')
					case 'f':
						b.WriteRune('\f')
					case 'u':
						if i+4 < len(r) {
							var v rune
							if _, err := fmt.Sscanf(string(r[i+1:i+5]), "%04x", &v); err == nil {
								b.WriteRune(v)
								i += 4
								continue
							}
						}
						b.WriteRune('u')
					default:
						b.WriteRune(r[i])
					}
					continue
				}
				b.WriteRune(r[i])
			}
			if i >= len(r) {
				return nil, &Error{Line: start, Msg: "a quote that never closes"}
			}
			i++
			k := tString
			if q != '\'' {
				k = tQuoted
			}
			out = append(out, token{k, b.String(), start})
		case c == '@':
			j := i + 1
			for j < len(r) && (unicode.IsDigit(r[j]) || strings.ContainsRune("-:.TZ+", r[j])) {
				j++
			}
			text := string(r[i+1 : j])
			k := tDate
			if strings.HasPrefix(text, "T") {
				k, text = tTime, text[1:]
			}
			out = append(out, token{k, text, line})
			i = j
		case unicode.IsDigit(c):
			j := i
			for j < len(r) && unicode.IsDigit(r[j]) {
				j++
			}
			if j+1 < len(r) && r[j] == '.' && unicode.IsDigit(r[j+1]) {
				j++
				for j < len(r) && unicode.IsDigit(r[j]) {
					j++
				}
			}
			out = append(out, token{tNumber, string(r[i:j]), line})
			i = j
		case unicode.IsLetter(c) || c == '_' || c == '$':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '$') {
				j++
			}
			out = append(out, token{tIdent, string(r[i:j]), line})
			i = j
		default:
			two := ""
			if i+1 < len(r) {
				two = string(r[i : i+2])
			}
			switch two {
			case "<=", ">=", "!=", "!~":
				out = append(out, token{tPunct, two, line})
				i += 2
				continue
			}
			if !strings.ContainsRune("()[]{},:.+-*/^&=~<>|;", c) {
				return nil, &Error{Line: line, Msg: fmt.Sprintf("unexpected %q", c)}
			}
			out = append(out, token{tPunct, string(c), line})
			i++
		}
	}
	return append(out, token{tEOF, "", line}), nil
}
