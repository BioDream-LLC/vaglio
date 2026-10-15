package fairness

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/pastest"
)

func TestFisher(t *testing.T) {
	// R's fisher.test(matrix(c(3,1,1,3),2)) gives p = 0.4857; matrix(c(10,0,0,10),2) gives 1.083e-05.
	for _, c := range []struct {
		a, b, c, d int
		p          float64
	}{{3, 1, 1, 3, 0.4857}, {10, 0, 0, 10, 1.0825e-05}, {0, 0, 0, 0, 1}, {5, 5, 5, 5, 1}} {
		if got := Fisher(c.a, c.b, c.c, c.d); math.Abs(got-c.p) > 1e-4*math.Max(1, c.p*100) {
			t.Errorf("Fisher(%d,%d,%d,%d) = %g, want %g", c.a, c.b, c.c, c.d, got, c.p)
		}
	}
}

func TestReadPatient(t *testing.T) {
	b := pastest.Bundle([]string{"72148"})
	pt := pastest.Map(pastest.Entries(b)[1])
	pt["gender"], pt["birthDate"] = "female", "1950-06-01"
	pt["extension"] = []any{
		map[string]any{"url": "http://hl7.org/fhir/us/core/StructureDefinition/us-core-race", "extension": []any{
			map[string]any{"url": "ombCategory", "valueCoding": map[string]any{"code": "2054-5"}}}},
		map[string]any{"url": "http://hl7.org/fhir/us/core/StructureDefinition/us-core-ethnicity", "extension": []any{
			map[string]any{"url": "ombCategory", "valueCoding": map[string]any{"code": "2186-5"}}}},
	}
	p, err := ReadPatient([]byte(pastest.JSON(b)), "WA")
	if err != nil {
		t.Fatal(err)
	}
	created := pastest.Map(pastest.Entries(b)[0])["created"]
	if p.Sex != "female" || p.Race != "Black or African American" || p.Ethnicity != "Not Hispanic or Latino" ||
		p.State != "WA" || p.Line != unknown || (created != nil && p.Age != "75 and over" && p.Age != "65-74") {
		t.Fatalf("%+v (created %v)", p, created)
	}
	q, _ := ReadPatient([]byte(pastest.JSON(pastest.Bundle([]string{"72148"}))), "")
	if q.Sex != unknown || q.Race != unknown || q.State != unknown {
		t.Fatalf("nothing recorded: %+v", q)
	}
}

func TestBuild(t *testing.T) {
	now := time.Now().UTC()
	var cs []cases.Case
	var pts []Patient
	add := func(n int, sex string, outcome decide.Outcome, final string, appealOverturned bool) {
		for i := 0; i < n; i++ {
			c := cases.Case{ID: string(rune('a'+len(cs)%26)) + time.Duration(len(cs)).String(), Created: now.Format(time.RFC3339Nano),
				Status: cases.Decided, Opening: cases.Opening{Policy: "p", Result: &decide.Result{Outcome: outcome},
					Answer: cases.Answer{Decision: final}}}
			if outcome == decide.Pended {
				c.Result.Missing = []decide.Item{{Argument: "x"}}
				c.Status, c.Review = cases.Reviewed, &cases.Review{Decision: final}
			}
			if appealOverturned {
				c.Appeal = &cases.Appeal{Outcome: "overturned"}
			}
			cs = append(cs, c)
			pts = append(pts, Patient{Sex: sex, Age: "45-64", Race: unknown, Ethnicity: unknown, State: "WA", Line: unknown})
		}
	}
	// Women: 20 denials, 15 overturned on appeal. Men: 20 denials, 1 overturned. 40 approvals each.
	add(40, "female", decide.Approved, "approve", false)
	add(15, "female", decide.Denied, "deny", true)
	add(5, "female", decide.Denied, "deny", false)
	add(40, "male", decide.Approved, "approve", false)
	add(1, "male", decide.Denied, "deny", true)
	add(19, "male", decide.Denied, "deny", false)
	// Five "other": too few to show.
	add(5, "other", decide.Approved, "approve", false)
	index := map[*cases.Case]Patient{}
	for i := range cs {
		index[&cs[i]] = pts[i]
	}
	r := Build(cs, now.AddDate(0, 0, -1), now, "", func(c *cases.Case) (Patient, error) { return index[c], nil })
	if r.Requests != 125 {
		t.Fatalf("requests %d", r.Requests)
	}
	var sex Dimension
	for _, d := range r.Dimensions {
		if d.Name == "sex" {
			sex = d
		}
	}
	g := map[string]Group{}
	for _, x := range sex.Groups {
		g[x.Value] = x
	}
	if f := g["female"]; f.EngineDenials != 20 || f.Reversed != 15 || len(f.Flags) == 0 || !strings.Contains(f.Flags[0], "reversed 75%") {
		t.Fatalf("women: %+v", f)
	}
	if m := g["male"]; len(m.Flags) == 0 {
		t.Fatalf("men differ from the rest too: %+v", m)
	}
	if o := g["other"]; !o.Suppressed || o.EngineDenials != 0 || o.ApprovedPercent != nil {
		t.Fatalf("a small group is shown: %+v", o)
	}
	var state Dimension
	for _, d := range r.Dimensions {
		if d.Name == "state" {
			state = d
		}
	}
	if len(state.Groups) != 1 || len(state.Groups[0].Flags) != 0 {
		t.Fatalf("one group has nothing to differ from: %+v", state)
	}
}
