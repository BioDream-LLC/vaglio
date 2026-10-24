package cql

// The HL7 CQL conformance tests (github.com/cqframework/cql-tests, Apache-2.0, the commit in testdata/cql-tests/SOURCE):
// about two thousand expressions with the value each must give. Each runs as a define, its expected output as
// another, and the two are compared as CQL equivalence (null with null, 1.0 with 1.00). A test marked invalid passes
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

func runConformance(t *testing.T) (pass, total int, failed []ctResult) {
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
	return pass, total, failed
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
	if got == nil || want == nil || !equivalent(got, want) {
		return fmt.Sprintf("got %s, want %s", Show(got), Show(want))
	}
	return ""
}

func TestConformance(t *testing.T) {
	pass, total, failed := runConformance(t)
	t.Logf("CQL conformance tests: %d of %d pass", pass, total)
	if out := os.Getenv("VAGLIO_CQL_CONFORMANCE"); out != "" {
		sort.Slice(failed, func(i, j int) bool {
			if failed[i].file != failed[j].file {
				return failed[i].file < failed[j].file
			}
			return failed[i].group < failed[j].group
		})
		var b strings.Builder
		fmt.Fprintf(&b, "%d of %d pass\n", pass, total)
		for _, f := range failed {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", f.file, f.group, f.name, strings.ReplaceAll(f.why, "\n", " "))
		}
		os.WriteFile(out, []byte(b.String()), 0o644)
	}
	if pass < conformanceFloor {
		t.Errorf("%d conformance tests pass, fewer than the %d that did", pass, conformanceFloor)
	}
}
