// Command um-standin is a stand-in for a plan's case-review (utilization management) system, to try Vaglio's -um link
// without one: a starting point to read, not a product. It keeps cases in memory, shows them on one page, and lets a
// reviewer approve or deny each; the decision is pushed to Vaglio's /api/feeds/um, signed with the shared token.
//
// Its API is deliberately not Vaglio's own shape, so the link is tried through field maps the way a real system's
// would be (examples/um.json):
//
//	POST /cases          {"externalId", "summary", "due", "link", "receipt", "clinicianOnly", "missing", "proof"}
//	                     answered {"data": {"caseId": "UM-1"}}
//	GET  /cases/{id}     {"caseId", "externalId", "status": "IN_REVIEW" | "APPROVED" | "DENIED", "determination": {...}}
//
// Run it beside Vaglio:
//
//	UM_KEY=... UM_PUSH=... um-standin -addr 127.0.0.1:8791 -vaglio https://127.0.0.1:8740 -vaglio-ca DATA/tls/ca.crt
//	UM_KEY=... UM_PUSH=... vaglio serve ... -um examples/um.json -um-token-env UM_PUSH
//
// Plain http is accepted on this machine only.
package main

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Case is one case as the stand-in keeps it.
type Case struct {
	CaseID        string         `json:"caseId"`
	ExternalID    string         `json:"externalId"`
	Status        string         `json:"status"`
	Summary       string         `json:"summary"`
	Due           string         `json:"due"`
	Link          string         `json:"link"`
	Receipt       string         `json:"receipt"`
	ClinicianOnly bool           `json:"clinicianOnly"`
	Missing       []any          `json:"missing,omitempty"`
	Determination *Determination `json:"determination,omitempty"`
	proof         []byte
}

// Determination is a reviewer's decision.
type Determination struct {
	Note string `json:"note"`
	By   struct {
		Name       string `json:"name"`
		NPI        string `json:"npi,omitempty"`
		Credential string `json:"credential,omitempty"`
	} `json:"by"`
}

type server struct {
	mu     sync.Mutex
	cases  map[string]*Case
	key    string
	push   string
	vaglio string
	client *http.Client
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8791", "address to listen on")
	vaglio := flag.String("vaglio", "", "Vaglio's address, where decisions are pushed (/api/feeds/um)")
	ca := flag.String("vaglio-ca", "", "Vaglio's CA certificate (DATA/tls/ca.crt), when it uses its own")
	flag.Parse()
	s := &server{cases: map[string]*Case{}, key: os.Getenv("UM_KEY"), push: os.Getenv("UM_PUSH"), vaglio: *vaglio,
		client: &http.Client{Timeout: 15 * time.Second}}
	if s.key == "" {
		log.Fatal("UM_KEY is the key Vaglio opens cases with: set it")
	}
	if *ca != "" {
		pem, err := os.ReadFile(*ca)
		if err != nil {
			log.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		s.client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	log.Printf("case-review stand-in on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, s.routes()))
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cases", s.api(s.open))
	mux.HandleFunc("GET /cases/{id}", s.api(s.get))
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /proof/{id}", s.proof)
	mux.HandleFunc("POST /decide/{id}", s.decide)
	return mux
}

// api checks the key Vaglio sends.
func (s *server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Key")), []byte(s.key)) != 1 {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *server) open(w http.ResponseWriter, r *http.Request) {
	var c Case
	var body struct {
		Case
		Proof []byte `json:"proof"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c = body.Case
	c.proof = body.Proof
	s.mu.Lock()
	c.CaseID, c.Status = fmt.Sprintf("UM-%d", len(s.cases)+1), "IN_REVIEW"
	s.cases[c.CaseID] = &c
	s.mu.Unlock()
	log.Printf("opened %s for Vaglio case %s", c.CaseID, c.ExternalID)
	writeJSON(w, map[string]any{"data": map[string]any{"caseId": c.CaseID}})
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, c)
}

func (s *server) proof(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, ok := s.cases[r.PathValue("id")]
	s.mu.Unlock()
	if !ok || len(c.proof) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Write(c.proof)
}

// decide is a reviewer's decision from the page; it is pushed to Vaglio when -vaglio is set (or polled by it).
func (s *server) decide(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	status := map[string]string{"approve": "APPROVED", "deny": "DENIED"}[r.FormValue("decision")]
	note, name := strings.TrimSpace(r.FormValue("note")), strings.TrimSpace(r.FormValue("name"))
	if status == "" || note == "" || name == "" {
		http.Error(w, "a decision, a reason and the reviewer's name", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	c, ok := s.cases[r.PathValue("id")]
	if !ok || c.Status != "IN_REVIEW" {
		s.mu.Unlock()
		http.Error(w, "no such case waiting", http.StatusConflict)
		return
	}
	d := &Determination{Note: note}
	d.By.Name, d.By.NPI, d.By.Credential = name, strings.TrimSpace(r.FormValue("npi")), strings.TrimSpace(r.FormValue("credential"))
	c.Status, c.Determination = status, d
	body, _ := json.Marshal(c)
	s.mu.Unlock()
	if s.vaglio != "" {
		req, _ := http.NewRequest("POST", strings.TrimRight(s.vaglio, "/")+"/api/feeds/um", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.push)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			log.Printf("pushing %s to Vaglio: %v (it will poll, if it is set to)", c.CaseID, err)
		} else {
			var res []map[string]any
			json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()
			log.Printf("pushed %s: %s %v", c.CaseID, resp.Status, res)
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

var page = template.Must(template.New("p").Parse(`<!doctype html><meta charset="utf-8"><title>Case review (stand-in)</title>
<style>body{font:15px system-ui;margin:2em;max-width:60em}article{border:1px solid #ccc;border-radius:8px;padding:1em;margin:1em 0}
label{display:block;margin:.3em 0}input,textarea{font:inherit;width:100%}</style>
<h1>Case review (stand-in)</h1><p>Not a product: a stand-in to try Vaglio's case-review link.</p>
{{range .}}<article><h2>{{.CaseID}} · {{.Status}}</h2><p>{{.Summary}}</p>
<p>Due {{.Due}}{{if .ClinicianOnly}} · <b>a clinician decides this case</b>{{end}} · receipt {{.Receipt}}</p>
<p><a href="/proof/{{.CaseID}}">The proof (PDF)</a>{{if .Link}} · <a href="{{.Link}}">The case in Vaglio</a>{{end}}</p>
{{if eq .Status "IN_REVIEW"}}<form method="post" action="/decide/{{.CaseID}}">
<label>Reason <textarea name="note" required></textarea></label>
<label>Your name <input name="name" required></label>
<label>NPI (clinicians) <input name="npi"></label><label>Credential (MD, DO, RN) <input name="credential"></label>
<button name="decision" value="approve">Approve</button> <button name="decision" value="deny">Deny</button></form>
{{else}}<p>{{.Determination.By.Name}}: {{.Determination.Note}}</p>{{end}}</article>{{else}}<p>No cases yet.</p>{{end}}`))

func (s *server) page(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var list []Case
	for _, c := range s.cases {
		list = append(list, *c)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CaseID > list[j].CaseID })
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page.Execute(w, list)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
