package cql

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Env is what a library runs over: the libraries it may include, the value sets it may name, one patient's data, the
// parameters, and the time it runs at.
type Env struct {
	Libraries  map[string]*Library  // by name
	ValueSets  map[string]*ValueSet // by URL
	Resources  []map[string]any     // the patient's FHIR resources (a Bundle's entries), the Patient among them
	Parameters map[string]any       // the main library's parameters, by name
	Now        time.Time            // Now() and Today(); the zero time is the current time
	// MaxSteps bounds the work one Evaluate may do (expressions evaluated); 0 is DefaultMaxSteps. A library that
	// recurses without end, or multiplies queries past it, is an error for that define, never a hung server.
	MaxSteps int

	patient map[string]any
	steps   int
	depth   int
	nest    int
	memo    map[string]any
	busy    map[string]bool
	read    map[string]bool
	reads   []string
	started bool
}

// DefaultMaxSteps is the work an Evaluate may do unless Env says otherwise: far more than a DTR library needs (the
// largest real one tried takes a few thousand), far less than a request may wait for.
const DefaultMaxSteps = 2_000_000

// maxDepth bounds nested function calls, maxNest nested evaluation of any kind (a parameter whose default names
// itself, a define through a parameter): the Go stack is never what stops a library.
const (
	maxDepth = 200
	maxNest  = 5000
)

// Read is the references (Type/id) of every resource a retrieve returned while evaluating: what an answer rests on.
func (e *Env) Read() []string { return append([]string{}, e.reads...) }

