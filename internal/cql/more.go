package cql

// More of CQL's system functions and operators, added against the HL7 conformance tests (conformance_test.go).

import (
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// moreBuiltin is the functions builtin does not have; ok false when this has none either.
func (e *Env) moreBuiltin(name string, args []any, n *node, sc *scope) (any, bool) {
	arg := func(i int) any {
		if i < len(args) {
			return sys(args[i])
		}
		return nil
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
	nums := func() ([]float64, bool) {
		var out []float64
		for _, v := range nonNull(toList(arg(0))) {
			f, ok := num(v)
			if !ok {
				return nil, false
			}
			out = append(out, f)
		}
		return out, true
	}
	switch name {
	case "Power":
		return arith(e, sc, n, "^", arg(0), arg(1)), true
	case "Log":
		x, ok1 := num(arg(0))
		b, ok2 := num(arg(1))
		if !ok1 || !ok2 || x <= 0 || b <= 0 || b == 1 {
			return nil, true
		}
		return decimal(math.Log(x) / math.Log(b)), true
	case "Precision":
		switch v := arg(0).(type) {
		case float64:
			s := strings.TrimRight(strings.TrimRight(Show(v), "0"), ".")
			if _, frac, ok := strings.Cut(s, "."); ok {
				return int64(len(frac)), true
			}
			return int64(0), true
		case DateTime:
			if v.TimeOnly {
				return int64(map[int]int{pHour: 2, pMinute: 4, pSecond: 6, pMilli: 9}[v.Prec]), true
			}
			return int64(map[int]int{pYear: 4, pMonth: 6, pDay: 8, pHour: 10, pMinute: 12, pSecond: 14, pMilli: 17}[v.Prec]), true
		}
		return nil, true
	case "LowBoundary", "HighBoundary":
		return boundary(arg(0), arg(1), name == "HighBoundary"), true
	case "Product":
		l := nonNull(toList(arg(0)))
		if len(l) == 0 {
			return nil, true
		}
		var acc any = l[0]
		for _, v := range l[1:] {
			acc = arith(e, sc, n, "*", acc, v)
		}
		return acc, true
	case "Mode":
		l := nonNull(toList(arg(0)))
		if len(l) == 0 {
			return nil, true
		}
		best, bestN := l[0], 0
		for _, x := range l {
			c := 0
			for _, y := range l {
				if eq, _ := equal(x, y); eq {
					c++
				}
			}
			if c > bestN {
				best, bestN = x, c
			}
		}
		return best, true
	case "Median":
		l := nonNull(toList(arg(0)))
		if len(l) == 0 {
			return nil, true
		}
		sort.SliceStable(l, func(i, j int) bool { return order(l[i], l[j]) < 0 })
		if len(l)%2 == 1 {
			return l[len(l)/2], true
		}
		return arith(e, sc, n, "/", arith(e, sc, n, "+", l[len(l)/2-1], l[len(l)/2]), float64(2)), true
	case "Variance", "StdDev", "PopulationVariance", "PopulationStdDev":
		if q, ok := firstQuantity(arg(0)); ok {
			// Of quantities in one unit: the same, in that unit (squared for a variance, kept as written).
			var vals []any
			for _, v := range nonNull(toList(arg(0))) {
				vals = append(vals, v.(Quantity).Value)
			}
			r, _ := e.moreBuiltin(name, []any{vals}, n, sc)
			if f, ok := r.(float64); ok {
				return Quantity{f, q.Unit}, true
			}
			return nil, true
		}
		fs, ok := nums()
		pop := strings.HasPrefix(name, "Population")
		if !ok || len(fs) == 0 || (!pop && len(fs) < 2) {
			return nil, true
		}
		mean := 0.0
		for _, f := range fs {
			mean += f
		}
		mean /= float64(len(fs))
		ss := 0.0
		for _, f := range fs {
			ss += (f - mean) * (f - mean)
		}
		d := float64(len(fs) - 1)
		if pop {
			d = float64(len(fs))
		}
		v := ss / d
		if strings.HasSuffix(name, "StdDev") {
			v = math.Sqrt(v)
		}
		return decimal(v), true
	case "GeometricMean":
		fs, ok := nums()
		if !ok || len(fs) == 0 {
			return nil, true
		}
		p := 0.0
		for _, f := range fs {
			if f <= 0 {
				return nil, true
			}
			p += math.Log(f)
		}
		return decimal(math.Exp(p / float64(len(fs)))), true
	case "ToRatio":
		switch v := arg(0).(type) {
		case Ratio:
			return v, true
		case string:
			a, b, ok := strings.Cut(v, ":")
			if !ok {
				return nil, true
			}
			qa, ok1 := e.builtin("ToQuantity", []any{strings.TrimSpace(a)}, n, sc).(Quantity)
			qb, ok2 := e.builtin("ToQuantity", []any{strings.TrimSpace(b)}, n, sc).(Quantity)
			if !ok1 || !ok2 {
				return nil, true
			}
			return Ratio{qa, qb}, true
		}
		return nil, true
	case "ToLong":
		switch v := arg(0).(type) {
		case Long:
			return v, true
		case int64:
			return Long(v), true
		case string:
			i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, true
			}
			return Long(i), true
		case bool:
			if v {
				return Long(1), true
			}
			return Long(0), true
		}
		return nil, true
	case "Slice":
		l, ok := arg(0).([]any)
		if !ok {
			return nil, true
		}
		start, _ := arg(1).(int64)
		end := int64(len(l))
		if v, ok := arg(2).(int64); ok {
			end = v
		}
		// negative counts from the end
		if start < 0 {
			start = max(0, int64(len(l))+start)
		}
		if end < 0 {
			end = int64(len(l)) + end
		}
		if end > int64(len(l)) {
			end = int64(len(l))
		}
		if start >= end {
			return []any{}, true
		}
		return append([]any{}, l[start:end]...), true
	case "Indexer":
		i, ok := arg(1).(int64)
		if !ok {
			return nil, true
		}
		switch c := arg(0).(type) {
		case []any:
			if i < 0 || i >= int64(len(c)) {
				return nil, true
			}
			return c[i], true
		case string:
			r := []rune(c)
			if i < 0 || i >= int64(len(r)) {
				return nil, true
			}
			return string(r[i]), true
		}
		return nil, true
	case "Concatenate":
		var b strings.Builder
		for i := range args {
			s, ok := arg(i).(string)
			if !ok {
				return nil, true
			}
			b.WriteString(s)
		}
		return b.String(), true
	case "ReplaceMatches", "SplitOnMatches", "MatchesFull":
		s, ok := arg(0).(string)
		pat, ok2 := arg(1).(string)
		if !ok || !ok2 {
			return nil, true
		}
		if name == "MatchesFull" {
			pat = "^(?:" + pat + ")$"
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			e.fail(sc.lib, n, "%s: %v", name, err)
		}
		switch name {
		case "MatchesFull":
			return re.MatchString(s), true
		case "SplitOnMatches":
			out := []any{}
			for _, p := range re.Split(s, -1) {
				out = append(out, p)
			}
			return out, true
		}
		repl, ok := arg(2).(string)
		if !ok {
			return nil, true
		}
		// CQL's $1 is Go's ${1}: a group number followed by more letters stays the group.
		repl = strings.ReplaceAll(repl, `\$`, "$$") // \$ is a dollar sign
		repl = regexp.MustCompile(`\$(\d+)`).ReplaceAllString(repl, "$${$1}")
		return re.ReplaceAllString(s, repl), true
	case "Size":
		iv, ok := arg(0).(Interval)
		if !ok {
			return nil, true
		}
		lo, hi := closedBounds(iv)
		if lo == nil || hi == nil {
			return nil, true
		}
		w := arith(e, sc, n, "-", hi, lo)
		return arith(e, sc, n, "+", w, unitOf(lo)), true
	case "ToTime":
		s, ok := arg(0).(string)
		if !ok {
			if d, ok := arg(0).(DateTime); ok && d.TimeOnly {
				return d, true
			}
			return nil, true
		}
		// T14:30:00.0+05:30: the time; an offset is dropped (a time of day has none)
		s = strings.TrimPrefix(s, "T")
		if i := strings.IndexAny(s, "+-Z"); i > 0 {
			s = s[:i]
		}
		d, err := ParseTime(s)
		if err != nil {
			return nil, true
		}
		return d, true
	case "Time":
		parts := []int{0, 0, 0, 0}
		prec := pHour - 1
		for i := 0; i < len(args) && i < 4; i++ {
			v, ok := arg(i).(int64)
			if !ok {
				break
			}
			parts[i] = int(v)
			prec = pHour + i
		}
		if prec < pHour || parts[0] > 23 || parts[1] > 59 || parts[2] > 59 || parts[3] > 999 {
			return nil, true
		}
		return DateTime{T: timeDay.Add(time.Duration(parts[0])*time.Hour + time.Duration(parts[1])*time.Minute +
			time.Duration(parts[2])*time.Second + time.Duration(parts[3])*time.Millisecond), Prec: prec, TimeOnly: true}, true
	}
	return nil, false
}

func firstQuantity(v any) (Quantity, bool) {
	for _, x := range toList(v) {
		if q, ok := sys(x).(Quantity); ok {
			return q, true
		}
	}
	return Quantity{}, false
}

// boundary is the least (or, high, the greatest) value a Decimal or a date could be, written to more precision:
// 1.587 to 8 places is at least 1.58700000 and at most 1.58799999; @2014 to month precision is @2014-01 to @2014-12.
func boundary(v, p any, high bool) any {
	switch x := v.(type) {
	case float64:
		prec := int64(8)
		if pv, ok := p.(int64); ok {
			prec = pv
		} else if p != nil {
			return nil
		}
		if prec < 0 || prec > 8 {
			return nil
		}
		_, frac, _ := strings.Cut(Show(x), ".")
		if int64(len(frac)) >= prec {
			return x
		}
		widen := math.Pow(10, -float64(len(frac))) - math.Pow(10, -float64(prec))
		switch {
		case high && x >= 0:
			return roundTo(x+widen, int(prec))
		case !high && x < 0:
			return roundTo(x-widen, int(prec))
		}
		return x
	case DateTime:
		target := pMilli
		if pv, ok := p.(int64); ok {
			target = map[int64]int{4: pYear, 6: pMonth, 8: pDay, 10: pHour, 12: pMinute, 14: pSecond, 17: pMilli}[pv]
			if x.TimeOnly {
				target = map[int64]int{2: pHour, 4: pMinute, 6: pSecond, 9: pMilli}[pv]
			}
		} else if p != nil {
			return nil
		}
		if x.DateOnly && p == nil {
			target = pDay
		}
		if target == 0 || target < x.Prec || (x.DateOnly && target > pDay) || (x.TimeOnly && target < pHour) {
			return nil
		}
		out := x
		out.Prec = target
		if high {
			out.T = endOf(x)
		}
		return out
	}
	return nil
}

// endOf is the last instant a date or time at its precision covers.
func endOf(d DateTime) time.Time {
	t := d.truncate(d.Prec)
	switch d.Prec {
	case pYear:
		t = t.AddDate(1, 0, 0)
	case pMonth:
		t = t.AddDate(0, 1, 0)
	case pDay:
		t = t.AddDate(0, 0, 1)
	case pHour:
		t = t.Add(time.Hour)
	case pMinute:
		t = t.Add(time.Minute)
	case pSecond:
		t = t.Add(time.Second)
	default:
		return d.T
	}
	return t.Add(-time.Millisecond)
}

// closedBounds is an interval's first and last points, open ends moved in by one step.
func closedBounds(iv Interval) (any, any) {
	lo, hi := sys(iv.Low), sys(iv.High)
	if !iv.LowClosed && lo != nil {
		lo = successor(lo)
	}
	if !iv.HighClosed && hi != nil {
		hi = predecessor(hi)
	}
	return lo, hi
}

// unitOf is one step of a point type: 1, 1L, 0.00000001, 1 'unit', a day at a date's precision.
func unitOf(v any) any {
	switch x := v.(type) {
	case int64:
		return int64(1)
	case Long:
		return Long(1)
	case float64:
		return 1e-8
	case Quantity:
		return Quantity{1, x.Unit}
	}
	return nil
}

// successor and predecessor are the next and previous values of a point type, null past its end.
func successor(v any) any { return step(v, 1) }

func predecessor(v any) any { return step(v, -1) }

func step(v any, dir int64) any {
	switch x := sys(v).(type) {
	case int64:
		return fitInt(big.NewInt(x+dir), false)
	case Long:
		r := new(big.Int).Add(big.NewInt(int64(x)), big.NewInt(dir))
		return fitInt(r, true)
	case float64:
		return decimal(x + float64(dir)*1e-8)
	case Quantity:
		return Quantity{roundTo(x.Value+float64(dir)*1e-8, 8), x.Unit}
	case DateTime:
		out := x
		switch x.Prec {
		case pYear:
			out.T = x.T.AddDate(int(dir), 0, 0)
		case pMonth:
			out.T = x.T.AddDate(0, int(dir), 0)
		case pDay:
			out.T = x.T.AddDate(0, 0, int(dir))
		case pHour:
			out.T = x.T.Add(time.Duration(dir) * time.Hour)
		case pMinute:
			out.T = x.T.Add(time.Duration(dir) * time.Minute)
		case pSecond:
			out.T = x.T.Add(time.Duration(dir) * time.Second)
		default:
			out.T = x.T.Add(time.Duration(dir) * time.Millisecond)
		}
		if x.TimeOnly && (out.T.Before(timeDay) || !out.T.Before(timeDay.AddDate(0, 0, 1))) {
			return nil
		}
		if !x.TimeOnly && (out.T.Year() < 1 || out.T.Year() > 9999) {
			return nil
		}
		return out
	}
	return nil
}

// minMaxOf is minimum T and maximum T.
func minMaxOf(t string, max bool) any {
	switch strings.TrimPrefix(t, "System.") {
	case "Integer":
		if max {
			return int64(math.MaxInt32)
		}
		return int64(math.MinInt32)
	case "Long":
		if max {
			return Long(math.MaxInt64)
		}
		return Long(math.MinInt64)
	case "Decimal":
		if max {
			return 99999999999999999999.99999999
		}
		return -99999999999999999999.99999999
	case "Date":
		if max {
			return DateTime{T: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), Prec: pDay, DateOnly: true}
		}
		return DateTime{T: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), Prec: pDay, DateOnly: true}
	case "DateTime":
		if max {
			return DateTime{T: time.Date(9999, 12, 31, 23, 59, 59, 999e6, time.UTC), Prec: pMilli}
		}
		return DateTime{T: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), Prec: pMilli}
	case "Time":
		if max {
			return DateTime{T: timeDay.Add(24*time.Hour - time.Millisecond), Prec: pMilli, TimeOnly: true}
		}
		return DateTime{T: timeDay, Prec: pMilli, TimeOnly: true}
	}
	return nil
}

