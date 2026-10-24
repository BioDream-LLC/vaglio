package cql

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// node is one expression.
type node struct {
	op   string // see parseTerm and the binary levels
	line int
	s    string // a name, an operator, a precision
	lib  string // a library qualifier for an identifier or a call
	val  any    // a literal's value; an interval's closedness [2]bool; a tuple's field names []string
	typ  string // a type specifier: Quantity, FHIR.Quantity, List<Observation>
	args []*node
	q    *query
}

type query struct {
	sources  []qsource
	lets     []qlet
	rels     []qrel
	where    *node
	ret      *node
	distinct bool
	sort     []qsort
	sortDir  string // a sort with no "by": asc or desc
	agg      *qagg
}

// qagg is a query's aggregate clause: aggregate [all|distinct] R [starting S]: expr.
type qagg struct {
	name     string
	start    *node
	expr     *node
	distinct bool
}

type qsource struct {
	expr  *node
	alias string
}

type qlet struct {
	name string
	expr *node
}

type qrel struct {
	without bool
	src     qsource
	such    *node
}

type qsort struct {
	expr *node
	desc bool
}

// Library is one compiled CQL library.
type Library struct {
	Name, Version string
	Includes      []Include
	CodeSystems   map[string]Def
	ValueSets     map[string]Def
	Codes         map[string]CodeDef
	Concepts      map[string]ConceptDef
	Parameters    map[string]*node
	defines       map[string]*node
	functions     map[string]*function
	order         []string // defines, in the order written
	Context       string
}

// Include is a library this one includes, by its local name.
type Include struct{ Name, Version, Alias string }

// Def is a code system or value set: its id (a URL) and version.
type Def struct{ ID, Version string }

// CodeDef is a code declaration.
type CodeDef struct{ Code, System, Display string }

// ConceptDef is a concept declaration: the names of its codes.
type ConceptDef struct {
	Codes   []string
	Display string
}

type function struct {
	params []string
	body   *node
	fluent bool
}

// Defines is every define the library has, in the order written.
func (l *Library) Defines() []string { return append([]string{}, l.order...) }

type parser struct {
	toks  []token
	i     int
	lib   string
	depth int
}

// maxParseDepth bounds how deep an expression may nest in the text.
const maxParseDepth = 500

func (p *parser) peek() token       { return p.toks[p.i] }
func (p *parser) peekN(n int) token { return p.toks[min(p.i+n, len(p.toks)-1)] }
func (p *parser) next() token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *parser) fail(format string, args ...any) {
	panic(&Error{Library: p.lib, Line: p.peek().line, Msg: fmt.Sprintf(format, args...)})
}

// is: the next token is this word (case-sensitive keywords, as CQL's) or punctuation.
func (p *parser) is(words ...string) bool {
	for n, w := range words {
		t := p.peekN(n)
		if (t.kind != tIdent && t.kind != tPunct) || t.text != w {
			return false
		}
	}
	return true
}

func (p *parser) accept(words ...string) bool {
	if p.is(words...) {
		for range words {
			p.next()
		}
		return true
	}
	return false
}

func (p *parser) expect(w string) {
	if !p.accept(w) {
		p.fail("expected %s, found %s", w, p.peek())
	}
}

// name is an identifier, plain or quoted.
func (p *parser) name() string {
	t := p.peek()
	if t.kind == tIdent || t.kind == tQuoted {
		p.next()
		return t.text
	}
	p.fail("expected a name, found %s", t)
	return ""
}

func (p *parser) str() string {
	t := p.next()
	if t.kind != tString {
		p.i--
		p.fail("expected a 'string', found %s", t)
	}
	return t.text
}

// Compile reads a library.
func Compile(src string) (lib *Library, err error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*Error)
			if !ok {
				panic(r)
			}
			lib, err = nil, e
		}
	}()
	return p.library(), nil
}

