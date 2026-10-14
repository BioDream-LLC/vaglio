package decide

import (
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
)

func BenchmarkDecideLumbar(b *testing.B) {
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		b.Fatal(err)
	}
	req := Request{Evidence: []Assertion{
		{Argument: "conservative_therapy_6wk", Finding: Present, Citation: Citation{Reference: "r", Quote: "q"}},
		{Argument: "recent_same_imaging", Finding: Present, Citation: Citation{Reference: "r", Quote: "q"}}}}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := Decide(p, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
