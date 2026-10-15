package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/policy"
)

const draftSrc = `# A test policy.
policy knee
  title "Knee"
  version 0.2.0
  source "s"
  status draft
  questionnaire https://vaglio.example/Questionnaire/knee
  service cpt 27447

evidence imaging
  text "Imaging"
  clause "c"
  find question imaging

criterion no_imaging
  text "No imaging."
  clause "c"
  defeated by imaging

decision approve approves
  text "Approve."
  clause "c"
  defeated by no_imaging

decision deny denies
  text "Deny."
  clause "c"
  defeated by imaging
`

func TestDrafts(t *testing.T) {
	d := Dir{Path: t.TempDir()}
	if list, err := d.Drafts(); err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	// Unfinished work is saved all the same, with where it fails.
	dr, err := d.SaveDraft("knee", []byte("policy knee\n  title"))
	if err != nil || dr.Error == "" || dr.Policy != nil {
		t.Fatalf("an unfinished draft: %+v %v", dr, err)
	}
	if dr, err = d.SaveDraft("knee", []byte(draftSrc)); err != nil || dr.Policy == nil {
		t.Fatalf("%+v %v", dr, err)
	}
	for name, src := range map[string]string{
		"another id":   strings.Replace(draftSrc, "policy knee", "policy hip", 1),
		"not a draft":  strings.Replace(draftSrc, "  status draft\n", "", 1),
		"a typed sign": strings.Replace(draftSrc, "  status draft\n", "  signed \"Dr X, MD\" 2026-10-13\n", 1),
	} {
		if _, err := d.SaveDraft("knee", []byte(src)); err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	for _, id := range []string{"../etc", "Knee", "k", ""} {
		if _, err := d.SaveDraft(id, []byte(draftSrc)); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
	if _, err := d.SaveDraft("knee", make([]byte, MaxSource+1)); err == nil {
		t.Error("an oversized draft was saved")
	}

	live := []*policy.Policy{{ID: "knee", Version: "0.2.0"}}
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", live); err == nil ||
		!strings.Contains(err.Error(), "new version") {
		t.Errorf("the same version was put in force again: %v", err)
	}
	other := []*policy.Policy{{ID: "tka", Version: "1", Services: []policy.Service{{System: policy.Systems["cpt"], Code: "27447"}}}}
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", other); err == nil ||
		!strings.Contains(err.Error(), "same rank") {
		t.Errorf("a service claimed twice: %v", err)
	}
	// The same service at a higher rank is no conflict: the other outranks it where both apply.
	other[0].Authority = policy.NCD
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", other); err != nil {
		t.Fatalf("a plan policy beside an NCD: %v", err)
	}
	d.SaveDraft("knee", []byte(draftSrc))
	p, signed, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "" || p.Signed == nil || p.Signed.By != "Dr A. Example, MD" {
		t.Errorf("signed %+v", p)
	}
	if !strings.HasPrefix(string(signed), "# A test policy.\n") || !strings.Contains(string(signed), "# Signed off by Dr A. Example, MD on 2026-10-13") {
		t.Errorf("the header was lost:\n%s", signed)
	}
	if on, _ := os.ReadFile(filepath.Join(d.Path, "knee.vaglio")); string(on) != string(signed) {
		t.Error("the signed policy was not written where policies in force live")
	}
	if _, ok, _ := d.Draft("knee"); ok {
		t.Error("the draft outlived its sign-off")
	}
}

func TestSignOffRefuses(t *testing.T) {
	for name, src := range map[string]string{
		"a failing check": strings.Replace(draftSrc, "  defeated by imaging\n", "", 1),
		"not a draft":     strings.Replace(draftSrc, "  status draft\n", "", 1),
		"inner comments":  strings.Replace(draftSrc, "evidence imaging", "# note\nevidence imaging", 1),
	} {
		if _, _, err := SignOff("x.vaglio", []byte(src), "Dr A, MD", "2026-10-13"); err == nil {
			t.Errorf("%s was signed", name)
		}
	}
	if _, _, err := SignOff("x.vaglio", []byte(draftSrc), " ", "2026-10-13"); err == nil {
		t.Error("signed by nobody")
	}
	if _, _, err := SignOff("x.vaglio", []byte(draftSrc), "Dr A, MD", "13/10/2026"); err == nil {
		t.Error("a malformed date")
	}
}

// A policy signed before its effective date waits in scheduled/, and Activate puts it in force on the day, replacing
// the version in force.
func TestScheduledPolicy(t *testing.T) {
	d := Dir{Path: t.TempDir()}
	src := strings.Replace(draftSrc, "  status draft\n", "  status draft\n  effective 2027-01-01\n", 1)
	if _, err := d.SaveDraft("knee", []byte(src)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d.LiveFile("knee"), []byte("in force now"), 0o644)
	p, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Effective != "2027-01-01" {
		t.Fatalf("%+v", p)
	}
	if b, _ := os.ReadFile(d.LiveFile("knee")); string(b) != "in force now" {
		t.Fatal("the version in force was replaced before the day")
	}
	sched, err := d.ScheduledPolicies()
	if err != nil || sched["knee"] == nil {
		t.Fatalf("not scheduled: %v %v", sched, err)
	}
	if moved, err := d.Activate("2026-12-31"); err != nil || len(moved) != 0 {
		t.Fatalf("activated early: %v %v", moved, err)
	}
	moved, err := d.Activate("2027-01-01")
	if err != nil || len(moved) != 1 || moved[0].ID != "knee" {
		t.Fatalf("not activated on the day: %v %v", moved, err)
	}
	if b, _ := os.ReadFile(d.LiveFile("knee")); !strings.Contains(string(b), "effective 2027-01-01") {
		t.Fatal("the scheduled version is not the one in force")
	}
	if s, _ := d.ScheduledPolicies(); len(s) != 0 {
		t.Fatal("still scheduled after activation")
	}
	// A policy that ended before it is signed is refused.
	ended := strings.Replace(draftSrc, "  status draft\n", "  status draft\n  ends 2026-01-31\n", 1)
	d.SaveDraft("knee", []byte(ended))
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", nil); err == nil || !strings.Contains(err.Error(), "never decide") {
		t.Fatalf("an ended policy: %v", err)
	}
}
