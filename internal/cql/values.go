package cql

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The values an expression has: nil (null), bool, int64 (Integer), float64 (Decimal), string, DateTime, Quantity, Code,
// Concept, Interval, []any (List), *Tuple, and FHIR data as *Elem.

// Precision of a DateTime, from year to millisecond.
const (
	pYear = iota + 1
	pMonth
	pDay
	pHour
	pMinute
	pSecond
	pMilli
)

var precIndex = map[string]int{"year": pYear, "month": pMonth, "day": pDay, "hour": pHour, "minute": pMinute,
	"second": pSecond, "millisecond": pMilli}

// DateTime is a Date (DateOnly) or a DateTime, known to its precision.
type DateTime struct {
	T        time.Time
	Prec     int
	DateOnly bool
}

func (d DateTime) String() string {
	layout := map[int]string{pYear: "2006", pMonth: "2006-01", pDay: "2006-01-02", pHour: "2006-01-02T15",
		pMinute: "2006-01-02T15:04", pSecond: "2006-01-02T15:04:05", pMilli: "2006-01-02T15:04:05.000"}[d.Prec]
	s := d.T.Format(layout)
	if !d.DateOnly && d.Prec >= pHour {
		s += d.T.Format("Z07:00")
	}
	return s
}

var dateRE = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2})(?:T(\d{2})(?::(\d{2})(?::(\d{2})(?:\.(\d+))?)?)?(Z|[+-]\d{2}:\d{2})?)?)?)?(T)?$`)

// ParseDateTime reads an ISO 8601 date or dateTime (as FHIR and CQL write them), keeping its precision.
func ParseDateTime(s string) (DateTime, error) {
	m := dateRE.FindStringSubmatch(s)
	if m == nil {
		return DateTime{}, fmt.Errorf("%q is not a date", s)
	}
	n := func(i, def int) int {
		if m[i] == "" {
			return def
		}
		v, _ := strconv.Atoi(m[i])
		return v
	}
	d := DateTime{Prec: pYear}
	for i, p := range []int{2, 3, 4, 5, 6, 7} {
		if m[p] != "" {
			d.Prec = pMonth + i
		}
	}
	ms := 0
	if m[7] != "" {
		f := (m[7] + "000")[:3]
		ms, _ = strconv.Atoi(f)
	}
	loc := time.UTC
	if z := m[8]; z != "" && z != "Z" {
		t, err := time.Parse("-07:00", z)
		if err == nil {
			loc = t.Location()
		}
	}
	d.DateOnly = d.Prec <= pDay && m[9] == ""
	d.T = time.Date(n(1, 0), time.Month(n(2, 1)), n(3, 1), n(4, 0), n(5, 0), n(6, 0), ms*1e6, loc)
	if d.T.Month() != time.Month(n(2, 1)) || d.T.Day() != n(3, 1) {
		return DateTime{}, fmt.Errorf("%q is not a calendar date", s)
	}
	return d, nil
}

// truncate drops what is finer than prec.
func (d DateTime) truncate(prec int) time.Time {
	t := d.T.UTC()
	if d.DateOnly || d.Prec <= pDay {
		t = d.T
	}
	y, mo, dd := t.Date()
	h, mi, s := t.Clock()
	ns := t.Nanosecond()
	if prec < pMonth {
		mo = 1
	}
	if prec < pDay {
		dd = 1
	}
	if prec < pHour {
		h = 0
	}
	if prec < pMinute {
		mi = 0
	}
	if prec < pSecond {
		s = 0
	}
	if prec < pMilli {
		ns = 0
	}
	return time.Date(y, mo, dd, h, mi, s, ns, t.Location())
}

// compareDT: -1, 0, 1, or uncertain (ok false) when the two agree to the coarser precision but one is less precise.
func compareDT(a, b DateTime) (int, bool) {
	p := min(a.Prec, b.Prec)
	ta, tb := a.truncate(p), b.truncate(p)
	switch {
	case ta.Before(tb):
		return -1, true
	case ta.After(tb):
		return 1, true
	case a.Prec != b.Prec:
		return 0, false
	}
	return 0, true
}

// Quantity is a number with a unit: UCUM, or a calendar duration (year, month, week, day, hour ...).
type Quantity struct {
	Value float64
	Unit  string
}

func (q Quantity) String() string {
	return strconv.FormatFloat(q.Value, 'f', -1, 64) + " '" + q.Unit + "'"
}

// calendar units, by UCUM and CQL spelling.
var calendar = map[string]string{"a": "year", "mo": "month", "wk": "week", "d": "day", "h": "hour", "min": "minute",
	"s": "second", "ms": "millisecond"}

func durationUnit(u string) string {
	if c, ok := calendar[u]; ok {
		return c
	}
	if p, ok := precisions[u]; ok {
		return p
	}
	return ""
}

// Code is a code from a code system.
type Code struct {
	Code, System, Display, Version string
}

// Concept is one or more codes meaning the same thing.
type Concept struct {
	Codes   []Code
	Display string
}

// Interval is a range, open or closed at each end; a null bound is unknown, or unbounded when closed.
type Interval struct {
	Low, High             any
	LowClosed, HighClosed bool
}

// Tuple is a structured value with named elements.
type Tuple struct {
	Keys []string
	M    map[string]any
}

// Elem is FHIR data: a resource, a part of one, or a primitive, as read from its JSON. Type is its FHIR type where it
// is known: a resource's type, or a choice element's (valueQuantity is Quantity).
type Elem struct {
	V    any // map[string]any, string, json.Number, bool
	Type string
	Ext  any // the primitive's _element (its id and extensions), if any
}

// ValueSet is a value set's codes, from a FHIR ValueSet's expansion (or its compose's listed concepts).
type ValueSet struct {
	URL, Version string
	codes        map[string]bool // system|code
}

// NewValueSet reads a FHIR ValueSet resource. It must list its codes: an expansion, or a compose that names concepts
// (filters need a terminology server, which this does not use).
func NewValueSet(raw []byte) (*ValueSet, error) {
	var vs struct {
		ResourceType string `json:"resourceType"`
		URL          string `json:"url"`
		Version      string `json:"version"`
		Expansion    *struct {
			Contains []vsCode `json:"contains"`
		} `json:"expansion"`
		Compose *struct {
			Include []struct {
				System  string `json:"system"`
				Concept []struct {
					Code string `json:"code"`
				} `json:"concept"`
				Filter   []any    `json:"filter"`
				ValueSet []string `json:"valueSet"`
			} `json:"include"`
		} `json:"compose"`
	}
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, err
	}
	if vs.ResourceType != "ValueSet" || vs.URL == "" {
		return nil, errors.New("not a ValueSet with a url")
	}
	out := &ValueSet{URL: vs.URL, Version: vs.Version, codes: map[string]bool{}}
	var walk func([]vsCode)
	walk = func(cs []vsCode) {
		for _, c := range cs {
			if c.Code != "" {
				out.codes[c.System+"|"+c.Code] = true
			}
			walk(c.Contains)
		}
	}
	switch {
	case vs.Expansion != nil:
		walk(vs.Expansion.Contains)
	case vs.Compose != nil:
		for _, in := range vs.Compose.Include {
			if len(in.Filter) > 0 || len(in.ValueSet) > 0 || len(in.Concept) == 0 {
				return nil, fmt.Errorf("value set %s is defined by filters or other value sets: give its expansion", vs.URL)
			}
			for _, c := range in.Concept {
				out.codes[in.System+"|"+c.Code] = true
			}
		}
	default:
		return nil, fmt.Errorf("value set %s has neither an expansion nor listed concepts", vs.URL)
	}
	return out, nil
}

type vsCode struct {
	System   string   `json:"system"`
	Code     string   `json:"code"`
	Contains []vsCode `json:"contains"`
}

// Has is whether the code is in the value set.
func (v *ValueSet) Has(c Code) bool { return v.codes[c.System+"|"+c.Code] }

// Len is how many codes it has.
func (v *ValueSet) Len() int { return len(v.codes) }

// Show is a value as CQL would write it, for messages.
func Show(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "'" + x + "'"
	case bool, int64:
		return fmt.Sprint(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case DateTime:
		return "@" + x.String()
	case Quantity:
		return x.String()
	case Code:
		return fmt.Sprintf("Code '%s' from '%s'", x.Code, x.System)
	case Concept:
		var cs []string
		for _, c := range x.Codes {
			cs = append(cs, Show(c))
		}
		return "Concept { " + strings.Join(cs, ", ") + " }"
	case Interval:
		l, h := "(", ")"
		if x.LowClosed {
			l = "["
		}
		if x.HighClosed {
			h = "]"
		}
		return "Interval" + l + Show(x.Low) + ", " + Show(x.High) + h
	case []any:
		if len(x) == 0 {
			return "{ }"
		}
		var out []string
		for _, e := range x {
			out = append(out, Show(e))
		}
		return "{ " + strings.Join(out, ", ") + " }"
	case *Tuple:
		var out []string
		for _, k := range x.Keys {
			out = append(out, k+": "+Show(x.M[k]))
		}
		return "Tuple { " + strings.Join(out, ", ") + " }"
	case *Elem:
		if m, ok := x.V.(map[string]any); ok {
			if rt, _ := m["resourceType"].(string); rt != "" {
				id, _ := m["id"].(string)
				return rt + "/" + id
			}
			return "FHIR." + x.Type
		}
		return Show(primitive(x))
	}
	return fmt.Sprint(v)
}

// primitive is a FHIR primitive's value as CQL's: a date or dateTime when its text is one.
func primitive(e *Elem) any {
	switch v := e.V.(type) {
	case string:
		if (len(v) >= 7 && v[4] == '-') && dateRE.MatchString(v) {
			if d, err := ParseDateTime(v); err == nil {
				return d
			}
		}
		return v
	case json.Number:
		if !strings.ContainsAny(string(v), ".eE") {
			if i, err := v.Int64(); err == nil && e.Type != "decimal" {
				return i
			}
		}
		f, _ := v.Float64()
		return f
	case bool:
		return v
	}
	return nil
}

func roundTo(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

func sortAny(list []any, less func(a, b any) bool) {
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
}

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }
