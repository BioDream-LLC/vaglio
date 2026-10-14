package readings

import (
	"testing"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func load(t *testing.T, path string) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAReadingAgreesWithItself(t *testing.T) {
	p := load(t, "../../library/tka.vaglio")
	c, err := Compare(p, p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Differ != 0 || len(c.Matched) != 14 || len(c.OnlyA)+len(c.OnlyB) != 0 || c.Records != 1<<14 {
		t.Errorf("%+v", c)
	}
}

func TestTwoReadingsOfTheTKALCD(t *testing.T) {
	c, err := Compare(load(t, "../../library/tka.vaglio"), load(t, "../../library/readings/tka.b.vaglio"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Differ == 0 || len(c.Groups) != 1 {
		t.Fatalf("%+v", c)
	}
	g := c.Groups[0]
	// The simplest record they disagree on: a failed osteotomy and nothing else.
	if g.A != decide.Approved || g.B != decide.Denied || len(g.Example) != 1 || g.Example[0] != "Failure of a previous osteotomy" {
		t.Errorf("%+v", g)
	}
}

func TestEvidenceOnlyOneReadingHas(t *testing.T) {
	c, err := Compare(load(t, "../../library/drafts/cpap_osa.machine.vaglio"), load(t, "../../library/cpap_osa.vaglio"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.OnlyA) == 0 || len(c.OnlyB) == 0 || c.Differ == 0 {
		t.Errorf("%+v", c)
	}
}