func (p *parser) library() *Library {
	l := &Library{CodeSystems: map[string]Def{}, ValueSets: map[string]Def{}, Codes: map[string]CodeDef{},
		Concepts: map[string]ConceptDef{}, Parameters: map[string]*node{}, defines: map[string]*node{},
		functions: map[string]*function{}}
	if p.accept("library") {
		l.Name = p.qualified()
		p.lib = l.Name
		if p.accept("version") {
			l.Version = p.str()
		}
	}
	for p.peek().kind != tEOF {
		access := func() {
			p.accept("private")
			p.accept("public")
		}
		access()
		switch {
		case p.accept("using"):
			p.qualified()
			if p.accept("version") {
				p.str()
			}
		case p.accept("include"):
			in := Include{Name: p.qualified()}
			if p.accept("version") {
				in.Version = p.str()
			}
			in.Alias = in.Name
			if p.accept("called") {
				in.Alias = p.name()
			}
			l.Includes = append(l.Includes, in)
		case p.accept("codesystem"):
			n := p.name()
			p.expect(":")
			d := Def{ID: p.str()}
			if p.accept("version") {
				d.Version = p.str()
			}
			l.CodeSystems[n] = d
		case p.accept("valueset"):
			n := p.name()
			p.expect(":")
			d := Def{ID: p.str()}
			if p.accept("version") {
				d.Version = p.str()
			}
			if p.accept("codesystems") {
				p.expect("{")
				for !p.accept("}") {
					p.next()
				}
			}
			l.ValueSets[n] = d
		case p.accept("code"):
			n := p.name()
			p.expect(":")
			c := CodeDef{Code: p.str()}
			p.expect("from")
			c.System = p.name()
			if p.accept("display") {
				c.Display = p.str()
			}
			l.Codes[n] = c
		case p.accept("concept"):
			n := p.name()
			p.expect(":")
			p.expect("{")
			var c ConceptDef
			for {
				c.Codes = append(c.Codes, p.name())
				if !p.accept(",") {
					break
				}
			}
			p.expect("}")
			if p.accept("display") {
				c.Display = p.str()
			}
			l.Concepts[n] = c
		case p.accept("parameter"):
			n := p.name()
			if !p.is("default") && p.peek().kind != tEOF && !p.isStatement() {
				p.typeSpec()
			}
			var def *node
			if p.accept("default") {
				def = p.expr()
			}
			l.Parameters[n] = def
		case p.accept("context"):
			l.Context = p.name()
		case p.accept("define"):
			access()
			fluent := p.accept("fluent")
			if p.accept("function") {
				n := p.name()
				f := &function{fluent: fluent}
				p.expect("(")
				for !p.accept(")") {
					f.params = append(f.params, p.name())
					p.typeSpec()
					p.accept(",")
				}
				if p.accept("returns") {
					p.typeSpec()
				}
				p.expect(":")
				if p.accept("external") {
					f.body = &node{op: "external", s: n, line: p.peek().line}
				} else {
					f.body = p.expr()
				}
				l.functions[n] = f
				continue
			}
			n := p.name()
			p.expect(":")
			if _, dup := l.defines[n]; dup {
				p.fail("%q is defined twice", n)
			}
			l.defines[n] = p.expr()
			l.order = append(l.order, n)
		default:
			p.fail("expected a declaration (define, code, valueset ...), found %s", p.peek())
		}
	}
	return l
}

func (p *parser) isStatement() bool {
	return p.is("define") || p.is("parameter") || p.is("context") || p.is("code") || p.is("concept") ||
		p.is("valueset") || p.is("codesystem") || p.is("include") || p.is("using") || p.is("private") || p.is("public")
}

func (p *parser) qualified() string {
	n := p.name()
	for p.is(".") && (p.peekN(1).kind == tIdent || p.peekN(1).kind == tQuoted) {
		p.next()
		n += "." + p.name()
	}
	return n
}

func (p *parser) typeSpec() string {
	if p.accept("List") || p.accept("Interval") {
		kind := p.toks[p.i-1].text
		p.expect("<")
		inner := p.typeSpec()
		p.expect(">")
		return kind + "<" + inner + ">"
	}
	if p.accept("Tuple") {
		p.expect("{")
		for !p.accept("}") {
			p.name()
			p.typeSpec()
			p.accept(",")
		}
		return "Tuple"
	}
	if p.accept("Choice") {
		p.expect("<")
		for !p.accept(">") {
			p.typeSpec()
			p.accept(",")
		}
		return "Choice"
	}
	return p.qualified()
}

var keywords, notTerm = map[string]bool{}, map[string]bool{}

