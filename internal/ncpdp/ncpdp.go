// Package ncpdp reads and writes the NCPDP SCRIPT electronic prior authorization (ePA) messages a payer answers:
// PAInitiationRequest (answered with a question set) and PARequest (answered with a determination).
//
// Where it comes from: the NCPDP SCRIPT standard itself (2017071 and later) is licensed to NCPDP members; this package
// is written from public descriptions of it, chiefly the Surescripts Electronic Prior Authorization Companion Guide, and
// has not been checked against the standard's XML schema or certified by any intermediary. Known from those public
// pages: the Message/Header/Body envelope with version attributes and no XML namespace; the header's To, From,
// MessageID, RelatesToMessageID and SentTime; PAReferenceID (the prescriber's) and PACaseID (the payer's) echoed on
// every message; BenefitsCoordination, Patient, Prescriber and MedicationPrescribed in the body; and the statuses Open,
// Closed, Approved, Denied, PartiallyDenied and InProcess with reason codes and a PANote. Inferred, and named so in the
// code: the element names inside a question set and its answers (QuestionSet, Question, QuestionID, QuestionText,
// QuestionType/Select/Choice, ChoiceID, SelectedChoice) and of an approval's dates. Reading is lenient where the
// public pages are silent; writing follows one form. Before production, check both against the plan's intermediary.
package ncpdp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Node is one XML element: its local name, attributes, text and children. Namespaces are ignored: SCRIPT uses none.
type Node struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*Node
}

// Parse reads an XML document into its root element.
func Parse(r io.Reader) (*Node, error) {
	dec := xml.NewDecoder(io.LimitReader(r, 8<<20))
	dec.Strict = true
	var stack []*Node
	var root *Node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Name: t.Name.Local, Attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.Attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		case xml.Directive:
			return nil, errors.New("a DOCTYPE or other directive is not accepted")
		}
	}
	if root == nil {
		return nil, errors.New("no XML element")
	}
	return root, nil
}

