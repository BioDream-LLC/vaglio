package policy

// The policy language.
//
// JSON is the form the engine reads and the API serves. People write this instead: one block per argument, attacks
// written on the argument they defeat ("defeated by"), so each block reads as the clause it encodes and everything that
// overrides it. It compiles to exactly the Policy the JSON describes; Format writes any Policy back in it.
//
//	# comments run to the end of the line
//	policy lumbar_mri
//	  title "MRI of the lumbar spine"
//	  version 0.1.0
//	  source "Plan medical policy 123, section 4"
//	  status draft                                  # optional: not yet signed off; the server will not use it
//	  signed "Dr A. Example, MD" 2026-10-13         # optional: the clinician who signed it off, and when
//	  effective 2027-01-01                          # optional: the first day it decides (signed early, it waits)
//	  ends 2027-12-31                               # optional: the last day it decides
//	  authority lcd                                 # optional: ncd, lcd, state or plan (the default); a higher one wins
//	  lines medicare-advantage medicare             # optional: only these lines of business (medicaid, commercial)
//	  states WA OR                                  # optional: only patients in these states
//	  drug                                          # optional: a drug policy, counted apart in the CMS metrics
//	  questionnaire https://payer.example/Questionnaire/lumbar-mri
//	  service cpt 72148 72149 72158
//
//	evidence red_flag
//	  text "A red-flag finding"
//	  clause "MRI is indicated without a trial of conservative therapy when a red-flag condition is suspected."
//	  find question red-flag
//	  find condition icd10 G83.4 M46.26 when confirmed provisional
//	  find condition icd10 M54.50 onset at least 90 days   # began at least 90 days before the request
//	  find procedure cpt 72148 within 183 days
//	  find procedure cpt 97110 lasting 42 days not-done means absent
//	  find procedure cpt 64493 within 365 days times 2 apart 14 days   # two, on days at least 14 apart
//	  find observation loinc 39156-5 within 365 days at least 40 kg/m2
//	  find observation loinc 72166-2 coded snomed present 77176002 absent 266919005
//	  find observation loinc 11111-1 yes-no
//	  find medication rxnorm 105585 within 365 days lasting 90 days not-taken means absent
//	  find criteria interqual "KNEE-TKA-3.2"        # a criterion of the plan's own criteria service (serve -criteria)
//	  find provider specialty 207X00000X            # the provider's specialty, from the NPI registry
//	  find provider in-network                      # in the plan's network file (serve -network)
//	  find cql HomeOxygen "BloodGasQualifies"       # a define of a pinned CQL library, run over the request
//
// A policy running CQL pins each file it reads in its header, by SHA-256, so its own text covers the libraries':
//
//	  cql cql/o2/HomeOxygen.cql sha256 5f1c...       # a library (and each it includes), under the policy directory
//	  cql cql/o2/valuesets.json sha256 9a07...       # a ValueSet, or a Bundle of them, with its codes listed
//	  request document loinc 11506-3                # documents to ask for, by LOINC attachment code (CDex)
//
//	criterion indication_unestablished
//	  text "No indication has been established."
//	  clause "..."
//	  defeated by red_flag conservative_therapy_6wk
//
//	decision approve approves
//	  text "Approve."
//	  clause "..."
//	  defeated by indication_unestablished
//
// A code system is written as a URL, or as one of the names in Systems. A list of codes runs to the next keyword (when,
// within, lasting, not-done, yes-no, at, coded, present, absent). Errors name the file, line and column.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Systems are the code system names the language accepts in place of a URL.
var Systems = map[string]string{
	"cpt":      "http://www.ama-assn.org/go/cpt",
	"hcpcs":    "https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets",
	"icd10":    "http://hl7.org/fhir/sid/icd-10-cm",
	"icd10pcs": "http://www.cms.gov/Medicare/Coding/ICD10",
	"loinc":    "http://loinc.org",
	"snomed":   "http://snomed.info/sct",
	"rxnorm":   "http://www.nlm.nih.gov/research/umls/rxnorm",
}