func init() {
	for _, w := range strings.Fields(`and or xor implies is as in contains where return sort by let with without such
		that then else end when between union intersect except during overlaps before after starts ends meets includes
		included properly same within of div mod define context`) {
		notTerm[w] = true
	}
	// Words that end an expression when they follow it, though they may also name a property.
	for _, w := range strings.Fields(`code concept valueset codesystem`) {
		keywords[w] = true
	}
	for _, w := range strings.Fields(`and or xor not implies is as in contains where return sort by asc desc ascending
		descending let with without such that from then else end when case if null true false between union intersect
		except during overlaps before after starts ends meets includes included properly same within of on occurs
		div mod distinct all flatten exists collapse expand cast convert to start singleton point width successor
		predecessor difference duration year years month months week weeks day days hour hours minute minutes second
		seconds millisecond milliseconds define function context display version called using include library
		parameter default private public fluent returns external aggregate starting or less more`) {
		keywords[w] = true
	}
}

// precisions, singular, by any spelling.
var precisions = map[string]string{"year": "year", "years": "year", "month": "month", "months": "month",
	"week": "week", "weeks": "week", "day": "day", "days": "day", "hour": "hour", "hours": "hour", "minute": "minute",
	"minutes": "minute", "second": "second", "seconds": "second", "millisecond": "millisecond",
	"milliseconds": "millisecond"}

func (p *parser) mk(op string, args ...*node) *node {
	return &node{op: op, line: p.peek().line, args: args}
}

func (p *parser) expr() *node {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxParseDepth {
		p.fail("the expression nests more than %d deep", maxParseDepth)
	}
	return p.implies()
}

func (p *parser) implies() *node {
	l := p.or()
	for p.accept("implies") {
		l = &node{op: "binary", s: "implies", line: l.line, args: []*node{l, p.or()}}
	}
	return l
}

func (p *parser) or() *node {
	l := p.and()
	for {
		switch {
		case p.accept("or"):
			l = &node{op: "binary", s: "or", line: l.line, args: []*node{l, p.and()}}
		case p.accept("xor"):
			l = &node{op: "binary", s: "xor", line: l.line, args: []*node{l, p.and()}}
		default:
			return l
		}
	}
}

func (p *parser) and() *node {
	l := p.not()
	for p.accept("and") {
		l = &node{op: "binary", s: "and", line: l.line, args: []*node{l, p.not()}}
	}
	return l
}

func (p *parser) not() *node {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxParseDepth {
		p.fail("the expression nests more than %d deep", maxParseDepth)
	}
	if p.accept("not") {
		return &node{op: "unary", s: "not", line: p.peek().line, args: []*node{p.not()}}
	}
	if p.accept("exists") {
		return &node{op: "unary", s: "exists", line: p.peek().line, args: []*node{p.not()}}
	}
	return p.compare()
}

// compare is the equality, membership, null-test, between and timing level.
func (p *parser) compare() *node {
	l := p.relational()
	for {
		line := p.peek().line
		bin := func(op string) { l = &node{op: "binary", s: op, line: line, args: []*node{l, p.relational()}} }
		// binp: an operator with a precision (included in day of): the operands are compared at it
		binp := func(op string) {
			prec := p.precisionOf()
			l = &node{op: "binary", s: op, line: line, val: prec, args: []*node{l, p.relational()}}
		}
		switch {
		case p.accept("is"):
			neg := p.accept("not")
			var n *node
			switch {
			case p.accept("null"):
				n = &node{op: "unary", s: "is null", line: line, args: []*node{l}}
			case p.accept("true"):
				n = &node{op: "unary", s: "is true", line: line, args: []*node{l}}
			case p.accept("false"):
				n = &node{op: "unary", s: "is false", line: line, args: []*node{l}}
			default:
				n = &node{op: "is", typ: p.typeSpec(), line: line, args: []*node{l}}
			}
			if neg {
				n = &node{op: "unary", s: "not", line: line, args: []*node{n}}
			}
			l = n
		case p.is("~") && (p.peekN(1).text == "in" || p.peekN(1).text == "contains"):
			p.next()
			bin("~" + p.next().text) // equivalent membership: ~in, ~contains
		case p.is("=") || p.is("!=") || p.is("~") || p.is("!~"):
			bin(p.next().text)
		case p.accept("between"):
			lo := p.relational()
			p.expect("and")
			l = &node{op: "between", line: line, args: []*node{l, lo, p.relational()}}
		case p.accept("contains"):
			binp("contains")
		case p.accept("in"):
			binp("in")
		case p.accept("properly", "includes"):
			binp("properly includes")
		case p.accept("properly", "included", "in"), p.accept("properly", "during"):
			binp("properly included in")
		case p.accept("includes"):
			binp("includes")
		case p.accept("included", "in"):
			binp("included in")
		case p.accept("during"):
			binp("included in")
		case p.accept("overlaps"):
			switch {
			case p.accept("before"):
				binp("overlaps before")
			case p.accept("after"):
				binp("overlaps after")
			default:
				binp("overlaps")
			}
		case p.accept("meets"):
			switch {
			case p.accept("before"):
				binp("meets before")
			case p.accept("after"):
				binp("meets after")
			default:
				binp("meets")
			}
		case p.timingAhead():
			l = p.timing(l)
		default:
			return l
		}
	}
}

