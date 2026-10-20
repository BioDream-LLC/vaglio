package main

import (
	"fmt"
	"regexp"
	"strings"
)

// cqlLib is what import-dtr reads of a DTR package's CQL: not an engine, a reader of the shapes prepopulation
// libraries are mostly made of, so that a question pre-filled from the record gets a suggested find line. A
// suggestion is a comment in the draft, never a rule: the author decides whether the plan counts it.
type cqlLib struct {
	name     string
	defines  map[string]string // define name -> its expression
	codes    map[string]cqlCode
	valueSet map[string]string // valueset name -> its URL
	systems  map[string]string // codesystem name -> its URL
}

type cqlCode struct{ code, system string }

var (
	cqlLibraryRe  = regexp.MustCompile(`(?m)^library\s+"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	cqlSystemRe   = regexp.MustCompile(`(?m)^codesystem\s+"([^"]+)"\s*:\s*'([^']+)'`)
	cqlValueSetRe = regexp.MustCompile(`(?m)^valueset\s+"([^"]+)"\s*:\s*'([^']+)'`)
	cqlCodeRe     = regexp.MustCompile(`(?m)^code\s+"([^"]+)"\s*:\s*'([^']+)'\s+from\s+"([^"]+)"`)
	cqlDefineAt   = regexp.MustCompile(`(?m)^define\s+(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*:`)
	// A retrieve: [Condition: "name"], [Observation: "name"], and so on.
	cqlRetrieveRe = regexp.MustCompile(`\[\s*(Condition|Observation|Procedure|MedicationRequest|MedicationStatement)\s*:\s*"([^"]+)"\s*\]`)
	cqlLookBackRe = regexp.MustCompile(`(\d+)\s+(day|days|week|weeks|month|months|year|years)\b`)
	cqlRefRe      = regexp.MustCompile(`"([^"]+)"|\b([A-Z][A-Za-z0-9_]*)\b`)
	cqlListRe     = regexp.MustCompile(`^\{\s*((?:"[^"]+"\s*,?\s*)+)\}$`)
)

func parseCQL(src string) *cqlLib {
	l := &cqlLib{defines: map[string]string{}, codes: map[string]cqlCode{}, valueSet: map[string]string{}, systems: map[string]string{}}
	if m := cqlLibraryRe.FindStringSubmatch(src); m != nil {
		l.name = m[1]
	}
	for _, m := range cqlSystemRe.FindAllStringSubmatch(src, -1) {
		l.systems[m[1]] = m[2]
	}
	for _, m := range cqlValueSetRe.FindAllStringSubmatch(src, -1) {
		l.valueSet[m[1]] = m[2]
	}
	for _, m := range cqlCodeRe.FindAllStringSubmatch(src, -1) {
		l.codes[m[1]] = cqlCode{m[2], l.systems[m[3]]}
	}
	// A define's expression runs to the next top-level statement.
	at := cqlDefineAt.FindAllStringSubmatchIndex(src, -1)
	for i, m := range at {
		var name string
		if m[2] >= 0 {
			name = src[m[2]:m[3]]
		} else {
			name = src[m[4]:m[5]]
		}
		end := len(src)
		if i+1 < len(at) {
			end = at[i+1][0]
		}
		body := src[m[1]:end]
		if j := regexp.MustCompile(`(?m)^(define|context|parameter|//)`).FindStringIndex(body); j != nil {
			body = body[:j[0]]
		}
		l.defines[name] = strings.TrimSpace(body)
	}
	return l
}

// vaglioSystem is Vaglio's name for a code system URL, or "" when the language has none.
func vaglioSystem(url string) string {
	switch url {
	case "http://loinc.org":
		return "loinc"
	case "http://hl7.org/fhir/sid/icd-10-cm":
		return "icd10"
	case "http://snomed.info/sct":
		return "snomed"
	case "http://www.nlm.nih.gov/research/umls/rxnorm":
		return "rxnorm"
	case "http://www.ama-assn.org/go/cpt":
		return "cpt"
	case "https://bluebutton.cms.gov/resources/codesystem/hcpcs", "urn:oid:2.16.840.1.113883.6.285":
		return "hcpcs"
	}
	return ""
}

