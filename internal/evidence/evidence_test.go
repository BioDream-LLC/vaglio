package evidence

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

func lumbar(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile("../../policies/lumbar_mri.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func extract(t *testing.T, extra ...map[string]any) Extraction {
	t.Helper()
	req, problems := pas.Read(pastest.Bundle([]string{"72148"}, extra...), false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return Extract(lumbar(t), req)
}

// summary is each finding as argument=present|absent, in order.
func summary(fs []Finding) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Argument+"="+string(f.Finding))
	}
	return strings.Join(s, ",")
}

func TestQuestionnaireAnswers(t *testing.T) {
	x := extract(t, pastest.QR("q1", pastest.Questionnaire, "completed",
		pastest.Answer{LinkID: "conservative-therapy", Text: "Six weeks of conservative therapy failed?", Value: true},
		pastest.Answer{LinkID: "prior-imaging-6mo", Text: "Lumbar MRI in the last six months?", Value: false},
		pastest.Answer{LinkID: "red-flag", Text: "Red-flag finding?"}, // unanswered: not addressed
		pastest.Answer{LinkID: "surgical-planning", Text: "Surgery scheduled?", Value: "maybe"},
		pastest.Answer{LinkID: "unrelated", Text: "Something else", Value: true},
	))
	if got := summary(x.Findings); got != "conservative_therapy_6wk=present,recent_same_imaging=absent" {
		t.Fatalf("findings %s", got)
	}
	f := x.Findings[0]
	if f.Reference != pastest.Base+"/QuestionnaireResponse/q1#conservative-therapy" ||
		f.Quote != "Six weeks of conservative therapy failed? - Yes" {
		t.Errorf("citation %+v", f.Citation)
	}
	if len(x.Ignored) != 1 || !strings.Contains(x.Ignored[0], "surgical-planning") {
		t.Errorf("ignored %v, want the non-boolean answer reported", x.Ignored)
	}
}

func TestQuestionnairesThatDoNotCount(t *testing.T) {
	yes := pastest.Answer{LinkID: "red-flag", Text: "Red flag?", Value: true}
	x := extract(t,
		pastest.QR("other", "https://payer.example/Questionnaire/knee", "completed", yes), // another questionnaire's linkId
		pastest.QR("draft", pastest.Questionnaire, "in-progress", yes),                    // not yet given
	)
	if len(x.Findings) != 0 {
		t.Fatalf("findings %s, want none", summary(x.Findings))
	}
	if len(x.Ignored) != 1 || !strings.Contains(x.Ignored[0], "in-progress") {
		t.Errorf("ignored %v, want the in-progress response reported", x.Ignored)
	}
	// A versioned canonical is the same questionnaire.
	x = extract(t, pastest.QR("v", pastest.Questionnaire+"|2.0", "amended", yes))
	if summary(x.Findings) != "red_flag=present" {
		t.Errorf("versioned canonical: %s", summary(x.Findings))
	}
}

func TestConditions(t *testing.T) {
	cases := []struct {
		clinical, verification, want string
	}{
		{"active", "confirmed", "red_flag=present"},
		{"active", "provisional", "red_flag=present"}, // the clause is about a suspected condition
		{"recurrence", "differential", "red_flag=present"},
		{"active", "unconfirmed", ""}, // not accepted by this source
		{"", "refuted", "red_flag=absent"},
		{"resolved", "confirmed", ""},
		{"active", "entered-in-error", ""},
		{"active", "", ""},
	}
	for _, c := range cases {
		x := extract(t, pastest.Condition("k", "G83.4", "Cauda equina syndrome", c.clinical, c.verification))
		if got := summary(x.Findings); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.clinical, c.verification, got, c.want)
		}
	}
	x := extract(t, pastest.Condition("k", "G83.4", "Cauda equina syndrome", "active", "confirmed"))
	if q := x.Findings[0].Quote; q != "Cauda equina syndrome (G83.4), verification status confirmed, clinical status active" {
		t.Errorf("quote %q", q)
	}
	// A code the policy does not name, and the right code in another system, are nothing.
	if x := extract(t, pastest.Condition("k", "M54.5", "Low back pain", "active", "confirmed")); len(x.Findings) != 0 {
		t.Errorf("an unnamed code was found: %s", summary(x.Findings))
	}
}

