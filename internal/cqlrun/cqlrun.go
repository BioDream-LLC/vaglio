// Package cqlrun runs a policy's cql sources: the defines of the CQL libraries it pins (find cql LIBRARY "DEFINE"),
// over the request's own resources, as evidence.
//
// A policy names each file it runs (a .cql library, a .json ValueSet or Bundle of them) with its SHA-256, so the policy's
// text, and the hash an adoption or a sign-off records, covers what the libraries say: a library changed after the
// policy was signed no longer matches it and the policy does not load. The files live under the policy directory.
//
// A define that is true documents the evidence present, false documents it absent, and anything else (null, an error,
// a value set not given, something the engine does not do) leaves it not addressed, which never denies. Every finding
// cites the define and the resources its retrieves read.
package cqlrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/pas"
	"github.com/biodream-llc/vaglio/internal/cql"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Prefix starts the citation of every finding from a CQL define.
const Prefix = "cql:"

// Program is one policy's libraries and value sets, read, checked against their pins and compiled.
type Program struct {
	Libraries map[string]*cql.Library
	ValueSets map[string]*cql.ValueSet
}

// Load reads the files a policy pins from dir, refusing any whose SHA-256 differs from its pin, and checks every cql
// source names a library and a define it has. A policy with no cql sources has no program (nil).
func Load(p *policy.Policy, dir string) (*Program, error) {
	if len(p.CQL) == 0 {
		return nil, nil
	}
	prog := &Program{Libraries: map[string]*cql.Library{}, ValueSets: map[string]*cql.ValueSet{}}
	for _, f := range p.CQL {
		path := filepath.Join(dir, filepath.FromSlash(f.File))
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("policy %s: cql file %s: %w", p.ID, f.File, err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			return nil, fmt.Errorf("policy %s: cql file %s has changed since the policy pinned it (sha256 %s, pinned "+
				"%s): a library is part of the policy, so the policy must be pinned to the new text and signed off again",
				p.ID, f.File, got, f.SHA256)
		}
		switch {
		case strings.HasSuffix(f.File, ".cql"):
			l, err := cql.Compile(string(raw))
			if err != nil {
				return nil, fmt.Errorf("policy %s: %s: %w", p.ID, f.File, err)
			}
			if l.Name == "" {
				return nil, fmt.Errorf("policy %s: %s has no library name", p.ID, f.File)
			}
			prog.Libraries[l.Name] = l
		default:
			vss, err := valueSets(raw)
			if err != nil {
				return nil, fmt.Errorf("policy %s: %s: %w", p.ID, f.File, err)
			}
			for _, vs := range vss {
				prog.ValueSets[vs.URL] = vs
			}
		}
	}
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.CQL == nil {
				continue
			}
			l := prog.Libraries[s.CQL.Library]
			if l == nil {
				return nil, fmt.Errorf("policy %s: %s reads library %s, which no pinned file holds", p.ID, a.Name, s.CQL.Library)
			}
			found := false
			for _, d := range l.Defines() {
				found = found || d == s.CQL.Define
			}
			if !found {
				return nil, fmt.Errorf("policy %s: %s reads %q, which library %s does not define", p.ID, a.Name,
					s.CQL.Define, s.CQL.Library)
			}
		}
	}
	for _, l := range prog.Libraries {
		for _, in := range l.Includes {
			if in.Name != "FHIRHelpers" && prog.Libraries[in.Name] == nil {
				return nil, fmt.Errorf("policy %s: library %s includes %s, which no pinned file holds", p.ID, l.Name, in.Name)
			}
		}
	}
	return prog, nil
}

func valueSets(raw []byte) ([]*cql.ValueSet, error) {
	var head struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	if head.ResourceType != "Bundle" {
		vs, err := cql.NewValueSet(raw)
		if err != nil {
			return nil, err
		}
		return []*cql.ValueSet{vs}, nil
	}
	var out []*cql.ValueSet
	for _, e := range head.Entry {
		vs, err := cql.NewValueSet(e.Resource)
		if err != nil {
			return nil, err
		}
		out = append(out, vs)
	}
	return out, nil
}

