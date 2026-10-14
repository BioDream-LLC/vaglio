// Package reader reads the documents attached to a request with a language model, as proposals a reviewer confirms.
//
// Most of what decides a prior authorisation request is in notes: a consult letter, a therapy summary, an imaging report.
// No rule can read prose, so the engine lists such documents for a person. This package asks a model to find, in one
// document's text, the evidence the policy declares, each finding with the words it rests on. Its answer is never
// evidence by itself:
//
//   - A finding whose quote is not in the document, word for word, is refused. A model that paraphrases or invents is
//     caught here, not by the reviewer's reading.
//   - What passes is a proposal. Until a reviewer confirms it against the document, it is not evidence of anything and
//     changes no decision; confirmed, it is evidence like any other, cited to the document and the quoted words.
//   - A proposal can document evidence absent as well as present, and the reviewer confirms either the same way: the rule
//     that a denial needs evidence of absence is kept by the person, who confirms the absence, not by the model.
//
// The document is patient data, so the model is one the deployment trusts with it: the server takes its URL only from
// the command line, and refuses one that is not on this machine unless told the provider is covered by an agreement.
package reader

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// MaxText bounds a document's text: past it, a document is read only to here, and the reviewer is told.
const MaxText = 60000

// Document is the text of one attachment in a request.
type Document struct {
	Reference string `json:"reference"`
	What      string `json:"what"`
	Text      string `json:"-"`
	Truncated bool   `json:"truncated,omitempty"`
	// ContentType is the document's own, and ExtractedBy the program that made its text, when it was not text: the
	// reviewer confirms such a document's quotes against the original, not the extraction.
	ContentType string `json:"contentType,omitempty"`
	ExtractedBy string `json:"extractedBy,omitempty"`
	Raw         []byte `json:"-"`
}

// Documents returns the readable text of every document in a request: DocumentReferences and Binaries in the Bundle,
// and the Claim's supportingInfo attachments, whose content is inline and plain text, HTML or XML (C-CDA). A PDF or an
// image is not readable here, and is listed in skipped with why.
func Documents(req *pas.Request) (docs []Document, skipped []string) {
	return DocumentsWith(context.Background(), req, nil, false)
}

// DocumentsWith is Documents with extractors for documents that are not text: a PDF or an image one of them reads is
// returned too, marked with the extractor, the rest skipped as before. Extraction is slow (OCR can take a minute a
// page), so it runs only when run is set - when a document is read, not each time the list is shown.
func DocumentsWith(ctx context.Context, req *pas.Request, xs Extractors, run bool) (docs []Document, skipped []string) {
	add := func(ref, what string, att map[string]any) {
		ct := strings.ToLower(strings.TrimSpace(strings.Split(pas.Str(att["contentType"]), ";")[0]))
		data := pas.Str(att["data"])
		if data == "" {
			if u := pas.Str(att["url"]); u != "" {
				if e := req.Resolve(u); e != nil && pas.Str(pas.Map(e["resource"])["resourceType"]) == "Binary" {
					b := pas.Map(e["resource"])
					data, ct = pas.Str(b["data"]), strings.ToLower(pas.Str(b["contentType"]))
				}
			}
		}
		if data == "" {
			skipped = append(skipped, ref+": no inline content to read")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			skipped = append(skipped, ref+": content is not base64")
			return
		}
		var text string
		switch {
		case ct == "text/plain" || ct == "text/markdown" || ct == "":
			text = string(raw)
		case ct == "text/html" || ct == "application/xhtml+xml":
			text = untag(string(raw))
		case strings.HasSuffix(ct, "/xml") || strings.HasSuffix(ct, "+xml"):
			text = untag(string(raw))
		default:
			x := xs.For(ct)
			if x == nil {
				skipped = append(skipped, fmt.Sprintf("%s: %s is not text this reader can read", ref, ct))
				return
			}
			d := Document{Reference: ref, What: what, ContentType: ct, ExtractedBy: x.Name, Raw: raw}
			if run {
				extracted, err := x.Extract(ctx, raw)
				if err != nil {
					skipped = append(skipped, fmt.Sprintf("%s: %s", ref, err))
					return
				}
				d.Text = extracted
			}
			if len(d.Text) > MaxText {
				d.Text, d.Truncated = d.Text[:MaxText], true
			}
			docs = append(docs, d)
			return
		}
		d := Document{Reference: ref, What: what, Text: strings.TrimSpace(text), ContentType: ct, Raw: raw}
		if len(d.Text) > MaxText {
			d.Text, d.Truncated = d.Text[:MaxText], true
		}
		if d.Text != "" {
			docs = append(docs, d)
		}
	}
	for _, e := range req.Entries {
		res := pas.Map(e["resource"])
		ref := pas.Str(e["fullUrl"])
		if ref == "" {
			ref = pas.Str(res["resourceType"]) + "/" + pas.Str(res["id"])
		}
		switch pas.Str(res["resourceType"]) {
		case "DocumentReference":
			what := pas.Str(res["description"])
			if what == "" {
				what = pas.Str(pas.Map(res["type"])["text"])
			}
			for _, c := range pas.Slice(res["content"]) {
				add(ref, what, pas.Map(pas.Map(c)["attachment"]))
				break // the first representation: the others are the same document again
			}
		case "Binary":
			add(ref, "a document", res)
		}
	}
	for i, info := range pas.Slice(req.Claim["supportingInfo"]) {
		if a := pas.Map(pas.Map(info)["valueAttachment"]); len(a) > 0 {
			add(fmt.Sprintf("%s#supportingInfo[%d]", req.ClaimURL, i), pas.Str(a["title"]), a)
		}
	}
	return docs, skipped
}

