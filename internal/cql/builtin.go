package cql

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (e *Env) now() DateTime { return DateTime{T: e.Now, Prec: pMilli} }
func (e *Env) today() DateTime {
	y, m, d := e.Now.Date()
	return DateTime{T: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Prec: pDay, DateOnly: true}
}

func (e *Env) birthDate() (DateTime, bool) {
	if e.patient == nil {
		return DateTime{}, false
	}
	s, _ := e.patient["birthDate"].(string)
	d, err := ParseDateTime(s)
	return d, err == nil
}

// builtin is CQL's system functions.
func (e *Env) builtin(name string, args []any, n *node, sc *scope) any {
	arg := func(i int) any {
		if i < len(args) {
			return sys(args[i])
		}
		return nil
	}
	want := func(k int) {
		if len(args) != k {
			e.fail(sc.lib, n, "%s takes %d argument(s), given %d", name, k, len(args))
		}
	}
	list := func() []any {
		want(1)
		return toList(args[0])
	}
	nonNull := func(l []any) []any {
		out := []any{}
		for _, v := range l {
			if sys(v) != nil {
				out = append(out, sys(v))
			}
		}
		return out
	}
	if len(args) == 0 {
		switch name {
		case "Today", "Now", "TimeOfDay", "AgeInYears", "AgeInMonths", "AgeInWeeks", "AgeInDays", "Date", "DateTime",
			"Coalesce":
		default:
			e.fail(sc.lib, n, "%s needs an argument", name)
		}
	}
	switch name {
	case "Today":
		return e.today()
	case "Now":
		return e.now()
	case "TimeOfDay":
		t := e.Now
		if t.IsZero() {
			t = time.Now()
		}
		return DateTime{T: timeDay.Add(clock(t)), Prec: pMilli, TimeOnly: true}
	case "Date", "DateTime":
		parts := []int{0, 1, 1, 0, 0, 0, 0}
		prec := 0
		for i, a := range args {
			if i >= 7 {
				break
			}
			v, ok := sys(a).(int64)
			if !ok {
				if sys(a) == nil {
					break
				}
				e.fail(sc.lib, n, "%s takes whole numbers", name)
			}
			parts[i] = int(v)
			prec = i + 1
		}
		if prec == 0 {
			return nil
		}
		loc := time.UTC
		if name == "DateTime" && len(args) == 8 {
			// The eighth is the offset from UTC, in hours: -7.0 is seven hours behind.
			if off, ok := num(sys(args[7])); ok {
				loc = time.FixedZone("", int(math.Round(off*3600)))
			}
		}
		if parts[1] < 1 || parts[1] > 12 || parts[2] < 1 || parts[0] < 1 || parts[0] > 9999 {
			e.fail(sc.lib, n, "%s(%v) is not a calendar date", name, parts[:prec])
		}
		return DateTime{T: time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], parts[5],
			parts[6]*1e6, loc), Prec: prec, DateOnly: name == "Date"}
	case "AgeInYears", "AgeInMonths", "AgeInWeeks", "AgeInDays":
		b, ok := e.birthDate()
		if !ok {
			return nil
		}
		return between(b, e.today(), strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(name, "AgeIn"), "s")), false)
	case "AgeInYearsAt", "AgeInMonthsAt", "AgeInDaysAt":
		want(1)
		b, ok := e.birthDate()
		at, ok2 := arg(0).(DateTime)
		if !ok || !ok2 {
			return nil
		}
		return between(b, at, strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(name, "AgeIn"), "At"), "s")), false)
	case "CalculateAgeInYears", "CalculateAgeInMonths", "CalculateAgeInDays":
		b, ok := arg(0).(DateTime)
		if !ok {
			return nil
		}
		return between(b, e.today(), strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(name, "CalculateAgeIn"), "s")), false)
	case "CalculateAgeInYearsAt", "CalculateAgeInMonthsAt", "CalculateAgeInDaysAt":
		b, ok := arg(0).(DateTime)
		at, ok2 := arg(1).(DateTime)
		if !ok || !ok2 {
			return nil
		}
		return between(b, at, strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(name, "CalculateAgeIn"), "At"), "s")), false)
	case "Count":
		if args[0] == nil {
			return int64(0)
		}
		return int64(len(nonNull(list())))
	case "Exists":
		for _, v := range list() {
			if v != nil {
				return true
			}
		}
		return false
	case "First", "Last":
		l := list()
		if len(l) == 0 {
			return nil
		}
		if name == "First" {
			return l[0]
		}
		return l[len(l)-1]
	case "Min", "Max":
		l := nonNull(list())
		if len(l) == 0 {
			return nil
		}
		best := l[0]
		for _, v := range l[1:] {
			c, ok := compare(v, best)
			if !ok {
				e.fail(sc.lib, n, "%s of values that do not compare", name)
			}
			if (name == "Min" && c < 0) || (name == "Max" && c > 0) {
				best = v
			}
		}
		return best
	case "Sum", "Avg":
		l := nonNull(list())
		if len(l) == 0 {
			return nil
		}
		total := l[0]
		for _, v := range l[1:] {
			total = arith(e, sc, n, "+", total, v)
		}
		if name == "Avg" {
			if q, ok := total.(Quantity); ok {
				return Quantity{q.Value / float64(len(l)), q.Unit}
			}
			f, _ := num(total)
			return f / float64(len(l))
		}
		return total
	case "AllTrue":
		for _, v := range nonNull(list()) {
			if truth(v) == F {
				return false
			}
		}
		return true
	case "AnyTrue":
		for _, v := range nonNull(list()) {
			if truth(v) == T {
				return true
			}
		}
		return false
	case "Coalesce":
		if len(args) > 5 {
			e.fail(sc.lib, n, "Coalesce takes at most 5 arguments; more must be given as a list")
		}
		if len(args) == 1 {
			if l, ok := args[0].([]any); ok {
				args = l
			}
		}
		for _, v := range args {
			if sys(v) != nil {
				if l, ok := v.([]any); ok && len(l) == 0 {
					continue
				}
				return v
			}
		}
		return nil
	case "IsNull":
		want(1)
		return arg(0) == nil
	case "IsTrue":
		return truth(arg(0)) == T
	case "IsFalse":
		return truth(arg(0)) == F
	case "Length":
		want(1)
		switch v := sys(args[0]).(type) {
		case string:
			return int64(len([]rune(v)))
		case []any:
			return int64(len(v))
		case nil:
			if _, isList := args[0].([]any); isList || n.args != nil && len(n.args) > 0 && strings.HasPrefix(n.args[0].typ, "List") {
				return int64(0) // a null list has none
			}
			return nil
		}
	case "Distinct":
		return distinct(list())
	case "Flatten":
		if sys(args[0]) == nil {
			return nil
		}
		return flatten(list())
	case "SingletonFrom":
		l := list()
		if len(l) > 1 {
			e.fail(sc.lib, n, "singleton from a list of %d", len(l))
		}
		if len(l) == 0 {
			return nil
		}
		return l[0]
	case "Tail":
		if sys(args[0]) == nil {
			return nil
		}
		l := list()
		if len(l) <= 1 {
			return []any{}
		}
		return l[1:]
	case "Skip", "Take":
		want(2)
		if sys(args[0]) == nil {
			return nil
		}
		l := toList(args[0])
		k, isK := arg(1).(int64)
		if !isK && name == "Skip" {
			return l
		}
		if name == "Skip" && k < 0 {
			return []any{}
		}
		k = max(0, min(k, int64(len(l))))
		if name == "Skip" {
			return l[k:]
		}
		return l[:k]
	case "IndexOf":
		want(2)
		if sys(args[0]) == nil || sys(args[1]) == nil {
			return nil
		}
		for i, v := range toList(args[0]) {
			if eq, known := equal(v, args[1]); known && eq {
				return int64(i)
			}
		}
		return int64(-1)
	case "Combine":
		if l := nonNull(toList(args[0])); len(l) == 0 {
			return nil
		}
		var parts []string
		for _, v := range nonNull(toList(args[0])) {
			s, _ := v.(string)
			parts = append(parts, s)
		}
		sep := ""
		if len(args) > 1 {
			sep, _ = arg(1).(string)
		}
		return strings.Join(parts, sep)
	case "Split":
		s, ok := arg(0).(string)
		if !ok {
			return nil
		}
		sep, ok := arg(1).(string)
		if !ok {
			return []any{s}
		}
		out := []any{}
		for _, p := range strings.Split(s, sep) {
			out = append(out, p)
		}
		return out
	case "Lower", "Upper":
		s, ok := arg(0).(string)
		if !ok {
			return nil
		}
		if name == "Lower" {
			return strings.ToLower(s)
		}
		return strings.ToUpper(s)
	case "StartsWith", "EndsWith", "Matches", "PositionOf", "LastPositionOf":
		a, ok1 := arg(0).(string)
		b, ok2 := arg(1).(string)
		if !ok1 || !ok2 {
			return nil
		}
		switch name {
		case "StartsWith":
			return strings.HasPrefix(a, b)
		case "EndsWith":
			return strings.HasSuffix(a, b)
		case "PositionOf":
			return int64(strings.Index(b, a))
		case "LastPositionOf":
			return int64(strings.LastIndex(b, a))
		}
		re, err := regexp.Compile("^(?:" + b + ")$")
		if err != nil {
			e.fail(sc.lib, n, "Matches: %v", err)
		}
		return re.MatchString(a)
	case "Substring":
		s, ok := arg(0).(string)
		start, ok2 := arg(1).(int64)
		if !ok || !ok2 || start < 0 || int(start) > len(s) || int(start) == len(s) && len(s) > 0 {
			return nil
		}
		if l, ok := arg(2).(int64); ok {
			return s[start:min(int(start)+int(l), len(s))]
		}
		return s[start:]
	case "Abs", "Ceiling", "Floor", "Truncate", "Round", "Ln", "Exp", "Sqrt":
		v := arg(0)
		if q, ok := v.(Quantity); ok && name == "Abs" {
			return Quantity{math.Abs(q.Value), q.Unit}
		}
		if name == "Abs" && (isLong(v) || func() bool { _, ok := v.(int64); return ok }()) {
			if f, _ := num(v); f < 0 {
				r, _ := intArith("-", map[bool]any{true: Long(0), false: int64(0)}[isLong(v)], v)
				return r
			}
			return v
		}
		f, ok := num(v)
		if !ok {
			return nil
		}
		whole := func(f float64) any {
			if f > math.MaxInt32 || f < math.MinInt32 {
				return nil
			}
			return int64(f)
		}
		switch name {
		case "Abs":
			return decimal(math.Abs(f))
		case "Ceiling":
			return whole(math.Ceil(f))
		case "Floor":
			return whole(math.Floor(f))
		case "Truncate":
			return whole(math.Trunc(f))
		case "Round":
			places := int64(0)
			if p, ok := arg(1).(int64); ok {
				places = p
			}
			pw := math.Pow(10, float64(places))
			return decimal(math.Round(f*pw) / pw) // half away from zero: Round(-0.5) is -1
		case "Ln":
			if f <= 0 {
				return nil
			}
			return decimal(math.Log(f))
		case "Exp":
			return decimal(math.Exp(f))
		}
		if f < 0 {
			return nil
		}
		return decimal(math.Sqrt(f))
	case "ToString":
		return toString(arg(0))
	case "ToInteger":
		switch v := arg(0).(type) {
		case int64:
			return v
		case string:
			i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil
			}
			return i
		case bool:
			if v {
				return int64(1)
			}
			return int64(0)
		}
		return nil
	case "ToDecimal":
		switch v := arg(0).(type) {
		case int64:
			return float64(v)
		case float64:
			return v
		case string:
			if f, ok := parseDecimal(v); ok {
				return f
			}
		}
		return nil
	case "ToBoolean":
		switch v := arg(0).(type) {
		case bool:
			return v
		case int64, Long, float64:
			switch f, _ := num(v); f {
			case 1:
				return true
			case 0:
				return false
			}
			return nil
		case string:
			switch strings.ToLower(v) {
			case "true", "t", "yes", "y", "1":
				return true
			case "false", "f", "no", "n", "0":
				return false
			}
		}
		return nil
	case "ToDate", "ToDateTime":
		var d DateTime
		switch v := arg(0).(type) {
		case DateTime:
			d = v
		case string:
			var err error
			if d, err = ParseDateTime(v); err != nil {
				return nil
			}
		default:
			return nil
		}
		if name == "ToDate" {
			return DateTime{T: time.Date(d.T.Year(), d.T.Month(), d.T.Day(), 0, 0, 0, 0, time.UTC), Prec: min(d.Prec, pDay), DateOnly: true}
		}
		d.DateOnly = false
		return d
	case "ToQuantity":
		switch v := arg(0).(type) {
		case Quantity:
			return v
		case int64:
			return Quantity{float64(v), "1"}
		case float64:
			return Quantity{v, "1"}
		case string:
			// 5.5 'cm', or a number alone
			num, unit, _ := strings.Cut(strings.TrimSpace(v), " ")
			f, ok := parseDecimal(num)
			if !ok {
				return nil
			}
			unit = strings.Trim(strings.TrimSpace(unit), "'")
			if unit == "" {
				unit = "1"
			}
			return Quantity{f, unit}
		}
		return nil
	case "ToConcept":
		switch v := arg(0).(type) {
		case Concept:
			return v
		case Code:
			return Concept{Codes: []Code{v}, Display: v.Display}
		case []any:
			c := Concept{}
			for _, x := range v {
				c.Codes = append(c.Codes, codesOf(x)...)
			}
			return c
		}
		return nil
	case "ToCode":
		if c, ok := arg(0).(Code); ok {
			return c
		}
		return nil
	case "ConvertQuantity":
		want(2)
		q, ok := arg(0).(Quantity)
		u, ok2 := arg(1).(string)
		if !ok || !ok2 {
			return nil
		}
		if r, ok := convertUnit(q, u); ok {
			return r
		}
		e.fail(sc.lib, n, "converting %s to '%s' needs a unit conversion this engine does not know", Show(q), u)
	case "Message":
		// Message(source, condition, code, severity, message): an Error stops evaluation, with the message.
		if c, _ := arg(1).(bool); c {
			if sev, _ := arg(3).(string); strings.EqualFold(sev, "Error") {
				code, _ := arg(2).(string)
				msg, _ := arg(4).(string)
				e.fail(sc.lib, n, "%s: %s", code, msg)
			}
		}
		return args[0]
	case "start", "end":
		return e.unary(&node{op: "unary", s: name + " of", line: n.line, args: []*node{{op: "lit", val: arg(0)}}}, sc)
	}
	if v, ok := e.moreBuiltin(name, args, n, sc); ok {
		return v
	}
	e.fail(sc.lib, n, "%s is not a function this library defines or this engine has", name)
	return nil
}

