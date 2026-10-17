// Package letter writes the letter a decision sends, in plain words, from the decision's own record: what was asked
// for, what was decided and by whom, the policy's words it rests on, what the record showed, what would change it, and
// the receipt that checks it.
//
// Every sentence is a template filled from the case; no language model writes any of it, so the letter cannot say
// anything the decision does not. The templates are in English and Spanish. The policy's own words (its clauses and the
// names of its evidence) are the plan's, in the language the plan wrote them; a letter in another language quotes them
// as they are and says so, because a machine translation of a coverage rule is not the rule.
package letter

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
)

// Languages are the languages a letter can be written in.
var Languages = []string{"en", "es"}

// Letter is a letter as sections of plain text, for the console to lay out and print.
type Letter struct {
	Lang     string    `json:"lang"`
	Title    string    `json:"title"`
	Date     string    `json:"date"`
	Sections []Section `json:"sections"`
	// Quoted is set when the letter quotes the plan's words in a language other than its own (the policy's language).
	Quoted string `json:"quoted,omitempty"`
	// Receipt checks the decision the letter states.
	Receipt string `json:"receipt,omitempty"`
}

// Section is a heading, its paragraphs, and a list.
type Section struct {
	Heading    string   `json:"heading"`
	Paragraphs []string `json:"paragraphs,omitempty"`
	Items      []string `json:"items,omitempty"`
	// Quote is the policy's own words, shown apart.
	Quote string `json:"quote,omitempty"`
	// After are paragraphs that follow the list.
	After []string `json:"after,omitempty"`
}

// words are the templates of one language.
type words struct {
	approved, denied, pended, review, title                                        string
	asked, outcomeApproved, outcomeDenied, outcomePended, outcomeClinician         string
	byEngine, byDocuments, byReviewer, byClinician, appealOverturned, appealUpheld string
	documentsSaid                                                                  string
	why, rule, showed, present, absent, need, needIntro, change, changeIntro       string
	changeNone, contradicts, reviewerSaid, rights, rightsDenied, rightsOther       string
	check, checkText, quoted, dateLayout                                           string
	appealReceived, appealReason, rightsUpheld                                     string
	extended, extendedWhy, contact                                                 string
	months                                                                         [12]string
}

