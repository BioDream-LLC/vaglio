// Package intake turns a request that arrived by fax, phone, mail or a portal into the Da Vinci PAS request Bundle every
// other request is, so it is decided, recorded, reviewed, reported and lettered exactly the same way.
//
// A person reads what arrived and keys it in: who the patient is, the member's coverage, the provider, the service, and
// the answers to the policy's questions as the request shows them. Each answer is the person's reading, and the Bundle
// says so: the QuestionnaireResponse names who keyed it in and from what channel, so the proof cites it as such. What
// arrived itself (the fax, a scanned letter) goes in as a DocumentReference, which the reviewer and the document reader
// see beside the case. An answer left "not shown" is not addressed, which never supports a denial: a request keyed in
// with gaps pends and asks for them, as any request with gaps does.
package intake

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Channels a request can arrive by.
var Channels = []string{"fax", "phone", "mail", "portal"}

// Form is what a person keys in from one request.
type Form struct {
	Channel  string `json:"channel"`
	Received string `json:"received,omitempty"` // when it arrived (RFC 3339); empty, now
	// Reference is the request's own identifier on paper (a fax's number, the portal's request id), kept as the Claim's
	// identifier so a second copy of the same request is recognized.
	Reference string `json:"reference,omitempty"`
	Priority  string `json:"priority,omitempty"` // "stat" for an expedited request; empty, standard

	Patient  Patient  `json:"patient"`
	Coverage Coverage `json:"coverage"`
	Provider Provider `json:"provider"`
	Service  Service  `json:"service"`
	// Questionnaire is the canonical URL of the policy's questionnaire the answers are to.
	Questionnaire string `json:"questionnaire,omitempty"`
	// Answers are the questions as the request answers them, by linkId: "yes", "no", or "" (not shown).
	Answers map[string]string `json:"answers,omitempty"`
	// Documents are what arrived, kept with the request.
	Documents []Document `json:"documents,omitempty"`
	// KeyedBy is the account that keyed it in (set by the server, never by the form).
	KeyedBy string `json:"-"`
}

// Patient is who the request is for.
type Patient struct {
	Family    string `json:"family"`
	Given     string `json:"given,omitempty"`
	BirthDate string `json:"birthDate"`        // YYYY-MM-DD
	Gender    string `json:"gender,omitempty"` // female, male, other, unknown
	State     string `json:"state,omitempty"`  // two letters
	MemberID  string `json:"memberId"`
}

// Coverage is the member's coverage: the Source of Payment Typology code of its line of business (111 Medicare
// Advantage, 1 Medicare, 2 Medicaid, 5 private insurance), empty when the request does not say.
type Coverage struct {
	Type string `json:"type,omitempty"`
}

// Provider is who asks.
type Provider struct {
	Name string `json:"name"`
	NPI  string `json:"npi,omitempty"`
}

// Service is what is asked for: a code system (cpt, hcpcs or a URL) and a code.
type Service struct {
	System string `json:"system"`
	Code   string `json:"code"`
}

// Document is something that arrived with the request.
type Document struct {
	Description string `json:"description"`
	ContentType string `json:"contentType"`   // application/pdf, image/tiff, image/png, image/jpeg, text/plain
	Data        string `json:"data"`          // base64
	Fax         string `json:"fax,omitempty"` // the inbound fax it came from, when it did
}

// MaxDocument bounds one document, decoded.
const MaxDocument = 8 << 20

