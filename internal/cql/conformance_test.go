package cql

// The HL7 CQL conformance tests (github.com/cqframework/cql-tests, Apache-2.0, the commit in testdata/cql-tests/SOURCE):
// about two thousand expressions with the value each must give. Each runs as a define, its expected output as
// another, and the two are compared as CQL equivalence (null with null, 1.0 with 1.00), except that numbers must be
// equal to the last place shown: equivalence alone compares decimals at the coarser precision, and 1.00999999 is
// equivalent to 1.00, but it is not the answer 1.00. A test marked invalid passes
// when compiling or evaluating it is an error.
//
// The test records how many pass and fails if fewer do than conformanceFloor: the engine may grow, never shrink.
// VAGLIO_CQL_CONFORMANCE=FILE writes every failure, with why, to FILE.

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// conformanceFloor is the number of conformance tests that passed when it was last raised.
const conformanceFloor = 2003

// cqlVersion is the CQL version this engine runs. A test for a later version (2.0 changes successor and predecessor
// of a Decimal to its own precision) or only for an earlier one (the timezone keyword, gone after 1.3) does not
// apply, and is counted apart; for most of those, the suite has a test of the same expression for 1.5 that does.
const cqlVersion = "1.5.3"

// applies is whether a test covers cqlVersion.
func (tc ctTest) applies() bool {
	return (tc.Version == "" || versionLE(tc.Version, cqlVersion)) && (tc.VersionTo == "" || versionLE(cqlVersion, tc.VersionTo))
}

// versionLE is a <= b for dotted versions.
func versionLE(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x < y
		}
	}
	return true
}

type ctTests struct {
	Name   string    `xml:"name,attr"`
	Groups []ctGroup `xml:"group"`
}

type ctGroup struct {
	Name  string   `xml:"name,attr"`
	Tests []ctTest `xml:"test"`
}

type ctTest struct {
	Name       string `xml:"name,attr"`
	Version    string `xml:"version,attr"`   // the first CQL version the test is for
	VersionTo  string `xml:"versionTo,attr"` // the last, when it stops applying
	Expression struct {
		Text    string `xml:",chardata"`
		Invalid string `xml:"invalid,attr"`
	} `xml:"expression"`
	Outputs []struct {
		Text string `xml:",chardata"`
	} `xml:"output"`
}

type ctResult struct {
	file, group, name, why string
}

func runConformance(t *testing.T) (pass, total, other int, failed []ctResult) {
	files, _ := filepath.Glob(filepath.Join("testdata", "cql-tests", "*.xml"))
	if len(files) == 0 {
		t.Fatal("no conformance tests in testdata/cql-tests")
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var ts ctTests
		if err := xml.Unmarshal(raw, &ts); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, g := range ts.Groups {
			for _, tc := range g.Tests {
				if !tc.applies() {
					other++
					continue
				}
				total++
				why := ctRun(tc, now)
				if why == "" {
					pass++
				} else {
					failed = append(failed, ctResult{filepath.Base(f), g.Name, tc.Name, why})
				}
			}
		}
	}
	return pass, total, other, failed
}

// ctRun runs one test and says why it failed ("" when it passed).
func ctRun(tc ctTest, now time.Time) (why string) {
	defer func() {
		if r := recover(); r != nil {
			why = fmt.Sprintf("panic: %v", r)
		}
	}()
	expr := strings.TrimSpace(tc.Expression.Text)
	invalid := tc.Expression.Invalid == "true" || tc.Expression.Invalid == "semantic" || tc.Expression.Invalid == "syntax"
	src := "library T version '1'\ndefine \"E\": " + expr + "\n"
	for i, o := range tc.Outputs {
		src += fmt.Sprintf("define \"O%d\": %s\n", i, strings.TrimSpace(o.Text))
	}
	lib, err := Compile(src)
	if err != nil {
		if invalid {
			return ""
		}
		return "compile: " + err.Error()
	}
	env := &Env{Libraries: map[string]*Library{"T": lib}, Now: now, MaxSteps: 1_000_000}
	got, err := env.Evaluate("T", "E")
	if invalid {
		if err != nil {
			return ""
		}
		return "evaluated, where it is an error: " + Show(got)
	}
	if err != nil {
		return "evaluate: " + err.Error()
	}
	if len(tc.Outputs) == 0 {
		return ""
	}
	want, err := env.Evaluate("T", "O0")
	if err != nil {
		return "the expected output: " + err.Error()
	}
	if len(tc.Outputs) > 1 {
		// Several outputs: the expression is a list of them.
		var all []any
		for i := range tc.Outputs {
			v, err := env.Evaluate("T", fmt.Sprintf("O%d", i))
			if err != nil {
				return "the expected output: " + err.Error()
			}
			all = append(all, v)
		}
		want = all
	}
	if got == nil && want == nil {
		return ""
	}
	if got == nil || want == nil || !sameResult(got, want) {
		return fmt.Sprintf("got %s, want %s", Show(got), Show(want))
	}
	return ""
}

func TestConformance(t *testing.T) {
	pass, total, other, failed := runConformance(t)
	t.Logf("CQL conformance tests for CQL %s: %d of %d pass (%d more are for other versions)", cqlVersion, pass, total, other)
	if out := os.Getenv("VAGLIO_CQL_CONFORMANCE"); out != "" {
		sort.Slice(failed, func(i, j int) bool {
			if failed[i].file != failed[j].file {
				return failed[i].file < failed[j].file
			}
			return failed[i].group < failed[j].group
		})
		var b strings.Builder
		fmt.Fprintf(&b, "%d of %d pass (CQL %s; %d tests for other versions not run)\n", pass, total, cqlVersion, other)
		for _, f := range failed {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", f.file, f.group, f.name, strings.ReplaceAll(f.why, "\n", " "))
		}
		os.WriteFile(out, []byte(b.String()), 0o644)
	}
	if pass < conformanceFloor {
		t.Errorf("%d conformance tests pass, fewer than the %d that did", pass, conformanceFloor)
	}
}

// sameResult is CQL equivalence with numbers compared exactly (to 8 places), inside lists, intervals, tuples and
// quantities too.
func sameResult(a, b any) bool {
	a, b = sys(a), sys(b)
	switch x := a.(type) {
	case float64, int64, Long:
		fa, _ := num(x)
		fb, ok := num(b)
		return ok && roundTo(fa, 8) == roundTo(fb, 8)
	case Quantity:
		y, ok := b.(Quantity)
		if !ok {
			return false
		}
		if x.Unit == y.Unit {
			return roundTo(x.Value, 8) == roundTo(y.Value, 8)
		}
		return equivalent(x, y) // 1 'm' and 100 'cm'
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !(sys(x[i]) == nil && sys(y[i]) == nil) && !sameResult(x[i], y[i]) {
				return false
			}
		}
		return true
	case Interval:
		y, ok := b.(Interval)
		same := func(p, q any) bool {
			return sys(p) == nil && sys(q) == nil || sys(p) != nil && sys(q) != nil && sameResult(p, q)
		}
		return ok && x.LowClosed == y.LowClosed && x.HighClosed == y.HighClosed && same(x.Low, y.Low) && same(x.High, y.High)
	}
	return equivalent(a, b)
}