// precisionOf reads an optional "day of" (a precision for an interval operator); it is kept to the operand's own.
func (p *parser) precisionOf() string {
	if t := p.peek(); t.kind == tIdent && precisions[t.text] != "" && p.peekN(1).text == "of" {
		p.next()
		p.next()
		return precisions[t.text]
	}
	return ""
}

func (p *parser) timingAhead() bool {
	t := p.peek()
	if t.kind != tIdent {
		return t.kind == tNumber && (precisions[p.peekN(1).text] != "") &&
			(p.peekN(2).text == "before" || p.peekN(2).text == "after" || p.peekN(2).text == "or")
	}
	switch t.text {
	case "before", "after", "same", "starts", "ends", "occurs", "within":
		return true
	case "on":
		return p.peekN(1).text == "or"
	}
	return false
}

// timing: [starts|ends|occurs] (same [prec] (as|or before|or after) | [Q [or less|or more]] [on or] (before|after) [prec
// of] | within Q of | during | included in) [start|end of] operand. The left side's start or end is taken by
// "starts"/"ends"; the right side's by "start of"/"end of" (a unary operator).
func (p *parser) timing(l *node) *node {
	line := p.peek().line
	side := ""
	switch {
	case p.accept("starts"):
		side = "start"
	case p.accept("ends"):
		side = "end"
	default:
		p.accept("occurs")
	}
	if side != "" {
		// "A starts B" (and ends), the interval operators, rather than a timing phrase: A starts as B does and ends
		// no later.
		switch t := p.peek(); {
		case t.kind == tNumber, t.text == "same", t.text == "within", t.text == "before", t.text == "after",
			t.text == "on", t.text == "during", t.text == "included", t.text == "properly":
		default:
			prec := p.precisionOf()
			return &node{op: "binary", s: side + "s", line: line, val: prec, args: []*node{l, p.relational()}}
		}
	}
	if side != "" {
		l = &node{op: "unary", s: side + " of", line: line, args: []*node{l}}
	}
	mk := func(op, prec string, q *node) *node {
		n := &node{op: "timing", s: op, line: line, args: []*node{l, nil, q}, val: prec}
		n.args[1] = p.relational()
		return n
	}
	switch {
	case p.accept("same"):
		prec := ""
		if t := p.peek(); precisions[t.text] != "" {
			prec = precisions[p.next().text]
		}
		switch {
		case p.accept("as"):
			return mk("same as", prec, nil)
		case p.accept("or", "before"):
			return mk("same or before", prec, nil)
		case p.accept("or", "after"):
			return mk("same or after", prec, nil)
		}
		p.fail("expected as, or before, or or after after same")
	case p.accept("within"):
		q := p.quantity()
		p.expect("of")
		return mk("within", "", q)
	case p.accept("during") || p.accept("included", "in"):
		p.precisionOf()
		return &node{op: "binary", s: "included in", line: line, args: []*node{l, p.relational()}}
	}
	var q *node
	bound := ""
	if p.peek().kind == tNumber {
		q = p.quantity()
		switch {
		case p.accept("or", "less"):
			bound = " or less"
		case p.accept("or", "more"):
			bound = " or more"
		}
	}
	orOn := p.accept("on", "or")
	dir := ""
	switch {
	case p.accept("before"):
		dir = "before"
	case p.accept("after"):
		dir = "after"
	default:
		p.fail("expected before or after, found %s", p.peek())
	}
	prec := p.precisionOf()
	op := dir
	if orOn {
		op = "on or " + dir
	}
	if q != nil {
		op = "by " + dir + bound
	}
	return mk(op, prec, q)
}

func (p *parser) quantity() *node {
	t := p.next()
	if t.kind != tNumber {
		p.i--
		p.fail("expected a quantity, found %s", t)
	}
	v, _ := strconv.ParseFloat(t.text, 64)
	unit := ""
	if u := p.peek(); u.kind == tIdent && precisions[u.text] != "" {
		unit = precisions[p.next().text]
	} else if u.kind == tString {
		unit = p.next().text
	}
	return &node{op: "lit", line: t.line, val: Quantity{Value: v, Unit: unit}}
}