var (
	dateRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	stateRe  = regexp.MustCompile(`^[A-Z]{2}$`)
	codeRe   = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,20}$`)
	linkIDRe = regexp.MustCompile(`^[A-Za-z0-9.\-_]{1,64}$`)
	soptRe   = regexp.MustCompile(`^[0-9]{1,5}$`)
)

var contentTypes = []string{"application/pdf", "image/tiff", "image/png", "image/jpeg", "text/plain"}

// AcceptedType is whether a document of this type is kept with a request.
func AcceptedType(t string) bool { return slices.Contains(contentTypes, t) }

// Check says what is wrong with a form, or nil.
func (f *Form) Check() error {
	var bad []string
	if !slices.Contains(Channels, f.Channel) {
		bad = append(bad, "the channel is fax, phone, mail or portal")
	}
	if f.Received != "" {
		if _, err := time.Parse(time.RFC3339, f.Received); err != nil {
			bad = append(bad, "received is a time, such as 2026-10-15T09:30:00Z")
		}
	}
	if f.Priority != "" && f.Priority != "stat" {
		bad = append(bad, `priority is "stat" (expedited) or empty`)
	}
	if strings.TrimSpace(f.Patient.Family) == "" || strings.TrimSpace(f.Patient.MemberID) == "" {
		bad = append(bad, "the patient's family name and member id are needed")
	}
	if !dateRe.MatchString(f.Patient.BirthDate) {
		bad = append(bad, "the patient's birth date is YYYY-MM-DD")
	}
	if g := f.Patient.Gender; g != "" && !slices.Contains([]string{"female", "male", "other", "unknown"}, g) {
		bad = append(bad, "gender is female, male, other or unknown")
	}
	if s := f.Patient.State; s != "" && !stateRe.MatchString(s) {
		bad = append(bad, "the state is two capital letters")
	}
	if t := f.Coverage.Type; t != "" && !soptRe.MatchString(t) {
		bad = append(bad, "the coverage type is a Source of Payment Typology code (digits)")
	}
	if strings.TrimSpace(f.Provider.Name) == "" {
		bad = append(bad, "the provider's name is needed")
	}
	if n := f.Provider.NPI; n != "" && !auth.ValidNPI(n) {
		bad = append(bad, "the NPI is not valid (10 digits with a check digit)")
	}
	if f.Service.System == "" || !codeRe.MatchString(f.Service.Code) {
		bad = append(bad, "the service needs a code system and a code")
	}
	for id, v := range f.Answers {
		if !linkIDRe.MatchString(id) {
			bad = append(bad, fmt.Sprintf("%q is not a question's linkId", id))
		}
		if v != "yes" && v != "no" && v != "" {
			bad = append(bad, fmt.Sprintf("the answer to %s is yes, no or empty (not shown)", id))
		}
	}
	if len(f.Answers) > 0 && f.Questionnaire == "" {
		bad = append(bad, "answers need the questionnaire they answer")
	}
	for i, d := range f.Documents {
		if !slices.Contains(contentTypes, d.ContentType) {
			bad = append(bad, fmt.Sprintf("document %d is %q; PDF, TIFF, PNG, JPEG or text", i+1, d.ContentType))
		}
		raw, err := base64.StdEncoding.DecodeString(d.Data)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("document %d is not base64", i+1))
		case len(raw) == 0 || len(raw) > MaxDocument:
			bad = append(bad, fmt.Sprintf("document %d is empty or over %d MB", i+1, MaxDocument>>20))
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

// Base is the FHIR base the resources of a keyed-in request are named under. They live in no FHIR server: .invalid is
// reserved, so the name can never reach one.
const Base = "https://intake.invalid/fhir"

// Bundle is the form as a PAS request Bundle, and the Claim's identifier.
func (f *Form) Bundle(now time.Time) (map[string]any, string) {
	id := newID()
	received := f.Received
	if received == "" {
		received = now.UTC().Format(time.RFC3339)
	}
	ref := f.Reference
	if ref == "" {
		ref = id
	}
	system := f.Service.System
	if url, ok := policy.Systems[system]; ok {
		system = url
	}
	url := func(kind string) string { return Base + "/" + kind + "/" + id }
	entry := func(kind string, res map[string]any) map[string]any {
		res["resourceType"], res["id"] = kind, id
		return map[string]any{"fullUrl": url(kind), "resource": res}
	}
	claim := map[string]any{
		"status": "active", "use": "preauthorization", "created": received[:10],
		"identifier": []any{map[string]any{"system": Base + "/" + f.Channel, "value": ref}},
		"type": map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/claim-type", "code": "professional"}}},
		"patient":  map[string]any{"reference": "Patient/" + id},
		"insurer":  map[string]any{"reference": "Organization/plan-" + id},
		"provider": map[string]any{"reference": "Organization/" + id},
		"insurance": []any{map[string]any{"sequence": 1, "focal": true,
			"coverage": map[string]any{"reference": "Coverage/" + id}}},
		"item": []any{map[string]any{"sequence": 1, "productOrService": map[string]any{
			"coding": []any{map[string]any{"system": system, "code": f.Service.Code}}}}},
		"extension": []any{map[string]any{"url": Base + "/channel", "valueCode": f.Channel}},
	}
	if f.Priority != "" {
		claim["priority"] = map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/processpriority", "code": f.Priority}}}
	}
	patient := map[string]any{
		"name":       []any{map[string]any{"family": f.Patient.Family, "given": nonEmpty(f.Patient.Given)}},
		"birthDate":  f.Patient.BirthDate,
		"identifier": []any{map[string]any{"system": Base + "/member", "value": f.Patient.MemberID}},
	}
	if f.Patient.Gender != "" {
		patient["gender"] = f.Patient.Gender
	}
	if f.Patient.State != "" {
		patient["address"] = []any{map[string]any{"use": "home", "state": f.Patient.State}}
	}
	provider := map[string]any{"name": f.Provider.Name}
	if f.Provider.NPI != "" {
		provider["identifier"] = []any{map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": f.Provider.NPI}}
	}
	coverage := map[string]any{"status": "active", "beneficiary": map[string]any{"reference": "Patient/" + id},
		"payor": []any{map[string]any{"reference": "Organization/plan-" + id}}}
	if f.Coverage.Type != "" {
		coverage["type"] = map[string]any{"coding": []any{map[string]any{"system": "https://nahdo.org/sopt", "code": f.Coverage.Type}}}
	}
	entries := []any{
		entry("Claim", claim),
		entry("Patient", patient),
		map[string]any{"fullUrl": Base + "/Organization/plan-" + id, "resource": map[string]any{
			"resourceType": "Organization", "id": "plan-" + id, "name": "The health plan"}},
		entry("Organization", provider),
		entry("Coverage", coverage),
	}
	if len(f.Answers) > 0 {
		var items []any
		ids := make([]string, 0, len(f.Answers))
		for k := range f.Answers {
			ids = append(ids, k)
		}
		slices.Sort(ids)
		for _, k := range ids {
			it := map[string]any{"linkId": k}
			switch f.Answers[k] {
			case "yes":
				it["answer"] = []any{map[string]any{"valueBoolean": true}}
			case "no":
				it["answer"] = []any{map[string]any{"valueBoolean": false}}
			}
			items = append(items, it)
		}
		entries = append(entries, entry("QuestionnaireResponse", map[string]any{
			"questionnaire": f.Questionnaire, "status": "completed", "authored": received,
			"subject": map[string]any{"reference": "Patient/" + id},
			// Who read the paper and keyed the answers: the proof cites them as the person's reading of what arrived.
			"author": map[string]any{"display": fmt.Sprintf("keyed in by %s from a request received by %s", f.KeyedBy, f.Channel)},
			"item":   items,
		}))
	}
	for i, d := range f.Documents {
		desc := d.Description
		if desc == "" {
			desc = "The request as received by " + f.Channel
		}
		did := fmt.Sprintf("%s-%d", id, i+1)
		entries = append(entries, map[string]any{"fullUrl": Base + "/DocumentReference/" + did, "resource": map[string]any{
			"resourceType": "DocumentReference", "id": did, "status": "current", "description": desc,
			"subject": map[string]any{"reference": "Patient/" + id},
			"content": []any{map[string]any{"attachment": map[string]any{"contentType": d.ContentType, "data": d.Data,
				"title": desc}}}}})
	}
	return map[string]any{"resourceType": "Bundle", "type": "collection", "timestamp": received, "entry": entries},
		Base + "/" + f.Channel + "|" + ref
}

func nonEmpty(s string) []any {
	if strings.TrimSpace(s) == "" {
		return []any{}
	}
	return []any{s}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
