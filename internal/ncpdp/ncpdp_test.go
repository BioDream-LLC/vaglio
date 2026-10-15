package ncpdp

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestReadAndAnswer(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ncpdp/painitiation.xml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	q := ReadRequest(m)
	if m.Transaction != "PAInitiationRequest" || q.PAReferenceID != "REF-20261015-1" || q.MemberID != "M1" ||
		q.Drug.RxNorm != "327361" || q.PrescriberNPI != "1234567893" || q.Patient.BirthDate != "1971-03-04" ||
		q.Patient.Gender != "female" || len(q.Diagnoses) != 1 || q.Diagnoses[0] != "M069" {
		t.Fatalf("%+v %+v", m, q)
	}
	out := Write(m, Response{Transaction: "PAInitiationResponse", Status: "Open", PAReferenceID: q.PAReferenceID,
		PACaseID: "VG1", QuestionSetID: "p@1", Title: "T & U", Questions: []Question{{ID: "ra-diagnosis", Text: "RA <diagnosed>?"}}},
		"MSG-2", "2026-10-15T14:00:01.0Z")
	back, err := ReadMessage(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if back.To != "EHR-EXAMPLE" || back.ToQual != "EPAINI" || back.Root.Value("Header", "RelatesToMessageID") != "MSG-1001" ||
		back.Body.Value("PACaseID") != "VG1" || back.Body.Path("Response", "ResponseStatus", "Open") == nil ||
		back.Body.Find("QuestionText").Text != "RA <diagnosed>?" || back.Root.Attrs["TransactionVersion"] != "20230115" {
		t.Errorf("%s", out)
	}
	// Answers read back as a PARequest would carry them.
	ans := strings.Replace(string(raw), "<PAInitiationRequest>", "<PARequest><PACaseID>VG1</PACaseID>"+
		"<QuestionSet><Question><QuestionID>ra-diagnosis</QuestionID><QuestionResponse><SelectedChoice><ChoiceID>yes</ChoiceID></SelectedChoice></QuestionResponse></Question>"+
		"<Question><QuestionID>methotrexate-trial</QuestionID><QuestionResponse><FreeTextResponse>N</FreeTextResponse></QuestionResponse></Question>"+
		"<Question><QuestionID>skipped</QuestionID></Question></QuestionSet>", 1)
	ans = strings.Replace(ans, "</PAInitiationRequest>", "</PARequest>", 1)
	m, err = ReadMessage(strings.NewReader(ans))
	if err != nil {
		t.Fatal(err)
	}
	q = ReadRequest(m)
	if q.PACaseID != "VG1" || q.Answers["ra-diagnosis"] != "yes" || q.Answers["methotrexate-trial"] != "no" || len(q.Answers) != 2 {
		t.Errorf("%+v", q)
	}
}

func TestRefuses(t *testing.T) {
	for name, doc := range map[string]string{
		"not xml":     "{}",
		"not message": "<Foo/>",
		"no id":       "<Message><Header/><Body><PARequest/></Body></Message>",
		"two":         "<Message><Header><MessageID>1</MessageID></Header><Body><A/><B/></Body></Message>",
		"doctype":     `<!DOCTYPE x [<!ENTITY a "b">]><Message/>`,
	} {
		if _, err := ReadMessage(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