// suggest reads what a question's pre-fill expression looks up and returns suggested find lines and notes: the
// retrieves it reaches through the defines it uses, with their look-back, whether it is negated (the question is yes
// when the record has none), and what could not be read.
func (l *cqlLib) suggest(expr string) (finds, notes []string) {
	if l == nil {
		return nil, nil
	}
	name := expr
	if i := strings.LastIndex(expr, "."); i >= 0 {
		name = expr[i+1:]
	}
	name = strings.Trim(name, `"`)
	body, ok := l.defines[name]
	if !ok {
		return nil, []string{fmt.Sprintf("%s is not defined in %s", name, l.name)}
	}
	negated := regexp.MustCompile(`^\s*not\s+exists`).MatchString(body)
	seen := map[string]bool{}
	looked := false
	var walk func(string, int)
	walk = func(b string, depth int) {
		if depth > 6 {
			return
		}
		within := ""
		if m := cqlLookBackRe.FindStringSubmatch(b); m != nil {
			within = lookBackDays(m[1], m[2])
		}
		for _, m := range cqlRetrieveRe.FindAllStringSubmatch(b, -1) {
			kind, ref := m[1], m[2]
			if seen[kind+ref] {
				continue
			}
			seen[kind+ref] = true
			looked = true
			finds, notes = l.retrieve(kind, ref, within, finds, notes)
		}
		for _, m := range cqlRefRe.FindAllStringSubmatch(b, -1) {
			ref := m[1] + m[2]
			if d, ok := l.defines[ref]; ok && ref != name && !seen["define "+ref] {
				seen["define "+ref] = true
				walk(d, depth+1)
			}
		}
	}
	walk(body, 0)
	if len(finds) == 0 && len(notes) == 0 {
		notes = append(notes, fmt.Sprintf("%s reads the request or computes a value; there is no record lookup to suggest", name))
	}
	if negated && looked {
		notes = append(notes, "the CQL answers yes when the record has NONE of this: these finds are for the opposite "+
			"fact (write it as evidence of its own and a criterion defeated by it)")
	}
	if regexp.MustCompile(`(?i)Highest|Lowest|>|<|Threshold`).MatchString(body) && !regexp.MustCompile(`[<>]=?\s*\d`).MatchString(body) {
		notes = append(notes, "the define's name speaks of a threshold, but the CQL compares no value")
	}
	for _, f := range finds {
		if strings.HasSuffix(f, "VALUE UNIT") {
			notes = append(notes, "an observation is read one stated way: replace VALUE UNIT with the plan's threshold "+
				"(\"at least 56 %\"), or write \"at most\", \"yes-no\" or coded values")
			break
		}
	}
	return finds, notes
}

// retrieve is one suggested find line for [Kind: "name"], name being a code list define, a code or a value set.
func (l *cqlLib) retrieve(kind, ref, within string, finds, notes []string) ([]string, []string) {
	what := map[string]string{"Condition": "condition", "Observation": "observation", "Procedure": "procedure",
		"MedicationRequest": "medication", "MedicationStatement": "medication"}[kind]
	var codes []cqlCode
	if c, ok := l.codes[ref]; ok {
		codes = append(codes, c)
	} else if d, ok := l.defines[ref]; ok {
		if m := cqlListRe.FindStringSubmatch(strings.TrimSpace(d)); m != nil {
			for _, n := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
				if c, ok := l.codes[n[1]]; ok {
					codes = append(codes, c)
				}
			}
		}
	} else if url, ok := l.valueSet[ref]; ok {
		return finds, append(notes, fmt.Sprintf("%s by the value set %q (%s): expand it and write its codes as "+
			"find %s <system> <codes>", what, ref, url, what))
	}
	if len(codes) == 0 {
		return finds, append(notes, fmt.Sprintf("%s by %q: not a code the CQL lists", what, ref))
	}
	bySystem := map[string][]string{}
	var order []string
	for _, c := range codes {
		s := vaglioSystem(c.system)
		if s == "" {
			notes = append(notes, fmt.Sprintf("code %s of %s: Vaglio has no name for that system", c.code, c.system))
			continue
		}
		if bySystem[s] == nil {
			order = append(order, s)
		}
		bySystem[s] = append(bySystem[s], c.code)
	}
	for _, s := range order {
		line := fmt.Sprintf("find %s %s %s", what, s, strings.Join(bySystem[s], " "))
		if within != "" {
			line += " within " + within + " days"
		}
		if what == "observation" {
			// The language reads an observation's value one stated way; the CQL's existence test states none.
			line += " at least VALUE UNIT"
		}
		finds = append(finds, line)
	}
	return finds, notes
}

func lookBackDays(n, unit string) string {
	var k int
	fmt.Sscan(n, &k)
	switch strings.TrimSuffix(unit, "s") {
	case "week":
		k *= 7
	case "month":
		k = k * 61 / 2 // 30.5 days, rounded down: never longer than the CQL's months
	case "year":
		k *= 365
	}
	return fmt.Sprint(k)
}