// collapse merges a list of intervals that overlap or meet into the fewest that cover the same points.
func collapse(e *Env, sc *scope, n *node, v, per any) any {
	if v == nil {
		return nil
	}
	var ivs []Interval
	for _, x := range toList(v) {
		if iv, ok := sys(x).(Interval); ok && !(iv.Low == nil && iv.High == nil && !iv.LowClosed && !iv.HighClosed) {
			ivs = append(ivs, iv)
		}
	}
	if len(ivs) == 0 {
		return []any{}
	}
	sort.SliceStable(ivs, func(i, j int) bool {
		a, b := sys(ivs[i].Low), sys(ivs[j].Low)
		if a == nil || b == nil {
			return a == nil && b != nil
		}
		return order(a, b) < 0
	})
	out := []any{}
	cur := ivs[0]
	for _, nx := range ivs[1:] {
		_, curHi := closedBounds(cur)
		nxLo, _ := closedBounds(nx)
		joins := curHi == nil && cur.High == nil // an open-ended interval covers all after it
		if !joins && curHi != nil && nxLo != nil {
			if c, ok := compare(nxLo, curHi); ok && c <= 0 {
				joins = true
			} else if eq, known := equal(successor(curHi), nxLo); known && eq {
				joins = true
			} else if q, ok := per.(Quantity); ok {
				// within the per of each other
				if c, ok := compare(nxLo, arith(e, sc, n, "+", curHi, q)); ok && c <= 0 {
					joins = true
				}
			}
		}
		if nxLo == nil && nx.Low == nil {
			joins = true
		}
		if joins {
			if cur.High != nil && (nx.High == nil && nx.HighClosed || nx.High != nil && order(sys(nx.High), sys(cur.High)) > 0) {
				cur.High, cur.HighClosed = nx.High, nx.HighClosed
			}
			continue
		}
		out = append(out, cur)
		cur = nx
	}
	return append(out, cur)
}

