package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/report"
)

// editorEnv is a server whose policy directory is a copy of the lumbar and bariatric policies, with the editor on.
func editorEnv(t *testing.T) (*env, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"lumbar_mri", "bariatric_surgery"} {
		src, err := os.ReadFile("../../policies/" + f + policy.Ext)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, f+policy.Ext), src, 0o644)
	}
	e := newEnvWith(t, "", func(c *Config) { c.PolicyDir = dir })
	return e, dir
}

// nextLumbar is the lumbar policy as a draft of its next version.
func nextLumbar(t *testing.T, version string) string {
	t.Helper()
	p, err := policy.LoadFile("../../policies/lumbar_mri" + policy.Ext)
	if err != nil {
		t.Fatal(err)
	}
	p.Version, p.Status, p.Signed = version, policy.Draft, nil
	return policy.Format(p)
}

func TestPolicyEditor(t *testing.T) {
	e, dir := editorEnv(t)
	admin, reviewer := e.signedIn(t, "admin"), e.signedIn(t, "reviewer")
	body := func(src string) string { b, _ := json.Marshal(map[string]string{"source": src}); return string(b) }

	// Anyone signed in may check a source; nothing is saved.
	w := do(e.signedIn(t, "viewer"), "POST", "/api/policies/check", body("policy x\n  title"))
	var chk SourceCheck
	json.Unmarshal(w.Body.Bytes(), &chk)
	if w.Code != 200 || chk.Error == "" || chk.Line == 0 {
		t.Errorf("a broken source: %d %+v", w.Code, chk)
	}

	// A reviewer without the author scope may not save drafts; an admin may.
	if w := do(reviewer, "PUT", "/api/drafts/lumbar_mri", body(nextLumbar(t, "0.2.0"))); w.Code != 403 {
		t.Errorf("a reviewer saved a draft: %d", w.Code)
	}
	if w := do(admin, "POST", "/api/policies/lumbar_mri/draft", ""); w.Code != 200 {
		t.Fatalf("draft from live: %d %s", w.Code, w.Body)
	}
	if w := do(admin, "POST", "/api/policies/lumbar_mri/draft", ""); w.Code != 409 {
		t.Errorf("a second draft over the first: %d", w.Code)
	}
	// Started from the version in force, it is not ready: the version must change.
	w = do(admin, "GET", "/api/drafts/lumbar_mri", "")
	var dv DraftView
	json.Unmarshal(w.Body.Bytes(), &dv)
	if dv.Ready || !strings.Contains(dv.NotReady, "new version") || dv.InForce == "" {
		t.Errorf("a draft at the live version: %+v", dv.SourceCheck)
	}
	if w := do(admin, "PUT", "/api/drafts/lumbar_mri", body(nextLumbar(t, "0.2.0"))); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}

	// The reviewer who will sign it reads it, though they may not write it.
	if w := do(reviewer, "GET", "/api/drafts/lumbar_mri", ""); w.Code != 200 {
		t.Errorf("a reviewer could not read the draft they would sign: %d", w.Code)
	}

	// Signing off takes a clinician: the admin is not one, nor is a reviewer until recorded as one.
	if w := do(admin, "POST", "/api/drafts/lumbar_mri/signoff", ""); w.Code != 403 {
		t.Errorf("a non-clinician signed off: %d", w.Code)
	}
	if err := e.users.SetClinician("reviewer", testNPI, "MD, radiology"); err != nil {
		t.Fatal(err)
	}
	w = do(reviewer, "POST", "/api/drafts/lumbar_mri/signoff", "")
	if w.Code != 200 {
		t.Fatalf("sign-off: %d %s", w.Code, w.Body)
	}
	// In force at once, signed by the account, and on disk.
	w = do(reviewer, "GET", "/api/policies/lumbar_mri", "")
	var p policy.Policy
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Version != "0.2.0" || p.Signed == nil || p.Signed.By != "reviewer, MD, radiology" {
		t.Errorf("in force: %s %+v", p.Version, p.Signed)
	}
	if on, err := policy.LoadFile(filepath.Join(dir, "lumbar_mri"+policy.Ext)); err != nil || on.Version != "0.2.0" {
		t.Errorf("on disk: %v %v", on, err)
	}
	// And in the audit log, before it took effect.
	found := false
	for _, r := range e.log.Records(0, 0) {
		if r.Action == ActSigned && strings.Contains(string(r.Data), `"replaces":"0.1.0"`) &&
			strings.Contains(string(r.Data), testNPI) {
			found = true
		}
	}
	if !found {
		t.Error("the sign-off is not in the audit log with the version it replaced and the NPI")
	}

	// A draft claiming a service another policy decides is refused at sign-off.
	bar, _ := policy.LoadFile("../../policies/bariatric_surgery" + policy.Ext)
	steal := nextLumbar(t, "0.3.0")
	steal = strings.Replace(steal, "service cpt 72148", "service cpt "+bar.Services[0].Code, 1)
	do(admin, "PUT", "/api/drafts/lumbar_mri", body(steal))
	if w := do(reviewer, "POST", "/api/drafts/lumbar_mri/signoff", ""); w.Code != 409 {
		t.Errorf("a stolen service was signed: %d %s", w.Code, w.Body)
	}
	if w := do(admin, "DELETE", "/api/drafts/lumbar_mri", ""); w.Code != 204 {
		t.Errorf("remove: %d", w.Code)
	}
}