var lang = map[string]words{
	"en": {
		title:            "Prior authorization decision",
		approved:         "Approved",
		denied:           "Not approved",
		pended:           "More information needed",
		review:           "With a clinician for review",
		asked:            "Your provider asked your health plan to approve %s (code %s) on %s.",
		outcomeApproved:  "Your health plan approved it.",
		outcomeDenied:    "Your health plan did not approve it.",
		outcomePended:    "Your health plan has not decided yet: it needs more information from your provider.",
		outcomeClinician: "Your health plan has not decided yet: a licensed clinician is reviewing the request.",
		byEngine:         "This decision was made by applying the plan's written policy to the information your provider sent.",
		byReviewer:       "This decision was made by a reviewer at your health plan.",
		byDocuments:      "This decision was made by applying the plan's written policy to the documents your provider sent.",
		documentsSaid:    "What the documents showed",
		byClinician:      "This decision was made by a licensed clinician (%s) at your health plan.",
		appealOverturned: "On appeal, the decision was changed: it is now approved.",
		appealUpheld:     "On appeal, the decision was kept.",
		why:              "Why",
		rule:             "The plan's policy, %s, says:",
		showed:           "What the information sent showed",
		present:          "Shown: %s",
		absent:           "Shown not to be the case: %s",
		need:             "What is needed",
		needIntro:        "Your provider has been asked for the following. Nothing missing is held against you: the request waits for it.",
		change:           "What would change this decision",
		changeIntro:      "The request would be approved if the record showed any one of these:",
		changeNone:       "Under this policy, no further information would change it; only a correction to the record could.",
		contradicts:      "%s (the record sent says it is not, so this would need a correction)",
		reviewerSaid:     "The reviewer's reason",
		rights:           "Your rights",
		rightsDenied:     "You, or your provider for you, can ask your health plan for an appeal: another review, by people who did not make this decision. Your plan's member materials say how, and by when. You can ask your plan for a copy of the policy this decision used, free of charge.",
		rightsOther:      "You can ask your health plan for a copy of the policy this decision used, free of charge.",
		appealReceived:   "Your appeal was received on %s. A decision is due by %s, from people who did not make the first decision.",
		appealReason:     "The appeal's reason",
		extended:         "Your health plan is taking up to %d more days to decide. A decision is now due by %s.",
		extendedWhy:      "Why: %s",
		contact:          "Questions",
		rightsUpheld:     "You may be able to ask for a further review by an independent organization outside your health plan; for some plans it is sent there for you. Your plan's member materials say how, and by when. You can ask your plan for a copy of the policy this decision used, free of charge.",
		check:            "Check this letter",
		checkText:        "Anyone can check that this decision is genuine and unchanged, without seeing any of your health information: scan the code, or enter the receipt at your health plan's decision-check page.",
		quoted:           "",
		dateLayout:       "%[2]s %[1]d, %[3]d",
		months:           [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	},
	"es": {
		title:            "Decisión de autorización previa",
		approved:         "Aprobada",
		denied:           "No aprobada",
		pended:           "Se necesita más información",
		review:           "En revisión por un profesional clínico",
		asked:            "Su proveedor pidió a su plan de salud que aprobara %s (código %s) el %s.",
		outcomeApproved:  "Su plan de salud lo aprobó.",
		outcomeDenied:    "Su plan de salud no lo aprobó.",
		outcomePended:    "Su plan de salud todavía no ha decidido: necesita más información de su proveedor.",
		outcomeClinician: "Su plan de salud todavía no ha decidido: un profesional clínico con licencia está revisando la solicitud.",
		byEngine:         "Esta decisión se tomó aplicando la póliza escrita del plan a la información que envió su proveedor.",
		byReviewer:       "Esta decisión la tomó un revisor de su plan de salud.",
		byDocuments:      "Esta decisión se tomó aplicando la póliza escrita del plan a los documentos que envió su proveedor.",
		documentsSaid:    "Lo que mostraron los documentos",
		byClinician:      "Esta decisión la tomó un profesional clínico con licencia (%s) de su plan de salud.",
		appealOverturned: "En la apelación, la decisión cambió: ahora está aprobada.",
		appealUpheld:     "En la apelación, se mantuvo la decisión.",
		why:              "Por qué",
		rule:             "La póliza del plan, %s, dice:",
		showed:           "Lo que mostró la información enviada",
		present:          "Se mostró: %s",
		absent:           "Se mostró que no es así: %s",
		need:             "Lo que se necesita",
		needIntro:        "Se le pidió a su proveedor lo siguiente. Nada de lo que falta se usa en su contra: la solicitud espera esa información.",
		change:           "Lo que cambiaría esta decisión",
		changeIntro:      "La solicitud se aprobaría si el expediente mostrara cualquiera de estas opciones:",
		changeNone:       "Según esta póliza, ninguna información adicional la cambiaría; solo una corrección del expediente podría hacerlo.",
		contradicts:      "%s (el expediente enviado dice que no, así que haría falta una corrección)",
		reviewerSaid:     "El motivo del revisor",
		rights:           "Sus derechos",
		rightsDenied:     "Usted, o su proveedor en su nombre, puede pedir a su plan de salud una apelación: otra revisión, hecha por personas que no tomaron esta decisión. Los materiales para miembros de su plan explican cómo y en qué plazo. Puede pedir a su plan, sin costo, una copia de la póliza que se usó en esta decisión.",
		rightsOther:      "Puede pedir a su plan de salud, sin costo, una copia de la póliza que se usó en esta decisión.",
		appealReceived:   "Su apelación se recibió el %s. Debe decidirse a más tardar el %s, por personas que no tomaron la primera decisión.",
		appealReason:     "El motivo de la decisión de la apelación",
		extended:         "Su plan de salud tomará hasta %d días más para decidir. Ahora debe decidirse a más tardar el %s.",
		extendedWhy:      "Motivo: %s",
		contact:          "Preguntas",
		rightsUpheld:     "Es posible que pueda pedir otra revisión a una organización independiente, fuera de su plan de salud; en algunos planes se envía allí por usted. Los materiales para miembros de su plan explican cómo y en qué plazo. Puede pedir a su plan, sin costo, una copia de la póliza que se usó en esta decisión.",
		check:            "Verifique esta carta",
		checkText:        "Cualquier persona puede verificar que esta decisión es auténtica y no ha cambiado, sin ver nada de su información de salud: escanee el código o escriba el recibo en la página de verificación de decisiones de su plan de salud.",
		quoted:           "Las palabras de la póliza del plan se citan en el idioma en que el plan las escribió (inglés). No se traducen: una traducción automática de una regla de cobertura no es la regla.",
		dateLayout:       "%[1]d de %[2]s de %[3]d",
		months:           [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	},
}

// Supported reports whether a letter can be written in this language.
func Supported(l string) bool { _, ok := lang[l]; return ok }

func (w words) date(t time.Time) string {
	return fmt.Sprintf(w.dateLayout, t.Day(), w.months[t.Month()-1], t.Year())
}

// Write writes the letter for a case's current decision, dated now, with its receipt, in the generic words.
func Write(c cases.Case, l, receipt string, now time.Time) (*Letter, error) {
	return WriteFor(c, l, receipt, now, nil)
}

// WriteFor writes it with the plan's own appeal deadline, addresses and numbers, when plan is set.
func WriteFor(c cases.Case, l, receipt string, now time.Time, plan *Plan) (*Letter, error) {
	w, ok := lang[l]
	if !ok {
		return nil, fmt.Errorf("letter: no letters in %q; there are letters in %s", l, strings.Join(Languages, ", "))
	}
	res := c.Result
	if res == nil {
		return nil, fmt.Errorf("letter: the case has no decision")
	}
	created, _ := time.Parse(time.RFC3339Nano, c.Created)
	service := c.Service
	if i := strings.LastIndex(service, "|"); i >= 0 {
		service = service[i+1:]
	}
	out := &Letter{Lang: l, Date: w.date(now), Receipt: receipt, Quoted: w.quoted}

	// What stands: an appeal over a review over the engine.
	decision, by := c.Answer.Decision, w.byEngine
	clinicianPend := c.Proposed != nil
	if c.Review != nil {
		decision, clinicianPend = c.Review.Decision, false
		by = w.byReviewer
		if c.Review.Automatic {
			by = w.byDocuments
		} else if c.Review.ReviewerNPI != "" {
			cred := c.Review.Credential
			if cred == "" {
				cred = "NPI " + c.Review.ReviewerNPI
			}
			by = fmt.Sprintf(w.byClinician, cred)
		}
	}
	head := Section{Paragraphs: []string{fmt.Sprintf(w.asked, c.PolicyTitle, service, w.date(created))}}
	switch {
	case decision == "approve":
		out.Title = w.title + ": " + w.approved
		head.Paragraphs = append(head.Paragraphs, w.outcomeApproved, by)
	case decision == "deny":
		out.Title = w.title + ": " + w.denied
		head.Paragraphs = append(head.Paragraphs, w.outcomeDenied, by)
	case clinicianPend:
		out.Title = w.title + ": " + w.review
		head.Paragraphs = append(head.Paragraphs, w.outcomeClinician)
	default:
		out.Title = w.title + ": " + w.pended
		head.Paragraphs = append(head.Paragraphs, w.outcomePended)
	}
	if t := c.AppealTaken; t != nil && c.Appeal == nil {
		got, _ := time.Parse(time.RFC3339, t.Received)
		due, _ := time.Parse(time.RFC3339, t.Due)
		head.Paragraphs = append(head.Paragraphs, fmt.Sprintf(w.appealReceived, w.date(got), w.date(due)))
	}
	if x := c.Extension; x != nil && (decision == "pend" || clinicianPend) {
		due, _ := time.Parse(time.RFC3339Nano, x.Due)
		p := fmt.Sprintf(w.extended, x.Days, w.date(due))
		if x.Note != "" {
			p += " " + fmt.Sprintf(w.extendedWhy, x.Note)
		}
		head.Paragraphs = append(head.Paragraphs, p)
	}
	if c.Appeal != nil {
		if c.Appeal.Outcome == "overturned" {
			out.Title = w.title + ": " + w.approved
			head.Paragraphs = append(head.Paragraphs, w.appealOverturned)
		} else {
			head.Paragraphs = append(head.Paragraphs, w.appealUpheld)
		}
	}
	out.Sections = append(out.Sections, head)

	// Why: the engine's own reasoning, when it is what decided; a reviewer's reason when a reviewer did.
	if c.Review != nil {
		heading := w.reviewerSaid
		if c.Review.Automatic {
			heading = w.documentsSaid
		}
		out.Sections = append(out.Sections, Section{Heading: heading, Paragraphs: []string{c.Review.Reason}})
	} else if res.Proof != nil && (decision == "approve" || decision == "deny") {
		out.Sections = append(out.Sections, Section{Heading: w.why,
			Paragraphs: []string{plain(res.Proof.Text), fmt.Sprintf(w.rule, c.PolicyTitle)}, Quote: res.Proof.Clause})
	}

	// What the record showed: every finding the decision read, present or absent, by its plain description.
	var showed []string
	for _, n := range nodes(res.Proof) {
		if n.Kind == "evidence" {
			showed = append(showed, fmt.Sprintf(w.present, n.Text))
		}
	}
	for _, it := range res.Relied {
		showed = append(showed, fmt.Sprintf(w.absent, it.Text))
	}
	if len(showed) > 0 && c.Review == nil {
		out.Sections = append(out.Sections, Section{Heading: w.showed, Items: unique(showed)})
	}

	if decision == "pend" && !clinicianPend && len(res.Missing) > 0 {
		var items []string
		for _, it := range res.Missing {
			items = append(items, it.Text)
		}
		out.Sections = append(out.Sections, Section{Heading: w.need, Paragraphs: []string{w.needIntro}, Items: items})
	}
	if decision == "deny" && (c.Appeal == nil || c.Appeal.Outcome != "overturned") && res.Outcome != decide.Approved {
		s := Section{Heading: w.change}
		if len(res.ToApprove) == 0 {
			s.Paragraphs = []string{w.changeNone}
		} else {
			s.Paragraphs = []string{w.changeIntro}
			for _, route := range res.ToApprove {
				var parts []string
				for _, ch := range route {
					t := ch.Text
					if ch.ContradictsRecord {
						t = fmt.Sprintf(w.contradicts, t)
					}
					parts = append(parts, t)
				}
				s.Items = append(s.Items, strings.Join(parts, " + "))
			}
		}
		out.Sections = append(out.Sections, s)
	}
	if c.Appeal != nil && c.Appeal.Reason != "" {
		out.Sections = append(out.Sections, Section{Heading: w.appealReason, Paragraphs: []string{c.Appeal.Reason}})
	}
	rights := Section{Heading: w.rights, Paragraphs: []string{w.rightsOther}}
	pw := planLang[l]
	switch {
	case decision == "deny" && c.Appeal != nil && c.Appeal.Outcome == "upheld":
		rights.Paragraphs = []string{w.rightsUpheld}
		if plan != nil {
			if t := plan.appealFor(c.Line).External[l]; t != "" {
				rights.Paragraphs = []string{t, w.rightsOther}
			}
		}
	case decision == "deny" && c.Appeal == nil && c.AppealTaken == nil:
		rights.Paragraphs = []string{w.rightsDenied}
		if plan != nil {
			if paras, items, ok := plan.rightsDenied(w, pw, c.Line, now); ok {
				rights.Paragraphs, rights.Items = paras, items
				if h := plan.appealFor(c.Line).ExpeditedHours; h > 0 {
					rights.After = append(rights.After, fmt.Sprintf(pw.fast, h))
				}
				rights.After = append(rights.After, w.rightsOther)
			}
		}
	}
	out.Sections = append(out.Sections, rights)
	if plan != nil {
		if s := plan.contact(pw); s != "" {
			out.Sections = append(out.Sections, Section{Heading: w.contact, Paragraphs: []string{s}})
		}
	}
	if receipt != "" {
		text := w.checkText
		if plan != nil && plan.CheckURL != "" {
			text = fmt.Sprintf(pw.check, plan.CheckURL)
		}
		out.Sections = append(out.Sections, Section{Heading: w.check, Paragraphs: []string{text}})
	}
	return out, nil
}

// nodes is every node of a proof, depth first.
func nodes(n *decide.Node) []*decide.Node {
	if n == nil {
		return nil
	}
	out := []*decide.Node{n}
	for _, d := range n.Defeats {
		out = append(out, nodes(d.By)...)
	}
	return out
}

func unique(s []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// plain drops the engine's own label from a decision's text ("Deny: repeat study ..." reads "Repeat study ...").
func plain(s string) string {
	for _, p := range []string{"Deny: ", "Approve: "} {
		if t, ok := strings.CutPrefix(s, p); ok && t != "" {
			return strings.ToUpper(t[:1]) + t[1:]
		}
	}
	return s
}