var (
	tagRe   = regexp.MustCompile(`(?s)<(script|style)\b.*?</(script|style)>|<[^>]*>`)
	spaceRe = regexp.MustCompile(`[ \t]*\n[ \t\n]*`)
)

// untag is markup's text: tags dropped, entities decoded, blank lines collapsed.
func untag(s string) string {
	s = tagRe.ReplaceAllString(s, "\n")
	return spaceRe.ReplaceAllString(html.UnescapeString(s), "\n")
}

// Proposal is one finding a model proposes in a document.
type Proposal struct {
	Argument string         `json:"argument"`
	Text     string         `json:"text"` // the evidence, as the policy words it
	Finding  decide.Finding `json:"finding"`
	Document string         `json:"document"` // the reference of the document it is in
	Quote    string         `json:"quote"`    // the document's words, verbatim
}

// Reading is what a model proposed in one document, and what it said that was refused.
type Reading struct {
	Document  string     `json:"document"`
	Model     string     `json:"model"`
	Proposals []Proposal `json:"proposals"`
	Refused   []string   `json:"refused"`
	Truncated bool       `json:"truncated,omitempty"`
}

const system = `You read one clinical document for a prior authorization reviewer. You are given the evidence a coverage
policy looks for, each with a name and what it means, and the document's text.

For each piece of evidence the document clearly speaks to, give one finding:
- "present" when the document states it is so;
- "absent" when the document states it is not so (a normal examination, a test that was negative, a treatment not given).
Say nothing about evidence the document does not speak to. Do not infer, guess or generalise: a finding you cannot back
with the document's own words is not a finding.

Each finding quotes the words in the document it rests on, copied exactly, character for character: one sentence or the
shortest part of one that establishes it. A quote that is not in the document is thrown away.

Answer with JSON only, no prose:
{"findings": [{"evidence": "<name>", "finding": "present" | "absent", "quote": "<exact words>"}]}`

// Read asks the model about one document, and keeps only findings that name declared evidence and quote the document.
func Read(ctx context.Context, m draft.Model, model string, p *policy.Policy, d Document) (*Reading, error) {
	var ev strings.Builder
	for _, a := range p.Arguments {
		if a.Kind == policy.Evidence {
			fmt.Fprintf(&ev, "- %s: %s (policy: %q)\n", a.Name, a.Text, a.Clause)
		}
	}
	user := fmt.Sprintf("Evidence the policy looks for:\n%s\nThe document (%s):\n<<<\n%s\n>>>", ev.String(), d.What, d.Text)
	answer, err := m.Complete(ctx, []draft.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
	if err != nil {
		return nil, err
	}
	r := Gate(p, d, answer)
	r.Model = model
	return r, nil
}

var jsonRe = regexp.MustCompile("(?s)\\{.*\\}")

// Gate keeps a model's findings that name declared evidence, say present or absent, and quote the document word for
// word; it refuses the rest, saying why. It is the whole of what stands between a model's answer and a reviewer.
func Gate(p *policy.Policy, d Document, answer string) *Reading {
	r := &Reading{Document: d.Reference, Proposals: []Proposal{}, Refused: []string{}, Truncated: d.Truncated}
	raw := jsonRe.FindString(answer)
	var out struct {
		Findings []struct {
			Evidence string `json:"evidence"`
			Finding  string `json:"finding"`
			Quote    string `json:"quote"`
		} `json:"findings"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	if err := dec.Decode(&out); err != nil {
		r.Refused = append(r.Refused, "the answer was not the JSON asked for: "+err.Error())
		return r
	}
	text := draft.Normalize(d.Text)
	seen := map[string]bool{}
	for _, f := range out.Findings {
		a, ok := p.Argument(f.Evidence)
		quote := strings.TrimSpace(f.Quote)
		switch {
		case !ok || a.Kind != policy.Evidence:
			r.Refused = append(r.Refused, fmt.Sprintf("%q is not evidence this policy declares", f.Evidence))
		case f.Finding != string(decide.Present) && f.Finding != string(decide.Absent):
			r.Refused = append(r.Refused, fmt.Sprintf("%s: %q is not present or absent", f.Evidence, f.Finding))
		case len(draft.Normalize(quote)) < 8:
			r.Refused = append(r.Refused, fmt.Sprintf("%s: the quote %q is too short to establish anything", f.Evidence, quote))
		case !strings.Contains(text, draft.Normalize(quote)):
			r.Refused = append(r.Refused, fmt.Sprintf("%s: the quote is not in the document word for word: %q", f.Evidence, quote))
		case seen[f.Evidence+"|"+f.Finding]:
			// one finding per evidence and way is enough to confirm; the others say the same
		default:
			seen[f.Evidence+"|"+f.Finding] = true
			r.Proposals = append(r.Proposals, Proposal{Argument: a.Name, Text: a.Text, Finding: decide.Finding(f.Finding),
				Document: d.Reference, Quote: quote})
		}
	}
	return r
}