func (p *parser) relational() *node {
	l := p.union()
	for p.is("<") || p.is("<=") || p.is(">") || p.is(">=") {
		op := p.next().text
		l = &node{op: "binary", s: op, line: l.line, args: []*node{l, p.union()}}
	}
	return l
}

func (p *parser) union() *node {
	l := p.typeOps()
	for {
		switch {
		case p.accept("union") || p.accept("|"):
			l = &node{op: "binary", s: "union", line: l.line, args: []*node{l, p.typeOps()}}
		case p.accept("intersect"):
			l = &node{op: "binary", s: "intersect", line: l.line, args: []*node{l, p.typeOps()}}
		case p.accept("except"):
			l = &node{op: "binary", s: "except", line: l.line, args: []*node{l, p.typeOps()}}
		default:
			return l
		}
	}
}

func (p *parser) typeOps() *node {
	l := p.additive()
	for p.is("as") {
		p.next()
		l = &node{op: "as", typ: p.typeSpec(), line: l.line, args: []*node{l}}
	}
	return l
}

func (p *parser) additive() *node {
	l := p.multiplicative()
	for p.is("+") || p.is("-") || p.is("&") {
		op := p.next().text
		l = &node{op: "binary", s: op, line: l.line, args: []*node{l, p.multiplicative()}}
	}
	return l
}

func (p *parser) multiplicative() *node {
	l := p.power()
	for p.is("*") || p.is("/") || p.is("div") || p.is("mod") {
		op := p.next().text
		l = &node{op: "binary", s: op, line: l.line, args: []*node{l, p.power()}}
	}
	return l
}

func (p *parser) power() *node {
	l := p.unary()
	if p.accept("^") {
		return &node{op: "binary", s: "^", line: l.line, args: []*node{l, p.power()}}
	}
	return l
}

func (p *parser) unary() *node {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxParseDepth {
		p.fail("the expression nests more than %d deep", maxParseDepth)
	}
	line := p.peek().line
	u := func(op string) *node { return &node{op: "unary", s: op, line: line, args: []*node{p.unary()}} }
	switch {
	case p.is("-") && p.peekN(1).kind == tNumber && !(p.peekN(2).kind == tIdent && precisions[p.peekN(2).text] != "") &&
		p.peekN(2).kind != tString && p.peekN(2).text != "." && p.peekN(2).text != "[":
		// A negative literal, so the most negative Integer and Long are literals too.
		p.next()
		t := p.next()
		return &node{op: "lit", line: line, val: p.number(t.text, true)}
	case p.accept("-"):
		return u("-")
	case p.accept("+"):
		return p.unary()
	case p.accept("start", "of"):
		return u("start of")
	case p.accept("end", "of"):
		return u("end of")
	case p.accept("width", "of"):
		return u("width of")
	case p.accept("singleton", "from"):
		return u("singleton from")
	case p.accept("point", "from"):
		return u("point from")
	case p.accept("successor", "of"):
		return u("successor of")
	case p.accept("predecessor", "of"):
		return u("predecessor of")
	case p.accept("distinct"):
		return u("distinct")
	case p.accept("flatten"):
		return u("flatten")
	case p.is("collapse"), p.is("expand"):
		op := p.next().text
		x := p.postfixNoQuery() // never a query: "per" after it is not an alias
		var per *node
		if p.accept("per") {
			if t := p.peek(); t.kind == tIdent && precisions[t.text] != "" {
				// per day: one of that precision's unit
				p.next()
				per = &node{op: "lit", line: line, val: Quantity{1, precisions[t.text]}}
			} else {
				per = p.unary()
			}
		}
		return &node{op: "unary", s: op, line: line, args: []*node{x, per}}
	case p.accept("cast"):
		x := p.unary()
		p.expect("as")
		return &node{op: "as", typ: p.typeSpec(), line: line, args: []*node{x}, val: true}
	case p.accept("convert"):
		x := p.unary()
		p.expect("to")
		if t := p.peek(); t.kind == tString {
			p.next()
			return &node{op: "convertunit", s: t.text, line: line, args: []*node{x}}
		}
		return &node{op: "convert", typ: p.typeSpec(), line: line, args: []*node{x}}
	case p.accept("difference", "in"), p.accept("duration", "in"):
		kind := p.toks[p.i-2].text
		prec := precisions[p.next().text]
		if prec == "" {
			p.fail("expected a precision after %s in", kind)
		}
		p.expect("between")
		a := p.additive()
		p.expect("and")
		return &node{op: "between-dt", s: kind, val: prec, line: line, args: []*node{a, p.additive()}}
	}
	if t := p.peek(); t.kind == tIdent && p.peekN(1).text == "from" && (precisions[t.text] != "" || t.text == "date" ||
		t.text == "time" || t.text == "timezoneoffset") {
		p.next()
		p.next()
		return &node{op: "datepart", s: precisions[t.text] + map[string]string{"date": "date", "time": "time", "timezoneoffset": "timezoneoffset"}[t.text],
			line: line, args: []*node{p.unary()}}
	}
	if t := p.peek(); t.kind == tIdent && precisions[t.text] != "" && p.peekN(1).text == "between" {
		prec := precisions[p.next().text]
		p.next()
		a := p.additive()
		p.expect("and")
		return &node{op: "between-dt", s: "duration", val: prec, line: line, args: []*node{a, p.additive()}}
	}
	return p.postfix()
}