func TestContradiction(t *testing.T) {
	x := extract(t,
		pastest.QR("q1", pastest.Questionnaire, "completed",
			pastest.Answer{LinkID: "red-flag", Text: "Red flag?", Value: false},
			pastest.Answer{LinkID: "conservative-therapy", Text: "Therapy failed?", Value: true}),
		pastest.Condition("k", "G83.4", "Cauda equina syndrome", "active", "confirmed"),
	)
	if len(x.Contradictions) != 1 || x.Contradictions[0].Argument != "red_flag" {
		t.Fatalf("contradictions %+v", x.Contradictions)
	}
	req := x.Request("lumbar_mri")
	if len(req.Contradicted) != 1 || req.Contradicted[0] != "red_flag" {
		t.Errorf("contradicted %v", req.Contradicted)
	}
	for _, a := range req.Evidence {
		if a.Argument == "red_flag" {
			t.Fatal("contradicted evidence was asserted")
		}
	}
	if len(req.Evidence) != 1 || req.Evidence[0].Argument != "conservative_therapy_6wk" {
		t.Errorf("evidence %+v", req.Evidence)
	}
}

func TestRequestKeepsTheFirstCitation(t *testing.T) {
	x := extract(t,
		pastest.QR("q1", pastest.Questionnaire, "completed", pastest.Answer{LinkID: "red-flag", Text: "Red flag?", Value: true}),
		pastest.Condition("k", "G83.4", "Cauda equina syndrome", "active", "confirmed"),
	)
	req := x.Request("lumbar_mri")
	if len(req.Evidence) != 1 || !strings.HasSuffix(req.Evidence[0].Reference, "#red-flag") {
		t.Fatalf("evidence %+v", req.Evidence)
	}
	if _, err := decide.Decide(lumbar(t), req); err != nil {
		t.Fatalf("the engine refused an extracted request: %v", err)
	}
}