// expand is the points (for an interval) or unit intervals (for a list of them) an interval covers, per a step:
// expand Interval[@2018-01-01, @2018-01-04] per 2 days is { @2018-01-01, @2018-01-03 }.
func expand(e *Env, sc *scope, n *node, v, per any) any {
	if v == nil {
		return nil
	}
	if iv, ok := sys(v).(Interval); ok {
		out := []any{}
		for _, x := range expandOne(e, sc, n, iv, per) {
			out = append(out, x.Low)
		}
		return out
	}
	out := []any{}
	for _, x := range toList(v) {
		if iv, ok := sys(x).(Interval); ok {
			for _, y := range expandOne(e, sc, n, iv, per) {
				out = append(out, y)
			}
		}
	}
	return out
}

func expandOne(e *Env, sc *scope, n *node, iv Interval, per any) []Interval {
	lo, hi := sys(iv.Low), sys(iv.High)
	if !iv.LowClosed && lo != nil {
		lo = successor(lo)
	}
	if lo == nil || hi == nil {
		return nil
	}
	var out []Interval
	switch l := lo.(type) {
	case DateTime:
		h, ok := hi.(DateTime)
		if !ok {
			return nil
		}
		q, ok := per.(Quantity)
		if !ok {
			q = Quantity{1, map[int]string{pYear: "year", pMonth: "month", pDay: "day", pHour: "hour", pMinute: "minute",
				pSecond: "second", pMilli: "millisecond"}[min(l.Prec, h.Prec)]}
		}
		p := precIndex[durationUnit(q.Unit)]
		if durationUnit(q.Unit) == "week" {
			p = pDay
		}
		if p == 0 || l.Prec < p || h.Prec < p {
			return nil
		}
		at := func(d DateTime) DateTime {
			return DateTime{T: d.truncate(p), Prec: p, DateOnly: d.DateOnly, TimeOnly: d.TimeOnly}
		}
		cur, end := at(l), at(h)
		unit := Quantity{-1, durationUnit(q.Unit)}
		if durationUnit(q.Unit) == "week" {
			unit = Quantity{-1, "day"}
		}
		for i := 0; i < 100000; i++ {
			last := addTime(e, sc, n, addTime(e, sc, n, cur, q), unit)
			if c, ok := compareDT(last, end); !ok || c > 0 {
				break
			}
			out = append(out, Interval{Low: cur, High: last, LowClosed: true, HighClosed: true})
			nx := addTime(e, sc, n, cur, q)
			if c, _ := compareDT(nx, cur); c <= 0 {
				break // wrapped past midnight
			}
			cur = nx
		}
		return out
	}
	// Numbers: per 1 by default; to the per's own decimal places.
	stepV := per
	if stepV == nil {
		stepV = unitOf(lo)
		if _, isDec := lo.(float64); isDec {
			stepV = int64(1)
		}
	}
	if _, ia := lo.(int64); ia {
		if _, ib := hi.(int64); ib {
			if s, is := stepV.(int64); is && s > 0 {
				hiI := hi.(int64)
				if !iv.HighClosed {
					hiI--
				}
				for p := lo.(int64); p+s-1 <= hiI; p += s {
					out = append(out, Interval{Low: p, High: p + s - 1, LowClosed: true, HighClosed: true})
				}
				return out
			}
		}
	}
	s, ok := num(stepV)
	if !ok || s <= 0 {
		return nil
	}
	d := 0
	if _, frac, has := strings.Cut(Show(stepV), "."); has {
		d = len(frac)
	}
	unit := math.Pow(10, -float64(d))
	fl, _ := num(lo)
	fh, _ := num(hi)
	if _, isInt := hi.(int64); isInt {
		fh = fh + 1 - unit // an Integer end covers up to the next one
	}
	fl = math.Floor(fl/unit+1e-9) * unit
	fh = math.Floor(fh/unit+1e-9) * unit
	for p, i := fl, 0; roundTo(p+s-unit, 8) <= roundTo(fh, 8) && i < 100000; p, i = roundTo(p+s, 8), i+1 {
		out = append(out, Interval{Low: roundTo(p, 8), High: roundTo(p+s-unit, 8), LowClosed: true, HighClosed: true})
	}
	return out
}