func (p *parser) postfix() *node {
	x := p.term()
	for {
		switch {
		case p.is(".") && (p.peekN(1).kind == tIdent || p.peekN(1).kind == tQuoted):
			p.next()
			n := p.name()
			if p.is("(") {
				args := p.args()
				x = &node{op: "method", s: n, line: x.line, args: append([]*node{x}, args...)}
				continue
			}
			x = &node{op: "prop", s: n, line: x.line, args: []*node{x}}
		case p.is("["):
			p.next()
			i := p.expr()
			p.expect("]")
			x = &node{op: "index", line: x.line, args: []*node{x, i}}
		case p.is("{") && (x.op == "id" || x.op == "prop") && (p.peekN(1).kind == tIdent || p.peekN(1).kind == tQuoted) &&
			p.peekN(2).text == ":":
			// an instance selector: System.Quantity { value: 5, unit: 'mg' }
			t := p.listOrTuple()
			t.op, t.typ = "instance", typeName(x)
			x = t
		default:
			if t := p.peek(); (t.kind == tIdent && !keywords[t.text]) || t.kind == tQuoted {
				return p.queryFrom([]qsource{{expr: x, alias: p.name()}})
			}
			return x
		}
	}
}

func (p *parser) args() []*node {
	p.expect("(")
	var out []*node
	for !p.accept(")") {
		out = append(out, p.expr())
		if !p.accept(",") {
			p.expect(")")
			break
		}
	}
	return out
}

func (p *parser) queryFrom(sources []qsource) *node {
	line := p.peek().line
	q := &query{sources: sources}
	for p.accept(",") {
		e := p.postfixNoQuery()
		q.sources = append(q.sources, qsource{expr: e, alias: p.name()})
	}
	if p.accept("let") {
		for {
			n := p.name()
			p.expect(":")
			q.lets = append(q.lets, qlet{name: n, expr: p.expr()})
			if !p.accept(",") {
				break
			}
		}
	}
	for p.is("with") || p.is("without") {
		without := p.next().text == "without"
		e := p.postfixNoQuery()
		src := qsource{expr: e, alias: p.name()}
		p.expect("such")
		p.expect("that")
		q.rels = append(q.rels, qrel{without: without, src: src, such: p.expr()})
	}
	if p.accept("where") {
		q.where = p.expr()
	}
	if p.accept("aggregate") {
		a := &qagg{}
		if p.accept("distinct") {
			a.distinct = true
		} else {
			p.accept("all")
		}
		a.name = p.name()
		if p.accept("starting") {
			a.start = p.relational()
		}
		p.expect(":")
		a.expr = p.expr()
		q.agg = a
		return &node{op: "query", q: q, line: line}
	}
	if p.accept("return") {
		// distinct unless "all": a return clause removes duplicates by default
		q.distinct = !p.accept("all")
		p.accept("distinct")
		q.ret = p.expr()
	}
	if p.accept("sort") {
		switch {
		case p.accept("by"):
			for {
				it := qsort{expr: p.expr()}
				switch {
				case p.accept("desc") || p.accept("descending"):
					it.desc = true
				default:
					_ = p.accept("asc") || p.accept("ascending")
				}
				q.sort = append(q.sort, it)
				if !p.accept(",") {
					break
				}
			}
		case p.accept("desc") || p.accept("descending"):
			q.sortDir = "desc"
		default:
			_ = p.accept("asc") || p.accept("ascending")
			q.sortDir = "asc"
		}
	}
	return &node{op: "query", q: q, line: line}
}

