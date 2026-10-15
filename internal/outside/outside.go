// Package outside finds evidence a request does not carry, from sources the plan names: a criteria service it runs or
// licenses, its provider network, and the federal NPI registry.
//
// Vaglio ships no licensed criteria (InterQual, MCG and the like are sold under licences that do not allow it), and it
// is not a provider network. A plan that licenses criteria runs them as a service and names each criterion in a policy
// (find criteria SERVICE CRITERION); the service answers met, not met or unknown, and the proof cites it. A plan's
// network is its own list of providers. The NPI registry (NPPES) is public: a provider's specialties are read from it.
//
// Every source fails safe. No answer, an error, a timeout, an unknown provider: the evidence is not addressed, which
// never denies. Only a definite "not met" from the plan's own criteria service, or a provider the registry lists with
// other specialties, or one missing from the plan's own network list, documents absence.
package outside

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/evidence"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Sources are the outside sources a server has. Any may be nil.
type Sources struct {
	Criteria map[string]*CriteriaService
	Network  *Network
	Registry Registry
}

// Registry is where a provider's specialties are read: the NPPES API, or a file extracted from its download.
type Registry interface {
	// Taxonomies are the provider's NUCC taxonomy codes, and what to cite. ok is false when the registry does not know
	// the NPI.
	Taxonomies(ctx context.Context, npi string) (codes []string, cite string, ok bool, err error)
}

// Uses says whether a policy reads any outside source.
func Uses(p *policy.Policy) bool {
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.Criteria != nil || s.Provider != nil {
				return true
			}
		}
	}
	return false
}

// timeout bounds every outside question: a request is never kept waiting on one.
const timeout = 5 * time.Second

// Findings asks the sources about the policy's outside evidence for this request. raw is the request Bundle as
// received, sent to a criteria service. notes say what could not be asked, for the extraction's ignored list.
func (s *Sources) Findings(ctx context.Context, p *policy.Policy, req *pas.Request, raw []byte, service string) ([]evidence.Finding, []string) {
	if s == nil || !Uses(p) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	npi := ProviderNPI(req)
	var out []evidence.Finding
	var notes []string
	var taxonomies []string
	var taxCite string
	taxKnown, taxAsked := false, false
	for _, a := range p.Arguments {
		for _, src := range a.Find {
			switch {
			case src.Criteria != nil:
				svc := s.Criteria[src.Criteria.Service]
				if svc == nil {
					notes = append(notes, fmt.Sprintf("%s: no criteria service %q is configured (serve -criteria)", a.Name, src.Criteria.Service))
					continue
				}
				f, cite, err := svc.Ask(ctx, src.Criteria.Criterion, p, service, raw)
				if err != nil {
					notes = append(notes, fmt.Sprintf("%s: criteria service %s: %v", a.Name, src.Criteria.Service, err))
					continue
				}
				if f != "" {
					out = append(out, evidence.Finding{Argument: a.Name, Finding: f, Citation: cite})
				}
			case src.Provider != nil && src.Provider.InNetwork:
				if s.Network == nil {
					notes = append(notes, a.Name+": no provider network is configured (serve -network)")
					continue
				}
				if npi == "" {
					continue // not addressed: the request names no provider by NPI
				}
				in := s.Network.Has(npi)
				f := decide.Present
				q := fmt.Sprintf("NPI %s is in the plan's network (%s)", npi, s.Network.Source)
				if !in {
					f, q = decide.Absent, fmt.Sprintf("NPI %s is not in the plan's network (%s)", npi, s.Network.Source)
				}
				out = append(out, evidence.Finding{Argument: a.Name, Finding: f,
					Citation: decide.Citation{Reference: "network:" + s.Network.Source, Quote: q}})
			case src.Provider != nil:
				if s.Registry == nil {
					notes = append(notes, a.Name+": no NPI registry is configured (serve -npi-registry)")
					continue
				}
				if npi == "" {
					continue
				}
				if !taxAsked {
					taxAsked = true
					var err error
					taxonomies, taxCite, taxKnown, err = s.Registry.Taxonomies(ctx, npi)
					if err != nil {
						notes = append(notes, fmt.Sprintf("%s: the NPI registry: %v", a.Name, err))
						taxKnown = false
					}
				}
				if !taxKnown {
					continue
				}
				f := decide.Absent
				for _, t := range src.Provider.Taxonomy {
					if slices.Contains(taxonomies, t) {
						f = decide.Present
					}
				}
				q := fmt.Sprintf("NPI %s: specialties %s", npi, strings.Join(taxonomies, ", "))
				if len(taxonomies) == 0 {
					q = fmt.Sprintf("NPI %s: no specialty listed", npi)
				}
				out = append(out, evidence.Finding{Argument: a.Name, Finding: f, Citation: decide.Citation{Reference: taxCite, Quote: q}})
			}
		}
	}
	return out, notes
}

