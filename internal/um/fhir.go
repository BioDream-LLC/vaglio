package um

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// FHIR opens each case as a FHIR R4 Task on the plan's FHIR server and reads the decision from the Task once a reviewer
// completes it. The Task's shape is Vaglio's own convention, not a published implementation guide's: input and output
// are told apart by type.text (or a type.coding code) with the names below, which the plan's system must write.
//
// Out, the Task carries: identifier (CaseSystem, Vaglio's case id), status requested, intent order, priority stat for an
// expedited request, description (why it waits), restriction.period.end (when the decision is due), and inputs
// receipt, link, policy, service, missing (one per item), proposed-denial, and proof (the PDF as an attachment).
//
// In, a reviewer completes it: status completed, owner the reviewer (display, and an identifier in the NPI system), and
// outputs decision (approve or deny), reason, and optionally reviewer-npi and reviewer-credential.
type FHIR struct {
	httpBase
	Base string
}

// CaseSystem is the identifier system Vaglio's case ids are given under.
const CaseSystem = "urn:vaglio:case"

const npiSystem = "http://hl7.org/fhir/sid/us-npi"

type coding struct {
	System string `json:"system,omitempty"`
	Code   string `json:"code,omitempty"`
}

type concept struct {
	Text   string   `json:"text,omitempty"`
	Coding []coding `json:"coding,omitempty"`
}

type param struct {
	Type            concept         `json:"type"`
	ValueString     string          `json:"valueString,omitempty"`
	ValueURL        string          `json:"valueUrl,omitempty"`
	ValueCode       string          `json:"valueCode,omitempty"`
	ValueConcept    *concept        `json:"valueCodeableConcept,omitempty"`
	ValueAttachment *map[string]any `json:"valueAttachment,omitempty"`
}

type identifier struct {
	System string `json:"system,omitempty"`
	Value  string `json:"value,omitempty"`
}

type reference struct {
	Reference  string      `json:"reference,omitempty"`
	Display    string      `json:"display,omitempty"`
	Identifier *identifier `json:"identifier,omitempty"`
}

type task struct {
	ResourceType string       `json:"resourceType"`
	ID           string       `json:"id,omitempty"`
	Identifier   []identifier `json:"identifier,omitempty"`
	Status       string       `json:"status"`
	Intent       string       `json:"intent"`
	Priority     string       `json:"priority,omitempty"`
	Code         *concept     `json:"code,omitempty"`
	Description  string       `json:"description,omitempty"`
	For          *reference   `json:"for,omitempty"`
	Owner        *reference   `json:"owner,omitempty"`
	LastModified string       `json:"lastModified,omitempty"`
	Restriction  *struct {
		Period struct {
			End string `json:"end,omitempty"`
		} `json:"period"`
	} `json:"restriction,omitempty"`
	Input  []param `json:"input,omitempty"`
	Output []param `json:"output,omitempty"`
}

func named(name string) concept { return concept{Text: name} }

// Open posts the Task.
func (f *FHIR) Open(ctx context.Context, p Packet) (Opened, error) {
	t := task{ResourceType: "Task", Identifier: []identifier{{System: CaseSystem, Value: p.Case}}, Status: "requested",
		Intent: "order", Priority: "routine", Code: &concept{Text: "Prior authorization review"}, Description: p.Why}
	if p.Expedited {
		t.Priority = "stat"
	}
	if strings.HasPrefix(p.Patient, "Patient/") {
		t.For = &reference{Reference: p.Patient}
	} else if p.Patient != "" {
		t.For = &reference{Display: p.Patient}
	}
	t.Restriction = &struct {
		Period struct {
			End string `json:"end,omitempty"`
		} `json:"period"`
	}{}
	t.Restriction.Period.End = p.Due
	add := func(name, v string) {
		if v != "" {
			t.Input = append(t.Input, param{Type: named(name), ValueString: v})
		}
	}
	add("receipt", p.Receipt)
	if p.Link != "" {
		t.Input = append(t.Input, param{Type: named("link"), ValueURL: p.Link})
	}
	add("policy", p.Policy+" "+p.PolicyVersion+": "+p.PolicyTitle)
	add("service", p.Service)
	add("reason", p.Reason)
	add("proposed-denial", p.ProposedDenial)
	for _, m := range p.Missing {
		add("missing", m.Text)
	}
	att := map[string]any{"contentType": "application/pdf", "data": p.ProofPDF, "title": "Vaglio case " + p.Case}
	t.Input = append(t.Input, param{Type: named("proof"), ValueAttachment: &att})
	body, _ := json.Marshal(t)
	resp, ans, err := f.do(ctx, http.MethodPost, f.Base+"/Task", "application/fhir+json", body)
	if err != nil {
		return Opened{}, err
	}
	var got task
	_ = json.Unmarshal(ans, &got)
	o := Opened{Ref: got.ID}
	if o.Ref == "" {
		// Location: [base]/Task/[id]/_history/[vid]
		loc := strings.TrimRight(resp.Header.Get("Location"), "/")
		if i := strings.Index(loc, "/_history"); i >= 0 {
			loc = loc[:i]
		}
		if strings.Contains(loc, "Task/") {
			o.Ref = path.Base(loc)
		}
	}
	if o.Ref == "" {
		return Opened{}, errors.New("the FHIR server created the Task but gave no id for it")
	}
	o.URL = f.Base + "/Task/" + url.PathEscape(o.Ref)
	return o, nil
}