// systemName is the short name for a system URL, or the URL itself.
func systemName(url string) string {
	names := make([]string, 0, len(Systems))
	for n := range Systems {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if Systems[n] == url {
			return n
		}
	}
	return url
}

// ParseError is a mistake in a policy source, at a line and column (both from 1).
type ParseError struct {
	File      string
	Line, Col int
	Msg       string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Col, e.Msg)
}

type token struct {
	text   string
	quoted bool
	line   int
	col    int
}

type parser struct {
	file  string
	lines [][]token
}

func (ps *parser) errAt(t token, format string, args ...any) error {
	return &ParseError{File: ps.file, Line: t.line, Col: t.col, Msg: fmt.Sprintf(format, args...)}
}

// tokenize splits each line into words and "quoted strings", dropping comments and blank lines.
func tokenize(file, src string) ([][]token, error) {
	var out [][]token
	for i, line := range strings.Split(src, "\n") {
		var toks []token
		rs := []rune(line)
		for j := 0; j < len(rs); {
			switch {
			case unicode.IsSpace(rs[j]):
				j++
			case rs[j] == '#':
				j = len(rs)
			case rs[j] == '"':
				start := j
				var b strings.Builder
				j++
				closed := false
				for j < len(rs) {
					if rs[j] == '\\' && j+1 < len(rs) && (rs[j+1] == '"' || rs[j+1] == '\\') {
						b.WriteRune(rs[j+1])
						j += 2
						continue
					}
					if rs[j] == '"' {
						closed = true
						j++
						break
					}
					b.WriteRune(rs[j])
					j++
				}
				if !closed {
					return nil, &ParseError{File: file, Line: i + 1, Col: start + 1, Msg: "a string is not closed on its line"}
				}
				toks = append(toks, token{text: b.String(), quoted: true, line: i + 1, col: start + 1})
			default:
				start := j
				for j < len(rs) && !unicode.IsSpace(rs[j]) && rs[j] != '"' && rs[j] != '#' {
					j++
				}
				toks = append(toks, token{text: string(rs[start:j]), line: i + 1, col: start + 1})
			}
		}
		if len(toks) > 0 {
			out = append(out, toks)
		}
	}
	return out, nil
}

