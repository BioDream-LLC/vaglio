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

	live := map[string]*policy.Policy{"knee": {ID: "knee", Version: "0.2.0"}}
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", live); err == nil ||
		!strings.Contains(err.Error(), "new version") {
		t.Errorf("the same version was put in force again: %v", err)
	}
	other := map[string]*policy.Policy{"tka": {ID: "tka", Version: "1", Services: []policy.Service{{System: policy.Systems["cpt"], Code: "27447"}}}}
	if _, _, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", other); err == nil ||
		!strings.Contains(err.Error(), "one policy") {
		t.Errorf("a service claimed twice: %v", err)
	}
	p, signed, err := d.Promote("knee", "Dr A. Example, MD", "2026-10-13", map[string]*policy.Policy{})
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
