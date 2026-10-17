package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/library"
)

func init() { auth.Iterations = 1000 }

func writePolicies(t *testing.T, files map[string]string) string {
	t.Helper()
	for name := range files {
		if !strings.HasSuffix(name, ".vaglio") && !strings.HasSuffix(name, ".json") {
			t.Fatalf("%s: a policy file is .vaglio or .json", name)
		}
	}
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func example(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../policies/lumbar_mri.vaglio")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLoadDir(t *testing.T) {
	if _, err := loadDir(writePolicies(t, map[string]string{"a.vaglio": example(t)})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirRefusesOneServiceTwice(t *testing.T) {
	second := strings.Replace(example(t), "policy lumbar_mri\n", "policy lumbar_mri_2\n", 1)
	_, err := loadDir(writePolicies(t, map[string]string{"a.vaglio": example(t), "b.vaglio": second}))
	if err == nil || !strings.Contains(err.Error(), "same rank") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadDirRefusesAPolicyThatFailsItsCheck(t *testing.T) {
	// Without this attack the policy approves and denies a red flag with a recent study.
	src := example(t)
	line := "  defeated by no_recent_imaging new_or_worsening_symptoms red_flag\n"
	i := strings.LastIndex(src, line) // deny_duplicate's
	broken := src[:i] + "  defeated by no_recent_imaging new_or_worsening_symptoms\n" + src[i+len(line):]
	if broken == example(t) {
		t.Fatal("the fixture edit did not apply")
	}
	_, err := loadDir(writePolicies(t, map[string]string{"a.vaglio": broken}))
	if err == nil || !strings.Contains(err.Error(), "fails its check") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadDirRefusesADraft(t *testing.T) {
	draft := strings.Replace(example(t), "  version 0.1.0\n", "  version 0.1.0\n  status draft\n", 1)
	if _, err := loadDir(writePolicies(t, map[string]string{"a.vaglio": draft})); err == nil || !strings.Contains(err.Error(), "is a draft") {
		t.Fatalf("err = %v", err)
	}
}

func TestPASURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://pas.example/fhir": true, "http://127.0.0.1:8080/fhir": true, "http://localhost:8080/fhir": true,
		"http://pas.example/fhir": false, "ftp://127.0.0.1/": false, "nonsense": false,
	} {
		if err := checkPASURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestDataDirectoryMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no Unix modes: the directory's ACL is the operator's to set")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	if _, err := openData(dir); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("err = %v", err)
	}
	os.Chmod(dir, 0o700)
	d, err := openData(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.log.Close()
}

func TestUserCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var out strings.Builder
	if err := userCmd([]string{"add", "-data", dir, "-role", "reviewer", "dr.lee"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "user dr.lee (reviewer), password: ") {
		t.Errorf("add said %q", out.String())
	}
	if err := userCmd([]string{"add", "-data", dir, "-role", "reviewer", "dr.lee"}, &out); err == nil {
		t.Error("adding an existing user succeeded")
	}
	if err := userCmd([]string{"add", "-data", dir, "dr.kim"}, &out); err == nil {
		t.Error("adding without a role succeeded")
	}
	if err := userCmd([]string{"reset", "-data", dir, "dr.lee"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	userCmd([]string{"list", "-data", dir}, &out)
	if !strings.Contains(out.String(), "dr.lee") || !strings.Contains(out.String(), "reviewer") {
		t.Errorf("list %q", out.String())
	}
	if err := userCmd([]string{"remove", "-data", dir, "dr.lee"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := auditCmd([]string{"verify", "-data", dir}, &out); err != nil || !strings.Contains(out.String(), "3 records") {
		t.Errorf("verify: %v %q", err, out.String())
	}
}

func TestClinicianCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var out strings.Builder
	userCmd([]string{"add", "-data", dir, "-role", "reviewer", "dr.lee"}, &out)
	if err := userCmd([]string{"clinician", "-data", dir, "-npi", "1234567890", "-credential", "MD", "dr.lee"}, &out); err == nil {
		t.Error("a bad NPI was recorded")
	}
	if err := userCmd([]string{"clinician", "-data", dir, "-npi", "1234567893", "dr.lee"}, &out); err == nil {
		t.Error("an NPI without a credential was recorded")
	}
	if err := userCmd([]string{"clinician", "-data", dir, "-npi", "1234567893", "-credential", "MD, radiology", "dr.lee"}, &out); err != nil {
		t.Fatal(err)
	}
	// A new password keeps the clinician's record.
	userCmd([]string{"reset", "-data", dir, "dr.lee"}, &out)
	out.Reset()
	userCmd([]string{"list", "-data", dir}, &out)
	if !strings.Contains(out.String(), "clinician NPI 1234567893, MD, radiology") {
		t.Errorf("list %q", out.String())
	}
	if err := userCmd([]string{"clinician", "-data", dir, "-clear", "dr.lee"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	userCmd([]string{"list", "-data", dir}, &out)
	if strings.Contains(out.String(), "clinician") {
		t.Errorf("cleared, but %q", out.String())
	}
}

func TestQuestionnaireCommand(t *testing.T) {
	var out strings.Builder
	if err := questionnaireCmd([]string{"../../policies/lumbar_mri.vaglio"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"resourceType": "Questionnaire"`) || !strings.Contains(out.String(), `"linkId": "red-flag"`) {
		t.Errorf("%s", out.String())
	}
	if err := questionnaireCmd([]string{"-put", "http://example.org/fhir", "../../policies/lumbar_mri.vaglio"}, &out); err == nil {
		t.Error("PUT over plain http to another machine")
	}
}

func TestReportAndReplayOnAnEmptyLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(dir, 0o700)
	var out strings.Builder
	if err := reportCmd([]string{"metrics", "-data", dir, "-csv"}, &out); err != nil || !strings.Contains(out.String(), `"Requests",0,0`) {
		t.Errorf("%v %q", err, out.String())
	}
	out.Reset()
	if err := replayCmd([]string{"-data", dir, "../../policies/lumbar_mri.vaglio"}, &out); err != nil ||
		!strings.Contains(out.String(), "0 case(s) replayed") {
		t.Errorf("%v %q", err, out.String())
	}
}

func TestSignoff(t *testing.T) {
	dir := t.TempDir()
	// The library's policy is in force as published; made a draft again, it is one a clinician signs.
	src, _ := os.ReadFile("../../library/cpap_osa.vaglio")
	src = []byte(strings.Replace(string(src), "\n  version ", "\n  status draft\n  version ", 1))
	f := filepath.Join(dir, "cpap_osa.vaglio")
	os.WriteFile(f, src, 0o644)
	now := func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC) }
	var out strings.Builder
	if err := signoffCmd([]string{f}, &out, now); err == nil {
		t.Error("signed with no name")
	}
	if err := signoffCmd([]string{"-by", "Dr A. Example, MD", f}, &out, now); err != nil {
		t.Fatal(err)
	}
	p, err := policy.LoadFile(f)
	if err != nil || p.Status != "" || p.Signed == nil || p.Signed.Date != "2026-10-13" || p.Signed.By != "Dr A. Example, MD" {
		t.Fatalf("%v %+v", err, p)
	}
	signed, _ := os.ReadFile(f)
	if !strings.HasPrefix(string(signed), "# CPAP (E0601)") {
		t.Error("the header comments were dropped")
	}
	if err := signoffCmd([]string{"-by", "Someone", f}, &out, now); err == nil {
		t.Error("signed twice")
	}
	if ps, err := loadDir(dir); err != nil || ps["cpap_osa"] == nil {
		t.Errorf("the signed policy does not load: %v", err)
	}
}

func TestReadAppealsCSV(t *testing.T) {
	got, err := readAppealsCSV(strings.NewReader("\ufeffReference,Claim,Item,Outcome,Reason,Decided\n" +
		"APL-1,http://p.example/claims|7,1,Overturned,\"New imaging, reviewed\",2026-10-20\n"))
	if err != nil || len(got) != 1 || got[0].Outcome != "overturned" || got[0].Reason != "New imaging, reviewed" ||
		got[0].Claim != "http://p.example/claims|7" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, src := range map[string]string{
		"a misspelt column": "reference,outcom,reason\nA,upheld,r\n",
		"no reason column":  "reference,outcome\nA,upheld\n",
		"no rows":           "reference,outcome,reason\n",
	} {
		if _, err := readAppealsCSV(strings.NewReader(src)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The library decides beside a plan's own policies; the plan's policy of a library id overrides the library's, and the
// illustrative policies tie with none of the library's.
func TestLoadWithLibrary(t *testing.T) {
	lib, err := library.Policies()
	if err != nil {
		t.Fatal(err)
	}
	if live, _, err := loadPolicies(t.TempDir(), lib); err != nil || len(live) != len(lib) {
		t.Fatalf("the library alone: %d %v", len(live), err)
	}
	if _, _, err := loadPolicies("../../policies", lib); err != nil {
		t.Fatalf("with the illustrative policies: %v", err)
	}
	dir := t.TempDir()
	src, _ := os.ReadFile("../../library/cpap_osa.vaglio")
	src = []byte(strings.Replace(string(src), "\n  version 0.1.0", "\n  version 0.2.0", 1))
	os.WriteFile(filepath.Join(dir, "cpap_osa.vaglio"), src, 0o644)
	live, _, err := loadPolicies(dir, lib)
	if err != nil || live["cpap_osa"].Version != "0.2.0" || live["esi"] != lib["esi"] {
		t.Fatalf("an override: %v %+v", err, live["cpap_osa"])
	}
}

// A plan's SAML sign-on from a metadata file (Entra ID's, as Perfuse captured it): set up, and refused with OIDC beside
// it or an ACS URL that is not this server's.
func TestOpenSAML(t *testing.T) {
	sp := planSpec{Hostname: "plan.example", SAML: &samlSpec{Metadata: "../../internal/saml/testdata/entra-metadata.xml",
		EntityID: "urn:vaglio:plan", ACSURL: "https://plan.example/api/sso/saml"}}
	sso, err := openSAML(context.Background(), sp, true)
	if err != nil || sso.SAML == nil {
		t.Fatalf("%v", err)
	}
	bad := sp
	bad.SAML = &samlSpec{Metadata: sp.SAML.Metadata, EntityID: "x", ACSURL: "https://other.example/api/sso/saml"}
	if _, err := openSAML(context.Background(), bad, true); err == nil {
		t.Error("an ACS URL on another plan's host")
	}
	bad = sp
	bad.SSO = &ssoSpec{Issuer: "https://idp.example"}
	if _, err := openSAML(context.Background(), bad, false); err == nil {
		t.Error("both OIDC and SAML")
	}
}