// Parse compiles a policy written in the language, then validates it as Load does.
func Parse(file, src string) (*Policy, error) {
	lines, err := tokenize(file, src)
	if err != nil {
		return nil, err
	}
	ps := &parser{file: file, lines: lines}
	p, err := ps.parse()
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

type pendingAttack struct {
	from token
	to   string
}

func (ps *parser) parse() (*Policy, error) {
	if len(ps.lines) == 0 {
		return nil, &ParseError{File: ps.file, Line: 1, Col: 1, Msg: "empty: a policy starts with \"policy <id>\""}
	}
	p := &Policy{Services: []Service{}, Arguments: []Argument{}, Attacks: []Attack{}}
	var cur *Argument
	seen := map[string]token{}
	var attacks []pendingAttack
	have := map[string]bool{} // header fields and per-argument fields already given, to refuse a second one

	once := func(t token, key string) error {
		if have[key] {
			return ps.errAt(t, "%s is given twice", t.text)
		}
		have[key] = true
		return nil
	}
	finish := func() {
		if cur != nil {
			p.Arguments = append(p.Arguments, *cur)
			cur = nil
		}
	}

	for li, line := range ps.lines {
		kw := line[0]
		if kw.quoted {
			return nil, ps.errAt(kw, "a line starts with a keyword, not a string")
		}
		args := line[1:]
		if li == 0 && kw.text != "policy" {
			return nil, ps.errAt(kw, "a policy starts with \"policy <id>\", not %q", kw.text)
		}
		switch kw.text {
		case "policy":
			if li != 0 {
				return nil, ps.errAt(kw, "one policy per file")
			}
			if len(args) != 1 || args[0].quoted {
				return nil, ps.errAt(kw, "write: policy <id>")
			}
			p.ID = args[0].text
			continue
		case "evidence", "criterion", "decision":
			finish()
			want := 1
			if kw.text == "decision" {
				want = 2
			}
			if len(args) != want || args[0].quoted {
				if kw.text == "decision" {
					return nil, ps.errAt(kw, "write: decision <name> approves|denies")
				}
				return nil, ps.errAt(kw, "write: %s <name>", kw.text)
			}
			name := args[0]
			if prev, dup := seen[name.text]; dup {
				return nil, ps.errAt(name, "%q is already declared at line %d", name.text, prev.line)
			}
			seen[name.text] = name
			cur = &Argument{Name: name.text, Kind: Kind(kw.text)}
			if kw.text == "decision" {
				switch args[1].text {
				case "approves":
					cur.Verdict = Approve
				case "denies":
					cur.Verdict = Deny
				default:
					return nil, ps.errAt(args[1], "a decision approves or denies, not %q", args[1].text)
				}
			}
			for k := range have {
				if strings.HasPrefix(k, "arg.") {
					delete(have, k)
				}
			}
			continue
		}

		if cur == nil { // header fields
			switch kw.text {
			case "title", "version", "source", "status", "questionnaire":
				if err := once(kw, kw.text); err != nil {
					return nil, err
				}
				if len(args) != 1 {
					return nil, ps.errAt(kw, "%s takes one value", kw.text)
				}
				v := args[0].text
				switch kw.text {
				case "title":
					p.Title = v
				case "version":
					p.Version = v
				case "source":
					p.Source = v
				case "status":
					p.Status = v
				case "questionnaire":
					p.Questionnaire = v
				}
			case "effective", "ends", "authority":
				if err := once(kw, kw.text); err != nil {
					return nil, err
				}
				if len(args) != 1 || args[0].quoted {
					return nil, ps.errAt(kw, "%s takes one value", kw.text)
				}
				switch kw.text {
				case "effective":
					p.Effective = args[0].text
				case "ends":
					p.Ends = args[0].text
				default:
					p.Authority = args[0].text
				}
			case "lines", "states":
				if err := once(kw, kw.text); err != nil {
					return nil, err
				}
				if len(args) == 0 {
					return nil, ps.errAt(kw, "write: %s <value>...", kw.text)
				}
				for _, a := range args {
					if kw.text == "lines" {
						p.Lines = append(p.Lines, a.text)
					} else {
						p.States = append(p.States, a.text)
					}
				}
			case "cql":
				if len(args) != 3 || args[1].text != "sha256" {
					return nil, ps.errAt(kw, "write: cql <file.cql or file.json> sha256 <hex>")
				}
				p.CQL = append(p.CQL, CQLFile{File: args[0].text, SHA256: strings.ToLower(args[2].text)})
			case "drug":
				if err := once(kw, "drug"); err != nil {
					return nil, err
				}
				if len(args) != 0 {
					return nil, ps.errAt(kw, "drug takes no value")
				}
				p.Drug = true
			case "signed":
				if err := once(kw, "signed"); err != nil {
					return nil, err
				}
				if len(args) != 2 || !args[0].quoted {
					return nil, ps.errAt(kw, `write: signed "<name, credential>" <YYYY-MM-DD>`)
				}
				p.Signed = &SignOff{By: args[0].text, Date: args[1].text}
			case "service":
				if len(args) < 2 {
					return nil, ps.errAt(kw, "write: service <system> <code>...")
				}
				sys, err := ps.system(args[0])
				if err != nil {
					return nil, err
				}
				for _, c := range args[1:] {
					p.Services = append(p.Services, Service{System: sys, Code: c.text})
				}
			default:
				return nil, ps.errAt(kw, "%q is not a policy field (title, version, source, status, signed, effective, ends, authority, lines, "+
					"states, drug, questionnaire, service, cql), "+
					"and no argument has been started", kw.text)
			}
			continue
		}

		switch kw.text { // argument fields
		case "text", "clause":
			if err := once(kw, "arg."+kw.text); err != nil {
				return nil, err
			}
			if len(args) != 1 || !args[0].quoted {
				return nil, ps.errAt(kw, "%s takes one quoted string", kw.text)
			}
			if kw.text == "text" {
				cur.Text = args[0].text
			} else {
				cur.Clause = args[0].text
			}
		case "find":
			if cur.Kind != Evidence {
				return nil, ps.errAt(kw, "only evidence is found in a record; %q is a %s", cur.Name, cur.Kind)
			}
			src, err := ps.source(kw, args)
			if err != nil {
				return nil, err
			}
			cur.Find = append(cur.Find, src)
		case "request":
			if cur.Kind != Evidence {
				return nil, ps.errAt(kw, "only evidence is documented; %q is a %s", cur.Name, cur.Kind)
			}
			if len(args) < 3 || args[0].text != "document" || args[1].text != "loinc" {
				return nil, ps.errAt(kw, "write: request document loinc <code>...")
			}
			for _, c := range args[2:] {
				cur.Documents = append(cur.Documents, c.text)
			}
		case "defeated":
			if len(args) < 2 || args[0].text != "by" {
				return nil, ps.errAt(kw, "write: defeated by <name>...")
			}
			for _, a := range args[1:] {
				attacks = append(attacks, pendingAttack{from: a, to: cur.Name})
			}
		default:
			return nil, ps.errAt(kw, "%q is not an argument field (text, clause, find, request document, defeated by)", kw.text)
		}
	}
	finish()

	for _, at := range attacks {
		if _, ok := seen[at.from.text]; !ok {
			return nil, ps.errAt(at.from, "%q is not declared", at.from.text)
		}
		p.Attacks = append(p.Attacks, Attack{From: at.from.text, To: at.to})
	}
	return p, nil
}

func (ps *parser) system(t token) (string, error) {
	if url, ok := Systems[t.text]; ok {
		return url, nil
	}
	if strings.Contains(t.text, "://") || strings.HasPrefix(t.text, "urn:") {
		return t.text, nil
	}
	names := make([]string, 0, len(Systems))
	for n := range Systems {
		names = append(names, n)
	}
	sort.Strings(names)
	return "", ps.errAt(t, "%q is not a code system: write a URL or one of %s", t.text, strings.Join(names, ", "))
}

// keywords end a list of codes.
var keywords = map[string]bool{"when": true, "within": true, "lasting": true, "not-done": true, "not-taken": true,
	"yes-no": true, "at": true, "coded": true, "present": true, "absent": true, "times": true, "apart": true, "onset": true}

// count reads "<n>" at args[i], a whole number of at least min.
func (ps *parser) count(at token, args []token, i, min int) (int, int, error) {
	if i >= len(args) {
		return 0, i, ps.errAt(at, "write: %s <n>", at.text)
	}
	n, err := strconv.Atoi(args[i].text)
	if err != nil || n < min {
		return 0, i, ps.errAt(args[i], "%q is not a whole number of at least %d", args[i].text, min)
	}
	return n, i + 1, nil
}

// codeList reads codes from args[i:] up to the next keyword.
func (ps *parser) codeList(at token, args []token, i int) ([]string, int, error) {
	var out []string
	for ; i < len(args) && !keywords[args[i].text]; i++ {
		out = append(out, args[i].text)
	}
	if len(out) == 0 {
		return nil, i, ps.errAt(at, "at least one code is needed here")
	}
	return out, i, nil
}

// days reads "<n> days" at args[i].
func (ps *parser) days(at token, args []token, i int) (int, int, error) {
	if i+1 >= len(args) || args[i+1].text != "days" {
		return 0, i, ps.errAt(at, "write: %s <n> days", at.text)
	}
	n, err := strconv.Atoi(args[i].text)
	if err != nil || n <= 0 {
		return 0, i, ps.errAt(args[i], "%q is not a number of days above 0", args[i].text)
	}
	return n, i + 2, nil
}

func (ps *parser) source(kw token, args []token) (Source, error) {
	if len(args) == 0 {
		return Source{}, ps.errAt(kw, "write: find question|condition|procedure|observation|medication|criteria|provider|cql ...")
	}
	what := args[0]
	if what.text == "question" {
		if len(args) != 2 {
			return Source{}, ps.errAt(what, "write: find question <linkId>")
		}
		return Source{Question: args[1].text}, nil
	}
	if what.text == "criteria" {
		if len(args) != 3 || args[2].quoted && strings.TrimSpace(args[2].text) == "" {
			return Source{}, ps.errAt(what, "write: find criteria <service> <criterion>")
		}
		return Source{Criteria: &CriteriaRef{Service: args[1].text, Criterion: args[2].text}}, nil
	}
	if what.text == "cql" {
		if len(args) != 3 || args[1].quoted || !args[2].quoted && strings.TrimSpace(args[2].text) == "" {
			return Source{}, ps.errAt(what, `write: find cql <Library> "<define>"`)
		}
		return Source{CQL: &CQLRef{Library: args[1].text, Define: args[2].text}}, nil
	}
	if what.text == "provider" {
		switch {
		case len(args) == 2 && args[1].text == "in-network":
			return Source{Provider: &ProviderTest{InNetwork: true}}, nil
		case len(args) >= 3 && args[1].text == "specialty":
			var codes []string
			for _, a := range args[2:] {
				codes = append(codes, a.text)
			}
			return Source{Provider: &ProviderTest{Taxonomy: codes}}, nil
		}
		return Source{}, ps.errAt(what, "write: find provider in-network, or find provider specialty <NUCC code>...")
	}
	if what.text != "condition" && what.text != "procedure" && what.text != "observation" && what.text != "medication" {
		return Source{}, ps.errAt(what, "a source is a question, condition, procedure, observation or medication, not %q", what.text)
	}
	if len(args) < 3 {
		return Source{}, ps.errAt(what, "write: find %s <system> <code>...", what.text)
	}
	sys, err := ps.system(args[1])
	if err != nil {
		return Source{}, err
	}
	codes, i, err := ps.codeList(args[1], args, 2)
	if err != nil {
		return Source{}, err
	}
	unexpected := func(t token) error {
		return ps.errAt(t, "%q does not belong in a %s source here", t.text, what.text)
	}

	switch what.text {
	case "condition":
		c := &Codes{System: sys, Codes: codes}
		for i < len(args) {
			t := args[i]
			switch t.text {
			case "when":
				for i++; i < len(args) && !keywords[args[i].text]; i++ {
					c.Present = append(c.Present, args[i].text)
				}
				if len(c.Present) == 0 {
					return Source{}, ps.errAt(t, "write: when <verification status>...")
				}
			case "onset":
				if i+2 >= len(args) || args[i+1].text != "at" || args[i+2].text != "least" {
					return Source{}, ps.errAt(t, "write: onset at least <n> days")
				}
				if c.OnsetDays, i, err = ps.days(t, args, i+3); err != nil {
					return Source{}, err
				}
			default:
				return Source{}, unexpected(t)
			}
		}
		return Source{Condition: c}, nil

	case "procedure":
		pr := &Procedure{System: sys, Codes: codes}
		for i < len(args) {
			t := args[i]
			switch t.text {
			case "within":
				if pr.WithinDays, i, err = ps.days(t, args, i+1); err != nil {
					return Source{}, err
				}
			case "lasting":
				if pr.MinDays, i, err = ps.days(t, args, i+1); err != nil {
					return Source{}, err
				}
			case "not-done":
				if i+2 >= len(args) || args[i+1].text != "means" || args[i+2].text != "absent" {
					return Source{}, ps.errAt(t, "write: not-done means absent")
				}
				pr.NotDoneIsAbsent, i = true, i+3
			case "times":
				if pr.Times, i, err = ps.count(t, args, i+1, 2); err != nil {
					return Source{}, err
				}
			case "apart":
				if pr.ApartDays, i, err = ps.days(t, args, i+1); err != nil {
					return Source{}, err
				}
			default:
				return Source{}, unexpected(t)
			}
		}
		return Source{Procedure: pr}, nil

	case "medication":
		m := &Medication{System: sys, Codes: codes}
		for i < len(args) {
			t := args[i]
			switch t.text {
			case "within":
				if m.WithinDays, i, err = ps.days(t, args, i+1); err != nil {
					return Source{}, err
				}
			case "lasting":
				if m.MinDays, i, err = ps.days(t, args, i+1); err != nil {
					return Source{}, err
				}
			case "not-taken":
				if i+2 >= len(args) || args[i+1].text != "means" || args[i+2].text != "absent" {
					return Source{}, ps.errAt(t, "write: not-taken means absent")
				}
				m.NotTakenIsAbsent, i = true, i+3
			default:
				return Source{}, unexpected(t)
			}
		}
		return Source{Medication: m}, nil
	}

	o := &Observation{System: sys, Codes: codes}
	for i < len(args) {
		t := args[i]
		switch t.text {
		case "within":
			if o.WithinDays, i, err = ps.days(t, args, i+1); err != nil {
				return Source{}, err
			}
		case "yes-no":
			o.Boolean, i = true, i+1
		case "at":
			if i+3 >= len(args) || (args[i+1].text != "least" && args[i+1].text != "most") {
				return Source{}, ps.errAt(t, "write: at least|at most <value> <unit>")
			}
			v, err := strconv.ParseFloat(args[i+2].text, 64)
			if err != nil {
				return Source{}, ps.errAt(args[i+2], "%q is not a number", args[i+2].text)
			}
			th := &Threshold{Value: v, Unit: args[i+3].text}
			if args[i+1].text == "least" {
				o.AtLeast = th
			} else {
				o.AtMost = th
			}
			i += 4
		case "coded":
			if i+1 >= len(args) {
				return Source{}, ps.errAt(t, "write: coded <system> present <code>... absent <code>...")
			}
			vs, err := ps.system(args[i+1])
			if err != nil {
				return Source{}, err
			}
			v := &ValueCodes{System: vs}
			i += 2
			for i < len(args) && (args[i].text == "present" || args[i].text == "absent") {
				which := args[i]
				var list []string
				if list, i, err = ps.codeList(which, args, i+1); err != nil {
					return Source{}, err
				}
				if which.text == "present" {
					v.Present = append(v.Present, list...)
				} else {
					v.Absent = append(v.Absent, list...)
				}
			}
			o.Values = v
		default:
			return Source{}, unexpected(t)
		}
	}
	return Source{Observation: o}, nil
}

// Format writes a policy in the language. Parse(Format(p)) is p, attacks grouped by the argument they defeat.
func Format(p *Policy) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("policy %s\n", p.ID)
	w("  title %s\n", quote(p.Title))
	w("  version %s\n", word(p.Version))
	w("  source %s\n", quote(p.Source))
	if p.Status != "" {
		w("  status %s\n", p.Status)
	}
	if p.Signed != nil {
		w("  signed %s %s\n", quote(p.Signed.By), p.Signed.Date)
	}
	if p.Effective != "" {
		w("  effective %s\n", p.Effective)
	}
	if p.Ends != "" {
		w("  ends %s\n", p.Ends)
	}
	if p.Authority != "" {
		w("  authority %s\n", p.Authority)
	}
	if len(p.Lines) > 0 {
		w("  lines %s\n", strings.Join(p.Lines, " "))
	}
	if len(p.States) > 0 {
		w("  states %s\n", strings.Join(p.States, " "))
	}
	if p.Drug {
		w("  drug\n")
	}
	if p.Questionnaire != "" {
		w("  questionnaire %s\n", p.Questionnaire)
	}
	for _, f := range p.CQL {
		w("  cql %s sha256 %s\n", f.File, f.SHA256)
	}
	for i := 0; i < len(p.Services); {
		j := i
		var codes []string
		for ; j < len(p.Services) && p.Services[j].System == p.Services[i].System; j++ {
			codes = append(codes, p.Services[j].Code)
		}
		w("  service %s %s\n", systemName(p.Services[i].System), strings.Join(codes, " "))
		i = j
	}
	for _, a := range p.Arguments {
		w("\n%s %s", a.Kind, a.Name)
		if a.Kind == Decision {
			if a.Verdict == Approve {
				w(" approves")
			} else {
				w(" denies")
			}
		}
		w("\n  text %s\n  clause %s\n", quote(a.Text), quote(a.Clause))
		for _, s := range a.Find {
			w("  find %s\n", formatSource(s))
		}
		if len(a.Documents) > 0 {
			w("  request document loinc %s\n", strings.Join(a.Documents, " "))
		}
		var by []string
		for _, at := range p.Attacks {
			if at.To == a.Name {
				by = append(by, at.From)
			}
		}
		if len(by) > 0 {
			w("  defeated by %s\n", strings.Join(by, " "))
		}
	}
	return b.String()
}