// ProviderNPI is the NPI of the Claim's provider, when the Bundle carries a valid one.
func ProviderNPI(req *pas.Request) string {
	res := pas.Map(req.Resolve(pas.Str(pas.Map(req.Claim["provider"])["reference"]))["resource"])
	for _, id := range pas.Slice(res["identifier"]) {
		m := pas.Map(id)
		if pas.Str(m["system"]) == "http://hl7.org/fhir/sid/us-npi" && auth.ValidNPI(pas.Str(m["value"])) {
			return pas.Str(m["value"])
		}
	}
	return ""
}

// CriteriaService is a plan's criteria service. Vaglio posts it
//
//	{"criterion": "KNEE-TKA-3.2", "policy": "tka", "policyVersion": "1.0.0", "service": "http://www.ama-assn.org/go/cpt|27447",
//	 "bundle": <the PAS request Bundle>}
//
// and reads {"result": "met" | "not-met" | "unknown", "reason": "...", "reference": "..."}: met documents the evidence
// present, not-met absent, and anything else nothing. reason is quoted in the proof, reference cited (the criterion's
// version, say).
type CriteriaService struct {
	Name  string
	URL   string
	Token string
	HTTP  *http.Client
}

// NewCriteriaService checks a service's URL: it is sent patient records, so it is https, or http on this machine.
func NewCriteriaService(name, raw, token string, hc *http.Client) (*CriteriaService, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("criteria service %s: %q is not an http(s) URL", name, raw)
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, fmt.Errorf("criteria service %s is sent patient records: https, or http on this machine only", name)
	}
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &CriteriaService{Name: name, URL: raw, Token: token, HTTP: hc}, nil
}

