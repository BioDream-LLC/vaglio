package notice

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	zipRe   = regexp.MustCompile(`^\d{5}(-\d{4})?$`)
	stateRe = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// MemberAddress is the patient's mailing address and the language of their letters, from the request's Patient: the
// address marked for postal use (type postal or both), else the home address, else the first; Spanish when the
// patient's preferred language is Spanish, English otherwise. A US address with a street, city, state and ZIP is
// needed: anything less is not mailed, and the case says so.
func MemberAddress(bundle map[string]any, patientRef string) (Address, string, error) {
	p := findPatient(bundle, patientRef)
	if p == nil {
		return Address{}, "", errors.New("not mailed: the request has no Patient")
	}
	name := patientName(p)
	if name == "" {
		return Address{}, "", errors.New("not mailed: the Patient has no name")
	}
	addrs, _ := p["address"].([]any)
	var best map[string]any
	score := -1
	for _, raw := range addrs {
		a, _ := raw.(map[string]any)
		if a == nil || str(a["use"]) == "old" || hasEnd(a) {
			continue
		}
		s := 0
		switch str(a["type"]) {
		case "postal", "both":
			s = 3
		}
		if str(a["use"]) == "home" {
			s++
		}
		if s > score {
			best, score = a, s
		}
	}
	if best == nil {
		return Address{}, "", errors.New("not mailed: the Patient has no current address")
	}
	out := Address{Name: name, City: str(best["city"]), State: strings.ToUpper(str(best["state"])),
		Zip: str(best["postalCode"]), Country: strings.ToUpper(str(best["country"]))}
	lines, _ := best["line"].([]any)
	for _, l := range lines {
		if s := str(l); s != "" {
			out.Lines = append(out.Lines, s)
		}
	}
	switch out.Country {
	case "", "US", "USA", "UNITED STATES":
		out.Country = "US"
	default:
		return Address{}, "", fmt.Errorf("not mailed: the address is outside the US (%s)", out.Country)
	}
	if len(out.Lines) == 0 || out.City == "" || !stateRe.MatchString(out.State) || !zipRe.MatchString(out.Zip) {
		return Address{}, "", errors.New("not mailed: the address lacks a street, city, two-letter state or ZIP code")
	}
	return out, language(p), nil
}

func findPatient(b map[string]any, ref string) map[string]any {
	entries, _ := b["entry"].([]any)
	var only map[string]any
	n := 0
	for _, e := range entries {
		em, _ := e.(map[string]any)
		r, _ := em["resource"].(map[string]any)
		if r == nil || str(r["resourceType"]) != "Patient" {
			continue
		}
		n++
		only = r
		if ref != "" && (ref == "Patient/"+str(r["id"]) || strings.HasSuffix(str(em["fullUrl"]), "/"+ref) || str(em["fullUrl"]) == ref) {
			return r
		}
	}
	if n == 1 {
		return only
	}
	return nil
}

func patientName(p map[string]any) string {
	names, _ := p["name"].([]any)
	var pick map[string]any
	for _, raw := range names {
		n, _ := raw.(map[string]any)
		if n == nil || str(n["use"]) == "old" {
			continue
		}
		if pick == nil || str(n["use"]) == "official" {
			pick = n
		}
	}
	if pick == nil {
		return ""
	}
	if t := str(pick["text"]); t != "" {
		return t
	}
	var parts []string
	given, _ := pick["given"].([]any)
	for _, g := range given {
		if s := str(g); s != "" {
			parts = append(parts, s)
		}
	}
	if f := str(pick["family"]); f != "" {
		parts = append(parts, f)
	}
	return strings.Join(parts, " ")
}

// language is the letter's language from Patient.communication: the preferred one, else the first.
func language(p map[string]any) string {
	comms, _ := p["communication"].([]any)
	lang := ""
	for _, raw := range comms {
		c, _ := raw.(map[string]any)
		l, _ := c["language"].(map[string]any)
		code := ""
		codings, _ := l["coding"].([]any)
		for _, cd := range codings {
			cm, _ := cd.(map[string]any)
			if s := str(cm["code"]); s != "" {
				code = s
				break
			}
		}
		if code == "" {
			continue
		}
		if pref, _ := c["preferred"].(bool); pref || lang == "" {
			lang = code
		}
	}
	if strings.HasPrefix(strings.ToLower(lang), "es") {
		return "es"
	}
	return "en"
}

// hasEnd is whether an address's period has ended.
func hasEnd(a map[string]any) bool {
	p, _ := a["period"].(map[string]any)
	end := str(p["end"])
	if len(end) < 10 {
		return end != ""
	}
	t, err := time.Parse("2006-01-02", end[:10])
	return err != nil || t.Before(time.Now().Truncate(24*time.Hour))
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
