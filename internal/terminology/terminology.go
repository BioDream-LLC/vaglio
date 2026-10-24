// Package terminology expands value sets through a FHIR terminology server (ValueSet/$expand), for the CQL a policy
// runs: the expansion is fetched once, when a policy is drafted or its value sets refreshed, and kept beside the
// policy, pinned by its hash like the libraries. Nothing is asked of a terminology server while deciding.
//
// tx.fhir.org (HL7's public server) expands many published value sets, VSAC's among them; VSAC itself
// (cts.nlm.nih.gov/fhir) needs a UMLS licence's API key, sent as the password of HTTP basic sign-in.
package terminology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Server is a FHIR terminology server.
type Server struct {
	Base   string       // https://tx.fhir.org/r4
	APIKey string       // for VSAC: the UMLS API key (basic sign-in, user "apikey"); "" for none
	Client *http.Client // nil for a client with a 60 s timeout
	Page   int          // codes asked for per request; 0 for 1000
}

// Expand returns the value set named by canonical URL (and version, "" for the server's current one) as a ValueSet
// resource with its whole expansion: every code, fetched page by page, with the server's own expansion timestamp and
// identifier, and a note of where it came from.
func (s *Server) Expand(ctx context.Context, canonical, version string) (json.RawMessage, error) {
	if !strings.HasPrefix(s.Base, "https://") && !strings.HasPrefix(s.Base, "http://127.0.0.1") &&
		!strings.HasPrefix(s.Base, "http://localhost") {
		return nil, errors.New("the terminology server is https (or plain http on this machine only)")
	}
	page := s.Page
	if page <= 0 {
		page = 1000
	}
	var first map[string]any
	var contains []any
	seen := 0 // entries the server gave, nested and abstract ones too (what its total counts)
	total := -1
	for offset := 0; total < 0 || seen < total; offset += page {
		vs, err := s.get(ctx, canonical, version, offset, page)
		if err != nil {
			return nil, err
		}
		exp, _ := vs["expansion"].(map[string]any)
		if exp == nil {
			return nil, fmt.Errorf("%s: the server's answer has no expansion", canonical)
		}
		got, _ := exp["contains"].([]any)
		contains = append(contains, flatten(got)...)
		seen += count(got)
		if first == nil {
			first = vs
		}
		if t, ok := exp["total"].(float64); ok {
			total = int(t)
		} else if total < 0 {
			total = seen // the server did not say: it gave everything at once
		}
		if len(got) == 0 {
			break
		}
	}
	if total >= 0 && seen < total {
		return nil, fmt.Errorf("%s: the server said %d codes and gave %d", canonical, total, seen)
	}
	exp, _ := first["expansion"].(map[string]any)
	expansion := map[string]any{"total": len(contains), "contains": contains,
		"parameter": []any{map[string]any{"name": "used-server", "valueUri": s.Base}}}
	for _, k := range []string{"identifier", "timestamp"} {
		if v, ok := exp[k]; ok && v != nil {
			expansion[k] = v
		}
	}
	out := map[string]any{"resourceType": "ValueSet", "url": canonical, "expansion": expansion}
	for _, k := range []string{"version", "name", "title", "status"} {
		if v, ok := first[k]; ok {
			out[k] = v
		}
	}
	return json.MarshalIndent(out, "", " ")
}

// count is how many entries contains holds, nested ones included.
func count(cs []any) int {
	n := 0
	for _, c := range cs {
		n++
		if m, ok := c.(map[string]any); ok {
			if sub, ok := m["contains"].([]any); ok {
				n += count(sub)
			}
		}
	}
	return n
}

// flatten lifts nested contains (a hierarchy) to one list of codes, dropping the abstract ones.
func flatten(cs []any) []any {
	var out []any
	for _, c := range cs {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if abs, _ := m["abstract"].(bool); !abs && m["code"] != nil {
			c := map[string]any{}
			for _, k := range []string{"system", "version", "code", "display"} {
				if v, ok := m[k]; ok && v != nil {
					c[k] = v
				}
			}
			out = append(out, c)
		}
		if sub, ok := m["contains"].([]any); ok {
			out = append(out, flatten(sub)...)
		}
	}
	return out
}

func (s *Server) get(ctx context.Context, canonical, version string, offset, count int) (map[string]any, error) {
	q := url.Values{"url": {canonical}, "offset": {strconv.Itoa(offset)}, "count": {strconv.Itoa(count)}}
	if version != "" {
		q.Set("valueSetVersion", version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.Base, "/")+"/ValueSet/$expand?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/fhir+json")
	if s.APIKey != "" {
		req.SetBasicAuth("apikey", s.APIKey)
	}
	hc := s.Client
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var vs map[string]any
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, fmt.Errorf("%s: %s, not FHIR JSON", canonical, resp.Status)
	}
	if resp.StatusCode != http.StatusOK || vs["resourceType"] != "ValueSet" {
		why := resp.Status
		if issues, ok := vs["issue"].([]any); ok && len(issues) > 0 {
			if m, ok := issues[0].(map[string]any); ok {
				if d, ok := m["diagnostics"].(string); ok {
					why += ": " + d
				} else if det, ok := m["details"].(map[string]any); ok {
					why += fmt.Sprintf(": %v", det["text"])
				}
			}
		}
		return nil, fmt.Errorf("expanding %s: %s", canonical, why)
	}
	return vs, nil
}
