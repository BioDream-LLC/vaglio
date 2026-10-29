package draft

import (
	"context"
	"strings"
	"testing"
)

// otherReading reads the same document differently: osteoarthritis alone, which the first reading ignores, denies.
const otherReading = "```vaglio\n" + `policy knee
  title "Knee arthroscopy"
  version 1
  source "Plan Policy 7.1"
  questionnaire https://payer.example/Q/knee
  service cpt 29881

evidence mechanical
  text "Mechanical symptoms after conservative care"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  find question mechanical

evidence oa
  text "Osteoarthritis alone"
  clause "Arthroscopy is not covered for osteoarthritis alone (ICD-10 M17.11)."
  find question oa

criterion unmet
  text "No indication"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by mechanical

criterion met
  text "Indicated and not excluded"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by unmet
  defeated by oa

decision approve approves
  text "Approve"
  clause "Arthroscopy is covered when mechanical symptoms persist after conservative care."
  defeated by unmet
  defeated by oa

decision deny denies
  text "Deny"
  clause "Arthroscopy is not covered for osteoarthritis alone (ICD-10 M17.11)."
  defeated by met
` + "```"

// TestTwiceAgrees: the same reading twice agrees on every record, and says so in one line.
func TestTwiceAgrees(t *testing.T) {
	m := &scripted{answers: []string{good}}
	tw, err := Twice(context.Background(), m, m, Request{ID: "knee", Document: doc, Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !tw.Agree() || !strings.Contains(tw.Report(), "the same way") {
		t.Fatalf("%v\n%s", tw.Problems, tw.Report())
	}
}

// TestTwiceDisagrees: a second model reading the document differently is caught, with the records and the clauses.
func TestTwiceDisagrees(t *testing.T) {
	if _, failures := GateAll(Request{ID: "knee", Document: doc}, otherReading); len(failures) > 0 {
		t.Fatalf("the other reading should pass every gate: %v", failures)
	}
	a, b := &scripted{answers: []string{good}}, &scripted{answers: []string{otherReading}}
	tw, err := Twice(context.Background(), a, b, Request{ID: "knee", Document: doc, Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	r := tw.Report()
	if tw.Agree() || !strings.Contains(r, "DISAGREE") || !strings.Contains(r, "only the second draft decides on \"Osteoarthritis alone\"") {
		t.Fatalf("%s", r)
	}
	if !strings.HasPrefix(tw.Commented(), "# Drafted twice") {
		t.Errorf("%q", tw.Commented())
	}
}