// pointsOf are an interval's first and last points (open ends moved in), with whether each end is known: a closed
// null end is unbounded (unknown false is a null the interval does not know).
func pointsOf(iv Interval) (lo, hi any) {
	lo, hi = closedBounds(iv)
	typeOf := func(v any) string {
		switch x := sys(v).(type) {
		case int64:
			return "Integer"
		case Long:
			return "Long"
		case float64:
			return "Decimal"
		case DateTime:
			switch {
			case x.DateOnly:
				return "Date"
			case x.TimeOnly:
				return "Time"
			}
			return "DateTime"
		}
		return ""
	}
	if lo == nil && iv.LowClosed && hi != nil {
		lo = minMaxOf(typeOf(hi), false)
	}
	if hi == nil && iv.HighClosed && lo != nil {
		hi = minMaxOf(typeOf(lo), true)
	}
	return lo, hi
}

// intervalOp is meets (before, after), starts and ends.
func intervalOp(e *Env, sc *scope, n *node, op string, a, b any) any {
	ia, aok := sys(a).(Interval)
	ib, bok := sys(b).(Interval)
	if !aok || !bok {
		return nil
	}
	prec, _ := n.val.(string)
	at := func(v any) any {
		if d, ok := sys(v).(DateTime); ok && prec != "" {
			p := precIndex[prec]
			return DateTime{T: d.truncate(p), Prec: min(d.Prec, p), DateOnly: d.DateOnly, TimeOnly: d.TimeOnly}
		}
		return v
	}
	aLo, aHi := pointsOf(ia)
	bLo, bHi := pointsOf(ib)
	switch op {
	case "meets before":
		return equalVal(at(successor(aHi)), at(bLo))
	case "meets after":
		return equalVal(at(successor(bHi)), at(aLo))
	case "meets":
		return or3(equalVal(at(successor(aHi)), at(bLo)), equalVal(at(successor(bHi)), at(aLo)))
	case "starts":
		return and3(equalVal(at(aLo), at(bLo)), cmpOp(e, sc, n, "<=", at(aHi), at(bHi)))
	case "ends":
		return and3(cmpOp(e, sc, n, ">=", at(aLo), at(bLo)), equalVal(at(aHi), at(bHi)))
	}
	return nil
}

