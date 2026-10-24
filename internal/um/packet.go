package um

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/letter"
	"github.com/biodream-llc/vaglio/internal/notice"
)

// Packet is what a case is opened with in the case-review system: who and what the request is, why Vaglio could not
// decide it alone, what the record shows and lacks, the proof, and how to check it. Nothing in it is computed for the
// receiver: it is the case's own record, laid out.
type Packet struct {
	Case          string `json:"case"`              // Vaglio's case id
	Link          string `json:"link,omitempty"`    // the case in Vaglio's console
	Receipt       string `json:"receipt,omitempty"` // checks the decision record against the log
	Opened        string `json:"opened"`
	Due           string `json:"due"` // when the decision is due (CMS-0057-F timeframes, or an extension's)
	Expedited     bool   `json:"expedited"`
	Drug          bool   `json:"drug,omitempty"`
	Policy        string `json:"policy"`
	PolicyVersion string `json:"policyVersion"`
	PolicyTitle   string `json:"policyTitle"`
	Service       string `json:"service"`
	Claim         string `json:"claim"`
	Request       string `json:"request,omitempty"` // the PAS server's id for the request
	Patient       string `json:"patient"`
	Provider      string `json:"provider"`
	ProviderNPI   string `json:"providerNpi,omitempty"`
	Line          string `json:"line,omitempty"`
	PatientState  string `json:"patientState,omitempty"`
	// Reason is why it waits: needs_information, indeterminate, contradictory_record, unsatisfied, conflicted, or
	// proposed_denial (the policy denies it, and only a clinician may).
	Reason string `json:"reason"`
	Why    string `json:"why"` // what the provider was told
	// ProposedDenial is the denial the engine proposes, when only a clinician may make it.
	ProposedDenial string `json:"proposedDenial,omitempty"`
	// ClinicianOnly: only a clinician may decide this case (a proposed denial, or one sent to a physician).
	ClinicianOnly bool       `json:"clinicianOnly"`
	Missing       []Item     `json:"missing,omitempty"`   // what the record does not address
	ToApprove     [][]string `json:"toApprove,omitempty"` // each smallest set of evidence that would approve it
	Findings      []Finding  `json:"findings,omitempty"`  // what the record shows, each quoted from it
	Proof         []string   `json:"proof,omitempty"`     // the proof, one line per step, indented by depth
	ProofPDF      []byte     `json:"proofPdf"`            // all of the above as a PDF (base64 in JSON)
}

// Item is a piece of the policy's evidence, with the policy's words for it.
type Item struct {
	Evidence string `json:"evidence"`
	Text     string `json:"text"`
	Clause   string `json:"clause"`
}

// Finding is what the record says about one piece of evidence.
type Finding struct {
	Evidence  string `json:"evidence"`
	Text      string `json:"text"`
	Found     string `json:"found"` // present or absent
	Reference string `json:"reference"`
	Quote     string `json:"quote"`
}

// BuildPacket lays a pended case out. link is the case's address in the console, "" when there is none to give.
func BuildPacket(c cases.Case, receipt, link string) (Packet, error) {
	if c.Status != cases.Pended {
		return Packet{}, fmt.Errorf("case %s is not waiting for a reviewer", c.ID)
	}
	p := Packet{Case: c.ID, Link: link, Receipt: receipt, Opened: c.Created, Due: c.Due().UTC().Format(time.RFC3339),
		Expedited: c.Expedited(), Drug: c.Drug, Policy: c.Policy, PolicyVersion: c.PolicyVersion, PolicyTitle: c.PolicyTitle,
		Service: c.Service, Claim: c.Claim, Request: c.Request, Patient: c.Patient, Provider: c.Provider,
		ProviderNPI: c.ProviderNPI, Line: c.Line, PatientState: c.PatientState, Why: c.Answer.Why,
		ClinicianOnly: c.Proposed != nil || c.Escalated != nil}
	texts := map[string]string{}
	if res := c.Result; res != nil {
		p.Reason = string(res.Reason)
		for _, m := range res.Missing {
			p.Missing = append(p.Missing, Item{Evidence: m.Argument, Text: m.Text, Clause: m.Clause})
		}
		for _, route := range res.ToApprove {
			var r []string
			for _, ch := range route {
				t := ch.Text
				if ch.ContradictsRecord {
					t += " (the record documents it absent)"
				}
				r = append(r, t)
			}
			p.ToApprove = append(p.ToApprove, r)
		}
		for _, list := range [][]decide.Item{res.Missing, res.Assumptions, res.Relied, res.Contradicted} {
			for _, it := range list {
				texts[it.Argument] = it.Text
			}
		}
		proofLines(res.Proof, 0, &p.Proof, texts)
	}
	if c.Proposed != nil {
		p.Reason, p.ProposedDenial = "proposed_denial", c.Proposed.Why
	}
	for _, f := range c.Evidence.Findings {
		t := texts[f.Argument]
		if t == "" {
			t = f.Argument
		}
		p.Findings = append(p.Findings, Finding{Evidence: f.Argument, Text: t, Found: string(f.Finding),
			Reference: f.Reference, Quote: f.Quote})
	}
	p.ProofPDF = notice.PDF(proofLetter(p), notice.Layout{})
	return p, nil
}