func formatSource(s Source) string {
	switch {
	case s.Question != "":
		return "question " + s.Question
	case s.Criteria != nil:
		return "criteria " + s.Criteria.Service + " " + word(s.Criteria.Criterion)
	case s.CQL != nil:
		return "cql " + s.CQL.Library + " " + quote(s.CQL.Define)
	case s.Provider != nil:
		if s.Provider.InNetwork {
			return "provider in-network"
		}
		return "provider specialty " + strings.Join(s.Provider.Taxonomy, " ")
	case s.Condition != nil:
		out := "condition " + systemName(s.Condition.System) + " " + strings.Join(s.Condition.Codes, " ")
		if len(s.Condition.Present) > 0 {
			out += " when " + strings.Join(s.Condition.Present, " ")
		}
		if s.Condition.OnsetDays > 0 {
			out += fmt.Sprintf(" onset at least %d days", s.Condition.OnsetDays)
		}
		return out
	case s.Procedure != nil:
		pr := s.Procedure
		out := "procedure " + systemName(pr.System) + " " + strings.Join(pr.Codes, " ")
		if pr.WithinDays > 0 {
			out += fmt.Sprintf(" within %d days", pr.WithinDays)
		}
		if pr.MinDays > 0 {
			out += fmt.Sprintf(" lasting %d days", pr.MinDays)
		}
		if pr.Times > 1 {
			out += fmt.Sprintf(" times %d", pr.Times)
		}
		if pr.ApartDays > 0 {
			out += fmt.Sprintf(" apart %d days", pr.ApartDays)
		}
		if pr.NotDoneIsAbsent {
			out += " not-done means absent"
		}
		return out
	case s.Medication != nil:
		m := s.Medication
		out := "medication " + systemName(m.System) + " " + strings.Join(m.Codes, " ")
		if m.WithinDays > 0 {
			out += fmt.Sprintf(" within %d days", m.WithinDays)
		}
		if m.MinDays > 0 {
			out += fmt.Sprintf(" lasting %d days", m.MinDays)
		}
		if m.NotTakenIsAbsent {
			out += " not-taken means absent"
		}
		return out
	}
	o := s.Observation
	out := "observation " + systemName(o.System) + " " + strings.Join(o.Codes, " ")
	if o.WithinDays > 0 {
		out += fmt.Sprintf(" within %d days", o.WithinDays)
	}
	switch {
	case o.Boolean:
		out += " yes-no"
	case o.AtLeast != nil:
		out += " at least " + strconv.FormatFloat(o.AtLeast.Value, 'f', -1, 64) + " " + o.AtLeast.Unit
	case o.AtMost != nil:
		out += " at most " + strconv.FormatFloat(o.AtMost.Value, 'f', -1, 64) + " " + o.AtMost.Unit
	case o.Values != nil:
		out += " coded " + systemName(o.Values.System)
		if len(o.Values.Present) > 0 {
			out += " present " + strings.Join(o.Values.Present, " ")
		}
		if len(o.Values.Absent) > 0 {
			out += " absent " + strings.Join(o.Values.Absent, " ")
		}
	}
	return out
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// word writes a value bare when it reads back as one word, quoted otherwise.
func word(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"#") {
		return s
	}
	return quote(s)
}