func TestEditorOff(t *testing.T) {
	e := newEnv(t, "")
	if w := do(e.signedIn(t, "admin"), "GET", "/api/drafts", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "-edit-policies") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestScopedAccess(t *testing.T) {
	e := newEnv(t, "")
	// A viewer with the author scope edits nothing here (the editor is off) but may reach the routes it opens; one with
	// audit reads the log; one with appeals records appeals; policy:ID narrows the cases seen.
	if err := e.users.SetScopes("viewer", []string{auth.ScopeAudit, auth.ScopeAppeals, "policy:bariatric_surgery"}); err != nil {
		t.Fatal(err)
	}
	// A pending lumbar case exists; the narrowed viewer cannot see it, even by id.
	a := submit(t, e, request("", "", "", pends...))
	if a.Decision != "pend" {
		t.Fatalf("%+v", a)
	}
	c := e.cases.List("")[0]
	v := e.signedIn(t, "viewer")
	if w := do(v, "GET", "/api/audit", ""); w.Code != 200 {
		t.Errorf("audit scope: %d", w.Code)
	}
	if w := do(v, "GET", "/api/cases", ""); w.Code != 200 || strings.Contains(w.Body.String(), c.ID) {
		t.Errorf("a case of another policy was listed: %s", w.Body)
	}
	if w := do(v, "GET", "/api/cases/"+c.ID, ""); w.Code != 404 {
		t.Errorf("a case of another policy was shown: %d", w.Code)
	}
	if w := do(v, "POST", "/api/cases/"+c.ID+"/appeal", `{"outcome":"upheld","reason":"r"}`); w.Code != 404 {
		t.Errorf("an appeal on another policy's case: %d", w.Code)
	}
	if w := do(v, "POST", "/api/cases/"+c.ID+"/review", `{"decision":"approve","reason":"r"}`); w.Code != 403 {
		t.Errorf("a viewer reviewed: %d", w.Code)
	}
	// A plain reviewer still sees every policy's cases.
	if w := do(e.signedIn(t, "reviewer"), "GET", "/api/cases/"+c.ID, ""); w.Code != 200 {
		t.Errorf("an unscoped reviewer: %d", w.Code)
	}
}

func TestExtensions(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) { c.ExtensionDays = 10 })
	submit(t, e, request(testNPI, "", "", pends...))
	c := e.cases.List("")[0]
	rev := e.signedIn(t, "reviewer")
	path := "/api/cases/" + c.ID + "/extend"
	for _, bad := range []string{`{"days":11,"reason":"evidence","note":"n"}`, `{"days":5,"reason":"busy","note":"n"}`,
		`{"days":5,"reason":"evidence","note":" "}`, `{"days":0,"reason":"evidence","note":"n"}`} {
		if w := do(rev, "POST", path, bad); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if w := do(e.signedIn(t, "viewer"), "POST", path, `{"days":5,"reason":"evidence","note":"n"}`); w.Code != 403 {
		t.Errorf("a viewer extended: %d", w.Code)
	}
	w := do(rev, "POST", path, `{"days":10,"reason":"evidence","note":"Waiting for the outside MRI report."}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got, _ := e.cases.Get(c.ID)
	if got.Extension == nil || !got.Due().Equal(mustTime(t, got.Created).Add(17*24*time.Hour)) {
		t.Errorf("due %v", got.Due())
	}
	if w := do(rev, "POST", path, `{"days":1,"reason":"requested","note":"n"}`); w.Code != 400 {
		t.Errorf("extended twice: %d", w.Code)
	}
	do(rev, "POST", "/api/cases/"+c.ID+"/review", `{"decision":"approve","reason":"The report arrived and shows a red flag."}`)
	var m report.Metrics
	json.Unmarshal(do(rev, "GET", "/api/reports/metrics", "").Body.Bytes(), &m)
	if m.Standard.Extended != 1 || m.Standard.ApprovedAfterExtension != 1 || *m.Standard.ApprovedAfterExtensionPercent != 100 {
		t.Errorf("%+v", m.Standard)
	}
	if w := do(rev, "POST", path, `{"days":1,"reason":"requested","note":"n"}`); w.Code != 409 {
		t.Errorf("a reviewed case extended: %d", w.Code)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAppealsFeed(t *testing.T) {
	e := newEnvWith(t, "", func(c *Config) { c.AppealsToken = "feed-token" })
	submit(t, e, request(testNPI, "", "", denies...))
	c := e.cases.List("")[0]
	feed := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/feeds/appeals", strings.NewReader(body))
		r.Host = "127.0.0.1:8740"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		return w
	}
	one := fmt.Sprintf(`[{"claim":%q,"item":"1","reference":"APL-1","outcome":"overturned","reason":"New imaging.","decided":"2026-10-20","by":"Dr Q, MD"}]`, c.Claim)
	if w := feed("", one); w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	if w := feed("wrong", one); w.Code != 401 {
		t.Errorf("a wrong token: %d", w.Code)
	}
	var res []AppealResult
	json.Unmarshal(feed("feed-token", one).Body.Bytes(), &res)
	if len(res) != 1 || res[0].Status != "recorded" || res[0].Case != c.ID {
		t.Fatalf("%+v", res)
	}
	got, _ := e.cases.Get(c.ID)
	if got.Appeal == nil || got.Appeal.Outcome != "overturned" || !strings.Contains(got.Appeal.Reason, "Dr Q, MD") {
		t.Errorf("%+v", got.Appeal)
	}
	// Sent again: recognised, not recorded twice. A different outcome under another reference is refused.
	json.Unmarshal(feed("feed-token", one).Body.Bytes(), &res)
	if res[0].Status != "already-recorded" {
		t.Errorf("a second delivery: %+v", res)
	}
	json.Unmarshal(feed("feed-token", fmt.Sprintf(`[{"claim":%q,"reference":"APL-2","outcome":"upheld","reason":"r"},
		{"claim":"nope|1","reference":"APL-3","outcome":"upheld","reason":"r"},{"case":%q,"outcome":"upheld","reason":"r"}]`,
		c.Claim, c.ID)).Body.Bytes(), &res)
	if len(res) != 3 || res[0].Status != "refused" || res[1].Status != "refused" || res[2].Status != "refused" {
		t.Errorf("%+v", res)
	}
	if w := feed("feed-token", `{"not":"an array"}`); w.Code != 400 {
		t.Errorf("a malformed delivery: %d", w.Code)
	}
	off := newEnv(t, "")
	r := httptest.NewRequest("POST", "/api/feeds/appeals", strings.NewReader(one))
	r.Host = "127.0.0.1:8740"
	w := httptest.NewRecorder()
	off.h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("the feed off: %d", w.Code)
	}
}