func toString(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case Long:
		return strconv.FormatInt(int64(x), 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case DateTime:
		return strings.TrimPrefix(x.String(), "T") // a time of day as 09:30:01.003
	case Quantity:
		return strconv.FormatFloat(x.Value, 'f', -1, 64) + " '" + x.Unit + "'"
	case Code:
		return x.Code
	}
	return Show(v)
}

// helpers is FHIRHelpers, built in: its conversions from FHIR data to CQL's values. Any FHIRHelpers version a library
// includes is answered by these.
func (e *Env) helpers(name string, args []any, n *node, sc *scope) any {
	if len(args) != 1 {
		e.fail(sc.lib, n, "FHIRHelpers.%s takes one argument", name)
	}
	v := args[0]
	if v == nil {
		return nil
	}
	el, isElem := v.(*Elem)
	switch name {
	case "ToCode", "ToConcept", "ToQuantity", "ToInterval", "ToSimpleQuantity":
		if !isElem {
			return sys(v)
		}
		m, ok := el.V.(map[string]any)
		if !ok {
			return nil
		}
		switch name {
		case "ToCode":
			return toCode(m)
		case "ToConcept":
			return toConcept(m)
		case "ToQuantity", "ToSimpleQuantity":
			return toQuantity(m)
		}
		if _, isRange := m["low"]; isRange || fhirKind(el) == "Range" {
			return Interval{Low: sys(property(el, "low")), High: sys(property(el, "high")), LowClosed: true, HighClosed: true}
		}
		return Interval{Low: sys(property(el, "start")), High: sys(property(el, "end")), LowClosed: true, HighClosed: true}
	case "ToString", "ToBoolean", "ToInteger", "ToDecimal", "ToDate", "ToDateTime", "ToTime", "ToUri", "ToCodeString":
		p := sys(v)
		switch name {
		case "ToString", "ToUri", "ToCodeString":
			if s, ok := p.(string); ok {
				return s
			}
			if d, ok := p.(DateTime); ok {
				return d.String()
			}
			return toString(p)
		case "ToDate", "ToDateTime":
			return e.builtin(name, []any{p}, n, sc)
		case "ToTime":
			e.fail(sc.lib, n, "times of day are not supported")
		}
		return e.builtin(name, []any{p}, n, sc)
	}
	e.fail(sc.lib, n, "FHIRHelpers.%s is not a function this engine has", name)
	return nil
}