// FromBundle reads the resources of a FHIR Bundle (or a single resource) for Env.Resources.
func FromBundle(raw []byte) ([]map[string]any, error) {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var b map[string]any
	if err := d.Decode(&b); err != nil {
		return nil, err
	}
	if b["resourceType"] != "Bundle" {
		return []map[string]any{b}, nil
	}
	var out []map[string]any
	for _, e := range asList(b["entry"]) {
		if m, ok := e.(map[string]any); ok {
			if r, ok := m["resource"].(map[string]any); ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

type libRef struct{ lib *Library }
type helpersRef struct{}

type scope struct {
	lib      *Library
	vars     map[string]any
	parent   *scope
	implicit any // in a sort by, the element its unqualified names are properties of
}

func (s *scope) lookup(name string) (any, bool) {
	for c := s; c != nil; c = c.parent {
		if v, ok := c.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (s *scope) child(vars map[string]any) *scope { return &scope{lib: s.lib, vars: vars, parent: s} }

func (e *Env) fail(lib *Library, n *node, format string, args ...any) {
	name, line := "", 0
	if lib != nil {
		name = lib.Name
	}
	if n != nil {
		line = n.line
	}
	panic(&Error{Library: name, Line: line, Msg: fmt.Sprintf(format, args...)})
}

func (e *Env) init() {
	if e.started {
		return
	}
	e.started = true
	e.memo, e.busy, e.read = map[string]any{}, map[string]bool{}, map[string]bool{}
	if e.Now.IsZero() {
		e.Now = time.Now()
	}
	for _, r := range e.Resources {
		if r["resourceType"] == "Patient" {
			e.patient = r
			break
		}
	}
}

// Evaluate is one define's value, or why it has none: a value set not given, something this engine does not do.
func (e *Env) Evaluate(library, define string) (v any, err error) {
	e.init()
	lib := e.Libraries[library]
	if lib == nil {
		return nil, fmt.Errorf("library %s is not given", library)
	}
	if _, ok := lib.defines[define]; !ok {
		return nil, fmt.Errorf("library %s has no define %q", library, define)
	}
	defer func() {
		if r := recover(); r != nil {
			ce, ok := r.(*Error)
			if !ok {
				panic(r)
			}
			v, err = nil, ce
		}
	}()
	e.steps = 0
	return e.define(lib, define, nil), nil
}

func (e *Env) define(lib *Library, name string, at *node) any {
	key := lib.Name + "\x00" + name
	if v, ok := e.memo[key]; ok {
		return v
	}
	if e.busy[key] {
		e.fail(lib, at, "%q refers to itself", name)
	}
	e.busy[key] = true
	v := e.eval(lib.defines[name], &scope{lib: lib})
	delete(e.busy, key)
	e.memo[key] = v
	return v
}

func (e *Env) include(lib *Library, alias string) (any, bool) {
	for _, in := range lib.Includes {
		if in.Alias == alias {
			if in.Name == "FHIRHelpers" {
				return helpersRef{}, true
			}
			l := e.Libraries[in.Name]
			if l == nil {
				e.fail(lib, nil, "library %s (included as %s) is not given", in.Name, alias)
			}
			return libRef{l}, true
		}
	}
	return nil, false
}

// member is a name declared in a library: a define, a parameter, a code, a concept, a value set.
func (e *Env) member(lib *Library, name string, n *node) (any, bool) {
	if _, ok := lib.defines[name]; ok {
		return e.define(lib, name, n), true
	}
	if def, ok := lib.Parameters[name]; ok {
		if v, given := e.Parameters[name]; given && lib == e.mainOf(lib) {
			return v, true
		}
		if def == nil {
			return nil, true
		}
		return e.eval(def, &scope{lib: lib}), true
	}
	if c, ok := lib.Codes[name]; ok {
		sys, ok := lib.CodeSystems[c.System]
		if !ok {
			e.fail(lib, n, "code %q names code system %q, which is not declared", name, c.System)
		}
		return Code{Code: c.Code, System: sys.ID, Display: c.Display, Version: sys.Version}, true
	}
	if c, ok := lib.Concepts[name]; ok {
		out := Concept{Display: c.Display}
		for _, cn := range c.Codes {
			cd, ok := lib.Codes[cn]
			if !ok {
				e.fail(lib, n, "concept %q lists %q, which is not a code", name, cn)
			}
			sys, ok := lib.CodeSystems[cd.System]
			if !ok {
				e.fail(lib, n, "code %q names code system %q, which is not declared", cn, cd.System)
			}
			code := Code{Code: cd.Code, System: sys.ID, Display: cd.Display, Version: sys.Version}
			out.Codes = append(out.Codes, code)
		}
		return out, true
	}
	if d, ok := lib.ValueSets[name]; ok {
		vs := e.ValueSets[d.ID]
		if vs == nil {
			e.fail(lib, n, "value set %q (%s) is not given: give its expansion", name, d.ID)
		}
		return vs, true
	}
	if d, ok := lib.CodeSystems[name]; ok {
		return d, true
	}
	return nil, false
}

// mainOf: parameters given to Env are the main library's; every library's are taken by name.
func (e *Env) mainOf(lib *Library) *Library { return lib }

// tick counts one unit of work, and stops the evaluation past the limit.
func (e *Env) tick(sc *scope, n *node) {
	e.steps++
	limit := e.MaxSteps
	if limit <= 0 {
		limit = DefaultMaxSteps
	}
	if e.steps > limit {
		e.fail(sc.lib, n, "the evaluation took more than its limit of work, and was stopped")
	}
}

func (e *Env) eval(n *node, sc *scope) any {
	e.tick(sc, n)
	e.nest++
	defer func() { e.nest-- }()
	if e.nest > maxNest {
		e.fail(sc.lib, n, "the expression nests more than %d deep, and was stopped", maxNest)
	}
	switch n.op {
	case "lit":
		return n.val
	case "id":
		return e.ident(n, sc)
	case "prop":
		t := e.eval(n.args[0], sc)
		switch r := t.(type) {
		case libRef:
			v, ok := e.member(r.lib, n.s, n)
			if !ok {
				e.fail(sc.lib, n, "library %s has no %q", r.lib.Name, n.s)
			}
			return v
		case helpersRef:
			e.fail(sc.lib, n, "FHIRHelpers.%s is not a function this engine has", n.s)
		}
		return property(t, n.s)
	case "method":
		t := e.eval(n.args[0], sc)
		switch r := t.(type) {
		case libRef:
			return e.callIn(r.lib, n.s, e.evalArgs(n.args[1:], sc), n, sc)
		case helpersRef:
			return e.helpers(n.s, e.evalArgs(n.args[1:], sc), n, sc)
		}
		args := append([]any{t}, e.evalArgs(n.args[1:], sc)...)
		if f, ok := sc.lib.functions[n.s]; ok && f.fluent {
			return e.apply(sc.lib, f, args, n)
		}
		return e.builtin(n.s, args, n, sc)
	case "call":
		return e.callIn(sc.lib, n.s, e.evalArgs(n.args, sc), n, sc)
	case "index":
		l, i := e.eval(n.args[0], sc), sys(e.eval(n.args[1], sc))
		k, ok := i.(int64)
		if l == nil || !ok {
			return nil
		}
		if s, isStr := sys(l).(string); isStr {
			if k < 0 || int(k) >= len(s) {
				return nil
			}
			return string(s[k])
		}
		list := toList(l)
		if k < 0 || int(k) >= len(list) {
			return nil
		}
		return list[k]
	case "list":
		out := []any{}
		for _, a := range n.args {
			out = append(out, e.eval(a, sc))
		}
		return out
	case "tuple":
		t := &Tuple{Keys: n.val.([]string), M: map[string]any{}}
		for i, k := range t.Keys {
			t.M[k] = e.eval(n.args[i], sc)
		}
		return t
	case "instance":
		names := n.val.([]string)
		f := map[string]any{}
		for i, k := range names {
			f[k] = sys(e.eval(n.args[i], sc))
		}
		switch strings.TrimPrefix(strings.TrimPrefix(n.typ, "System."), "FHIR.") {
		case "Quantity":
			v, ok := num(f["value"])
			if !ok {
				return nil
			}
			u, _ := f["unit"].(string)
			if u == "" {
				u = "1"
			}
			return Quantity{Value: v, Unit: u}
		case "Code":
			s := func(k string) string { v, _ := f[k].(string); return v }
			return Code{Code: s("code"), System: s("system"), Display: s("display"), Version: s("version")}
		case "ValueSet", "CodeSystem":
			// A vocabulary named by its id (or url) and version: no codes are known from the selector alone.
			s := func(k string) string { v, _ := f[k].(string); return v }
			url := s("id")
			if url == "" {
				url = s("url")
			}
			return &ValueSet{URL: url, Version: s("version"), codes: map[string]bool{}}
		}
		t := &Tuple{Keys: names, M: f}
		return t
	case "interval":
		c := n.val.([2]bool)
		iv := Interval{Low: sys(e.eval(n.args[0], sc)), High: sys(e.eval(n.args[1], sc)), LowClosed: c[0], HighClosed: c[1]}
		if iv.Low != nil && iv.High != nil {
			// an interval whose low is past its high (or a point with an open end) holds nothing, and is an error
			if cmp, ok := compare(iv.Low, iv.High); ok && (cmp > 0 || cmp == 0 && !(iv.LowClosed && iv.HighClosed)) {
				e.fail(sc.lib, n, "%s: its low is not below its high", Show(iv))
			}
		}
		return iv
	case "codesel":
		v, ok := e.member(sc.lib, n.s, n)
		d, isSys := v.(Def)
		if !ok || !isSys {
			e.fail(sc.lib, n, "%q is not a code system", n.s)
		}
		cd := n.val.([2]string)
		return Code{Code: cd[0], System: d.ID, Display: cd[1], Version: d.Version}
	case "conceptsel":
		c := Concept{Display: n.s}
		for _, a := range n.args {
			v := sys(e.eval(a, sc))
			items, isList := v.([]any) // Concept { codes: { Code { ... }, ... } }
			if !isList {
				items = []any{v}
			}
			for _, it := range items {
				code, ok := sys(it).(Code)
				if !ok {
					e.fail(sc.lib, n, "a Concept holds codes")
				}
				c.Codes = append(c.Codes, code)
			}
		}
		return c
	case "if":
		if truth(e.eval(n.args[0], sc)) == T {
			return e.eval(n.args[1], sc)
		}
		return e.eval(n.args[2], sc)
	case "case":
		if n.val != nil {
			x := e.eval(n.val.(*node), sc)
			for i := 0; i+1 < len(n.args); i += 2 {
				if eq, known := equal(x, e.eval(n.args[i], sc)); known && eq {
					return e.eval(n.args[i+1], sc)
				}
			}
		} else {
			for i := 0; i+1 < len(n.args); i += 2 {
				if truth(e.eval(n.args[i], sc)) == T {
					return e.eval(n.args[i+1], sc)
				}
			}
		}
		return e.eval(n.args[len(n.args)-1], sc)
	case "unary":
		return e.unary(n, sc)
	case "binary":
		return e.binary(n, sc)
	case "between":
		x, lo, hi := e.eval(n.args[0], sc), e.eval(n.args[1], sc), e.eval(n.args[2], sc)
		return and3(cmpOp(e, sc, n, ">=", x, lo), cmpOp(e, sc, n, "<=", x, hi))
	case "timing":
		return e.timing(n, sc)
	case "is":
		return isType(e.eval(n.args[0], sc), n.typ)
	case "as":
		v := e.eval(n.args[0], sc)
		if v == nil || isType(v, n.typ) {
			return v
		}
		return nil
	case "convert":
		return e.builtin("To"+strings.TrimPrefix(strings.TrimPrefix(n.typ, "System."), "FHIR."), []any{e.eval(n.args[0], sc)}, n, sc)
	case "convertunit":
		q, ok := sys(e.eval(n.args[0], sc)).(Quantity)
		if !ok {
			return nil
		}
		if r, ok := convertUnit(q, n.s); ok {
			return r
		}
		e.fail(sc.lib, n, "converting %s to '%s' needs a unit conversion this engine does not know", Show(q), n.s)
	case "datepart":
		d, ok := sys(e.eval(n.args[0], sc)).(DateTime)
		if !ok {
			return nil
		}
		if d.TimeOnly && (n.s == "year" || n.s == "month" || n.s == "day" || n.s == "date") {
			return nil
		}
		switch n.s {
		case "date":
			return DateTime{T: time.Date(d.T.Year(), d.T.Month(), d.T.Day(), 0, 0, 0, 0, time.UTC), Prec: min(d.Prec, pDay), DateOnly: true}
		case "time":
			if d.DateOnly || d.Prec < pHour {
				return nil
			}
			return DateTime{T: timeDay.Add(clock(d.T)), Prec: d.Prec, TimeOnly: true}
		case "millisecond":
			if d.Prec < pMilli {
				return nil
			}
			return int64(d.T.Nanosecond() / 1e6)
		case "timezoneoffset":
			if d.DateOnly || d.TimeOnly {
				return nil
			}
			_, off := d.T.Zone()
			return roundTo(float64(off)/3600, 8)
		case "year":
			return int64(d.T.Year())
		case "month":
			if d.Prec < pMonth {
				return nil
			}
			return int64(d.T.Month())
		case "day":
			if d.Prec < pDay {
				return nil
			}
			return int64(d.T.Day())
		case "hour":
			if d.Prec < pHour {
				return nil
			}
			return int64(d.T.Hour())
		case "minute":
			if d.Prec < pMinute {
				return nil
			}
			return int64(d.T.Minute())
		case "second":
			if d.Prec < pSecond {
				return nil
			}
			return int64(d.T.Second())
		}
		e.fail(sc.lib, n, "%s from is not supported", n.s)
	case "between-dt":
		a, aok := sys(e.eval(n.args[0], sc)).(DateTime)
		b, bok := sys(e.eval(n.args[1], sc)).(DateTime)
		if !aok || !bok {
			return nil
		}
		return between(a, b, n.val.(string), n.s == "difference")
	case "retrieve":
		return e.retrieve(n, sc)
	case "query":
		return e.query(n, sc)
	case "external":
		e.fail(sc.lib, n, "%s is an external function, which this engine does not have", n.s)
	}
	e.fail(sc.lib, n, "%s is not supported", n.op)
	return nil
}

func (e *Env) evalArgs(ns []*node, sc *scope) []any {
	out := make([]any, len(ns))
	for i, a := range ns {
		out[i] = e.eval(a, sc)
	}
	return out
}

func (e *Env) ident(n *node, sc *scope) any {
	if v, ok := sc.lookup(n.s); ok {
		return v
	}
	if v, ok := e.member(sc.lib, n.s, n); ok {
		return v
	}
	if v, ok := e.include(sc.lib, n.s); ok {
		return v
	}
	if n.s == "Patient" && sc.lib.Context == "Patient" {
		if e.patient == nil {
			return nil
		}
		return &Elem{V: e.patient, Type: "Patient"}
	}
	for c := sc; c != nil; c = c.parent {
		if c.implicit != nil {
			return property(c.implicit, n.s)
		}
	}
	if n.s == "$this" {
		return nil
	}
	e.fail(sc.lib, n, "%q is not defined", n.s)
	return nil
}

func (e *Env) callIn(lib *Library, name string, args []any, n *node, sc *scope) any {
	if f, ok := lib.functions[name]; ok {
		return e.apply(lib, f, args, n)
	}
	return e.builtin(name, args, n, sc)
}

func (e *Env) apply(lib *Library, f *function, args []any, n *node) any {
	if len(args) != len(f.params) {
		e.fail(lib, n, "the function takes %d argument(s), given %d", len(f.params), len(args))
	}
	vars := map[string]any{}
	for i, p := range f.params {
		vars[p] = args[i]
	}
	e.depth++
	defer func() { e.depth-- }()
	if e.depth > maxDepth {
		e.fail(lib, n, "functions call each other more than %d deep, and were stopped", maxDepth)
	}
	return e.eval(f.body, &scope{lib: lib, vars: vars})
}

// retrieve is the patient's resources of a type, those coded in a terminology when one is given.
func (e *Env) retrieve(n *node, sc *scope) any {
	var term any
	if len(n.args) > 0 {
		term = e.eval(n.args[0], sc)
	}
	path := n.s
	if path == "" {
		path = codePath[n.typ]
	}
	out := []any{}
	for _, r := range e.Resources {
		if r["resourceType"] != n.typ {
			continue
		}
		el := &Elem{V: r, Type: n.typ}
		if len(n.args) > 0 {
			if path == "" {
				e.fail(sc.lib, n, "a retrieve of %s by terminology needs the code path ([%s: path in ...])", n.typ, n.typ)
			}
			if !codedIn(property(el, path), term) {
				continue
			}
		}
		out = append(out, el)
		ref := fmt.Sprintf("%s/%v", n.typ, r["id"])
		if !e.read[ref] {
			e.read[ref] = true
			e.reads = append(e.reads, ref)
		}
	}
	return out
}

// codePath is each resource's primary code element: what a retrieve by terminology filters on (FHIR's QI-Core model
// info calls it the primaryCodePath).
var codePath = map[string]string{"Condition": "code", "Observation": "code", "Procedure": "code",
	"MedicationRequest": "medication", "MedicationStatement": "medication", "MedicationDispense": "medication",
	"MedicationAdministration": "medication", "ServiceRequest": "code", "DeviceRequest": "code", "Encounter": "type",
	"Coverage": "type", "AllergyIntolerance": "code", "Immunization": "vaccineCode", "DiagnosticReport": "code",
	"Device": "type", "Goal": "category", "CarePlan": "category", "Communication": "category",
	"CommunicationRequest": "category", "FamilyMemberHistory": "relationship", "Specimen": "type",
	"NutritionOrder": "oralDiet.type", "Substance": "code", "Medication": "code", "DocumentReference": "type",
	"Claim": "type", "ClaimResponse": "type"}

// codedIn: one of the element's codings is in the terminology (a value set, a code, a concept, a list of codes).
func codedIn(v any, term any) bool {
	for _, c := range codesOf(v) {
		if inTerm(c, term) {
			return true
		}
	}
	return false
}

func inTerm(c Code, term any) bool {
	switch t := sys(term).(type) {
	case *ValueSet:
		return t.Has(c)
	case Code:
		return c.Code == t.Code && c.System == t.System
	case Concept:
		for _, x := range t.Codes {
			if c.Code == x.Code && c.System == x.System {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if inTerm(c, x) {
				return true
			}
		}
	}
	return false
}

// codesOf is every code in a value: a Code, a Concept, a FHIR Coding or CodeableConcept, a list of them.
func codesOf(v any) []Code {
	switch x := sys(v).(type) {
	case Code:
		return []Code{x}
	case Concept:
		return x.Codes
	case []any:
		var out []Code
		for _, e := range x {
			out = append(out, codesOf(e)...)
		}
		return out
	}
	return nil
}

func (e *Env) query(n *node, sc *scope) any {
	q := n.q
	type src struct {
		alias  string
		items  []any
		single bool
	}
	var srcs []src
	for _, s := range q.sources {
		v := e.eval(s.expr, sc)
		if l, ok := v.([]any); ok {
			srcs = append(srcs, src{s.alias, l, false})
		} else {
			if v == nil {
				srcs = append(srcs, src{s.alias, nil, true})
			} else {
				srcs = append(srcs, src{s.alias, []any{v}, true})
			}
		}
	}
	single := len(srcs) == 1 && srcs[0].single
	var rows []map[string]any
	var walk func(i int, row map[string]any)
	walk = func(i int, row map[string]any) {
		e.tick(sc, n)
		if i == len(srcs) {
			cp := map[string]any{}
			for k, v := range row {
				cp[k] = v
			}
			rows = append(rows, cp)
			return
		}
		for _, it := range srcs[i].items {
			row[srcs[i].alias] = it
			walk(i+1, row)
		}
		delete(row, srcs[i].alias)
	}
	walk(0, map[string]any{})
	var out []any
	var acc any
	if q.agg != nil && q.agg.start != nil {
		acc = e.eval(q.agg.start, sc)
	}
	if q.agg != nil && q.agg.distinct {
		// Distinct rows: the source's value, or with several sources the tuple of theirs.
		var seen []any
		var kept []map[string]any
		for _, row := range rows {
			key := row[srcs[0].alias]
			if len(srcs) > 1 {
				t := &Tuple{M: map[string]any{}}
				for _, s := range srcs {
					t.Keys, t.M[s.alias] = append(t.Keys, s.alias), row[s.alias]
				}
				key = t
			}
			if inList(key, seen) != T {
				seen = append(seen, key)
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	for _, row := range rows {
		rs := sc.child(row)
		for _, l := range q.lets {
			rs.vars[l.name] = e.eval(l.expr, rs)
		}
		keep := true
		for _, r := range q.rels {
			items := toList(e.eval(r.src.expr, rs))
			found := false
			for _, it := range items {
				inner := rs.child(map[string]any{r.src.alias: it})
				if truth(e.eval(r.such, inner)) == T {
					found = true
					break
				}
			}
			if found == r.without {
				keep = false
				break
			}
		}
		if !keep {
			continue
		}
		if q.where != nil && truth(e.eval(q.where, rs)) != T {
			continue
		}
		if q.agg != nil {
			rs.vars[q.agg.name] = acc
			acc = e.eval(q.agg.expr, rs)
			continue
		}
		switch {
		case q.ret != nil:
			out = append(out, e.eval(q.ret, rs))
		case len(srcs) == 1:
			out = append(out, row[srcs[0].alias])
		default:
			t := &Tuple{M: map[string]any{}}
			for _, s := range srcs {
				t.Keys = append(t.Keys, s.alias)
				t.M[s.alias] = row[s.alias]
			}
			out = append(out, t)
		}
	}
	if q.agg != nil {
		return acc
	}
	if q.ret != nil && q.distinct {
		out = distinct(out)
	}
	switch {
	case len(q.sort) > 0:
		keys := make([][]any, len(out))
		for i, v := range out {
			isc := &scope{lib: sc.lib, parent: sc, implicit: v, vars: map[string]any{"$this": v}}
			for _, it := range q.sort {
				keys[i] = append(keys[i], e.eval(it.expr, isc))
			}
		}
		idx := make([]int, len(out))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			for k, it := range q.sort {
				c := order(keys[idx[a]][k], keys[idx[b]][k])
				if it.desc {
					c = -c
				}
				if c != 0 {
					return c < 0
				}
			}
			return false
		})
		sorted := make([]any, len(out))
		for i, j := range idx {
			sorted[i] = out[j]
		}
		out = sorted
	case q.sortDir != "":
		sortAny(out, func(a, b any) bool {
			if q.sortDir == "desc" {
				return order(a, b) > 0
			}
			return order(a, b) < 0
		})
	}
	if single {
		if len(out) == 0 {
			return nil
		}
		return out[0]
	}
	if out == nil {
		out = []any{}
	}
	return out
}

// order sorts nulls first, then by value; values that do not compare keep their places.
func order(a, b any) int {
	a, b = sys(a), sys(b)
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	c, ok := compare(a, b)
	if !ok {
		// Dates that agree to the coarser precision: the less precise first (@2012-10-05 before @2012-10-05T10).
		if x, isX := a.(DateTime); isX {
			if y, isY := b.(DateTime); isY {
				return cmpF(float64(x.Prec), float64(y.Prec))
			}
		}
		return 0
	}
	return c
}

// sys is a value with FHIR data turned into CQL's own values, as FHIRHelpers' implicit conversions do: a primitive
// into its value, a Quantity, Coding, CodeableConcept or Period into a Quantity, Code, Concept or Interval.
func sys(v any) any {
	el, ok := v.(*Elem)
	if !ok {
		return v
	}
	m, isMap := el.V.(map[string]any)
	if !isMap {
		return primitive(el)
	}
	if _, isRes := m["resourceType"]; isRes {
		return el
	}
	switch kind := fhirKind(el); kind {
	case "Quantity":
		return toQuantity(m)
	case "Coding":
		return toCode(m)
	case "CodeableConcept":
		return toConcept(m)
	case "Period":
		return Interval{Low: sys(property(el, "start")), High: sys(property(el, "end")), LowClosed: true, HighClosed: true}
	case "Range":
		lo, hi := property(el, "low"), property(el, "high")
		return Interval{Low: sys(lo), High: sys(hi), LowClosed: true, HighClosed: true}
	case "Ratio":
		num, nok := sys(wrap(m["numerator"], "Quantity", nil)).(Quantity)
		den, dok := sys(wrap(m["denominator"], "Quantity", nil)).(Quantity)
		if nok && dok {
			return Ratio{num, den}
		}
	}
	return el
}

var quantityTypes = map[string]bool{"Quantity": true, "Age": true, "Duration": true, "Distance": true, "Count": true,
	"SimpleQuantity": true, "MoneyQuantity": true}

// fhirKind is a FHIR complex value's type: its own where known, or what its shape says.
func fhirKind(el *Elem) string {
	if quantityTypes[el.Type] {
		return "Quantity"
	}
	switch el.Type {
	case "Coding", "CodeableConcept", "Period", "Range", "Ratio":
		return el.Type
	}
	if el.Type != "" {
		return el.Type
	}
	m, _ := el.V.(map[string]any)
	_, hasValue := m["value"]
	_, hasUnit := m["unit"]
	_, hasCode := m["code"]
	_, hasSystem := m["system"]
	_, hasCoding := m["coding"]
	_, hasStart := m["start"]
	_, hasEnd := m["end"]
	_, hasLow := m["low"]
	_, hasHigh := m["high"]
	_, hasNum := m["numerator"]
	_, hasDen := m["denominator"]
	switch {
	case hasNum && hasDen:
		return "Ratio"
	case hasCoding:
		return "CodeableConcept"
	case hasValue && (hasUnit || hasCode && hasSystem || hasCode):
		if _, num := m["value"].(json.Number); num {
			return "Quantity"
		}
	case hasCode && hasSystem:
		return "Coding"
	case hasStart || hasEnd:
		return "Period"
	case hasLow || hasHigh:
		return "Range"
	}
	return ""
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func toQuantity(m map[string]any) any {
	n, ok := m["value"].(json.Number)
	if !ok {
		return nil
	}
	f, _ := n.Float64()
	u := str(m, "code")
	if u == "" {
		u = str(m, "unit")
	}
	if u == "" {
		u = "1"
	}
	return Quantity{Value: f, Unit: u}
}

func toCode(m map[string]any) Code {
	return Code{Code: str(m, "code"), System: str(m, "system"), Display: str(m, "display"), Version: str(m, "version")}
}

func toConcept(m map[string]any) Concept {
	c := Concept{Display: str(m, "text")}
	for _, x := range asList(m["coding"]) {
		if cm, ok := x.(map[string]any); ok {
			c.Codes = append(c.Codes, toCode(cm))
		}
	}
	return c
}

// property is a value's element by name. FHIR data's path is followed through lists (a list's property is the
// flattened list of its elements' properties), and a choice element (value[x]) is found by its name and keeps its type.
func property(v any, name string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := []any{}
		for _, it := range x {
			switch p := property(it, name).(type) {
			case nil:
			case []any:
				out = append(out, p...)
			default:
				out = append(out, p)
			}
		}
		return out
	case *Elem:
		m, ok := x.V.(map[string]any)
		if !ok {
			switch name {
			case "value":
				return primitive(x)
			case "id", "extension":
				if em, ok := x.Ext.(map[string]any); ok {
					return wrap(em[name], "", nil)
				}
			}
			return nil
		}
		if raw, ok := m[name]; ok {
			return wrap(raw, elementType(x.Type, name), m["_"+name])
		}
		for k, raw := range m {
			if len(k) > len(name) && strings.HasPrefix(k, name) && k[len(name)] >= 'A' && k[len(name)] <= 'Z' {
				return wrap(raw, k[len(name):], m["_"+k])
			}
		}
		if _, has := m["_"+name]; has {
			return &Elem{Ext: m["_"+name]}
		}
		return nil
	case Code:
		return map[string]any{"code": x.Code, "system": x.System, "display": x.Display, "version": x.Version}[name]
	case Concept:
		switch name {
		case "codes":
			out := []any{}
			for _, c := range x.Codes {
				out = append(out, c)
			}
			return out
		case "display":
			return x.Display
		}
	case Quantity:
		switch name {
		case "value":
			return x.Value
		case "unit":
			return x.Unit
		}
	case Interval:
		return map[string]any{"low": x.Low, "high": x.High, "lowClosed": x.LowClosed, "highClosed": x.HighClosed}[name]
	case *Tuple:
		return x.M[name]
	}
	return nil
}

// elementType: the few element types worth knowing by name, where the JSON alone does not tell.
func elementType(parent, name string) string {
	switch name {
	case "coding":
		return "Coding"
	case "period", "effectivePeriod":
		return "Period"
	}
	switch parent + "." + name {
	case "Condition.code", "Observation.code", "Procedure.code", "Condition.clinicalStatus",
		"Condition.verificationStatus", "Encounter.type", "Observation.interpretation":
		return "CodeableConcept"
	case "Encounter.period":
		return "Period"
	case "Observation.issued":
		return "instant"
	case "Patient.birthDate":
		return "date"
	}
	return ""
}

func wrap(raw any, typ string, ext any) any {
	switch r := raw.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, 0, len(r))
		exts := asList(ext)
		for i, it := range r {
			var x any
			if i < len(exts) {
				x = exts[i]
			}
			if w := wrap(it, typ, x); w != nil {
				out = append(out, w)
			}
		}
		return out
	case map[string]any:
		if rt, ok := r["resourceType"].(string); ok {
			typ = rt
		}
		return &Elem{V: r, Type: typ}
	}
	return &Elem{V: raw, Type: typ, Ext: ext}
}

func toList(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []any{v}
}

func distinct(l []any) []any {
	out := []any{}
	for _, v := range l {
		dup := false
		for _, o := range out {
			if eq, known := equal(v, o); (known && eq) || (v == nil && o == nil) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

func flatten(l []any) []any {
	out := []any{}
	for _, v := range l {
		if inner, ok := v.([]any); ok {
			out = append(out, flatten(inner)...)
		} else {
			out = append(out, v)
		}
	}
	return out
}

// isType is CQL's "is": FHIR data by its type or shape, CQL values by theirs. Namespaces (FHIR., System.) are not
// told apart: FHIR.Quantity and System.Quantity both mean a quantity.
func isType(v any, t string) bool {
	if v == nil {
		return false
	}
	t = strings.TrimPrefix(strings.TrimPrefix(t, "FHIR."), "System.")
	if strings.HasPrefix(t, "List<") {
		_, ok := v.([]any)
		return ok
	}
	if strings.HasPrefix(t, "Interval<") {
		_, ok := sys(v).(Interval)
		return ok
	}
	lt := strings.ToLower(t)
	if el, ok := v.(*Elem); ok {
		if m, isMap := el.V.(map[string]any); isMap {
			if rt, _ := m["resourceType"].(string); rt != "" {
				return rt == t || t == "Resource" || t == "DomainResource" || t == "Any"
			}
			k := fhirKind(el)
			if quantityTypes[t] && k == "Quantity" {
				return el.Type == "" || el.Type == t || t == "Quantity"
			}
			return strings.EqualFold(k, t) || lt == "any"
		}
		if el.Type != "" {
			if strings.EqualFold(el.Type, t) {
				return true
			}
			// a dateTime choice holds a dateTime, never an instant or a date
			return false
		}
		v = primitive(el)
	}
	switch x := v.(type) {
	case bool:
		return lt == "boolean"
	case int64:
		return lt == "integer"
	case Long:
		return lt == "long"
	case Ratio:
		return lt == "ratio"
	case float64:
		return lt == "decimal"
	case string:
		return lt == "string" || lt == "code" || lt == "uri" || lt == "id"
	case DateTime:
		switch lt {
		case "date":
			return x.DateOnly
		case "time":
			return x.TimeOnly
		case "datetime", "instant":
			return !x.TimeOnly && (!x.DateOnly || lt == "datetime")
		}
		return false
	case Quantity:
		return quantityTypes[t]
	case Code:
		return t == "Code" || t == "Coding"
	case Concept:
		return t == "Concept" || t == "CodeableConcept"
	case Interval:
		return t == "Interval"
	case *ValueSet:
		return t == "ValueSet" || t == "Vocabulary"
	case *Tuple:
		return t == "Tuple"
	case []any:
		return t == "List"
	}
	return lt == "any"
}

// truth values of three-valued logic.
type tv int

const (
	N tv = iota // null
	T
	F
)

func truth(v any) tv {
	switch b := sys(v).(type) {
	case bool:
		if b {
			return T
		}
		return F
	}
	return N
}

func (t tv) val() any {
	switch t {
	case T:
		return true
	case F:
		return false
	}
	return nil
}

func and3(a, b any) any {
	x, y := truth(a), truth(b)
	switch {
	case x == F || y == F:
		return false
	case x == T && y == T:
		return true
	}
	return nil
}

func or3(a, b any) any {
	x, y := truth(a), truth(b)
	switch {
	case x == T || y == T:
		return true
	case x == F && y == F:
		return false
	}
	return nil
}

func not3(a any) any {
	switch truth(a) {
	case T:
		return false
	case F:
		return true
	}
	return nil
}

func (e *Env) unary(n *node, sc *scope) any {
	v := e.eval(n.args[0], sc)
	switch n.s {
	case "not":
		return not3(v)
	case "exists":
		for _, x := range toList(v) {
			if x != nil {
				return true
			}
		}
		return false
	case "is null":
		return sys(v) == nil
	case "is true":
		return truth(v) == T
	case "is false":
		return truth(v) == F
	case "-":
		switch x := sys(v).(type) {
		case int64, Long:
			r, _ := intArith("-", map[bool]any{true: Long(0), false: int64(0)}[isLong(x)], x)
			return r
		case float64:
			return -x
		case Quantity:
			return Quantity{-x.Value, x.Unit}
		case nil:
			return nil
		}
	case "distinct":
		if v == nil {
			return nil
		}
		return distinct(toList(v))
	case "flatten":
		if v == nil {
			return nil
		}
		return flatten(toList(v))
	case "singleton from":
		l := toList(v)
		switch len(l) {
		case 0:
			return nil
		case 1:
			return l[0]
		}
		e.fail(sc.lib, n, "singleton from a list of %d", len(l))
	case "start of", "end of", "width of", "point from":
		i, ok := sys(v).(Interval)
		if !ok {
			if v == nil {
				return nil
			}
			e.fail(sc.lib, n, "%s needs an interval, not %s", n.s, Show(v))
		}
		switch n.s {
		case "start of":
			lo, _ := closedBounds(i)
			return lo
		case "end of":
			_, hi := closedBounds(i)
			return hi
		case "point from":
			if i.Low == nil && i.High == nil && i.LowClosed && i.HighClosed {
				e.fail(sc.lib, n, "point from the maximum interval, Interval[null, null]")
			}
			if eq, known := equal(i.Low, i.High); known && eq {
				return i.Low
			} else if !known {
				return nil
			}
			e.fail(sc.lib, n, "point from an interval of more than one point")
		}
		return arith(e, sc, n, "-", i.High, i.Low)
	case "successor of":
		return successor(v)
	case "predecessor of":
		return predecessor(v)
	case "collapse":
		var per any
		if len(n.args) > 1 && n.args[1] != nil {
			per = e.eval(n.args[1], sc)
		}
		return collapse(e, sc, n, v, per)
	case "expand":
		var per any
		if len(n.args) > 1 && n.args[1] != nil {
			per = e.eval(n.args[1], sc)
		}
		return expand(e, sc, n, v, per)
	}
	e.fail(sc.lib, n, "%s is not supported here", n.s)
	return nil
}

func (e *Env) binary(n *node, sc *scope) any {
	switch n.s {
	case "and":
		a := e.eval(n.args[0], sc)
		if truth(a) == F {
			return false
		}
		return and3(a, e.eval(n.args[1], sc))
	case "or":
		a := e.eval(n.args[0], sc)
		if truth(a) == T {
			return true
		}
		return or3(a, e.eval(n.args[1], sc))
	case "implies":
		a := e.eval(n.args[0], sc)
		if truth(a) == F {
			return true
		}
		return or3(not3(a), e.eval(n.args[1], sc))
	case "xor":
		a, b := truth(e.eval(n.args[0], sc)), truth(e.eval(n.args[1], sc))
		if a == N || b == N {
			return nil
		}
		return a != b
	}
	a, b := e.eval(n.args[0], sc), e.eval(n.args[1], sc)
	if prec, _ := n.val.(string); prec != "" {
		// compared at a precision: dates and intervals of them cut to it
		a, b = atPrecision(a, prec), atPrecision(b, prec)
	}
	switch n.s {
	case "=", "!=", "~", "!~", "<", "<=", ">", ">=", "+", "-", "*", "/", "div", "mod", "^", "&":
		// list demotion (CQL 1.4): a list where one value is wanted is that value, when it holds one
		a, b = demote(a, b), demote(b, a)
	case "in":
		if _, list := sys(b).([]any); !list || isList(a) {
			a = demote(a, nil)
		} else {
			a = demote(a, b)
		}
	}
	switch n.s {
	case "=", "!=", "~", "!~":
		if isNullLit(n.args[0]) && isNullLit(n.args[1]) {
			e.fail(sc.lib, n, "%s between two untyped nulls: no operator matches (cast one, as in null as String)", n.s)
		}
		if ta, ok := sys(a).(*Tuple); ok {
			if tb, ok := sys(b).(*Tuple); ok && !sameKeys(ta, tb) {
				e.fail(sc.lib, n, "%s between tuples with different elements: no operator matches", n.s)
			}
		}
	}
	switch n.s {
	case "=":
		if (isUncertainty(a) || isUncertainty(b)) && !(isUncertainty(a) && isUncertainty(b)) {
			return uncertainCompare("=", a, b)
		}
		if eq, known := equal(a, b); known {
			return eq
		}
		return nil
	case "!=":
		if eq, known := equal(a, b); known {
			return !eq
		}
		return nil
	case "~":
		return equivalent(a, b)
	case "!~":
		return !equivalent(a, b)
	case "<", "<=", ">", ">=":
		return cmpOp(e, sc, n, n.s, a, b)
	case "+", "-", "*", "/", "div", "mod", "^":
		return arith(e, sc, n, n.s, a, b)
	case "&":
		as, _ := sys(a).(string)
		bs, _ := sys(b).(string)
		return as + bs
	case "in":
		return member(e, sc, n, a, b)
	case "contains":
		return member(e, sc, n, b, a)
	case "~in", "~contains":
		x, coll := a, b
		if n.s == "~contains" {
			x, coll = b, a
		}
		if sys(coll) == nil {
			return false
		}
		for _, v := range toList(coll) {
			if equivalent(x, v) {
				return true
			}
		}
		return false
	case "union":
		if a == nil && b == nil {
			return nil
		}
		if ia, ok := sys(a).(Interval); ok {
			if ib, ok := sys(b).(Interval); ok {
				return unionIv(e, sc, n, ia, ib)
			}
		}
		if a == nil || b == nil {
			if _, ok := sys(a).(Interval); ok {
				return nil
			}
			if _, ok := sys(b).(Interval); ok {
				return nil
			}
		}
		return distinct(append(append([]any{}, toList(a)...), toList(b)...))
	case "intersect":
		if a == nil || b == nil {
			return nil
		}
		if ia, ok := sys(a).(Interval); ok {
			if ib, ok := sys(b).(Interval); ok {
				return intersectIv(e, sc, n, ia, ib)
			}
		}
		out := []any{}
		for _, x := range toList(a) {
			if inList(x, toList(b)) == T {
				out = append(out, x)
			}
		}
		return distinct(out)
	case "except":
		if a == nil {
			return nil
		}
		if ia, ok := sys(a).(Interval); ok {
			if b == nil {
				return nil
			}
			if ib, ok := sys(b).(Interval); ok {
				return exceptIv(e, sc, n, ia, ib)
			}
		}
		out := []any{}
		for _, x := range toList(a) {
			if inList(x, toList(b)) != T {
				out = append(out, x)
			}
		}
		return distinct(out)
	case "includes", "properly includes":
		return includes(e, sc, n, a, b, n.s == "properly includes")
	case "included in", "properly included in":
		return includes(e, sc, n, b, a, n.s == "properly included in")
	case "overlaps", "overlaps before", "overlaps after":
		ia, aok := sys(a).(Interval)
		ib, bok := sys(b).(Interval)
		if !aok {
			if a == nil {
				return nil
			}
			if bok {
				return member(e, sc, n, a, b)
			}
		}
		if !bok {
			if b == nil {
				return nil
			}
			return member(e, sc, n, b, a)
		}
		ia, ib = fillNullBounds(ia, ib)
		base := overlapping(e, sc, n, ia, ib)
		aLo, aHi := pointsOf(ia)
		bLo, bHi := pointsOf(ib)
		switch n.s {
		case "overlaps before":
			return and3(base, cmpOp(e, sc, n, "<", aLo, bLo))
		case "overlaps after":
			return and3(base, cmpOp(e, sc, n, ">", aHi, bHi))
		}
		return base
	case "meets", "meets before", "meets after", "starts", "ends":
		return intervalOp(e, sc, n, n.s, a, b)
	}
	e.fail(sc.lib, n, "the operator %s is not supported", n.s)
	return nil
}

// isNullLit is whether a node is the bare null literal, with no type.
func isNullLit(n *node) bool { return n != nil && n.op == "lit" && n.val == nil && n.typ == "" }

// sameKeys is whether two tuples have the same element names.
func sameKeys(a, b *Tuple) bool {
	if len(a.M) != len(b.M) {
		return false
	}
	for k := range a.M {
		if _, ok := b.M[k]; !ok {
			return false
		}
	}
	return true
}

func equalVal(a, b any) any {
	if eq, known := equal(a, b); known {
		return eq
	}
	return nil
}

func member(e *Env, sc *scope, n *node, x, coll any) any {
	switch c := sys(coll).(type) {
	case nil:
		return false // nothing is in a null list or interval
	case *ValueSet:
		if x == nil {
			return nil
		}
		cs := codesOf(x)
		if len(cs) == 0 {
			if s, ok := sys(x).(string); ok {
				for k := range c.codes {
					if strings.HasSuffix(k, "|"+s) {
						return true
					}
				}
				return false
			}
		}
		for _, code := range cs {
			if c.Has(code) {
				return true
			}
		}
		return false
	case Interval:
		if x == nil {
			return nil
		}
		if xi, ok := sys(x).(Interval); ok {
			return includes(e, sc, n, c, xi, false)
		}
		lo := cmpOp(e, sc, n, map[bool]string{true: "<=", false: "<"}[c.LowClosed], c.Low, x)
		hi := cmpOp(e, sc, n, map[bool]string{true: "<=", false: "<"}[c.HighClosed], x, c.High)
		if c.Low == nil && c.LowClosed {
			lo = true
		}
		if c.High == nil && c.HighClosed {
			hi = true
		}
		return and3(lo, hi)
	case []any:
		if x == nil {
			for _, v := range c {
				if v == nil {
					return true
				}
			}
			return false
		}
		return inList(x, c).val()
	case Concept:
		for _, code := range codesOf(x) {
			if inTerm(code, c) {
				return true
			}
		}
		return false
	case Code:
		for _, code := range codesOf(x) {
			if inTerm(code, c) {
				return true
			}
		}
		return false
	}
	e.fail(sc.lib, n, "in needs a list, an interval or a value set, not %s", Show(coll))
	return nil
}

// inList is whether x is among l: an element equal to it, or a null for a null (as CQL's list operators count).
// Dates that agree only to the coarser precision leave it unknown.
func inList(x any, l []any) tv {
	unknown := false
	for _, v := range l {
		if sys(x) == nil || sys(v) == nil {
			if sys(x) == nil && sys(v) == nil {
				return T
			}
			continue
		}
		eq, known := equal(x, v)
		if known && eq {
			return T
		}
		if !known {
			unknown = true
		}
	}
	if unknown {
		return N
	}
	return F
}

func includes(e *Env, sc *scope, n *node, a, b any, proper bool) any {
	if _, isList := sys(a).([]any); isList || a == nil && (isListNode(n, 0) || isListNode(n, 1)) {
		if sys(a) == nil {
			if _, bl := sys(b).([]any); bl || isListNode(n, 0) && isListNode(n, 1) {
				return nil // a null list includes a list: unknown
			}
			return false
		}
		if sys(b) == nil && (isListNode(n, 0) && isListNode(n, 1)) {
			return nil
		}
		la := toList(a)
		if _, bl := sys(b).([]any); !bl {
			// an element: in the list, and the list holds more than it
			in := inList(b, la)
			if proper {
				return in == T && len(la) > 1
			}
			return in.val()
		}
	}
	if a == nil || b == nil {
		return nil
	}
	if ia, ok := sys(a).(Interval); ok && proper {
		if _, isIv := sys(b).(Interval); !isIv {
			// a point properly inside: in the interval and at neither end
			lo, hi := pointsOf(ia)
			return and3(cmpOp(e, sc, n, "<", lo, b), cmpOp(e, sc, n, "<", b, hi))
		}
	}
	if ia, ok := sys(a).(Interval); ok {
		ib, isInt := sys(b).(Interval)
		if !isInt {
			return member(e, sc, n, b, ia)
		}
		ia, ib = fillNullBounds(ia, ib)
		aLo, aHi := pointsOf(ia)
		bLo, bHi := pointsOf(ib)
		r := and3(cmpOp(e, sc, n, "<=", aLo, bLo), cmpOp(e, sc, n, ">=", aHi, bHi))
		if proper {
			return and3(r, not3(and3(equalVal(aLo, bLo), equalVal(aHi, bHi))))
		}
		return r
	}
	la := toList(a)
	for _, x := range toList(b) {
		if inList(x, la) != T {
			return false
		}
	}
	if proper {
		return len(toList(a)) > len(toList(b))
	}
	return true
}

// timing compares two points, or the ends of intervals, at a precision.
func (e *Env) timing(n *node, sc *scope) any {
	a, b := e.eval(n.args[0], sc), e.eval(n.args[1], sc)
	prec := n.val.(string)
	ia, aIsInt := sys(a).(Interval)
	ib, bIsInt := sys(b).(Interval)
	start := func(v any, i Interval, isInt bool) any {
		if isInt {
			return i.Low
		}
		return sys(v)
	}
	end := func(v any, i Interval, isInt bool) any {
		if isInt {
			return i.High
		}
		return sys(v)
	}
	at := func(v any) any {
		if d, ok := v.(DateTime); ok && prec != "" {
			p := precIndex[prec]
			if prec == "week" {
				p = pDay
			}
			return DateTime{T: d.truncate(p), Prec: min(d.Prec, p), DateOnly: d.DateOnly}
		}
		return v
	}
	var q Quantity
	if n.args[2] != nil {
		q = e.eval(n.args[2], sc).(Quantity)
	}
	op := n.s
	onOr := strings.HasSuffix(op, " on or")
	op = strings.TrimSuffix(op, " on or")
	strict := map[bool]string{true: "<=", false: "<"}[onOr] // the near end of "or less": open, or closed with "on or"
	switch op {
	case "before":
		return cmpOp(e, sc, n, "<", at(end(a, ia, aIsInt)), at(start(b, ib, bIsInt)))
	case "after":
		return cmpOp(e, sc, n, ">", at(start(a, ia, aIsInt)), at(end(b, ib, bIsInt)))
	case "on or before", "same or before":
		return cmpOp(e, sc, n, "<=", at(end(a, ia, aIsInt)), at(start(b, ib, bIsInt)))
	case "on or after", "same or after":
		return cmpOp(e, sc, n, ">=", at(start(a, ia, aIsInt)), at(end(b, ib, bIsInt)))
	case "same as":
		return equalVal(at(sys(a)), at(sys(b)))
	case "within":
		x, y := start(a, ia, aIsInt), start(b, ib, bIsInt)
		lo, hi := arith(e, sc, n, "-", y, q), arith(e, sc, n, "+", end(b, ib, bIsInt), q)
		return and3(cmpOp(e, sc, n, ">=", x, lo), cmpOp(e, sc, n, "<=", end(a, ia, aIsInt), hi))
	case "by before", "by before or less", "by before or more":
		x, y := end(a, ia, aIsInt), start(b, ib, bIsInt)
		edge := arith(e, sc, n, "-", y, q)
		switch op {
		case "by before or less":
			return and3(cmpOp(e, sc, n, ">=", at(x), at(edge)), cmpOp(e, sc, n, strict, at(x), at(y)))
		case "by before or more":
			return cmpOp(e, sc, n, "<=", at(x), at(edge))
		}
		return equalVal(at(x), at(edge))
	case "by after", "by after or less", "by after or more":
		x, y := start(a, ia, aIsInt), end(b, ib, bIsInt)
		edge := arith(e, sc, n, "+", y, q)
		switch op {
		case "by after or less":
			return and3(cmpOp(e, sc, n, "<=", at(x), at(edge)), cmpOp(e, sc, n, strict, at(y), at(x)))
		case "by after or more":
			return cmpOp(e, sc, n, ">=", at(x), at(edge))
		}
		return equalVal(at(x), at(edge))
	}
	e.fail(sc.lib, n, "the timing %s is not supported", n.s)
	return nil
}

// equal is CQL's =: (equal, known). Null on either side, or dates that agree only to the coarser precision, are unknown.
func equal(a, b any) (bool, bool) {
	a, b = sys(a), sys(b)
	if a == nil || b == nil {
		return false, false
	}
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false, ok || true
		}
		known := true
		for i := range x {
			if sys(x[i]) == nil && sys(y[i]) == nil {
				continue // lists are equal element by element, a null matching a null
			}
			eq, k := equal(x[i], y[i])
			if k && !eq {
				return false, true
			}
			known = known && k
		}
		return true, known
	case *Tuple:
		y, ok := b.(*Tuple)
		if !ok || len(x.Keys) != len(y.Keys) {
			return false, true
		}
		known := true
		for _, k := range x.Keys {
			eq, kn := equal(x.M[k], y.M[k])
			if x.M[k] == nil && y.M[k] == nil {
				continue
			}
			if kn && !eq {
				return false, true
			}
			known = known && kn
		}
		return true, known
	case Code:
		y, ok := b.(Code)
		return ok && x.Code == y.Code && x.System == y.System && (x.Version == y.Version || x.Version == "" || y.Version == ""), true
	case Concept:
		y, ok := b.(Concept)
		if !ok || len(x.Codes) != len(y.Codes) {
			return false, true
		}
		for i := range x.Codes {
			if eq, _ := equal(x.Codes[i], y.Codes[i]); !eq {
				return false, true
			}
		}
		return true, true
	case Interval:
		y, ok := b.(Interval)
		if !ok {
			return false, true
		}
		l, lk := equal(x.Low, y.Low)
		h, hk := equal(x.High, y.High)
		if x.Low == nil && y.Low == nil {
			l, lk = true, true
		}
		if x.High == nil && y.High == nil {
			h, hk = true, true
		}
		return l && h && x.LowClosed == y.LowClosed && x.HighClosed == y.HighClosed, lk && hk
	case *Elem:
		y, ok := b.(*Elem)
		if !ok {
			return false, true
		}
		xm, _ := x.V.(map[string]any)
		ym, _ := y.V.(map[string]any)
		if xm != nil && ym != nil && xm["resourceType"] != nil {
			return xm["resourceType"] == ym["resourceType"] && fmt.Sprint(xm["id"]) == fmt.Sprint(ym["id"]), true
		}
		bx, _ := json.Marshal(x.V)
		by, _ := json.Marshal(y.V)
		return string(bx) == string(by), true
	case Ratio:
		y, ok := b.(Ratio)
		if !ok {
			return false, true
		}
		n, nk := equal(x.Num, y.Num)
		d, dk := equal(x.Den, y.Den)
		return n && d, nk && dk
	case Quantity:
		y, ok := b.(Quantity)
		if !ok {
			return false, true
		}
		c, comparable, known := qcompare(x, y, false)
		if !comparable {
			return false, true
		}
		return c == 0, known
	}
	c, ok := compare(a, b)
	if !ok {
		if _, dt := a.(DateTime); dt {
			if _, dt2 := b.(DateTime); dt2 {
				return false, false
			}
		}
		return false, true
	}
	return c == 0, true
}

// equivalent is CQL's ~: never null; strings ignore case and spacing; codes compare by code and system; a concept is
// equivalent to a code or concept it shares a code with.
func equivalent(a, b any) bool {
	a, b = sys(a), sys(b)
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && strings.EqualFold(strings.Join(strings.Fields(x), " "), strings.Join(strings.Fields(y), " "))
	case Code, Concept:
		for _, c := range codesOf(x) {
			for _, d := range codesOf(b) {
				if c.Code == d.Code && c.System == d.System {
					return true
				}
			}
		}
		return false
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equivalent(x[i], y[i]) {
				return false
			}
		}
		return true
	case float64, int64, Long:
		// Decimals are equivalent when equal at the precision of the less precise one, trailing zeros not counting
		// (CQL 1.5, Equivalent): 1.0 ~ 1.00, 1.001 ~ 1.000, but not 1.5 ~ 1.55.
		fa, _ := num(a)
		fb, ok := num(b)
		if !ok {
			return false
		}
		p := min(places(fa), places(fb))
		return roundTo(fa, p) == roundTo(fb, p)
	case Quantity:
		y, ok := b.(Quantity)
		if !ok {
			return false
		}
		c, comparable, known := qcompare(x, y, true)
		return comparable && known && c == 0
	case Ratio:
		// equivalent ratios are equal fractions: 1:100 ~ 2:200
		y, ok := b.(Ratio)
		if !ok {
			return false
		}
		return equivalent(Quantity{x.Num.Value * y.Den.Value, x.Num.Unit}, Quantity{y.Num.Value * x.Den.Value, y.Num.Unit}) &&
			equivalent(Quantity{1, x.Den.Unit}, Quantity{1, y.Den.Unit})
	}
	eq, known := equal(a, b)
	return known && eq
}

// places is how many decimal places a number shows, at most 8, trailing zeros not counting.
func places(f float64) int {
	s := strconv.FormatFloat(roundTo(f, 8), 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return min(len(s)-i-1, 8)
	}
	return 0
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case Long:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// compare orders two values of the same kind: (order, ok); not ok when they do not compare (or are uncertain).
func compare(a, b any) (int, bool) {
	a, b = sys(a), sys(b)
	if fa, ok := num(a); ok {
		fb, ok := num(b)
		if !ok {
			return 0, false
		}
		switch {
		case fa < fb:
			return -1, true
		case fa > fb:
			return 1, true
		}
		return 0, true
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(x, y), true
	case DateTime:
		y, ok := b.(DateTime)
		if !ok {
			return 0, false
		}
		return compareDT(x, y)
	case Quantity:
		y, ok := b.(Quantity)
		if !ok {
			return 0, false
		}
		c, comparable, known := qcompare(x, y, false)
		return c, comparable && known
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		switch {
		case x == y:
			return 0, true
		case !x:
			return -1, true
		}
		return 1, true
	}
	return 0, false
}

func cmpOp(e *Env, sc *scope, n *node, op string, a, b any) any {
	a, b = sys(a), sys(b)
	if a == nil || b == nil {
		return nil
	}
	if isUncertainty(a) || isUncertainty(b) {
		return uncertainCompare(op, a, b)
	}
	c, ok := compare(a, b)
	if !ok {
		_, adt := a.(DateTime)
		_, bdt := b.(DateTime)
		if adt && bdt {
			// agree to the coarser precision: <= and >= hold, < and > are unknown
			if op == "<=" || op == ">=" {
				return nil
			}
			return nil
		}
		if _, aq := a.(Quantity); aq {
			if _, bq := b.(Quantity); bq {
				return nil // quantities of different dimensions, or a calendar year against UCUM's
			}
		}
		e.fail(sc.lib, n, "%s and %s do not compare", Show(a), Show(b))
	}
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	}
	return c >= 0
}

func arith(e *Env, sc *scope, n *node, op string, a, b any) any {
	a, b = sys(a), sys(b)
	if a == nil || b == nil {
		return nil
	}
	if isUncertainty(a) || isUncertainty(b) {
		return uncertainArith(e, sc, n, op, a, b)
	}
	if d, ok := a.(DateTime); ok {
		q, isQ := b.(Quantity)
		if !isQ {
			e.fail(sc.lib, n, "a date takes a quantity of time, not %s", Show(b))
		}
		if op == "-" {
			q.Value = -q.Value
		} else if op != "+" {
			e.fail(sc.lib, n, "dates add and subtract quantities only")
		}
		return addTime(e, sc, n, d, q)
	}
	if qa, ok := a.(Quantity); ok {
		qb, isQ := b.(Quantity)
		switch {
		case isQ && (op == "+" || op == "-" || op == "mod" || op == "div"):
			conv, ok := qconvert(qb, qa.Unit)
			if !ok {
				return nil // of different dimensions
			}
			switch op {
			case "-":
				return Quantity{roundTo(qa.Value-conv.Value, 8), qa.Unit}
			case "mod":
				if conv.Value == 0 {
					return nil
				}
				return Quantity{roundTo(math.Mod(qa.Value, conv.Value), 8), qa.Unit}
			case "div":
				if conv.Value == 0 {
					return nil
				}
				return Quantity{math.Trunc(qa.Value / conv.Value), "1"} // a ratio of like quantities has no unit
			}
			return Quantity{roundTo(qa.Value+conv.Value, 8), qa.Unit}
		case isQ && op == "/" && qa.Unit == qb.Unit:
			if qb.Value == 0 {
				return nil
			}
			return Quantity{roundTo(qa.Value/qb.Value, 8), "1"}
		case isQ && op == "*" && qa.Unit == qb.Unit:
			return Quantity{roundTo(qa.Value*qb.Value, 8), qa.Unit + "2"}
		case !isQ && (op == "*" || op == "/"):
			f, ok := num(b)
			if !ok {
				break
			}
			if op == "*" {
				return Quantity{qa.Value * f, qa.Unit}
			}
			if f == 0 {
				return nil
			}
			return Quantity{qa.Value / f, qa.Unit}
		}
		e.fail(sc.lib, n, "%s %s %s is not supported", Show(a), op, Show(b))
	}
	if sa, ok := a.(string); ok && op == "+" {
		if sb, ok := b.(string); ok {
			return sa + sb
		}
	}
	if r, ok := intArith(op, a, b); ok {
		return r
	}
	fa, aok := num(a)
	fb, bok := num(b)
	if !aok || !bok {
		e.fail(sc.lib, n, "%s %s %s is not supported", Show(a), op, Show(b))
	}
	var r float64
	switch op {
	case "+":
		r = fa + fb
	case "-":
		r = fa - fb
	case "*":
		r = fa * fb
	case "/":
		if fb == 0 {
			return nil
		}
		r = fa / fb
	case "div":
		if fb == 0 {
			return nil
		}
		r = math.Trunc(fa / fb)
	case "mod":
		if fb == 0 {
			return nil
		}
		r = math.Mod(fa, fb)
	case "^":
		r = math.Pow(fa, fb)
	default:
		return nil
	}
	return decimal(r)
}

// decimal is a Decimal result held to CQL's range and precision: 8 places, under 10^28; null outside it.
func decimal(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) >= 1e28 {
		return nil
	}
	return roundTo(f, 8)
}

// intArith is Integer and Long arithmetic, null on overflow (an Integer is 32 bits, a Long 64); ok false when
// either side is not an integer.
func intArith(op string, a, b any) (any, bool) {
	long := false
	val := func(v any) (int64, bool) {
		switch x := v.(type) {
		case int64:
			return x, true
		case Long:
			long = true
			return int64(x), true
		}
		return 0, false
	}
	ia, aok := val(a)
	ib, bok := val(b)
	if !aok || !bok {
		return nil, false
	}
	var r *big.Int
	x, y := big.NewInt(ia), big.NewInt(ib)
	switch op {
	case "+":
		r = new(big.Int).Add(x, y)
	case "-":
		r = new(big.Int).Sub(x, y)
	case "*":
		r = new(big.Int).Mul(x, y)
	case "div", "mod":
		if ib == 0 {
			return nil, true
		}
		if op == "div" {
			r = new(big.Int).Quo(x, y)
		} else {
			r = new(big.Int).Rem(x, y)
		}
	case "^":
		if ib < 0 {
			return nil, false // a negative power is a Decimal
		}
		// A base other than -1, 0 or 1 raised past the 63rd power cannot fit in 64 bits: say so before working
		// it out, since big.Int would build the whole number first (2000^9^9 is four billion bits).
		if ib > 63 && (ia > 1 || ia < -1) {
			return nil, true
		}
		r = new(big.Int).Exp(x, y, nil)
	default:
		return nil, false
	}
	return fitInt(r, long), true
}

// fitInt is r as an Integer (or a Long), or null when it does not fit.
func fitInt(r *big.Int, long bool) any {
	if !r.IsInt64() {
		return nil
	}
	v := r.Int64()
	if long {
		return Long(v)
	}
	if v > math.MaxInt32 || v < math.MinInt32 {
		return nil
	}
	return v
}

// addTime adds a calendar quantity, clamping to the end of a shorter month as CQL does.
func addTime(e *Env, sc *scope, n *node, d DateTime, q Quantity) DateTime {
	u := durationUnit(q.Unit)
	if d.TimeOnly {
		// A time of day takes hours and less, and wraps around midnight.
		unit := map[string]time.Duration{"hour": time.Hour, "minute": time.Minute, "second": time.Second,
			"millisecond": time.Millisecond}[u]
		if unit == 0 {
			e.fail(sc.lib, n, "a time of day takes hours, minutes, seconds or milliseconds, not %s", Show(q))
		}
		// Added at the time's own precision: @T10 + 90 minutes is @T11 (whole hours added), as for dates.
		add := time.Duration(math.Trunc(q.Value)) * unit
		day := 24 * time.Hour
		c := (clock(d.T) + add%day + day) % day
		return DateTime{T: timeDay.Add(c).Truncate(map[int]time.Duration{pHour: time.Hour, pMinute: time.Minute,
			pSecond: time.Second, pMilli: time.Millisecond}[d.Prec]), Prec: d.Prec, TimeOnly: true}
	}
	// A unit finer than the date's own precision is first converted to that precision, truncating: @2014 minus
	// 25 months is @2012 (2 years); @2014-06 minus 33 days is @2014-05 (1 month, of 30 days).
	if up := precIndex[u]; up > d.Prec || (u == "week" && d.Prec < pDay) {
		secs := map[string]float64{"week": 604800, "day": 86400, "hour": 3600, "minute": 60, "second": 1, "millisecond": 0.001}
		target := map[int]string{pYear: "year", pMonth: "month", pDay: "day", pHour: "hour", pMinute: "minute", pSecond: "second"}[d.Prec]
		v := q.Value
		switch {
		case u == "month" && target == "year":
			v = v / 12
		case target == "year":
			v = v * secs[u] / (365 * 86400)
		case target == "month":
			v = v * secs[u] / (30 * 86400)
		default:
			v = v * secs[u] / secs[target]
		}
		q, u = Quantity{math.Trunc(v), target}, target
	}
	whole := math.Trunc(q.Value)
	t := d.T
	switch u {
	case "year", "month":
		months := int(whole)
		if u == "year" {
			months *= 12
		}
		y, m, day := t.Date()
		first := time.Date(y, m, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).AddDate(0, months, 0)
		last := first.AddDate(0, 1, -1).Day()
		t = time.Date(first.Year(), first.Month(), min(day, last), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	case "week":
		t = t.AddDate(0, 0, int(whole)*7)
	case "day":
		t = t.AddDate(0, 0, int(whole))
	case "hour":
		t = t.Add(time.Duration(q.Value * float64(time.Hour)))
	case "minute":
		t = t.Add(time.Duration(q.Value * float64(time.Minute)))
	case "second":
		t = t.Add(time.Duration(q.Value * float64(time.Second)))
	case "millisecond":
		t = t.Add(time.Duration(q.Value * float64(time.Millisecond)))
	default:
		e.fail(sc.lib, n, "%s is not a quantity of time", Show(q))
	}
	if t.Year() < 1 || t.Year() > 9999 {
		e.fail(sc.lib, n, "%s plus %s is outside the years 1 to 9999", Show(d), Show(q))
	}
	return DateTime{T: t, Prec: d.Prec, DateOnly: d.DateOnly}
}

// between: difference (boundaries crossed) or duration (whole periods) from a to b. Where either is known only to a
// coarser precision than the answer needs, the answer is an uncertainty: the interval of what it could be (years
// between DateTime(2005) and DateTime(2010) is Interval[4, 5]), which compares as CQL says.
func between(a, b DateTime, prec string, difference bool) any {
	p := precIndex[prec]
	if prec == "week" {
		p = pDay
	}
	if p == 0 {
		return nil
	}
	fill := p
	if p < pDay {
		fill = pDay // whole years and months depend on the day
	}
	if a.TimeOnly != b.TimeOnly {
		return nil
	}
	if a.Prec < fill || b.Prec < fill {
		// Uncertain: each side is every instant it could stand for, from its first millisecond to its last, so the
		// result runs from the least whole count between them to the greatest (CQL's uncertainty intervals, as the
		// reference engine counts them: days between 2014-01-15 and 2014-02 is 16 to 44).
		at := func(d DateTime, high bool) DateTime {
			out := DateTime{T: d.truncate(d.Prec), Prec: pMilli, DateOnly: false, TimeOnly: d.TimeOnly}
			if high {
				out.T = endOf(d)
			}
			return out
		}
		lo := exactBetween(at(a, true), at(b, false), prec, p, difference)
		hi := exactBetween(at(a, false), at(b, true), prec, p, difference)
		if lo == nil || hi == nil {
			return nil
		}
		if lo.(int64) > hi.(int64) {
			lo, hi = hi, lo
		}
		if lo == hi {
			return lo
		}
		return Interval{Low: lo, High: hi, LowClosed: true, HighClosed: true}
	}
	return exactBetween(a, b, prec, p, difference)
}

func exactBetween(a, b DateTime, prec string, p int, difference bool) any {
	ta, tb := a.T, b.T
	if !a.DateOnly && !a.TimeOnly {
		tb = tb.In(ta.Location()) // calendar fields read in one offset
	}
	if difference {
		ta, tb = a.truncate(p), b.truncate(p)
	}
	sign := int64(1)
	if tb.Before(ta) {
		ta, tb, sign = tb, ta, -1
	}
	months := func() int64 {
		m := int64(tb.Year()-ta.Year())*12 + int64(tb.Month()-ta.Month())
		// A day past the end of the later month (the 31st, February the 29th) is reached on its last day.
		last := time.Date(tb.Year(), tb.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		day := min(ta.Day(), last)
		if tb.Day() < day || (tb.Day() == day && clock(tb) < clock(ta)) {
			m--
		}
		return m
	}
	var v int64
	switch prec {
	case "year":
		v = months() / 12
	case "month":
		v = months()
	case "week", "day":
		if !difference && a.Prec > pDay && b.Prec > pDay {
			// whole days (or weeks) elapsed between the two instants
			v = int64(tb.Sub(ta) / (24 * time.Hour))
		} else {
			v = int64(dayDiff(ta, tb))
		}
		if prec == "week" {
			v /= 7
		}
	case "hour":
		v = int64(tb.Sub(ta) / time.Hour)
	case "minute":
		v = int64(tb.Sub(ta) / time.Minute)
	case "second":
		v = int64(tb.Sub(ta) / time.Second)
	case "millisecond":
		v = int64(tb.Sub(ta) / time.Millisecond)
	}
	return sign * v
}

func clock(t time.Time) time.Duration {
	h, m, s := t.Clock()
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second +
		time.Duration(t.Nanosecond())
}

func dayDiff(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

func minAny(a, b any) any {
	if order(a, b) <= 0 {
		return a
	}
	return b
}

func maxAny(a, b any) any {
	if order(a, b) >= 0 {
		return a
	}
	return b
}

func parseDecimal(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

// demote is a list of at most one element as that element, where the other operand is not a list.
func demote(v, other any) any {
	l, ok := v.([]any)
	if !ok || isList(other) {
		return v
	}
	switch len(l) {
	case 0:
		return nil
	case 1:
		return l[0]
	}
	return v
}

// isListNode is whether a binary node's i-th operand is typed as a list (null as List<String>).
func isListNode(n *node, i int) bool {
	if n == nil || i >= len(n.args) || n.args[i] == nil {
		return false
	}
	a := n.args[i]
	return a.op == "list" || a.op == "as" && strings.HasPrefix(a.typ, "List")
}

func isLong(v any) bool {
	_, ok := v.(Long)
	return ok
}

func isList(v any) bool {
	_, ok := v.([]any)
	return ok
}
