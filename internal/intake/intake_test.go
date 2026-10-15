package intake

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/pas"
)

func form() Form {
	return Form{Channel: "fax", Reference: "fax-0042", Priority: "stat",
		Patient:  Patient{Family: "Example", Given: "Pat", BirthDate: "1958-03-14", Gender: "female", State: "WA", MemberID: "M123"},
		Coverage: Coverage{Type: "111"}, Provider: Provider{Name: "Example Spine Clinic", NPI: "1234567893"},
		Service: Service{System: "cpt", Code: "72148"}, Questionnaire: "https://vaglio.example/Questionnaire/lumbar-mri",
		Answers:   map[string]string{"red-flag": "yes", "prior-imaging-6mo": "no", "conservative-therapy": ""},
		Documents: []Document{{ContentType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fax"))}},
		KeyedBy:   "intake.clerk"}
}

func TestBundleReadsAsAPASRequest(t *testing.T) {
	f := form()
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	b, key := f.Bundle(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	if key != Base+"/fax|fax-0042" {
		t.Fatal(key)
	}
	raw, _ := json.Marshal(b)
	var back map[string]any
	json.Unmarshal(raw, &back)
	req, problems := pas.Read(back, false)
	if len(problems) > 0 {
		t.Fatalf("not a PAS request: %v", problems)
	}
	svc, ok := req.Service("1")
	if !ok || len(svc.Codes) == 0 {
		t.Fatal("no item 1")
	}
	if pas.Str(req.Claim["created"]) != "2026-10-15" {
		t.Fatalf("created %v", req.Claim["created"])
	}
	qr := req.Resources("QuestionnaireResponse")
	for i := range qr {
		qr[i] = pas.Map(qr[i]["resource"])
	}
	if len(qr) != 1 || !strings.Contains(pas.Str(pas.Map(qr[0]["author"])["display"]), "intake.clerk") {
		t.Fatalf("answers: %v", qr)
	}
	// "not shown" is an item with no answer: not addressed.
	for _, it := range pas.Slice(qr[0]["item"]) {
		m := pas.Map(it)
		if pas.Str(m["linkId"]) == "conservative-therapy" && m["answer"] != nil {
			t.Fatal("an answer not shown was recorded as one")
		}
	}
	if len(req.Resources("DocumentReference")) != 1 || len(req.Resources("Coverage")) != 1 {
		t.Fatal("document or coverage missing")
	}
}

func TestCheck(t *testing.T) {
	for name, bend := range map[string]func(*Form){
		"channel":   func(f *Form) { f.Channel = "pigeon" },
		"birth":     func(f *Form) { f.Patient.BirthDate = "14/03/1958" },
		"npi":       func(f *Form) { f.Provider.NPI = "1234567890" },
		"answer":    func(f *Form) { f.Answers["red-flag"] = "maybe" },
		"linkId":    func(f *Form) { f.Answers["../x"] = "yes" },
		"document":  func(f *Form) { f.Documents[0].ContentType = "text/html" },
		"base64":    func(f *Form) { f.Documents[0].Data = "!!" },
		"member":    func(f *Form) { f.Patient.MemberID = " " },
		"coverage":  func(f *Form) { f.Coverage.Type = "MA" },
		"no qnaire": func(f *Form) { f.Questionnaire = "" },
	} {
		f := form()
		f.Answers = map[string]string{"red-flag": "yes"}
		bend(&f)
		if f.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInbox(t *testing.T) {
	b := &Inbox{Dir: t.TempDir()}
	now := time.Now()
	f, err := b.Add([]byte("%PDF fax one"), "application/pdf", "+15550100", 2, "Patient: Example", now)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := b.Add([]byte("%PDF fax one"), "application/pdf", "", 0, "", now); again.ID != f.ID {
		t.Fatal("a fax delivered twice is kept twice")
	}
	if _, err := b.Add([]byte("<html>"), "text/html", "", 0, "", now); err == nil {
		t.Fatal("html accepted as a fax")
	}
	g, _ := b.Add([]byte("%PDF fax two"), "application/pdf", "", 1, "", now.Add(time.Minute))
	list, _ := b.List()
	if len(list) != 2 || list[0].ID != g.ID || list[0].Text != "" {
		t.Fatalf("list: %+v", list)
	}
	if got, ok := b.Get(f.ID); !ok || got.Text != "Patient: Example" {
		t.Fatal("get")
	}
	if raw, err := b.Content(f); err != nil || string(raw) != "%PDF fax one" {
		t.Fatal("content")
	}
	if _, err := b.Close(f.ID, "clerk", "", "", now); err == nil {
		t.Fatal("set aside without a reason")
	}
	if _, err := b.Close(f.ID, "clerk", "c-1", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Close(f.ID, "clerk", "c-2", "", now); !errors.Is(err, ErrDone) {
		t.Fatal("keyed in twice")
	}
	list, _ = b.List()
	if list[0].ID != g.ID || list[1].Done == "" {
		t.Fatal("handled faxes go last")
	}
	// A changed file on disk is refused.
	os.WriteFile(b.path(f.ID, ".pdf"), []byte("tampered"), 0o600)
	if _, err := b.Content(f); err == nil {
		t.Fatal("a changed fax was served")
	}
	if _, ok := b.Get("../../etc"); ok {
		t.Fatal("a path as an id")
	}
}
