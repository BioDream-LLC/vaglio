package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/vaglio/internal/receipt"
)

func TestReceipts(t *testing.T) {
	e := newEnv(t, "")
	a := submit(t, e, request("", "", "", pends...))
	if !strings.HasPrefix(a.Receipt, "VG-") {
		t.Fatalf("the PAS answer has no receipt: %q", a.Receipt)
	}
	// Anyone may check it, signed in or not, and learn nothing about the patient.
	w := e.raw("GET", "/api/receipts/"+a.Receipt, "", nil, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var c receipt.Check
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.Decision != "pend" || c.By != "engine" || c.Policy != "lumbar_mri" || !c.Chain.Verified {
		t.Errorf("%+v", c)
	}
	if strings.Contains(w.Body.String(), "Patient/") || strings.Contains(w.Body.String(), "QuestionnaireResponse") {
		t.Errorf("a receipt check reveals the record: %s", w.Body)
	}
	// The case lists the receipt, and a reviewer's decision gets one of its own that the first now points to.
	rv := e.signedIn(t, "reviewer")
	if w := do(rv, "POST", "/api/cases/"+a.Case+"/review", `{"decision":"approve","reason":"red flag documented"}`); w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body)
	}
	var d CaseDetail
	json.Unmarshal(do(rv, "GET", "/api/cases/"+a.Case, "").Body.Bytes(), &d)
	if len(d.Receipts) != 2 || d.Receipts[0].Receipt != a.Receipt {
		t.Fatalf("receipts: %+v", d.Receipts)
	}
	json.Unmarshal(e.raw("GET", "/api/receipts/"+strings.ToLower(a.Receipt), "", nil, "").Body.Bytes(), &c)
	if len(c.Later) != 1 || c.Later[0].Receipt != d.Receipts[1].Receipt || c.Later[0].Decision != "approve" {
		t.Errorf("later: %+v", c.Later)
	}
	if w := e.raw("GET", "/api/receipts/VG-0000-0000-0000-0000", "", nil, ""); w.Code != 404 {
		t.Errorf("unknown receipt: %d", w.Code)
	}
	if w := e.raw("GET", "/api/receipts/whatever", "", nil, ""); w.Code != 400 {
		t.Errorf("malformed receipt: %d", w.Code)
	}
	// Checking is limited per address, so the log cannot be swept for receipts.
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		limited = e.raw("GET", "/api/receipts/"+a.Receipt, "", nil, "").Code == 429
	}
	if !limited {
		t.Error("receipt checks are not limited")
	}
}