// Child is the first child with this name, or nil.
func (n *Node) Child(name string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Path follows children by name: Path("Header", "MessageID").
func (n *Node) Path(names ...string) *Node {
	for _, name := range names {
		n = n.Child(name)
	}
	return n
}

// Value is the trimmed text at a path, or "".
func (n *Node) Value(names ...string) string {
	if m := n.Path(names...); m != nil {
		return strings.TrimSpace(m.Text)
	}
	return ""
}

// All is every descendant with this name, depth first.
func (n *Node) All(name string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(m *Node) {
		for _, c := range m.Children {
			if c.Name == name {
				out = append(out, c)
			}
			walk(c)
		}
	}
	if n != nil {
		walk(n)
	}
	return out
}

// Find is the first descendant with this name.
func (n *Node) Find(name string) *Node {
	if all := n.All(name); len(all) > 0 {
		return all[0]
	}
	return nil
}

// Message is a parsed SCRIPT message: its header and its one transaction.
type Message struct {
	Root        *Node
	To, From    string
	ToQual      string
	FromQual    string
	MessageID   string
	Transaction string // PAInitiationRequest, PARequest, ...
	Body        *Node  // the transaction element
	Version     string // TransactionVersion
}

// ReadMessage parses a SCRIPT message.
func ReadMessage(r io.Reader) (*Message, error) {
	root, err := Parse(r)
	if err != nil {
		return nil, err
	}
	if root.Name != "Message" {
		return nil, fmt.Errorf("the root element is %s, not Message", root.Name)
	}
	m := &Message{Root: root, To: root.Value("Header", "To"), From: root.Value("Header", "From"),
		MessageID: root.Value("Header", "MessageID"), Version: root.Attrs["TransactionVersion"]}
	if t := root.Path("Header", "To"); t != nil {
		m.ToQual = t.Attrs["Qualifier"]
	}
	if f := root.Path("Header", "From"); f != nil {
		m.FromQual = f.Attrs["Qualifier"]
	}
	body := root.Child("Body")
	if body == nil || len(body.Children) != 1 {
		return nil, errors.New("the Body holds one transaction")
	}
	m.Body, m.Transaction = body.Children[0], body.Children[0].Name
	if m.MessageID == "" {
		return nil, errors.New("the Header has no MessageID")
	}
	return m, nil
}

// Drug is a prescribed drug's codes.
type Drug struct {
	Description string
	RxNorm      string
	NDC         string
}

// Request is what a payer reads from an initiation or a PA request.
type Request struct {
	PAReferenceID string
	PACaseID      string
	MemberID      string // PBMMemberID, else CardholderID
	Patient       struct{ Family, Given, BirthDate, Gender string }
	PrescriberNPI string
	Prescriber    string
	Drug          Drug
	Diagnoses     []string // ICD-10-CM codes, without dots as SCRIPT sends them or with
	// Answers are the question set's answers by question id, "yes" or "no"; a question answered otherwise is left out.
	Answers map[string]string
}

// ReadRequest reads the parts of a PAInitiationRequest or PARequest a decision needs.
func ReadRequest(m *Message) Request {
	b := m.Body
	var q Request
	q.PAReferenceID, q.PACaseID = b.Value("PAReferenceID"), b.Value("PACaseID")
	bc := b.Child("BenefitsCoordination")
	if q.MemberID = bc.Value("PBMMemberID"); q.MemberID == "" {
		q.MemberID = bc.Value("CardholderID")
	}
	p := b.Path("Patient", "HumanPatient")
	if p == nil {
		p = b.Child("Patient")
	}
	q.Patient.Family, q.Patient.Given = p.Value("Names", "Name", "LastName"), p.Value("Names", "Name", "FirstName")
	if q.Patient.Family == "" {
		q.Patient.Family, q.Patient.Given = p.Value("Name", "LastName"), p.Value("Name", "FirstName")
	}
	if q.Patient.BirthDate = p.Value("DateOfBirth", "Date"); q.Patient.BirthDate == "" {
		q.Patient.BirthDate = p.Value("DateOfBirth")
	}
	gender := p.Value("GenderAndSex", "AdministrativeGender")
	if gender == "" {
		gender = p.Value("Gender")
	}
	q.Patient.Gender = map[string]string{"M": "male", "F": "female", "U": "unknown"}[strings.ToUpper(gender)]
	pr := b.Path("Prescriber", "NonVeterinarian")
	if pr == nil {
		pr = b.Child("Prescriber")
	}
	q.PrescriberNPI = pr.Value("Identification", "NPI")
	q.Prescriber = strings.TrimSpace(pr.Value("Names", "Name", "FirstName") + " " + pr.Value("Names", "Name", "LastName"))
	if q.Prescriber == "" {
		q.Prescriber = strings.TrimSpace(pr.Value("Name", "FirstName") + " " + pr.Value("Name", "LastName"))
	}
	med := b.Child("MedicationPrescribed")
	q.Drug.Description = med.Value("DrugDescription")
	dc := med.Path("Product", "DrugCoded")
	if dc == nil {
		dc = med.Child("DrugCoded")
	}
	q.Drug.NDC = dc.Value("NDC")
	if pc := dc.Child("ProductCode"); pc != nil {
		if qual := strings.ToUpper(pc.Value("Qualifier")); qual == "ND" || qual == "" {
			q.Drug.NDC = pc.Value("Code")
		}
	}
	// RxNorm: DrugDBCode with an RxNorm qualifier (inferred from 2017071 descriptions), or an RxNorm element.
	if db := dc.Child("DrugDBCode"); db != nil {
		switch strings.ToUpper(db.Value("Qualifier")) {
		case "SCD", "SBD", "GPK", "BPK", "SGP", "SBP", "SCDC", "SBDC", "IN", "PIN", "MIN":
			q.Drug.RxNorm = db.Value("Code")
		}
	}
	if q.Drug.RxNorm == "" {
		q.Drug.RxNorm = dc.Value("RxNorm")
	}
	for _, d := range med.All("Diagnosis") {
		for _, v := range []string{d.Value("Primary", "Code"), d.Value("Secondary", "Code"), d.Value("Value")} {
			if v != "" {
				q.Diagnoses = append(q.Diagnoses, v)
			}
		}
	}
	q.Answers = map[string]string{}
	for _, qn := range b.All("Question") {
		id := qn.Value("QuestionID")
		if id == "" {
			continue
		}
		var got string
		for _, c := range qn.All("ChoiceID") {
			got = strings.TrimSpace(c.Text)
		}
		if got == "" {
			got = qn.Value("QuestionResponse", "FreeTextResponse")
		}
		switch strings.ToLower(got) {
		case "yes", "y", "true":
			q.Answers[id] = "yes"
		case "no", "n", "false":
			q.Answers[id] = "no"
		}
	}
	return q
}

// Question is one question of a question set: a yes-or-no question about the patient's record.
type Question struct {
	ID   string
	Text string
}

// Response is what a payer answers.
type Response struct {
	Transaction   string // PAInitiationResponse or PAResponse
	Status        string // Open, Closed, Approved, Denied, InProcess
	ReasonCode    string // for Closed and Denied
	Note          string // PANote: the reason, in words
	PAReferenceID string
	PACaseID      string
	QuestionSetID string
	Title         string
	Questions     []Question
	// Approved, for an approval: the dates it holds (element names inferred).
	EffectiveDate, ExpirationDate string
}

// Write writes the response to an inbound message: To and From swapped, a new MessageID, RelatesToMessageID set to the
// inbound one, the inbound version attributes kept.
func Write(in *Message, r Response, messageID, sentTime string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString("<Message")
	for _, a := range []string{"DatatypesVersion", "TransportVersion", "TransactionDomain", "TransactionVersion", "StructuresVersion", "ECLVersion"} {
		if v, ok := in.Root.Attrs[a]; ok {
			fmt.Fprintf(&b, ` %s="%s"`, a, esc(v))
		}
	}
	b.WriteString("><Header>")
	fmt.Fprintf(&b, `<To Qualifier="%s">%s</To><From Qualifier="%s">%s</From>`, esc(in.FromQual), esc(in.From), esc(in.ToQual), esc(in.To))
	el(&b, "MessageID", messageID)
	el(&b, "RelatesToMessageID", in.MessageID)
	el(&b, "SentTime", sentTime)
	b.WriteString("</Header><Body><" + r.Transaction + ">")
	b.WriteString("<Response><ResponseStatus><" + r.Status + ">")
	if r.ReasonCode != "" {
		el(&b, "ReasonCode", r.ReasonCode)
	}
	if r.Status == "Approved" && (r.EffectiveDate != "" || r.ExpirationDate != "") {
		b.WriteString("<ApprovedPA>") // inferred
		if r.EffectiveDate != "" {
			b.WriteString("<EffectiveDate><Date>" + esc(r.EffectiveDate) + "</Date></EffectiveDate>")
		}
		if r.ExpirationDate != "" {
			b.WriteString("<ExpirationDate><Date>" + esc(r.ExpirationDate) + "</Date></ExpirationDate>")
		}
		b.WriteString("</ApprovedPA>")
	}
	if r.Note != "" {
		el(&b, "PANote", r.Note)
	}
	b.WriteString("</" + r.Status + "></ResponseStatus></Response>")
	el(&b, "PAReferenceID", r.PAReferenceID)
	if r.PACaseID != "" {
		el(&b, "PACaseID", r.PACaseID)
	}
	if len(r.Questions) > 0 {
		// Inferred element names; see the package comment.
		b.WriteString("<QuestionSet>")
		el(&b, "QuestionSetIdentifier", r.QuestionSetID)
		el(&b, "QuestionSetTitle", r.Title)
		for _, q := range r.Questions {
			b.WriteString("<Question>")
			el(&b, "QuestionID", q.ID)
			el(&b, "QuestionText", q.Text)
			b.WriteString("<QuestionType><Select>")
			for _, c := range []struct{ id, text string }{{"yes", "Yes"}, {"no", "No"}} {
				b.WriteString("<Choice>")
				el(&b, "ChoiceID", c.id)
				el(&b, "ChoiceText", c.text)
				b.WriteString("</Choice>")
			}
			b.WriteString("</Select></QuestionType></Question>")
		}
		b.WriteString("</QuestionSet>")
	}
	b.WriteString("</" + r.Transaction + "></Body></Message>")
	return b.Bytes()
}

// Error is the SCRIPT Error message answering a message that cannot be processed: code 900 (transaction rejected) with
// the reason in words.
func Error(in *Message, code, why, messageID, sentTime string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header + "<Message")
	if in != nil {
		for _, a := range []string{"DatatypesVersion", "TransportVersion", "TransactionDomain", "TransactionVersion", "StructuresVersion", "ECLVersion"} {
			if v, ok := in.Root.Attrs[a]; ok {
				fmt.Fprintf(&b, ` %s="%s"`, a, esc(v))
			}
		}
	}
	b.WriteString("><Header>")
	if in != nil {
		fmt.Fprintf(&b, `<To Qualifier="%s">%s</To><From Qualifier="%s">%s</From>`, esc(in.FromQual), esc(in.From), esc(in.ToQual), esc(in.To))
	}
	el(&b, "MessageID", messageID)
	if in != nil {
		el(&b, "RelatesToMessageID", in.MessageID)
	}
	el(&b, "SentTime", sentTime)
	b.WriteString("</Header><Body><Error>")
	el(&b, "Code", code)
	el(&b, "Description", why)
	b.WriteString("</Error></Body></Message>")
	return b.Bytes()
}

func el(b *bytes.Buffer, name, v string) {
	b.WriteString("<" + name + ">" + esc(v) + "</" + name + ">")
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}