// proofLines writes the proof tree as lines: each accepted step, what was argued against it and what defeated that.
func proofLines(n *decide.Node, depth int, out *[]string, texts map[string]string) {
	if n == nil || depth > 40 {
		return
	}
	if n.Text != "" {
		texts[n.Argument] = n.Text
	}
	pad := strings.Repeat("    ", depth)
	line := pad + n.Kind + " " + n.Argument + ": " + n.Text
	if n.Citation != nil {
		line += fmt.Sprintf(" [%s: \u201c%s\u201d]", n.Citation.Reference, n.Citation.Quote)
	}
	*out = append(*out, line)
	for _, d := range n.Defeats {
		*out = append(*out, pad+"  against it: "+d.Attacker+" ("+d.Text+"), defeated by:")
		proofLines(d.By, depth+1, out, texts)
	}
}

// proofLetter is the packet as a printable document, for a system that keeps attachments rather than fields.
func proofLetter(p Packet) *letter.Letter {
	l := &letter.Letter{Lang: "en", Title: "Vaglio case " + p.Case + ": " + p.PolicyTitle, Date: "Opened " + p.Opened,
		Receipt: p.Receipt}
	why := letter.Section{Heading: "Why it waits for a reviewer", Paragraphs: []string{p.Why}}
	if p.ProposedDenial != "" {
		why.Paragraphs = append(why.Paragraphs, "The policy would deny it, and only a clinician may deny: "+p.ProposedDenial)
	}
	if p.ClinicianOnly {
		why.Paragraphs = append(why.Paragraphs, "Only a clinician may decide this case.")
	}
	l.Sections = append(l.Sections, why)
	req := letter.Section{Heading: "The request", Items: []string{
		"Policy: " + p.Policy + " version " + p.PolicyVersion,
		"Service: " + p.Service,
		"Claim: " + p.Claim,
		"Patient: " + p.Patient,
		"Provider: " + strings.TrimSpace(p.Provider+" "+npi(p.ProviderNPI)),
		"Decision due: " + p.Due + map[bool]string{true: " (expedited)", false: ""}[p.Expedited],
	}}
	l.Sections = append(l.Sections, req)
	if len(p.Findings) > 0 {
		s := letter.Section{Heading: "What the record shows"}
		for _, f := range p.Findings {
			s.Items = append(s.Items, fmt.Sprintf("%s: %s, %s: \u201c%s\u201d", f.Text, f.Found, f.Reference, f.Quote))
		}
		l.Sections = append(l.Sections, s)
	}
	if len(p.Missing) > 0 {
		s := letter.Section{Heading: "What the record does not address"}
		for _, m := range p.Missing {
			s.Items = append(s.Items, m.Text+" (policy: \u201c"+m.Clause+"\u201d)")
		}
		l.Sections = append(l.Sections, s)
	}
	if len(p.ToApprove) > 0 {
		s := letter.Section{Heading: "What would approve it", Paragraphs: []string{"Each line is enough on its own:"}}
		for _, r := range p.ToApprove {
			s.Items = append(s.Items, strings.Join(r, "; and "))
		}
		l.Sections = append(l.Sections, s)
	}
	if len(p.Proof) > 0 {
		l.Sections = append(l.Sections, letter.Section{Heading: "The proof", Items: p.Proof})
	}
	check := letter.Section{Heading: "Checking it"}
	if p.Receipt != "" {
		check.Paragraphs = append(check.Paragraphs, "Receipt "+p.Receipt+": it checks this decision record against "+
			"Vaglio's tamper-evident log.")
	}
	if p.Link != "" {
		check.Paragraphs = append(check.Paragraphs, "The case in Vaglio: "+p.Link)
	}
	check.Paragraphs = append(check.Paragraphs, "Decide the case where you work it; the decision comes back to Vaglio "+
		"and is recorded under your name.")
	l.Sections = append(l.Sections, check)
	return l
}

func npi(n string) string {
	if n == "" {
		return ""
	}
	return "(NPI " + n + ")"
}