// Poll reads the Task.
func (f *FHIR) Poll(ctx context.Context, ref string) (Decision, bool, error) {
	_, ans, err := f.do(ctx, http.MethodGet, f.Base+"/Task/"+url.PathEscape(ref), "", nil)
	if err != nil {
		return Decision{}, false, err
	}
	var t task
	if err := json.Unmarshal(ans, &t); err != nil || t.ResourceType != "Task" {
		return Decision{}, false, errors.New("the FHIR server's answer is not a Task")
	}
	d, err := fromTask(t)
	if err != nil {
		return Decision{}, false, nil // not completed yet, or completed without a decision a person can stand behind
	}
	return d, true, nil
}

// Parse reads a Task, or a Bundle of them (a subscription notification), each completed.
func (f *FHIR) Parse(body []byte) ([]Decision, error) {
	var head struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, err
	}
	var raws []json.RawMessage
	switch head.ResourceType {
	case "Task":
		raws = []json.RawMessage{body}
	case "Bundle":
		for _, e := range head.Entry {
			raws = append(raws, e.Resource)
		}
	default:
		return nil, fmt.Errorf("a Task or a Bundle of Tasks, not %q", head.ResourceType)
	}
	var out []Decision
	for _, r := range raws {
		var t task
		if err := json.Unmarshal(r, &t); err != nil {
			return nil, err
		}
		if t.ResourceType != "Task" {
			continue // a subscription status, say
		}
		d, err := fromTask(t)
		if err != nil {
			return nil, fmt.Errorf("Task %s: %w", t.ID, err)
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("no Task in it")
	}
	return out, nil
}

// fromTask reads a completed Task's decision.
func fromTask(t task) (Decision, error) {
	if t.Status != "completed" {
		return Decision{}, fmt.Errorf("its status is %q, not completed", t.Status)
	}
	d := Decision{Ref: t.ID, Decided: t.LastModified}
	for _, id := range t.Identifier {
		if id.System == CaseSystem {
			d.Case = id.Value
		}
	}
	if t.Owner != nil {
		d.Reviewer = t.Owner.Display
		if t.Owner.Identifier != nil && t.Owner.Identifier.System == npiSystem {
			d.NPI = t.Owner.Identifier.Value
		}
	}
	for _, o := range t.Output {
		v := o.ValueString
		if v == "" {
			v = o.ValueCode
		}
		if v == "" && o.ValueConcept != nil {
			v = o.ValueConcept.Text
			if len(o.ValueConcept.Coding) > 0 {
				v = o.ValueConcept.Coding[0].Code
			}
		}
		switch outputName(o.Type) {
		case "decision":
			d.Decision = strings.ToLower(strings.TrimSpace(v))
		case "reason":
			d.Reason = v
		case "reviewer-npi":
			d.NPI = v
		case "reviewer-credential":
			d.Credential = v
		case "reviewer":
			if d.Reviewer == "" {
				d.Reviewer = v
			}
		}
	}
	switch d.Decision {
	case "approved":
		d.Decision = "approve"
	case "denied":
		d.Decision = "deny"
	}
	if d.Decision != "approve" && d.Decision != "deny" {
		return Decision{}, fmt.Errorf("completed with no decision output (approve or deny), but %q", d.Decision)
	}
	return d, nil
}

func outputName(c concept) string {
	if c.Text != "" {
		return strings.ToLower(c.Text)
	}
	for _, cd := range c.Coding {
		if cd.Code != "" {
			return strings.ToLower(cd.Code)
		}
	}
	return ""
}