// postfixNoQuery is a query source: a term and its paths, never itself a query.
func (p *parser) postfixNoQuery() *node {
	x := p.term()
	for {
		switch {
		case p.is(".") && (p.peekN(1).kind == tIdent || p.peekN(1).kind == tQuoted):
			p.next()
			n := p.name()
			if p.is("(") {
				x = &node{op: "method", s: n, line: x.line, args: append([]*node{x}, p.args()...)}
				continue
			}
			x = &node{op: "prop", s: n, line: x.line, args: []*node{x}}
		case p.is("["):
			p.next()
			i := p.expr()
			p.expect("]")
			x = &node{op: "index", line: x.line, args: []*node{x, i}}
		default:
			return x
		}
	}
}

func (p *parser) term() *node {
	t := p.peek()
	line := t.line
	switch t.kind {
	case tString:
		p.next()
		return &node{op: "lit", line: line, val: t.text}
	case tIdent:
		if (t.text == "minimum" || t.text == "maximum") && p.peekN(1).kind == tIdent {
			p.next()
			typ := p.typeSpec()
			v := minMaxOf(typ, t.text == "maximum")
			if v == nil {
				p.fail("%s %s: no such value", t.text, typ)
			}
			return &node{op: "lit", line: line, val: v}
		}
	}
	switch t.kind {
	case tNumber:
		if u := p.peekN(1); (u.kind == tIdent && precisions[u.text] != "") || u.kind == tString {
			q := p.quantity()
			if p.is(":") && p.peekN(1).kind == tNumber {
				// a ratio: 1 'cm' : 2 'cm'
				p.next()
				d := p.quantity()
				return &node{op: "lit", line: line, val: Ratio{q.val.(Quantity), d.val.(Quantity)}}
			}
			return q
		}
		p.next()
		return &node{op: "lit", line: line, val: p.number(t.text, false)}
	case tDate:
		p.next()
		d, err := ParseDateTime(t.text)
		if err != nil {
			p.fail("@%s: %v", t.text, err)
		}
		return &node{op: "lit", line: line, val: d}
	case tTime:
		p.next()
		d, err := ParseTime(t.text)
		if err != nil {
			p.fail("%v", err)
		}
		return &node{op: "lit", line: line, val: d}
	case tQuoted:
		p.next()
		if p.is("(") {
			return &node{op: "call", s: t.text, line: line, args: p.args()}
		}
		return p.qualifiedID(t.text, line)
	case tPunct:
		switch t.text {
		case "(":
			p.next()
			x := p.expr()
			p.expect(")")
			return x
		case "{":
			return p.listOrTuple()
		case "[":
			return p.retrieve()
		}
	case tIdent:
		switch t.text {
		case "null":
			p.next()
			return &node{op: "lit", line: line}
		case "true", "false":
			p.next()
			return &node{op: "lit", line: line, val: t.text == "true"}
		case "if":
			p.next()
			c := p.expr()
			p.expect("then")
			a := p.expr()
			p.expect("else")
			return &node{op: "if", line: line, args: []*node{c, a, p.expr()}}
		case "case":
			p.next()
			n := &node{op: "case", line: line}
			if !p.is("when") {
				n.val = p.expr()
			}
			for p.accept("when") {
				w := p.expr()
				p.expect("then")
				n.args = append(n.args, w, p.expr())
			}
			p.expect("else")
			n.args = append(n.args, p.expr())
			p.expect("end")
			return n
		case "from":
			p.next()
			e := p.postfixNoQuery()
			return p.queryFrom([]qsource{{expr: e, alias: p.name()}})
		case "Interval":
			if p.peekN(1).text == "[" || p.peekN(1).text == "(" {
				p.next()
				lc := p.next().text == "["
				lo := p.expr()
				p.expect(",")
				hi := p.expr()
				hc := p.peek().text == "]"
				if !p.accept("]") {
					p.expect(")")
				}
				return &node{op: "interval", line: line, args: []*node{lo, hi}, val: [2]bool{lc, hc}}
			}
		case "Tuple":
			if p.peekN(1).text == "{" {
				p.next()
				return p.listOrTuple()
			}
		case "List":
			if p.peekN(1).text == "<" || p.peekN(1).text == "{" {
				p.next()
				if p.accept("<") {
					p.typeSpec()
					p.expect(">")
				}
				n := p.listOrTuple()
				if n.op != "list" {
					p.fail("a List selector holds a list")
				}
				return n
			}
		case "Code":
			if p.peekN(1).kind == tString {
				p.next()
				c := p.str()
				p.expect("from")
				sys := p.name()
				disp := ""
				if p.accept("display") {
					disp = p.str()
				}
				return &node{op: "codesel", line: line, s: sys, val: [2]string{c, disp}}
			}
		case "Concept":
			if p.peekN(1).text == "{" {
				p.next()
				n := p.listOrTuple()
				disp := ""
				if p.accept("display") {
					disp = p.str()
				}
				return &node{op: "conceptsel", line: line, args: n.args, s: disp}
			}
		}
		if notTerm[t.text] {
			p.fail("unexpected %s", t)
		}
		p.next()
		if p.is("(") {
			return &node{op: "call", s: t.text, line: line, args: p.args()}
		}
		return p.qualifiedID(t.text, line)
	case tEOF:
		p.fail("the expression ends early")
	}
	p.fail("unexpected %s", t)
	return nil
}

