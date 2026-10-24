package cql

import (
	"os"
	"testing"
	"time"
)

// FuzzCompile: any text either compiles or is refused with an error, never a panic or a hang, and a library that
// compiles evaluates every define within the work limit, to a value or an error.
func FuzzCompile(f *testing.F) {
	f.Add(lib)
	f.Add("library X\ndefine A: [Condition] C where C.code ~ 'x' return C.id\n")
	f.Add("library X\ndefine A: Interval[@2020-01-01, @2021-01-01) overlaps Interval[@2020-06-01, null]\n")
	f.Add("library X\ndefine function F(a Integer): if a > 0 then F(a - 1) else 0\ndefine A: F(10)\n")
	f.Add("library X\ndefine A: expand { Interval[@T10:00, @T12:30) } per hour\ndefine B: collapse { Interval[1,5], Interval[3,7] }\n")
	f.Add("library X\ndefine A: ({1,2,3}) N aggregate R starting 1: R * N\ndefine B: years between DateTime(2005) and DateTime(2010) > 4\n")
	f.Add("library X\ndefine A: 1 'cm' : 2 'cm' ~ 2 'cm' : 4 'cm'\ndefine B: maximum Long - 1L\ndefine C: Interval[1, 10] except Interval[3, 5]\n")
	raw, _ := os.ReadFile("testdata/patient.json")
	rs, _ := FromBundle(raw)
	f.Fuzz(func(t *testing.T, src string) {
		l, err := Compile(src)
		if err != nil {
			return
		}
		env := &Env{Libraries: map[string]*Library{l.Name: l}, Resources: rs, MaxSteps: 20_000,
			Now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
		for _, d := range l.Defines() {
			_, _ = env.Evaluate(l.Name, d)
		}
	})
}