// overlapping is whether two intervals share a point (null when it cannot be known).
func overlapping(e *Env, sc *scope, n *node, a, b Interval) any {
	if a.Low == nil && a.LowClosed && a.High == nil && a.HighClosed || b.Low == nil && b.LowClosed && b.High == nil && b.HighClosed {
		return true // Interval[null, null] is every value
	}
	aLo, aHi := pointsOf(a)
	bLo, bHi := pointsOf(b)
	le := func(x, y any, xLowNull, yHighNull bool) any {
		if xLowNull || yHighNull {
			return true
		}
		return cmpOp(e, sc, n, "<=", x, y)
	}
	return and3(le(aLo, bHi, aLo == nil && a.LowClosed, bHi == nil && b.HighClosed),
		le(bLo, aHi, bLo == nil && b.LowClosed, aHi == nil && a.HighClosed))
}

func intersectIv(e *Env, sc *scope, n *node, a, b Interval) any {
	if truth(overlapping(e, sc, n, a, b)) == F {
		return nil
	}
	lo, loC := a.Low, a.LowClosed
	if c, ok := compare(sys(b.Low), sys(a.Low)); a.Low == nil && !a.LowClosed || ok && (c > 0 || c == 0 && !b.LowClosed) {
		lo, loC = b.Low, b.LowClosed
	} else if a.Low == nil && a.LowClosed {
		lo, loC = b.Low, b.LowClosed
	}
	hi, hiC := a.High, a.HighClosed
	if c, ok := compare(sys(b.High), sys(a.High)); a.High == nil && !a.HighClosed || ok && (c < 0 || c == 0 && !b.HighClosed) {
		hi, hiC = b.High, b.HighClosed
	} else if a.High == nil && a.HighClosed {
		hi, hiC = b.High, b.HighClosed
	}
	if b.High == nil && !b.HighClosed || a.High == nil && !a.HighClosed {
		hi, hiC = nil, false
	}
	if b.Low == nil && !b.LowClosed || a.Low == nil && !a.LowClosed {
		lo, loC = nil, false
	}
	return Interval{Low: lo, High: hi, LowClosed: loC, HighClosed: hiC}
}

func unionIv(e *Env, sc *scope, n *node, a, b Interval) any {
	_, aHi := pointsOf(a)
	bLo, _ := pointsOf(b)
	_, bHi := pointsOf(b)
	aLo, _ := pointsOf(a)
	joined := overlapping(e, sc, n, a, b)
	if truth(joined) != T {
		// meeting intervals join too
		if truth(or3(equalVal(successor(aHi), bLo), equalVal(successor(bHi), aLo))) != T {
			return nil
		}
	}
	lo, loC := a.Low, a.LowClosed
	if c, ok := compare(sys(b.Low), sys(a.Low)); b.Low == nil && b.LowClosed || ok && c < 0 {
		lo, loC = b.Low, b.LowClosed
	}
	hi, hiC := a.High, a.HighClosed
	if c, ok := compare(sys(b.High), sys(a.High)); b.High == nil && b.HighClosed || ok && c > 0 {
		hi, hiC = b.High, b.HighClosed
	}
	return Interval{Low: lo, High: hi, LowClosed: loC, HighClosed: hiC}
}

// exceptIv is a's points not in b, when they form one interval; null when b splits a in two.
func exceptIv(e *Env, sc *scope, n *node, a, b Interval) any {
	if truth(overlapping(e, sc, n, a, b)) == F {
		return a
	}
	aLo, aHi := pointsOf(a)
	bLo, bHi := pointsOf(b)
	loIn := truth(cmpOp(e, sc, n, "<=", bLo, aLo)) == T || bLo == nil && b.LowClosed
	hiIn := truth(cmpOp(e, sc, n, ">=", bHi, aHi)) == T || bHi == nil && b.HighClosed
	switch {
	case loIn && hiIn:
		return nil // all of a is in b
	case loIn:
		return Interval{Low: successor(bHi), High: a.High, LowClosed: true, HighClosed: a.HighClosed}
	case hiIn:
		return Interval{Low: a.Low, High: predecessor(bLo), LowClosed: a.LowClosed, HighClosed: true}
	}
	return nil
}