// Finding is what one define said, with its citation.
type Finding struct {
	Argument string
	Finding  decide.Finding
	Citation decide.Citation
}

// Run evaluates the policy's cql sources over the request. notes say which defines were not addressed and why.
func (prog *Program) Run(p *policy.Policy, req *pas.Request) ([]Finding, []string) {
	if prog == nil {
		return nil, nil
	}
	var resources []map[string]any
	params := map[string]any{}
	for _, e := range req.Entries {
		r := pas.Map(e["resource"])
		if len(r) == 0 {
			continue
		}
		r = withNumbers(r)
		resources = append(resources, r)
		// The Da Vinci DTR libraries take the request being authorized as a parameter by its kind.
		for kind, name := range map[string]string{"DeviceRequest": "device_request", "ServiceRequest": "service_request",
			"MedicationRequest": "medication_request"} {
			if r["resourceType"] == kind && params[name] == nil {
				params[name] = &cql.Elem{V: r, Type: kind}
			}
		}
	}
	// Now is when the request was made, so a case decided again later reads the same.
	now := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, pas.Str(req.Claim["created"])); err == nil {
		now = t
	} else if t, err := time.Parse("2006-01-02", pas.Str(req.Claim["created"])); err == nil {
		now = t.Add(12 * time.Hour)
	}
	var out []Finding
	var notes []string
	for _, a := range p.Arguments {
		for _, s := range a.Find {
			if s.CQL == nil {
				continue
			}
			env := &cql.Env{Libraries: prog.Libraries, ValueSets: prog.ValueSets, Resources: resources,
				Parameters: params, Now: now}
			v, err := env.Evaluate(s.CQL.Library, s.CQL.Define)
			name := fmt.Sprintf("%s %q", s.CQL.Library, s.CQL.Define)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: %s: %v", a.Name, name, err))
				continue
			}
			b, ok := v.(bool)
			if !ok {
				if v != nil {
					notes = append(notes, fmt.Sprintf("%s: %s is %s, not true or false", a.Name, name, cql.Show(v)))
				}
				continue
			}
			read := env.Read()
			sort.Strings(read)
			over := "over none of the request's resources"
			if len(read) > 0 {
				over = "over " + strings.Join(read, ", ")
			}
			f := decide.Absent
			if b {
				f = decide.Present
			}
			out = append(out, Finding{Argument: a.Name, Finding: f, Citation: decide.Citation{
				Reference: Prefix + s.CQL.Library + "." + s.CQL.Define,
				Quote:     fmt.Sprintf("CQL %s is %v, %s", name, b, over)}})
		}
	}
	return out, notes
}

// withNumbers re-reads a resource so its numbers keep their text (an integer stays one), as the engine reads FHIR.
func withNumbers(r map[string]any) map[string]any {
	raw, err := json.Marshal(r)
	if err != nil {
		return r
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var out map[string]any
	if d.Decode(&out) != nil {
		return r
	}
	return out
}

// Pin is the line a policy pins a file with: cql FILE sha256 HEX.
func Pin(dir, file string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("cql %s sha256 %s", file, hex.EncodeToString(sum[:])), nil
}

// Cache loads each policy's program once per set of pins: a new version pinning other files gets its own, so a policy
// edited, scheduled or adopted while the server runs always runs exactly the files it names.
type Cache struct {
	Dir string // the policy directory the files are under

	mu   sync.Mutex
	byID map[string]*Program
}

// For is the policy's program (nil when it has no cql sources).
func (c *Cache) For(p *policy.Policy) (*Program, error) {
	if c == nil || len(p.CQL) == 0 {
		return nil, nil
	}
	var key strings.Builder
	key.WriteString(p.ID)
	for _, f := range p.CQL {
		key.WriteString("\x00" + f.File + "\x00" + f.SHA256)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prog, ok := c.byID[key.String()]; ok {
		return prog, nil
	}
	prog, err := Load(p, c.Dir)
	if err != nil {
		return nil, err
	}
	if c.byID == nil {
		c.byID = map[string]*Program{}
	}
	c.byID[key.String()] = prog
	return prog, nil
}