// Ask asks the service about one criterion.
func (c *CriteriaService) Ask(ctx context.Context, criterion string, p *policy.Policy, service string, bundle []byte) (decide.Finding, decide.Citation, error) {
	body, _ := json.Marshal(map[string]any{"criterion": criterion, "policy": p.ID, "policyVersion": p.Version,
		"service": service, "bundle": json.RawMessage(bundle)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", decide.Citation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", decide.Citation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", decide.Citation{}, fmt.Errorf("answered %d", resp.StatusCode)
	}
	var a struct {
		Result    string `json:"result"`
		Reason    string `json:"reason"`
		Reference string `json:"reference"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a); err != nil {
		return "", decide.Citation{}, fmt.Errorf("not JSON: %w", err)
	}
	ref := "criteria:" + c.Name + "/" + criterion
	if a.Reference != "" {
		ref += " (" + a.Reference + ")"
	}
	reason := strings.TrimSpace(a.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	switch a.Result {
	case "met":
		if reason == "" {
			reason = "met"
		}
		return decide.Present, decide.Citation{Reference: ref, Quote: reason}, nil
	case "not-met":
		if reason == "" {
			return "", decide.Citation{}, errors.New(`"not-met" without its reason is not used: a denial cites why`)
		}
		return decide.Absent, decide.Citation{Reference: ref, Quote: reason}, nil
	}
	return "", decide.Citation{}, nil // unknown: not addressed
}

// Network is the plan's provider network: the NPIs in it.
type Network struct {
	Source string // what the proof cites: the file and its date
	npis   map[string]bool
}

// Has is whether an NPI is in the network.
func (n *Network) Has(npi string) bool { return n.npis[npi] }

// LoadNetwork reads a network file: one NPI per line (or a CSV whose first column is the NPI; a header is skipped).
// Every NPI must be valid: a typo would put a provider out of network.
func LoadNetwork(path string) (*Network, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, _ := f.Stat()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	n := &Network{npis: map[string]bool{}}
	line := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		line++
		v := strings.TrimSpace(rec[0])
		if v == "" || (line == 1 && !auth.ValidNPI(v) && strings.Trim(v, "0123456789") != "") {
			continue // blank, or a header
		}
		if !auth.ValidNPI(v) {
			return nil, fmt.Errorf("%s line %d: %q is not a valid NPI", path, line, v)
		}
		n.npis[v] = true
	}
	if len(n.npis) == 0 {
		return nil, fmt.Errorf("%s holds no NPIs", path)
	}
	n.Source = fmt.Sprintf("%s, %s", baseName(path), info.ModTime().UTC().Format(time.DateOnly))
	return n, nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// FileRegistry is NPI taxonomies from a CSV of npi,taxonomy[,taxonomy...]: an extract of the NPPES download, for a
// server that does not call the registry over the internet.
type FileRegistry struct {
	Source string
	byNPI  map[string][]string
}

// LoadFileRegistry reads a registry extract.
func LoadFileRegistry(path string) (*FileRegistry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, _ := f.Stat()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	reg := &FileRegistry{byNPI: map[string][]string{}}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		npi := strings.TrimSpace(rec[0])
		if !auth.ValidNPI(npi) {
			continue
		}
		var codes []string
		for _, c := range rec[1:] {
			if c = strings.TrimSpace(c); c != "" {
				codes = append(codes, c)
			}
		}
		reg.byNPI[npi] = codes
	}
	reg.Source = fmt.Sprintf("npi-registry:%s, %s", baseName(path), info.ModTime().UTC().Format(time.DateOnly))
	return reg, nil
}

// Taxonomies implements Registry.
func (r *FileRegistry) Taxonomies(_ context.Context, npi string) ([]string, string, bool, error) {
	codes, ok := r.byNPI[npi]
	return codes, r.Source, ok, nil
}

// NPPES asks the public NPI Registry API (https://npiregistry.cms.hhs.gov/api/), keeping answers for a day. It sends
// only the provider's NPI, which is public; nothing about the patient.
type NPPES struct {
	URL  string // default https://npiregistry.cms.hhs.gov/api/
	HTTP *http.Client

	mu    sync.Mutex
	cache map[string]nppesAnswer
}

type nppesAnswer struct {
	codes []string
	known bool
	at    time.Time
}

// NPPESURL is the NPI Registry API.
const NPPESURL = "https://npiregistry.cms.hhs.gov/api/"

// Taxonomies implements Registry.
func (n *NPPES) Taxonomies(ctx context.Context, npi string) ([]string, string, bool, error) {
	n.mu.Lock()
	if a, ok := n.cache[npi]; ok && time.Since(a.at) < 24*time.Hour {
		n.mu.Unlock()
		return a.codes, "npi-registry:NPPES " + a.at.Format(time.DateOnly), a.known, nil
	}
	n.mu.Unlock()
	base := n.URL
	if base == "" {
		base = NPPESURL
	}
	q := url.Values{"version": {"2.1"}, "number": {npi}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", false, err
	}
	hc := n.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("answered %d", resp.StatusCode)
	}
	var body struct {
		ResultCount int `json:"result_count"`
		Results     []struct {
			Taxonomies []struct {
				Code string `json:"code"`
			} `json:"taxonomies"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, "", false, err
	}
	a := nppesAnswer{at: time.Now().UTC(), known: body.ResultCount > 0 && len(body.Results) > 0}
	if a.known {
		for _, t := range body.Results[0].Taxonomies {
			if t.Code != "" {
				a.codes = append(a.codes, t.Code)
			}
		}
	}
	n.mu.Lock()
	if n.cache == nil {
		n.cache = map[string]nppesAnswer{}
	}
	n.cache[npi] = a
	n.mu.Unlock()
	return a.codes, "npi-registry:NPPES " + a.at.Format(time.DateOnly), a.known, nil
}
