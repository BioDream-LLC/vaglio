package letter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Plan is what a health plan's letters say about it: who to call, and how, where and by when to appeal. Without one,
// a letter's rights paragraph is generic ("your plan's member materials say how, and by when"); with one it gives the
// plan's own deadline as a date, its addresses and numbers. The plan writes these from its own notices: Vaglio does
// not know any line of business's deadline itself, so nothing here has a default.
type Plan struct {
	Name     string `json:"name"`               // as the member knows it, "Example Health Plan"
	Phone    string `json:"phone,omitempty"`    // member services
	TTY      string `json:"tty,omitempty"`      // "711"
	Hours    string `json:"hours,omitempty"`    // "8 a.m. to 8 p.m., seven days a week"
	Website  string `json:"website,omitempty"`  // https only
	CheckURL string `json:"checkUrl,omitempty"` // the public receipt page, https://plan.example/#/receipt
	Appeal   Appeal `json:"appeal"`
	// Lines overrides Appeal for a line of business (the request's Coverage type: medicare-advantage, medicaid, ...):
	// each field given replaces the plan-wide one.
	Lines map[string]Appeal `json:"lines,omitempty"`
}

// Appeal is how a member asks for an appeal.
type Appeal struct {
	Days           int      `json:"days,omitempty"`           // to ask, counted from the date of the letter
	ExpeditedHours int      `json:"expeditedHours,omitempty"` // a fast appeal's timeframe
	Mail           []string `json:"mail,omitempty"`           // the appeals address, one line each
	Fax            string   `json:"fax,omitempty"`
	Phone          string   `json:"phone,omitempty"`
	Portal         string   `json:"portal,omitempty"` // https only
	// External is what the letter says after an appeal upholds a denial (independent review, a state fair hearing), by
	// language; a language left out keeps the generic sentence.
	External map[string]string `json:"external,omitempty"`
}

// LoadPlan reads a plan's letter settings, refusing unknown fields, so a misspelt one is an error and not silence.
func LoadPlan(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var p Plan
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &p, nil
}

func (p *Plan) check() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("the plan's name is needed: letters say who decided")
	}
	for _, u := range []string{p.Website, p.CheckURL} {
		if err := httpsURL(u); err != nil {
			return err
		}
	}
	all := map[string]Appeal{"": p.Appeal}
	for k, a := range p.Lines {
		all[k] = a
	}
	for k, a := range all {
		if a.Days < 0 || a.Days > 365 || a.ExpeditedHours < 0 || a.ExpeditedHours > 720 {
			return fmt.Errorf("appeal %q: days are 1 to 365 and expeditedHours 1 to 720", k)
		}
		if err := httpsURL(a.Portal); err != nil {
			return err
		}
		for l := range a.External {
			if !Supported(l) {
				return fmt.Errorf("appeal %q: no letters in %q", k, l)
			}
		}
	}
	return nil
}

func httpsURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q: an https address is needed", s)
	}
	return nil
}

// appealFor is the plan-wide appeal with the line's own fields over it.
func (p *Plan) appealFor(line string) Appeal {
	a := p.Appeal
	o, ok := p.Lines[line]
	if !ok {
		return a
	}
	if o.Days != 0 {
		a.Days = o.Days
	}
	if o.ExpeditedHours != 0 {
		a.ExpeditedHours = o.ExpeditedHours
	}
	if len(o.Mail) > 0 {
		a.Mail = o.Mail
	}
	if o.Fax != "" {
		a.Fax = o.Fax
	}
	if o.Phone != "" {
		a.Phone = o.Phone
	}
	if o.Portal != "" {
		a.Portal = o.Portal
	}
	if len(o.External) > 0 {
		a.External = o.External
	}
	return a
}

// planWords are the plan's paragraphs in one language.
type planWords struct {
	askBy, askDays, how, byMail, byFax, byPhone, online, fast, questions, tty, check string
}

var planLang = map[string]planWords{
	"en": {
		askBy:     "You, or your provider for you, can ask %s for an appeal: another review, by people who did not make this decision. Ask by %s",
		askDays:   " (%d days from the date of this letter).",
		how:       "How to ask:",
		byMail:    "By mail: %s",
		byFax:     "By fax: %s",
		byPhone:   "By phone: %s",
		online:    "Online: %s",
		fast:      "If waiting could seriously harm your health, ask for a fast appeal: it is decided within %d hours.",
		questions: "Questions? Call %s at %s",
		tty:       " (TTY %s)",
		check:     "Anyone can check that this decision is genuine and unchanged, without seeing any of your health information: scan the code, or enter the receipt at %s.",
	},
	"es": {
		askBy:     "Usted, o su proveedor en su nombre, puede pedir a %s una apelación: otra revisión, hecha por personas que no tomaron esta decisión. Pídala a más tardar el %s",
		askDays:   " (%d días a partir de la fecha de esta carta).",
		how:       "Cómo pedirla:",
		byMail:    "Por correo: %s",
		byFax:     "Por fax: %s",
		byPhone:   "Por teléfono: %s",
		online:    "En línea: %s",
		fast:      "Si esperar pudiera poner en grave peligro su salud, pida una apelación rápida: se decide en un plazo de %d horas.",
		questions: "¿Preguntas? Llame a %s al %s",
		tty:       " (TTY %s)",
		check:     "Cualquier persona puede verificar que esta decisión es auténtica y no ha cambiado, sin ver nada de su información de salud: escanee el código o escriba el recibo en %s.",
	},
}

// rightsDenied is the plan's own appeal paragraph and how-to list, or nothing when the plan gives no deadline.
func (p *Plan) rightsDenied(w words, pw planWords, line string, now time.Time) (paras, items []string, ok bool) {
	a := p.appealFor(line)
	if a.Days == 0 {
		return nil, nil, false
	}
	first := fmt.Sprintf(pw.askBy, p.Name, w.date(now.AddDate(0, 0, a.Days))) + fmt.Sprintf(pw.askDays, a.Days)
	paras = []string{first}
	if len(a.Mail) > 0 {
		items = append(items, fmt.Sprintf(pw.byMail, strings.Join(a.Mail, ", ")))
	}
	if a.Fax != "" {
		items = append(items, fmt.Sprintf(pw.byFax, a.Fax))
	}
	if a.Phone != "" {
		items = append(items, fmt.Sprintf(pw.byPhone, a.Phone))
	}
	if a.Portal != "" {
		items = append(items, fmt.Sprintf(pw.online, a.Portal))
	}
	if len(items) > 0 {
		paras = append(paras, pw.how)
	}
	return paras, items, true
}

// contact is the plan's member-services line, or "".
func (p *Plan) contact(pw planWords) string {
	if p.Phone == "" {
		return ""
	}
	s := fmt.Sprintf(pw.questions, p.Name, p.Phone)
	if p.TTY != "" {
		s += fmt.Sprintf(pw.tty, p.TTY)
	}
	if p.Hours != "" {
		s += ", " + p.Hours
	}
	s += "."
	if p.Website != "" {
		s += " " + p.Website
	}
	return s
}
