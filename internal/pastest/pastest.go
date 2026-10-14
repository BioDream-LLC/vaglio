// Package pastest builds synthetic Da Vinci PAS request Bundles for tests. Every resource in them is invented.
package pastest

import (
	"encoding/json"
	"strings"
)

// Base is the provider's FHIR base the synthetic resources live under.
const Base = "http://provider.example/fhir"

// Questionnaire is the example policy's questionnaire.
const Questionnaire = "https://vaglio.example/Questionnaire/lumbar-mri"

const (
	CPT    = "http://www.ama-assn.org/go/cpt"
	ICD    = "http://hl7.org/fhir/sid/icd-10-cm"
	LOINC  = "http://loinc.org"
	RxNorm = "http://www.nlm.nih.gov/research/umls/rxnorm"
)

// Created is the date every synthetic request is made: windows of days count back from it.
const Created = "2026-10-01"

// Bundle is a PAS request for the given CPT codes, one item each, followed by the extra entries.
func Bundle(cpt []string, extra ...map[string]any) map[string]any {
	var items []any
	for i, c := range cpt {
		items = append(items, map[string]any{"sequence": float64(i + 1),
			"productOrService": map[string]any{"coding": []any{map[string]any{"system": CPT, "code": c}}}})
	}
	entries := []any{
		map[string]any{"fullUrl": Base + "/Claim/c1", "resource": map[string]any{
			"resourceType": "Claim", "id": "c1", "status": "active", "use": "preauthorization", "created": Created,
			"patient": map[string]any{"reference": "Patient/p1"}, "insurer": map[string]any{"reference": "Organization/plan"},
			"provider": map[string]any{"reference": "Organization/clinic"}, "item": items}},
		map[string]any{"fullUrl": Base + "/Patient/p1", "resource": map[string]any{"resourceType": "Patient", "id": "p1"}},
		map[string]any{"fullUrl": Base + "/Organization/plan", "resource": map[string]any{"resourceType": "Organization", "id": "plan"}},
		map[string]any{"fullUrl": Base + "/Organization/clinic", "resource": map[string]any{"resourceType": "Organization", "id": "clinic"}},
	}
	for _, e := range extra {
		entries = append(entries, e)
	}
	return map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries}
}

// Answer is one questionnaire item: a linkId, its text, and its answer value (a bool, or anything else to test refusal;
// nil leaves it unanswered).
type Answer struct {
	LinkID string
	Text   string
	Value  any
}

// QR is a QuestionnaireResponse entry to the given questionnaire with the given status.
func QR(id, questionnaire, status string, answers ...Answer) map[string]any {
	var items []any
	for _, a := range answers {
		it := map[string]any{"linkId": a.LinkID, "text": a.Text}
		switch v := a.Value.(type) {
		case nil:
		case bool:
			it["answer"] = []any{map[string]any{"valueBoolean": v}}
		case string:
			it["answer"] = []any{map[string]any{"valueString": v}}
		}
		items = append(items, it)
	}
	return map[string]any{"fullUrl": Base + "/QuestionnaireResponse/" + id, "resource": map[string]any{
		"resourceType": "QuestionnaireResponse", "id": id, "questionnaire": questionnaire, "status": status,
		"subject": map[string]any{"reference": "Patient/p1"}, "item": items}}
}

// Condition is a Condition entry coded with an ICD-10-CM code.
func Condition(id, code, display, clinical, verification string) map[string]any {
	res := map[string]any{"resourceType": "Condition", "id": id, "subject": map[string]any{"reference": "Patient/p1"},
		"code": map[string]any{"coding": []any{map[string]any{"system": ICD, "code": code, "display": display}}}}
	if clinical != "" {
		res["clinicalStatus"] = map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": clinical}}}
	}
	if verification != "" {
		res["verificationStatus"] = map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/condition-ver-status", "code": verification}}}
	}
	return map[string]any{"fullUrl": Base + "/Condition/" + id, "resource": res}
}

// Procedure is a Procedure entry coded with a CPT code. performed is a date, or "start/end" for a course.
func Procedure(id, code, display, status, performed string) map[string]any {
	res := map[string]any{"resourceType": "Procedure", "id": id, "status": status, "subject": map[string]any{"reference": "Patient/p1"},
		"code": map[string]any{"coding": []any{map[string]any{"system": CPT, "code": code, "display": display}}}}
	if start, end, course := strings.Cut(performed, "/"); course {
		res["performedPeriod"] = map[string]any{"start": start, "end": end}
	} else if performed != "" {
		res["performedDateTime"] = performed
	}
	return map[string]any{"fullUrl": Base + "/Procedure/" + id, "resource": res}
}

// Observation is a final Observation entry with a LOINC code and the given value fields (valueQuantity and so on).
func Observation(id, code, display, effective string, value map[string]any) map[string]any {
	res := map[string]any{"resourceType": "Observation", "id": id, "status": "final", "subject": map[string]any{"reference": "Patient/p1"},
		"code": map[string]any{"coding": []any{map[string]any{"system": LOINC, "code": code, "display": display}}}}
	if effective != "" {
		res["effectiveDateTime"] = effective
	}
	for k, v := range value {
		res[k] = v
	}
	return map[string]any{"fullUrl": Base + "/Observation/" + id, "resource": res}
}

// Dispense is a MedicationDispense entry of an RxNorm-coded drug, handed over on a date with a days' supply.
func Dispense(id, rxnorm, status, handedOver string, days float64) map[string]any {
	return map[string]any{"fullUrl": Base + "/MedicationDispense/" + id, "resource": map[string]any{
		"resourceType": "MedicationDispense", "id": id, "status": status, "subject": map[string]any{"reference": "Patient/p1"},
		"medicationCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": RxNorm, "code": rxnorm}}},
		"whenHandedOver":            handedOver,
		"daysSupply":                map[string]any{"value": days, "unit": "days", "system": "http://unitsofmeasure.org", "code": "d"}}}
}

// Statement is a MedicationStatement entry of an RxNorm-coded drug taken from start to end.
func Statement(id, rxnorm, status, start, end string) map[string]any {
	return map[string]any{"fullUrl": Base + "/MedicationStatement/" + id, "resource": map[string]any{
		"resourceType": "MedicationStatement", "id": id, "status": status, "subject": map[string]any{"reference": "Patient/p1"},
		"medicationCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": RxNorm, "code": rxnorm}}},
		"effectivePeriod":           map[string]any{"start": start, "end": end}}}
}

// Quantity is a valueQuantity in a UCUM unit.
func Quantity(v float64, unit string) map[string]any {
	return map[string]any{"valueQuantity": map[string]any{"value": v, "unit": unit, "system": "http://unitsofmeasure.org", "code": unit}}
}

// Document is a DocumentReference entry.
func Document(id, description string) map[string]any {
	return map[string]any{"fullUrl": Base + "/DocumentReference/" + id, "resource": map[string]any{
		"resourceType": "DocumentReference", "id": id, "status": "current", "description": description,
		"subject": map[string]any{"reference": "Patient/p1"},
		"content": []any{map[string]any{"attachment": map[string]any{"contentType": "text/plain", "data": "bm90ZXM="}}}}}
}

// JSON is v encoded.
func JSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Entries is a Bundle's entries.
func Entries(b map[string]any) []any { return b["entry"].([]any) }

// Map is an entry's resource, for a test to edit.
func Map(entry any) map[string]any { return entry.(map[string]any)["resource"].(map[string]any) }