// units this engine converts between, each to a base of its kind: a short table, not UCUM. Months and years are
// calendar lengths and convert only to each other (12 months to a year).
var units = map[string]struct {
	kind   string
	factor float64
}{
	"g": {"mass", 1}, "kg": {"mass", 1000}, "mg": {"mass", 0.001}, "ug": {"mass", 1e-6}, "[lb_av]": {"mass", 453.59237},
	"m": {"length", 1}, "cm": {"length", 0.01}, "mm": {"length", 0.001}, "[in_i]": {"length", 0.0254},
	"L": {"volume", 1}, "mL": {"volume", 0.001}, "dL": {"volume", 0.1},
	"s": {"time", 1}, "min": {"time", 60}, "h": {"time", 3600}, "d": {"time", 86400}, "wk": {"time", 604800},
	"second": {"time", 1}, "minute": {"time", 60}, "hour": {"time", 3600}, "day": {"time", 86400}, "week": {"time", 604800},
	"mo": {"calendar", 1}, "a": {"calendar", 12}, "month": {"calendar", 1}, "year": {"calendar", 12},
	"Cel": {"", 0}, "[degF]": {"", 0},
}

func convertUnit(q Quantity, to string) (Quantity, bool) {
	if q.Unit == to {
		return q, true
	}
	a, ok1 := units[q.Unit]
	b, ok2 := units[to]
	if !ok1 || !ok2 || a.kind == "" || a.kind != b.kind {
		return Quantity{}, false
	}
	return Quantity{Value: q.Value * a.factor / b.factor, Unit: to}, true
}