// timed is a policy whose evidence is found only in procedures and observations, so each test reads one source.
func timed(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.Load(strings.NewReader(`{
	  "id": "timed", "title": "t", "version": "1", "source": "s",
	  "arguments": [
	    {"name": "recent_mri", "kind": "evidence", "text": "MRI in 180 days", "clause": "c",
	     "find": [{"procedure": {"system": "http://www.ama-assn.org/go/cpt", "codes": ["72148"], "withinDays": 180}}]},
	    {"name": "therapy_6wk", "kind": "evidence", "text": "Six weeks of therapy", "clause": "c",
	     "find": [{"procedure": {"system": "http://www.ama-assn.org/go/cpt", "codes": ["97110"], "minDays": 42, "notDoneIsAbsent": true}}]},
	    {"name": "bmi_40", "kind": "evidence", "text": "BMI 40 or more", "clause": "c",
	     "find": [{"observation": {"system": "http://loinc.org", "codes": ["39156-5"], "withinDays": 365, "atLeast": {"value": 40, "unit": "kg/m2"}}}]},
	    {"name": "smoker", "kind": "evidence", "text": "Smokes", "clause": "c",
	     "find": [{"observation": {"system": "http://loinc.org", "codes": ["72166-2"],
	       "values": {"system": "http://snomed.info/sct", "present": ["77176002"], "absent": ["266919005"]}}}]},
	    {"name": "approve", "kind": "decision", "verdict": "approve", "text": "t", "clause": "c"}
	  ],
	  "attacks": []
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func extractWith(t *testing.T, p *policy.Policy, entries ...map[string]any) Extraction {
	t.Helper()
	req, problems := pas.Read(pastest.Bundle([]string{"72148"}, entries...), false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return Extract(p, req)
}

func TestProcedures(t *testing.T) {
	p := timed(t)
	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"recent", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-06-01"), "recent_mri=present"},
		{"edge of the window", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-04-04"), "recent_mri=present"},
		{"too old", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-01-15"), ""},
		{"after the request", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-10-05"), ""},
		{"a month partly outside the window", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-04"), ""},
		{"a month inside it", pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-05"), "recent_mri=present"},
		{"no date", pastest.Procedure("a", "72148", "MRI lumbar", "completed", ""), ""},
		{"not done says nothing of another MRI", pastest.Procedure("a", "72148", "MRI lumbar", "not-done", ""), ""},
		{"in progress", pastest.Procedure("a", "72148", "MRI lumbar", "in-progress", "2026-09-01"), ""},
		{"a long enough course", pastest.Procedure("b", "97110", "Therapeutic exercise", "completed", "2026-07-01/2026-08-15"), "therapy_6wk=present"},
		{"too short a course", pastest.Procedure("b", "97110", "Therapeutic exercise", "completed", "2026-07-01/2026-07-20"), ""},
		{"a course with no period", pastest.Procedure("b", "97110", "Therapeutic exercise", "completed", "2026-07-01"), ""},
		{"therapy not done", pastest.Procedure("b", "97110", "Therapeutic exercise", "not-done", ""), "therapy_6wk=absent"},
	}
	for _, c := range cases {
		if got := summary(extractWith(t, p, c.entry).Findings); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	x := extractWith(t, p, pastest.Procedure("b", "97110", "Therapeutic exercise", "completed", "2026-07-01/2026-08-15"))
	if q := x.Findings[0].Quote; q != "Therapeutic exercise (97110), completed, 2026-07-01 to 2026-08-15" {
		t.Errorf("quote %q", q)
	}
}

func TestObservations(t *testing.T) {
	p := timed(t)
	coded := func(code string) map[string]any {
		return map[string]any{"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": "http://snomed.info/sct", "code": code}}}}
	}
	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"above", pastest.Observation("o", "39156-5", "BMI", "2026-09-01", pastest.Quantity(41.5, "kg/m2")), "bmi_40=present"},
		{"exactly", pastest.Observation("o", "39156-5", "BMI", "2026-09-01", pastest.Quantity(40, "kg/m2")), "bmi_40=present"},
		{"below is measured absence", pastest.Observation("o", "39156-5", "BMI", "2026-09-01", pastest.Quantity(33, "kg/m2")), "bmi_40=absent"},
		{"another unit", pastest.Observation("o", "39156-5", "BMI", "2026-09-01", pastest.Quantity(41, "lb/in2")), ""},
		{"too old", pastest.Observation("o", "39156-5", "BMI", "2025-06-01", pastest.Quantity(41, "kg/m2")), ""},
		{"a comparator", pastest.Observation("o", "39156-5", "BMI", "2026-09-01", map[string]any{
			"valueQuantity": map[string]any{"value": 40.0, "comparator": ">", "code": "kg/m2", "system": "http://unitsofmeasure.org"}}), ""},
		{"coded present", pastest.Observation("s", "72166-2", "Tobacco use", "", coded("77176002")), "smoker=present"},
		{"coded absent", pastest.Observation("s", "72166-2", "Tobacco use", "", coded("266919005")), "smoker=absent"},
		{"coded unknown", pastest.Observation("s", "72166-2", "Tobacco use", "", coded("266927001")), ""},
	}
	for _, c := range cases {
		if got := summary(extractWith(t, p, c.entry).Findings); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	prelim := pastest.Observation("o", "39156-5", "BMI", "2026-09-01", pastest.Quantity(41, "kg/m2"))
	pastest.Map(prelim)["status"] = "preliminary"
	if got := summary(extractWith(t, p, prelim).Findings); got != "" {
		t.Errorf("a preliminary result was read: %s", got)
	}
	x := extractWith(t, p, pastest.Observation("o", "39156-5", "Body mass index", "2026-09-01", pastest.Quantity(41.5, "kg/m2")))
	if q := x.Findings[0].Quote; q != "Body mass index (39156-5): 41.5 kg/m2, 2026-09-01" {
		t.Errorf("quote %q", q)
	}
}

func TestWindowsNeedARequestDate(t *testing.T) {
	b := pastest.Bundle([]string{"72148"}, pastest.Procedure("a", "72148", "MRI lumbar", "completed", "2026-09-01"))
	claim := pastest.Map(pastest.Entries(b)[0])
	delete(claim, "created")
	req, problems := pas.Read(b, false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	x := Extract(timed(t), req)
	if len(x.Findings) != 0 || len(x.Ignored) != 1 || !strings.Contains(x.Ignored[0], "no created date") {
		t.Fatalf("findings %s, ignored %v", summary(x.Findings), x.Ignored)
	}
}

func TestUnreadDocuments(t *testing.T) {
	x := extract(t,
		pastest.Document("d1", "Physical therapy discharge summary"),
		pastest.QR("other", "https://payer.example/Questionnaire/knee", "completed", pastest.Answer{LinkID: "x", Text: "x", Value: true}),
	)
	if len(x.Unread) != 2 || x.Unread[0].What != "a document: Physical therapy discharge summary" ||
		x.Unread[1].What != "answers to another questionnaire: https://payer.example/Questionnaire/knee" {
		t.Fatalf("unread %+v", x.Unread)
	}
}

func TestMedications(t *testing.T) {
	p, err := policy.LoadFile("../../policies/adalimumab_ra.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	fill := func(id, date string) map[string]any { return pastest.Dispense(id, "105586", "completed", date, 30) }
	trial := func(x Extraction) string {
		for _, f := range x.Findings {
			if f.Argument == "methotrexate_trial" {
				return string(f.Finding)
			}
		}
		return ""
	}
	cases := []struct {
		name    string
		entries []map[string]any
		want    string
	}{
		{"three 30-day fills make 90 days", []map[string]any{fill("a", "2026-03-01"), fill("b", "2026-04-01"), fill("c", "2026-05-01")}, "present"},
		{"two are not enough", []map[string]any{fill("a", "2026-03-01"), fill("b", "2026-04-01")}, ""},
		{"a fill over a year ago does not count", []map[string]any{fill("a", "2025-09-01"), fill("b", "2026-04-01"), fill("c", "2026-05-01")}, ""},
		{"a cancelled fill does not count", []map[string]any{fill("a", "2026-03-01"), fill("b", "2026-04-01"),
			pastest.Dispense("c", "105586", "cancelled", "2026-05-01", 30)}, ""},
		{"a fill and a statement add up", []map[string]any{fill("a", "2026-01-01"),
			pastest.Statement("s", "105586", "completed", "2026-02-01", "2026-04-01")}, "present"}, // 30 + 60,
		{"another drug does not count", []map[string]any{pastest.Dispense("a", "999999", "completed", "2026-03-01", 120)}, ""},
		// not-taken is read as absent only where the source says so, and this one does not
		{"not taken", []map[string]any{pastest.Statement("s", "105586", "not-taken", "2026-02-01", "2026-03-31")}, ""},
	}
	for _, c := range cases {
		if got := trial(extractWith(t, p, c.entries...)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	x := extractWith(t, p, fill("a", "2026-03-01"), fill("b", "2026-04-01"), fill("c", "2026-05-01"))
	if f := x.Findings[0]; !strings.HasPrefix(f.Quote, "90 days in all: ") || strings.Count(f.Reference, "MedicationDispense") != 3 {
		t.Errorf("%+v", f)
	}
	// A dispense with no days' supply says nothing of how long, and says so.
	nodays := pastest.Dispense("n", "105586", "completed", "2026-03-01", 0)
	delete(pastest.Map(nodays), "daysSupply")
	if x := extractWith(t, p, nodays); len(x.Findings) != 0 || !strings.Contains(strings.Join(x.Ignored, " "), "how many days") {
		t.Errorf("%+v %v", x.Findings, x.Ignored)
	}
}

func TestDateRules(t *testing.T) {
	p, err := policy.Parse("dates.vaglio", `policy dates
  title "t"
  version 1
  source "s"
  service cpt 64633

evidence chronic_pain
  text "Low back pain for at least 90 days"
  clause "c"
  find condition icd10 M54.50 onset at least 90 days

evidence two_blocks
  text "Two blocks, at least 14 days apart, in the last year"
  clause "c"
  find procedure cpt 64493 within 365 days times 2 apart 14 days

decision approve approves
  text "t"
  clause "c"
`)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := policy.Parse("again", policy.Format(p)); err != nil || policy.Format(again) != policy.Format(p) {
		t.Fatalf("does not round-trip: %v\n%s", err, policy.Format(p))
	}
	pain := func(onset string) map[string]any {
		c := pastest.Condition("c1", "M54.50", "Low back pain", "active", "confirmed")
		if onset != "" {
			c["resource"].(map[string]any)["onsetDateTime"] = onset
		}
		return c
	}
	block := func(id, on string) map[string]any {
		return pastest.Procedure(id, "64493", "Facet block", "completed", on)
	}
	cases := []struct {
		name    string
		entries []map[string]any
		want    string
	}{
		{"long enough", []map[string]any{pain("2026-06-01")}, "chronic_pain=present"},
		{"exactly 90 days", []map[string]any{pain("2026-07-03")}, "chronic_pain=present"},
		{"too recent", []map[string]any{pain("2026-08-01")}, ""},
		{"no onset says nothing", []map[string]any{pain("")}, ""},
		{"a month that may be too recent", []map[string]any{pain("2026-07")}, ""},
		{"refuted is absent whatever its onset", []map[string]any{func() map[string]any {
			c := pastest.Condition("c1", "M54.50", "Low back pain", "active", "refuted")
			return c
		}()}, "chronic_pain=absent"},
		{"one block is not two", []map[string]any{block("b1", "2026-06-01")}, ""},
		{"two, far enough apart", []map[string]any{block("b1", "2026-06-01"), block("b2", "2026-06-15")}, "two_blocks=present"},
		{"two, too close", []map[string]any{block("b1", "2026-06-01"), block("b2", "2026-06-10")}, ""},
		{"three, the middle one too close", []map[string]any{block("b1", "2026-06-01"), block("b2", "2026-06-05"),
			block("b3", "2026-06-20")}, "two_blocks=present"},
		{"one outside the window", []map[string]any{block("b1", "2025-06-01"), block("b2", "2026-06-15")}, ""},
		{"one undated", []map[string]any{block("b1", ""), block("b2", "2026-06-15")}, ""},
	}
	for _, c := range cases {
		if got := summary(extractWith(t, p, c.entries...).Findings); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	x := extractWith(t, p, block("b1", "2026-06-01"), block("b2", "2026-06-15"))
	if f := x.Findings[0]; !strings.Contains(f.Reference, "Procedure/b1") || !strings.Contains(f.Reference, "Procedure/b2") ||
		!strings.HasPrefix(f.Quote, "2 in all: ") {
		t.Errorf("cites %+v", f)
	}
	for _, bad := range []string{"apart 14 days", "times 1", "times 2 lasting 10 days"} {
		src := strings.Replace(policy.Format(p), "within 365 days times 2 apart 14 days", "within 365 days "+bad, 1)
		if _, err := policy.Parse("bad", src); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
