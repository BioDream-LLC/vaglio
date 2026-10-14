package evidence

import (
	"testing"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/pastest"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// decideLibrary runs a library policy over a synthetic request, as the server would: evidence from the request, then the
// decision. The library policies are drafts, so this is the only way they decide anything until they are signed off.
func decideLibrary(t *testing.T, file, code string, entries ...map[string]any) *decide.Result {
	t.Helper()
	p, err := policy.LoadFile("../../library/" + file)
	if err != nil {
		t.Fatal(err)
	}
	req, problems := pas.Read(pastest.Bundle([]string{code}, entries...), false)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	res, err := decide.Decide(p, Extract(p, req).Request(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func yes(link string) pastest.Answer { return pastest.Answer{LinkID: link, Text: link, Value: true} }
func no(link string) pastest.Answer  { return pastest.Answer{LinkID: link, Text: link, Value: false} }

func TestLibraryFacetCounts(t *testing.T) {
	const q = "https://vaglio.example/Questionnaire/facet-rfa"
	block := func(id, on string) map[string]any {
		return pastest.Procedure(id, "64493", "Facet block", "completed", on)
	}
	rfa := func(id, on string) map[string]any {
		return pastest.Procedure(id, "64635", "Facet RFA", "completed", on)
	}
	answers := []pastest.Answer{yes("axial-pain"), yes("conservative-failed"), no("untreated-radiculopathy"),
		no("non-facet-pathology"), yes("blocks-80-percent"), no("two-rfa-this-year")}

	// The two blocks are in the record, not answered: counted, they meet the criterion with the answered relief.
	res := decideLibrary(t, "facet_rfa.vaglio", "64635", pastest.QR("q", q, "completed", answers...),
		block("b1", "2026-05-01"), block("b2", "2026-05-20"))
	if res.Outcome != decide.Approved {
		t.Errorf("two blocks in the record: %s %s %+v", res.Outcome, res.Reason, res.Missing)
	}
	// One block is not two, and says nothing either way: it pends for the second.
	res = decideLibrary(t, "facet_rfa.vaglio", "64635", pastest.QR("q", q, "completed", answers...), block("b1", "2026-05-01"))
	if res.Outcome != decide.Pended {
		t.Errorf("one block: %s", res.Outcome)
	}
	// Two RFA sessions already this year, from the record, deny a third whatever else holds - and the provider's "no" to
	// the question beside them is a contradiction for a person, not a decision for the engine.
	res = decideLibrary(t, "facet_rfa.vaglio", "64635", pastest.QR("q", q, "completed", answers...),
		block("b1", "2026-05-01"), block("b2", "2026-05-20"), rfa("r1", "2026-01-10"), rfa("r2", "2026-07-01"))
	if res.Outcome == decide.Approved {
		t.Errorf("two RFAs this year were approved over")
	}
	withoutQ := []pastest.Answer{yes("axial-pain"), yes("conservative-failed"), no("untreated-radiculopathy"),
		no("non-facet-pathology"), yes("blocks-80-percent")}
	res = decideLibrary(t, "facet_rfa.vaglio", "64635", pastest.QR("q", q, "completed", withoutQ...),
		block("b1", "2026-05-01"), block("b2", "2026-05-20"), rfa("r1", "2026-01-10"), rfa("r2", "2026-07-01"))
	if res.Outcome != decide.Denied || res.Decision != "deny_excluded" {
		t.Errorf("two RFAs this year: %s %s", res.Outcome, res.Decision)
	}
}

func TestLibraryStepTherapyRecord(t *testing.T) {
	const q = "https://vaglio.example/Questionnaire/adalimumab-ra-ut"
	base := []pastest.Answer{yes("specialist"), yes("fda-use"), yes("no-concurrent-biologic"), yes("adult"), yes("active-ra")}
	mtx := func(id, on string) map[string]any { return pastest.Dispense(id, "283510", "completed", on, 30) }
	fills := []map[string]any{mtx("d1", "2026-03-01"), mtx("d2", "2026-04-01"), mtx("d3", "2026-05-01")}

	// Ninety days of methotrexate in the record, and the provider says it failed: approved.
	res := decideLibrary(t, "adalimumab_ra_ut.vaglio", "J0135", append([]map[string]any{
		pastest.QR("q", q, "completed", append(base, yes("dmard-failed"))...)}, fills...)...)
	if res.Outcome != decide.Approved {
		t.Errorf("a failed 90-day trial: %s %s %+v", res.Outcome, res.Reason, res.Missing)
	}
	// The same fills with nothing said about failure: a trial is not a failure, so it is not approved on them.
	res = decideLibrary(t, "adalimumab_ra_ut.vaglio", "J0135", append([]map[string]any{
		pastest.QR("q", q, "completed", base...)}, fills...)...)
	if res.Outcome == decide.Approved {
		t.Errorf("approved on a trial nobody said failed")
	}
	// Sixty days of methotrexate and thirty of sulfasalazine are not three months of one DMARD.
	mixed := []map[string]any{mtx("d1", "2026-03-01"), mtx("d2", "2026-04-01"),
		pastest.Dispense("s1", "198232", "completed", "2026-05-01", 30)}
	res = decideLibrary(t, "adalimumab_ra_ut.vaglio", "J0135", append([]map[string]any{
		pastest.QR("q", q, "completed", append(base, yes("dmard-failed"))...)}, mixed...)...)
	if res.Outcome == decide.Approved {
		t.Errorf("days of two drugs were added together")
	}
}

func TestLibraryLumbarPriorMRI(t *testing.T) {
	const q = "https://vaglio.example/Questionnaire/lumbar-mri-wa"
	answers := []pastest.Answer{yes("conservative-6wk"), yes("radiculopathy"), yes("chronic"), no("progressive-neuro-signs"),
		no("cauda-equina"), no("infection"), no("cancer"), no("fracture-risk"), no("neuro-worsening"), no("surgical-candidate"),
		no("prior-surgery")}
	// Radiculopathy after six weeks approves - unless the pain is chronic and the record holds an earlier lumbar MRI, when
	// only a reason to image again does.
	res := decideLibrary(t, "lumbar_mri_wa.vaglio", "72148", pastest.QR("q", q, "completed", append(answers, no("prior-mri"))...))
	if res.Outcome != decide.Approved {
		t.Errorf("no prior MRI: %s %+v", res.Outcome, res.Missing)
	}
	res = decideLibrary(t, "lumbar_mri_wa.vaglio", "72148", pastest.QR("q", q, "completed", answers...),
		pastest.Procedure("m1", "72148", "MRI lumbar", "completed", "2025-03-01"))
	if res.Outcome == decide.Approved {
		t.Errorf("approved again over a prior MRI with no reason to repeat it")
	}
}