// qualifiedID: an identifier, perhaps Lib."name" or Lib.f(args), resolved when it is evaluated.
// number is a numeric literal's value: an Integer (32 bits), a Long (42L, 64 bits) or a Decimal (28 digits before the
// point, 8 after). neg is a literal written after a minus sign, so the most negative value fits.
func (p *parser) number(text string, neg bool) any {
	if strings.HasSuffix(text, "L") {
		s := strings.TrimSuffix(text, "L")
		if neg {
			s = "-" + s
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			p.fail("%s is outside the range of a Long", text)
		}
		return Long(v)
	}
	if strings.Contains(text, ".") {
		whole, frac, _ := strings.Cut(text, ".")
		if len(strings.TrimLeft(whole, "0")) > 28 || len(frac) > 8 {
			p.fail("%s is outside the range or precision of a Decimal (28 digits, 8 after the point)", text)
		}
		v, _ := strconv.ParseFloat(text, 64)
		if neg {
			v = -v
		}
		return v
	}
	s := text
	if neg {
		s = "-" + s
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v > math.MaxInt32 || v < math.MinInt32 {
		p.fail("%s is outside the range of an Integer", text)
	}
	return v
}

func (p *parser) qualifiedID(name string, line int) *node {
	return &node{op: "id", s: name, line: line}
}

func (p *parser) listOrTuple() *node {
	line := p.peek().line
	p.expect("{")
	if p.accept("}") {
		return &node{op: "list", line: line}
	}
	if p.is(":") {
		p.next()
		p.expect("}")
		return &node{op: "tuple", line: line, val: []string{}}
	}
	if t := p.peek(); (t.kind == tIdent || t.kind == tQuoted) && p.peekN(1).text == ":" {
		n := &node{op: "tuple", line: line}
		var names []string
		for {
			names = append(names, p.name())
			p.expect(":")
			n.args = append(n.args, p.expr())
			if !p.accept(",") {
				break
			}
		}
		p.expect("}")
		n.val = names
		return n
	}
	n := &node{op: "list", line: line}
	for {
		n.args = append(n.args, p.expr())
		if !p.accept(",") {
			break
		}
	}
	p.expect("}")
	return n
}

// retrieve: [Type] or [Type: terminology] or [Type: path in terminology].
func (p *parser) retrieve() *node {
	line := p.peek().line
	p.expect("[")
	typ := p.qualified()
	if i := strings.LastIndex(typ, "."); i >= 0 {
		typ = typ[i+1:]
	}
	n := &node{op: "retrieve", typ: typ, line: line}
	if p.accept(":") {
		if (p.peek().kind == tIdent || p.peek().kind == tQuoted) && (p.peekN(1).text == "in" || p.peekN(1).text == "~" ||
			p.peekN(1).text == "=") {
			n.s = p.name()
			p.next()
		}
		n.args = []*node{p.expr()}
	}
	p.expect("]")
	return n
}

// typeName is the name an identifier or a dotted path spells: System.Quantity.
func typeName(n *node) string {
	if n.op == "prop" {
		return typeName(n.args[0]) + "." + n.s
	}
	return n.s
}