// qkind is a quantity in a base unit: its dimension and value there. Calendar years and months (the words) vary in
// length, so they are their own dimension ("calendar", in months); UCUM's 'a' and 'mo' are fixed lengths of time.
func qkind(q Quantity, equiv bool) (string, float64, bool) {
	u := q.Unit
	switch u {
	case "year", "years":
		if equiv {
			return "time", q.Value * 365 * 86400, true
		}
		return "calendar", q.Value * 12, true
	case "month", "months":
		if equiv {
			return "time", q.Value * 30 * 86400, true
		}
		return "calendar", q.Value, true
	case "a":
		return "ucum-a", q.Value * 365.25 * 86400, true
	case "mo":
		return "ucum-a", q.Value * 30.4375 * 86400, true
	}
	if w := durationUnit(u); w != "" {
		f := map[string]float64{"week": 604800, "day": 86400, "hour": 3600, "minute": 60, "second": 1, "millisecond": 0.001}[w]
		if f != 0 {
			return "time", q.Value * f, true
		}
	}
	if k, ok := units[u]; ok && k.kind != "" && k.kind != "time" && k.kind != "calendar" {
		return k.kind, q.Value * k.factor, true
	}
	return "unit:" + u, q.Value, false
}

// qcompare orders two quantities: c, whether they compare (same dimension), whether that is known (a calendar
// year against UCUM's 'a' is not).
func qcompare(a, b Quantity, equiv bool) (c int, comparable, known bool) {
	if equiv {
		// Equivalence treats a calendar year as 12 months and as UCUM's 'a'; a month as 'mo'; against days, a year
		// is 365 of them and a month 30.
		months := map[string]float64{"year": 12, "years": 12, "a": 12, "month": 1, "months": 1, "mo": 1}
		if ma, ok := months[a.Unit]; ok {
			if mb, ok := months[b.Unit]; ok {
				return cmpF(roundTo(a.Value*ma, 8), roundTo(b.Value*mb, 8)), true, true
			}
		}
	}
	if a.Unit == b.Unit || durationUnit(a.Unit) != "" && durationUnit(a.Unit) == durationUnit(b.Unit) && a.Unit != "a" &&
		b.Unit != "a" && a.Unit != "mo" && b.Unit != "mo" {
		return cmpF(a.Value, b.Value), true, true
	}
	ka, va, _ := qkind(a, equiv)
	kb, vb, _ := qkind(b, equiv)
	if ka == "calendar" && kb == "ucum-a" || ka == "ucum-a" && kb == "calendar" {
		if equiv {
			return cmpF(roundTo(va, 4), roundTo(vb, 4)), true, true
		}
		return 0, true, false
	}
	if ka == "ucum-a" {
		ka = "time"
	}
	if kb == "ucum-a" {
		kb = "time"
	}
	if ka != kb {
		return 0, false, true
	}
	return cmpF(roundTo(va, 8), roundTo(vb, 8)), true, true
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// qconvert is b in a's unit, when both have one dimension the engine knows.
func qconvert(b Quantity, unit string) (Quantity, bool) {
	if b.Unit == unit {
		return b, true
	}
	if r, ok := convertUnit(b, unit); ok {
		return r, true
	}
	kb, vb, _ := qkind(b, false)
	ka, va, _ := qkind(Quantity{1, unit}, false)
	if ka != kb || va == 0 {
		return Quantity{}, false
	}
	return Quantity{roundTo(vb/va, 8), unit}, true
}

// uncertain is an uncertainty's range (an Interval of Integers that a duration between dates of coarse precision
// gives), or a number as a range of one.
func uncertain(v any) (lo, hi any, ok bool) {
	switch x := v.(type) {
	case Interval:
		if _, isInt := sys(x.Low).(int64); isInt {
			if _, isInt := sys(x.High).(int64); isInt {
				return sys(x.Low), sys(x.High), true
			}
		}
		return nil, nil, false
	case int64, float64, Long:
		return x, x, true
	}
	return nil, nil, false
}

func isUncertainty(v any) bool {
	iv, ok := v.(Interval)
	if !ok {
		return false
	}
	_, a := sys(iv.Low).(int64)
	_, b := sys(iv.High).(int64)
	return a && b
}

// uncertainCompare compares where either side is an uncertainty: true or false when every value it could be agrees,
// null otherwise.
func uncertainCompare(op string, a, b any) any {
	alo, ahi, ok1 := uncertain(a)
	blo, bhi, ok2 := uncertain(b)
	if !ok1 || !ok2 {
		return nil
	}
	c := func(x, y any) int { r, _ := compare(x, y); return r }
	switch op {
	case "<":
		if c(ahi, blo) < 0 {
			return true
		}
		if c(alo, bhi) >= 0 {
			return false
		}
	case "<=":
		if c(ahi, blo) <= 0 {
			return true
		}
		if c(alo, bhi) > 0 {
			return false
		}
	case ">":
		if c(alo, bhi) > 0 {
			return true
		}
		if c(ahi, blo) <= 0 {
			return false
		}
	case ">=":
		if c(alo, bhi) >= 0 {
			return true
		}
		if c(ahi, blo) < 0 {
			return false
		}
	case "=":
		if c(ahi, blo) < 0 || c(alo, bhi) > 0 {
			return false
		}
		if c(alo, ahi) == 0 && c(blo, bhi) == 0 && c(alo, blo) == 0 {
			return true
		}
	}
	return nil
}

// uncertainArith adds, subtracts or multiplies uncertainties: the range of every result.
func uncertainArith(e *Env, sc *scope, n *node, op string, a, b any) any {
	alo, ahi, _ := uncertain(a)
	blo, bhi, _ := uncertain(b)
	var lo, hi any
	switch op {
	case "+":
		lo, hi = arith(e, sc, n, "+", alo, blo), arith(e, sc, n, "+", ahi, bhi)
	case "-":
		lo, hi = arith(e, sc, n, "-", alo, bhi), arith(e, sc, n, "-", ahi, blo)
	case "*":
		ps := []any{arith(e, sc, n, "*", alo, blo), arith(e, sc, n, "*", alo, bhi), arith(e, sc, n, "*", ahi, blo),
			arith(e, sc, n, "*", ahi, bhi)}
		lo, hi = ps[0], ps[0]
		for _, p := range ps[1:] {
			if order(p, lo) < 0 {
				lo = p
			}
			if order(p, hi) > 0 {
				hi = p
			}
		}
	default:
		e.fail(sc.lib, n, "%s of an uncertain duration is not defined", op)
	}
	if eq, _ := equal(lo, hi); eq {
		return lo
	}
	return Interval{Low: lo, High: hi, LowClosed: true, HighClosed: true}
}

// atPrecision cuts a date, or the bounds of an interval of dates, to a precision (day of, month of ...).
func atPrecision(v any, prec string) any {
	p := precIndex[prec]
	if prec == "week" {
		p = pDay
	}
	if p == 0 {
		return v
	}
	cut := func(x any) any {
		if d, ok := sys(x).(DateTime); ok {
			return DateTime{T: d.truncate(p), Prec: min(d.Prec, p), DateOnly: d.DateOnly, TimeOnly: d.TimeOnly}
		}
		return x
	}
	switch x := sys(v).(type) {
	case DateTime:
		return cut(x)
	case Interval:
		x.Low, x.High = cut(x.Low), cut(x.High)
		return x
	}
	return v
}
