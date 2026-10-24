package um

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/feed"
)

// Config is the server's -um file: which case-review system, and how to reach it. Secrets are named by environment
// variable, never written in the file.
type Config struct {
	// Name is the system's name on the cases and in the log ("guidingcare", "um"); default "um".
	Name string `json:"name,omitempty"`
	// Connector is "rest" (any JSON API, wired by field maps) or "fhir" (FHIR R4 Task).
	Connector string `json:"connector"`
	// URL is where cases are opened: the REST endpoint, or the FHIR server's base (Tasks go to URL/Task).
	URL string `json:"url"`
	// TokenEnv names a variable holding a bearer token for the system; HeaderEnv names variables for other headers
	// (header name -> variable), for systems that take an API key header.
	TokenEnv  string            `json:"tokenEnv,omitempty"`
	HeaderEnv map[string]string `json:"headerEnv,omitempty"`
	// Headers are fixed headers sent with every request (no secrets).
	Headers map[string]string `json:"headers,omitempty"`
	// CA is a PEM file of the certificate authorities the system's certificate is checked against, beside the
	// computer's own.
	CA string `json:"ca,omitempty"`
	// Console is Vaglio's own address as the plan's reviewers reach it (https://vaglio.plan.example): the case links
	// sent are built from it. Empty, no link is sent.
	Console string `json:"console,omitempty"`
	// PollEvery asks the system about open cases this often ("5m"), for a system that does not push its decisions;
	// empty, decisions arrive only at /api/feeds/um.
	PollEvery string `json:"pollEvery,omitempty"`
	// ReviewHere lets a linked case be decided in Vaglio's console too (off by default: it is worked in the system).
	ReviewHere bool `json:"reviewHere,omitempty"`

	// REST only. Fields maps the body sent to open a case: target field (dotted for nested objects) to a packet field
	// (case, link, receipt, due, policyTitle, missing, proofPdf ...), or "=text" for a fixed value. Empty, the whole
	// packet is sent as it is.
	Fields map[string]string `json:"fields,omitempty"`
	// RefField is where the answer gives the new case's id (dotted); empty, the last part of its Location header.
	// URLField is where it gives the case's address, if it does.
	RefField string `json:"refField,omitempty"`
	URLField string `json:"urlField,omitempty"`
	// DecisionURL is where one case's decision is asked for, {ref} standing for its id: polled with GET.
	DecisionURL string `json:"decisionUrl,omitempty"`
	// DecisionFields maps a decision's parts (case, ref, decision, reason, reviewer, npi, credential, decided) to where
	// the system's JSON has them (dotted); a part not named is read from the field of its own name.
	DecisionFields map[string]string `json:"decisionFields,omitempty"`
	// DecisionValues maps the system's words for its decisions to approve or deny ({"APPROVED": "approve"}); any other
	// value is not a decision yet (a case still in review).
	DecisionValues map[string]string `json:"decisionValues,omitempty"`
}

// LoadConfig reads a -um file.
func LoadConfig(file string) (Config, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", file, err)
	}
	if c.Name == "" {
		c.Name = "um"
	}
	return c, nil
}

// PollInterval is the configured poll interval, 0 for none.
func (c Config) PollInterval() (time.Duration, error) {
	if c.PollEvery == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.PollEvery)
	if err != nil || d < 10*time.Second {
		return 0, fmt.Errorf("pollEvery %q: a duration of at least 10s", c.PollEvery)
	}
	return d, nil
}

// Connect makes the connector the file describes. secret reads a named environment variable.
func (c Config) Connect(secret func(string) (string, error)) (Connector, error) {
	if err := feed.Check(c.URL); err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	if c.DecisionURL != "" {
		if err := feed.Check(strings.ReplaceAll(c.DecisionURL, "{ref}", "x")); err != nil {
			return nil, fmt.Errorf("decisionUrl: %w", err)
		}
	}
	if c.Console != "" {
		if u, err := url.Parse(c.Console); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("console %q: Vaglio's https address", c.Console)
		}
	}
	h := http.Header{}
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	if c.TokenEnv != "" {
		t, err := secret(c.TokenEnv)
		if err != nil || t == "" {
			return nil, fmt.Errorf("tokenEnv: %s is empty or unreadable", c.TokenEnv)
		}
		h.Set("Authorization", "Bearer "+t)
	}
	for k, env := range c.HeaderEnv {
		v, err := secret(env)
		if err != nil || v == "" {
			return nil, fmt.Errorf("headerEnv %s: %s is empty or unreadable", k, env)
		}
		h.Set(k, v)
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	if c.CA != "" {
		pem, err := os.ReadFile(c.CA)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca: no certificate in %s", c.CA)
		}
		cl.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	base := httpBase{client: cl, header: h}
	switch c.Connector {
	case "rest":
		for k, v := range c.DecisionValues {
			if v != "approve" && v != "deny" {
				return nil, fmt.Errorf("decisionValues %q: maps to approve or deny, not %q", k, v)
			}
		}
		for target, from := range c.Fields {
			if !strings.HasPrefix(from, "=") && !packetFields[from] {
				return nil, fmt.Errorf("fields %q: the packet has no %q (it has %s)", target, from, packetFieldList())
			}
		}
		for part := range c.DecisionFields {
			if !decisionParts[part] {
				return nil, fmt.Errorf("decisionFields: %q is not a part of a decision", part)
			}
		}
		return &REST{httpBase: base, cfg: c}, nil
	case "fhir":
		return &FHIR{httpBase: base, Base: strings.TrimRight(c.URL, "/")}, nil
	}
	return nil, fmt.Errorf("connector %q: rest or fhir", c.Connector)
}

// packetFields are the packet's parts by their JSON names, which fields may name.
var packetFields = func() map[string]bool {
	m := map[string]bool{}
	t := reflect.TypeOf(Packet{})
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		m[name] = true
	}
	return m
}()

func packetFieldList() string {
	var out []string
	for k := range packetFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

var decisionParts = map[string]bool{"case": true, "ref": true, "decision": true, "reason": true, "reviewer": true,
	"npi": true, "credential": true, "decided": true}

type httpBase struct {
	client *http.Client
	header http.Header
}

// do sends a request and reads at most 4 MB of the answer; anything but 2xx is an error carrying the system's words.
func (b httpBase) do(ctx context.Context, method, u, ctype string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range b.header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json, application/fhir+json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp, nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return resp, raw, fmt.Errorf("the case-review system answered %s: %s", resp.Status, msg)
	}
	return resp, raw, nil
}

// REST is any JSON API, wired to the packet and to decisions by field maps: no code for a system with a plain API.
type REST struct {
	httpBase
	cfg Config
}

// Open posts the packet (or the fields mapped from it) and reads the new case's id from the answer.
func (r *REST) Open(ctx context.Context, p Packet) (Opened, error) {
	raw, _ := json.Marshal(p)
	var body any
	if len(r.cfg.Fields) == 0 {
		body = json.RawMessage(raw)
	} else {
		var src map[string]any
		_ = json.Unmarshal(raw, &src)
		out := map[string]any{}
		for target, from := range r.cfg.Fields {
			var v any
			if lit, ok := strings.CutPrefix(from, "="); ok {
				v = lit
			} else if v, ok = get(src, from); !ok {
				continue // a part this case has none of (no missing evidence, no receipt): left out
			}
			if err := set(out, target, v); err != nil {
				return Opened{}, fmt.Errorf("fields: %w", err)
			}
		}
		body = out
	}
	b, _ := json.Marshal(body)
	resp, ans, err := r.do(ctx, http.MethodPost, r.cfg.URL, "application/json", b)
	if err != nil {
		return Opened{}, err
	}
	var o Opened
	var obj map[string]any
	_ = json.Unmarshal(ans, &obj)
	if r.cfg.RefField != "" {
		v, _ := get(obj, r.cfg.RefField)
		o.Ref = text(v)
	} else if loc := resp.Header.Get("Location"); loc != "" {
		o.Ref = path.Base(strings.TrimRight(loc, "/"))
	}
	if r.cfg.URLField != "" {
		v, _ := get(obj, r.cfg.URLField)
		o.URL = text(v)
	}
	if o.Ref == "" {
		return Opened{}, errors.New("the case-review system opened the case but its answer gives no id for it " +
			"(set refField, or have it answer with a Location header)")
	}
	return o, nil
}

// Poll asks for one case at decisionUrl.
func (r *REST) Poll(ctx context.Context, ref string) (Decision, bool, error) {
	if r.cfg.DecisionURL == "" {
		return Decision{}, false, ErrNoPoll
	}
	_, ans, err := r.do(ctx, http.MethodGet, strings.ReplaceAll(r.cfg.DecisionURL, "{ref}", url.PathEscape(ref)), "", nil)
	if err != nil {
		return Decision{}, false, err
	}
	var obj map[string]any
	if err := json.Unmarshal(ans, &obj); err != nil {
		return Decision{}, false, fmt.Errorf("the case-review system's answer is not a JSON object: %w", err)
	}
	d, decided := r.decision(obj)
	return d, decided, nil
}

// Parse reads one decision (a JSON object) or several (an array).
func (r *REST) Parse(body []byte) ([]Decision, error) {
	var objs []map[string]any
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &objs); err != nil {
			return nil, err
		}
	} else {
		var o map[string]any
		if err := json.Unmarshal(t, &o); err != nil {
			return nil, err
		}
		objs = []map[string]any{o}
	}
	var out []Decision
	for i, o := range objs {
		d, decided := r.decision(o)
		if !decided {
			return nil, fmt.Errorf("item %d: %q is not a decision (approve or deny, or a word decisionValues maps)", i, d.Decision)
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *REST) decision(obj map[string]any) (Decision, bool) {
	part := func(name string) string {
		p := r.cfg.DecisionFields[name]
		if p == "" {
			p = name
		}
		v, _ := get(obj, p)
		return strings.TrimSpace(text(v))
	}
	d := Decision{Case: part("case"), Ref: part("ref"), Reason: part("reason"), Reviewer: part("reviewer"),
		NPI: part("npi"), Credential: part("credential"), Decided: part("decided")}
	raw := part("decision")
	d.Decision = raw
	if len(r.cfg.DecisionValues) > 0 {
		for k, v := range r.cfg.DecisionValues {
			if strings.EqualFold(k, raw) {
				d.Decision = v
			}
		}
	} else {
		d.Decision = strings.ToLower(raw)
	}
	return d, d.Decision == "approve" || d.Decision == "deny"
}

// get reads a dotted path (a.b.c) from decoded JSON.
func get(v any, p string) (any, bool) {
	for _, k := range strings.Split(p, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// set writes a dotted path, making the objects on the way.
func set(m map[string]any, p string, v any) error {
	keys := strings.Split(p, ".")
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k]
		if !ok {
			n := map[string]any{}
			m[k], m = n, n
			continue
		}
		if m, ok = next.(map[string]any); !ok {
			return fmt.Errorf("%q is both a value and an object", p)
		}
	}
	m[keys[len(keys)-1]] = v
	return nil
}

// text is a JSON value as text: strings as they are, numbers and booleans as JSON writes them.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
