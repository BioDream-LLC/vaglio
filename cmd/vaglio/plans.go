package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/deliver"
	"github.com/biodream-llc/vaglio/internal/editor"
	"github.com/biodream-llc/vaglio/internal/eligibility"
	"github.com/biodream-llc/vaglio/internal/feed"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/intake"
	"github.com/biodream-llc/vaglio/internal/letter"
	"github.com/biodream-llc/vaglio/internal/notice"
	"github.com/biodream-llc/vaglio/internal/oidc"
	"github.com/biodream-llc/vaglio/internal/outside"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/reader"
	"github.com/biodream-llc/vaglio/internal/saml"
	"github.com/biodream-llc/vaglio/internal/seal"
	"github.com/biodream-llc/vaglio/internal/server"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
	"github.com/biodream-llc/vaglio/internal/versions"
	"github.com/biodream-llc/vaglio/internal/web"
)

// planSpec is one health plan a server decides for: its own policies, data (audit log, cases, accounts, versions,
// faxes), tokens and PAS server. With serve -plans, one server decides for several plans (a delegated utilization
// management organization's case), each at its own hostname, and nothing of one is reachable from another: a plan's
// console, accounts, sessions, cases, reports and audit chain are its own, and its PAS server's token opens only it.
type planSpec struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Policies string `json:"policies"`
	// Data is the plan's data directory; empty, DATA/plans/<id> under serve's -data.
	Data            string `json:"data,omitempty"`
	TokenEnv        string `json:"tokenEnv,omitempty"`
	PASURL          string `json:"pasUrl,omitempty"`
	PASAPI          string `json:"pasApiUrl,omitempty"`
	PASTokenEnv     string `json:"pasTokenEnv,omitempty"`
	PASCA           string `json:"pasCa,omitempty"`
	GoldCard        string `json:"goldcard,omitempty"`
	FaxTokenEnv     string `json:"faxTokenEnv,omitempty"`
	AppealsTokenEnv string `json:"appealsTokenEnv,omitempty"`
	// FaxConnectors is a file naming where else faxes come from: Sinch or Documo webhooks, SRFax or RingCentral polled,
	// a scan folder.
	FaxConnectors string `json:"faxConnectors,omitempty"`
	// ReplicaTokenEnv names the token a standby copies this plan's data with (vaglio standby).
	ReplicaTokenEnv string `json:"replicaTokenEnv,omitempty"`
	// Webhook is where every record about a case is pushed, signed with the secret in WebhookSecretEnv.
	Webhook string `json:"webhook,omitempty"`
	// Notices is the plan's notices file (JSON): which letters are mailed to the member, and how.
	Notices          string `json:"notices,omitempty"`
	WebhookSecretEnv string `json:"webhookSecretEnv,omitempty"`
	// ClinicianDenials: only clinicians deny this plan's requests (serve -clinician-denials turns it on for every plan).
	ClinicianDenials bool `json:"clinicianDenials,omitempty"`
	// EditPolicies: this plan's console has the policy editor (serve -edit-policies turns it on for every plan).
	EditPolicies bool `json:"editPolicies,omitempty"`
	// SSO is the plan's single sign-on: its own identity provider, client and callback on its own hostname.
	SSO *ssoSpec `json:"sso,omitempty"`
	// SAML is the plan's single sign-on through a SAML 2.0 identity provider instead (one of sso and saml).
	SAML *samlSpec `json:"saml,omitempty"`
	// Criteria are the plan's criteria services, by the name its policies give them: {"url": ..., "tokenEnv": ...}.
	Criteria map[string]criteriaSpec `json:"criteria,omitempty"`
	// Network is the plan's provider network file (NPIs); NPIRegistry is "nppes" for the public registry, or a file.
	Network string `json:"network,omitempty"`
	// Members is the plan's member file (CSV): coverage is checked before a request is approved or denied.
	Members string `json:"members,omitempty"`
	// Letters is the plan's letter settings (JSON): its appeal deadline, addresses and numbers, for letters and notices.
	Letters     string `json:"letters,omitempty"`
	NPIRegistry string `json:"npiRegistry,omitempty"`

	allowed []string // Host header values it answers to
}

// ssoSpec is a plan's OpenID Connect sign-in.
type ssoSpec struct {
	Issuer          string `json:"issuer"`
	ClientID        string `json:"clientId"`
	ClientSecretEnv string `json:"clientSecretEnv"`
	RedirectURL     string `json:"redirectUrl"`
	NameClaim       string `json:"nameClaim,omitempty"` // default preferred_username
	Group           string `json:"group,omitempty"`
	GroupsClaim     string `json:"groupsClaim,omitempty"` // default groups; roles for Entra ID app roles
	Label           string `json:"label,omitempty"`
}

// openSSO discovers a plan's identity provider. With several plans the callback must be on the plan's own hostname: a
// sign-in finished at another plan's address would make a session there.
func openSSO(ctx context.Context, sp planSpec, several bool) (server.SSO, error) {
	s := sp.SSO
	if s.Issuer == "" || s.ClientID == "" || s.RedirectURL == "" {
		return server.SSO{}, errors.New("single sign-on needs the issuer, the client id and the redirect URL")
	}
	u, err := url.Parse(s.RedirectURL)
	if err != nil || u.Scheme != "https" || u.Path != "/api/sso/callback" {
		return server.SSO{}, fmt.Errorf("single sign-on: the redirect URL is https://HOST/api/sso/callback, not %q", s.RedirectURL)
	}
	if several && !strings.EqualFold(u.Hostname(), sp.Hostname) {
		return server.SSO{}, fmt.Errorf("single sign-on: the redirect URL's host %s is not the plan's hostname %s", u.Hostname(), sp.Hostname)
	}
	secret, err := secretFrom(s.ClientSecretEnv)
	if err != nil {
		return server.SSO{}, fmt.Errorf("single sign-on client secret: %w", err)
	}
	claim := s.NameClaim
	if claim == "" {
		claim = "preferred_username"
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prov, err := oidc.Discover(dctx, oidc.Config{Issuer: s.Issuer, ClientID: s.ClientID, ClientSecret: secret,
		RedirectURL: s.RedirectURL, NameClaim: claim, Group: s.Group, GroupsClaim: s.GroupsClaim})
	if err != nil {
		return server.SSO{}, fmt.Errorf("single sign-on: %w", err)
	}
	return server.NewSSO(prov, s.Label), nil
}

// samlSpec is a plan's SAML 2.0 sign-in. The provider's metadata (a URL, fetched at start, or a file) gives its sign-on
// address and signing certificates; Vaglio trusts no other certificate.
type samlSpec struct {
	Metadata        string `json:"metadata"` // https URL or file of the identity provider's metadata
	EntityID        string `json:"entityId"` // this server's identifier, as registered (Entra ID: Identifier)
	ACSURL          string `json:"acsUrl"`   // https://HOST/api/sso/saml, as registered (Entra ID: Reply URL)
	NameAttribute   string `json:"nameAttribute,omitempty"`
	Group           string `json:"group,omitempty"`
	GroupsAttribute string `json:"groupsAttribute,omitempty"`
	Label           string `json:"label,omitempty"`
}

// openSAML reads a plan's identity provider metadata and makes its service provider.
func openSAML(ctx context.Context, sp planSpec, several bool) (server.SSO, error) {
	s := sp.SAML
	if sp.SSO != nil {
		return server.SSO{}, errors.New("single sign-on: give sso (OpenID Connect) or saml, not both")
	}
	if s.Metadata == "" || s.EntityID == "" || s.ACSURL == "" {
		return server.SSO{}, errors.New("SAML sign-on needs the provider's metadata, this server's entity id and its ACS URL")
	}
	u, err := url.Parse(s.ACSURL)
	if err != nil || u.Scheme != "https" || u.Path != "/api/sso/saml" {
		return server.SSO{}, fmt.Errorf("SAML sign-on: the ACS URL is https://HOST/api/sso/saml, not %q", s.ACSURL)
	}
	if several && !strings.EqualFold(u.Hostname(), sp.Hostname) {
		return server.SSO{}, fmt.Errorf("SAML sign-on: the ACS URL's host %s is not the plan's hostname %s", u.Hostname(), sp.Hostname)
	}
	var doc []byte
	if strings.HasPrefix(s.Metadata, "https://") {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(dctx, http.MethodGet, s.Metadata, nil)
		if err != nil {
			return server.SSO{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return server.SSO{}, fmt.Errorf("SAML sign-on: fetching the metadata: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return server.SSO{}, fmt.Errorf("SAML sign-on: the metadata URL answered %s", resp.Status)
		}
		if doc, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err != nil {
			return server.SSO{}, err
		}
	} else if strings.Contains(s.Metadata, "://") {
		return server.SSO{}, fmt.Errorf("SAML sign-on: the metadata URL must be https, not %q", s.Metadata)
	} else if doc, err = os.ReadFile(s.Metadata); err != nil {
		return server.SSO{}, fmt.Errorf("SAML sign-on: %w", err)
	}
	md, err := saml.ParseIDPMetadata(doc)
	if err != nil {
		return server.SSO{}, fmt.Errorf("SAML sign-on: %w", err)
	}
	prov, err := saml.New(saml.Config{EntityID: s.EntityID, ACSPath: s.ACSURL, IdPSSOURL: md.SSOURL, IdPCertPEM: md.CertPEM()})
	if err != nil {
		return server.SSO{}, err
	}
	return server.NewSAML(prov, s.NameAttribute, s.Group, s.GroupsAttribute, s.Label), nil
}

// criteriaSpec is one criteria service.
type criteriaSpec struct {
	URL      string `json:"url"`
	TokenEnv string `json:"tokenEnv,omitempty"`
	CA       string `json:"ca,omitempty"`
}

// openOutside makes a plan's outside sources, and refuses a policy reading one the plan lacks: its evidence would
// never be addressed, so every request would pend.
func openOutside(sp planSpec, policies map[string]*policy.Policy) (*outside.Sources, error) {
	src := &outside.Sources{Criteria: map[string]*outside.CriteriaService{}}
	for name, c := range sp.Criteria {
		token, err := secretFrom(c.TokenEnv)
		if err != nil {
			return nil, fmt.Errorf("criteria %s: %w", name, err)
		}
		hc, err := tlsserve.Client(c.CA, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("criteria %s: %w", name, err)
		}
		svc, err := outside.NewCriteriaService(name, c.URL, token, hc)
		if err != nil {
			return nil, err
		}
		src.Criteria[name] = svc
	}
	if sp.Network != "" {
		n, err := outside.LoadNetwork(sp.Network)
		if err != nil {
			return nil, err
		}
		src.Network = n
	}
	switch sp.NPIRegistry {
	case "":
	case "nppes":
		src.Registry = &outside.NPPES{}
	default:
		r, err := outside.LoadFileRegistry(sp.NPIRegistry)
		if err != nil {
			return nil, err
		}
		src.Registry = r
	}
	for _, p := range policies {
		for _, a := range p.Arguments {
			for _, s := range a.Find {
				switch {
				case s.Criteria != nil && src.Criteria[s.Criteria.Service] == nil:
					return nil, fmt.Errorf("policy %s reads criteria service %q, which is not configured (-criteria %s=URL)",
						p.ID, s.Criteria.Service, s.Criteria.Service)
				case s.Provider != nil && s.Provider.InNetwork && src.Network == nil:
					return nil, fmt.Errorf("policy %s asks whether the provider is in network: give the network file (-network)", p.ID)
				case s.Provider != nil && !s.Provider.InNetwork && src.Registry == nil:
					return nil, fmt.Errorf("policy %s reads the provider's specialty: give -npi-registry nppes, or a file", p.ID)
				}
			}
		}
	}
	return src, nil
}

// shared are the settings every plan of a server takes from serve's flags.
type shared struct {
	docs             *server.DocumentModel
	drafter          *server.Drafter
	extractors       reader.Extractors
	extensionDays    int
	clinicianDenials bool
	editPolicies     bool
	library          map[string]*policy.Policy // serve -library: the published library, nil when off
	demo             string
	readDocs         bool
	approveDocs      bool
	approveSample    int
	readWorkers      int
}

// openedPlan is a plan ready to serve.
type openedPlan struct {
	spec      planSpec
	cfg       server.Config
	st        *data
	moved     []*policy.Policy
	scheduled map[string]*policy.Policy
	gold      *goldcard.Config
	// connectors are the fax connectors started, for the start-up log.
	connectors []string
}

var planIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// readPlans reads a -plans file: {"plans": [planSpec...]}, every id and hostname distinct, every data and policy
// directory its plan's own.
func readPlans(file, sharedData string) ([]planSpec, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var f struct {
		Plans []planSpec `json:"plans"`
	}
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(f.Plans) == 0 {
		return nil, fmt.Errorf("%s names no plans", file)
	}
	seen := map[string]string{}
	claim := func(kind, v, id string) error {
		k := kind + "\x00" + v
		if other, dup := seen[k]; dup {
			return fmt.Errorf("%s: plans %s and %s share the %s %s; each plan's is its own", file, other, id, kind, v)
		}
		seen[k] = id
		return nil
	}
	for i := range f.Plans {
		p := &f.Plans[i]
		if !planIDRe.MatchString(p.ID) {
			return nil, fmt.Errorf("%s: plan id %q is lower case letters, digits and -", file, p.ID)
		}
		p.Hostname = strings.ToLower(strings.TrimSuffix(p.Hostname, "."))
		if p.Hostname == "" || net.ParseIP(p.Hostname) != nil || strings.ContainsAny(p.Hostname, ":/ ") {
			return nil, fmt.Errorf("%s: plan %s needs a hostname (a DNS name): it is how its requests and console reach it", file, p.ID)
		}
		if p.Policies == "" {
			return nil, fmt.Errorf("%s: plan %s needs its policies directory", file, p.ID)
		}
		if strings.TrimSpace(p.Name) == "" {
			p.Name = p.ID
		}
		if p.Data == "" {
			p.Data = filepath.Join(sharedData, "plans", p.ID)
		}
		absData, _ := filepath.Abs(p.Data)
		absPol, _ := filepath.Abs(p.Policies)
		for kind, v := range map[string]string{"id": p.ID, "hostname": p.Hostname, "data directory": absData,
			"policy directory": absPol, "PAS token variable": p.TokenEnv, "fax token variable": p.FaxTokenEnv} {
			if v == "" {
				continue
			}
			if err := claim(kind, v, p.ID); err != nil {
				return nil, err
			}
		}
		if absShared, _ := filepath.Abs(sharedData); absData == absShared {
			return nil, fmt.Errorf("%s: plan %s's data directory is the server's own -data; give it its own", file, p.ID)
		}
	}
	return f.Plans, nil
}

// openPlan opens one plan's data and policies and makes its server configuration.
func openPlan(ctx context.Context, sp planSpec, sh shared) (*openedPlan, error) {
	where := ""
	if sp.ID != "" {
		where = "plan " + sp.ID + ": "
	}
	fail := func(err error) (*openedPlan, error) { return nil, fmt.Errorf("%s%w", where, err) }
	var connectors []string
	token, err := secretFrom(sp.TokenEnv)
	if err != nil {
		return fail(fmt.Errorf("token-env: %w", err))
	}
	pasToken, err := secretFrom(sp.PASTokenEnv)
	if err != nil {
		return fail(fmt.Errorf("pas-token-env: %w", err))
	}
	if sp.PASTokenEnv != "" && sp.PASURL == "" {
		return fail(errors.New("a PAS token needs the PAS server's URL"))
	}
	if sp.PASURL != "" {
		if err := checkPASURL(sp.PASURL); err != nil {
			return fail(err)
		}
	}
	// A policy signed for a date that has come since the last start goes into force now, before anything decides.
	moved, err := (editor.Dir{Path: sp.Policies}).Activate(time.Now().Format(time.DateOnly))
	if err != nil {
		return fail(fmt.Errorf("putting scheduled policies in force: %w", err))
	}
	policies, scheduled, err := loadPolicies(sp.Policies, sh.library)
	if err != nil {
		return fail(err)
	}
	var members *eligibility.Members
	if sp.Members != "" {
		m, err := eligibility.Open(sp.Members)
		if err != nil {
			return fail(fmt.Errorf("-members: %w", err))
		}
		members = m
	}
	var letters *letter.Plan
	if sp.Letters != "" {
		if letters, err = letter.LoadPlan(sp.Letters); err != nil {
			return fail(fmt.Errorf("-letters: %w", err))
		}
	}
	src, err := openOutside(sp, policies)
	if err != nil {
		return fail(err)
	}
	for _, p := range scheduled {
		if _, err := openOutside(sp, map[string]*policy.Policy{p.ID: p}); err != nil {
			return fail(fmt.Errorf("scheduled: %w", err))
		}
	}
	var gold *goldcard.Config
	if sp.GoldCard != "" {
		if gold, err = goldcard.Load(sp.GoldCard, policies); err != nil {
			return fail(err)
		}
	}
	st, err := openData(sp.Data)
	if err != nil {
		return fail(err)
	}
	ok := false
	defer func() {
		if !ok {
			st.log.Close()
		}
	}()
	if n, err := st.users.Count(); err != nil {
		return fail(err)
	} else if n == 0 {
		// Nobody can sign in to make the first account, so the first start makes it, and shows its password once.
		pw, err := st.users.Set("admin", auth.Admin)
		if err != nil {
			return fail(err)
		}
		st.log.Append("system", "user.created", "", map[string]string{"name": "admin", "role": "admin", "by": "first start"})
		fmt.Printf("  %sNo users existed, so an administrator has been created.\n    username:  admin\n    password:  %s\n"+
			"  It is shown only now. Add reviewers with: vaglio user add -data %s -role reviewer <name>\n", where, pw, sp.Data)
	}
	cfg := server.Config{
		Policies:     policies,
		Library:      sh.library,
		Assets:       web.Assets(),
		AllowedHosts: sp.allowed,
		Token:        token,
		Sessions:     auth.NewSessions(st.users),
		Audit:        st.log,
		AuditPath:    st.auditPath,
		Cases:        st.cases,
		Secure:       true,
		GoldCard:     gold,
		// Set where the server is started, never in the console: who may deny care is the deployment's to decide.
		ClinicianDenials: sh.clinicianDenials || sp.ClinicianDenials,
		Reader:           sh.docs,
		Extractors:       sh.extractors,
		Drafter:          sh.drafter,
		ExtensionDays:    sh.extensionDays,
		Scheduled:        scheduled,
		PolicyRoot:       sp.Policies,
		Outside:          src,
		Members:          members,
		Letters:          letters,
	}
	if sp.FaxTokenEnv != "" {
		if cfg.FaxToken, err = secretFrom(sp.FaxTokenEnv); err != nil {
			return fail(fmt.Errorf("fax-token-env: %w", err))
		}
		if len(cfg.FaxToken) < 16 {
			return fail(errors.New("fax-token-env: the fax service's token is at least 16 characters"))
		}
		cfg.Faxes = &intake.Inbox{Dir: filepath.Join(sp.Data, "faxes")}
	}
	if sp.AppealsTokenEnv != "" {
		if cfg.AppealsToken, err = secretFrom(sp.AppealsTokenEnv); err != nil {
			return fail(fmt.Errorf("appeals-token-env: %w", err))
		}
		if cfg.AppealsToken == "" {
			return fail(fmt.Errorf("appeals-token-env: %s is empty", sp.AppealsTokenEnv))
		}
	}
	if sp.Webhook != "" {
		secret, err := secretFrom(sp.WebhookSecretEnv)
		if err != nil {
			return fail(fmt.Errorf("webhook-secret-env: %w", err))
		}
		hook := &feed.Webhook{URL: sp.Webhook, Secret: []byte(secret), Cursor: filepath.Join(sp.Data, "webhook.cursor"),
			Log: st.log}
		if err := hook.Start(ctx); err != nil {
			return fail(fmt.Errorf("webhook: %w", err))
		}
		cfg.Webhook = hook
	}
	if sp.Notices != "" {
		nc, sender, err := notice.LoadConfig(sp.Notices, secretFrom)
		if err != nil {
			return fail(fmt.Errorf("-notices: %w", err))
		}
		store := st.cases
		m := &notice.Mailer{Log: st.log, Cases: store, Plan: letters, Sender: sender, From: nc.From, Events: nc.Events,
			Cursor: filepath.Join(sp.Data, "notices.cursor"), Receipt: func(id string) string { return server.ReceiptOf(store, id) }}
		if err := m.Start(ctx); err != nil {
			return fail(fmt.Errorf("-notices: %w", err))
		}
		cfg.Notices = m
		log.Printf("notices to members of %s: %v, by %s", sp.Hostname, nc.Events, sender.Name())
	}
	if sh.demo != "" {
		name, pw, ok := strings.Cut(sh.demo, ":")
		if !ok {
			return fail(errors.New("-demo-account is NAME:PASSWORD"))
		}
		if err := st.users.SetPassword(name, pw); err != nil {
			return fail(fmt.Errorf("-demo-account: %w", err))
		}
		cfg.Demo, cfg.DemoName, cfg.DemoPassword = true, name, pw
	}
	if cfg.Versions, err = versions.Open(filepath.Join(sp.Data, "versions")); err != nil {
		return fail(err)
	}
	if sh.readDocs {
		cfg.AutoRead = &server.AutoRead{Approve: sh.approveDocs, SamplePercent: sh.approveSample, Workers: sh.readWorkers}
	}
	if sh.editPolicies || sp.EditPolicies {
		cfg.PolicyDir = sp.Policies
	} else if cfg.Drafter != nil {
		cfg.Drafter = nil // drafting writes into the editor; a plan without it does not draft
	}
	if sp.PASURL != "" {
		hc, err := tlsserve.Client(sp.PASCA, 20*time.Second)
		if err != nil {
			return fail(fmt.Errorf("pas-ca: %w", err))
		}
		client := &deliver.Client{Base: sp.PASURL, API: sp.PASAPI, Token: pasToken, HTTP: hc}
		cfg.Deliver = func(c cases.Case) { go client.Deliver(ctx, st.cases, c) }
		cfg.FetchDocuments = client.Documents
		go client.Run(ctx, st.cases, 30*time.Second)
	}
	cfg.BundleDir, cfg.UsersPath = filepath.Join(sp.Data, "bundles"), filepath.Join(sp.Data, "users.json")
	if sp.ReplicaTokenEnv != "" {
		t, err := secretFrom(sp.ReplicaTokenEnv)
		if err != nil || len(t) < 16 {
			return fail(fmt.Errorf("replica-token-env: the standby's token is at least 16 characters (%v)", err))
		}
		cfg.ReplicaToken = t
	}
	if sp.FaxConnectors != "" {
		started, err := openFaxConnectors(ctx, sp.FaxConnectors, &cfg, sp.Data)
		if err != nil {
			return fail(fmt.Errorf("fax-connectors: %w", err))
		}
		connectors = started
	}
	ok = true
	return &openedPlan{spec: sp, cfg: cfg, st: st, moved: moved, scheduled: scheduled, gold: gold, connectors: connectors}, nil
}

// report logs what a plan started with.
func (op *openedPlan) report(origin string) {
	who := ""
	if op.spec.ID != "" {
		who = op.spec.Name + " (" + op.spec.ID + "): "
	}
	log.Printf("%s%d policies, %s/", who, len(op.cfg.Policies), origin)
	if seal.On() {
		log.Printf("%spatient data on disk is sealed (AES-256-GCM, the key from %s)", who, seal.EnvKey)
	}
	for _, p := range op.moved {
		log.Printf("%spolicies: %s v%s in force from %s, its effective date", who, p.ID, p.Version, p.Effective)
		op.st.log.Append("system", server.ActInForce, "", map[string]string{"policy": p.ID, "version": p.Version,
			"effective": p.Effective})
	}
	for _, p := range op.scheduled {
		log.Printf("%spolicies: %s v%s is signed and scheduled: in force from %s", who, p.ID, p.Version, p.Effective)
	}
	if op.gold != nil {
		log.Printf("%sgold card: %d program(s) from %s", who, len(op.gold.Programs), op.spec.GoldCard)
	}
	if op.cfg.PolicyDir != "" {
		log.Printf("%spolicy editor: drafts in %s", who, filepath.Join(op.cfg.PolicyDir, "drafts"))
	}
	if op.cfg.Faxes != nil {
		log.Printf("%sfax inbox: the fax service posts to %s/api/feeds/fax; kept in %s", who, origin, op.cfg.Faxes.Dir)
	}
	if o := op.cfg.Outside; o != nil {
		for name, c := range o.Criteria {
			log.Printf("%scriteria service %s: %s", who, name, c.URL)
		}
		for _, c := range op.connectors {
			log.Printf("%sfaxes also from: %s", who, c)
		}
		if op.cfg.Webhook != nil {
			log.Printf("%swebhook: every record about a case goes to %s, signed", who, op.cfg.Webhook.URL)
		}
		if op.cfg.Members != nil {
			log.Printf("%smember file: %s, %d members; coverage is checked before a decision", who, op.spec.Members, op.cfg.Members.Size())
		}
		if o.Network != nil {
			log.Printf("%sprovider network: %s", who, o.Network.Source)
		}
		if o.Registry != nil {
			log.Printf("%sNPI registry: %s", who, op.spec.NPIRegistry)
		}
	}
	if op.cfg.ClinicianDenials {
		log.Printf("%sclinician denials: the engine proposes denials; only clinicians make them", who)
	}
}

// byHost sends each request to the plan its Host header names. A name no plan has is refused, and nothing in the
// answer says which plans this server holds.
func byHost(plans map[string]http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if h, ok := plans[host]; ok {
			h.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unexpected Host header", http.StatusMisdirectedRequest)
	})
}

// planNames is every plan's hostname, for the certificate.
func planNames(plans []planSpec) []string {
	var out []string
	for _, p := range plans {
		if p.Hostname != "" && !slices.Contains(out, p.Hostname) {
			out = append(out, p.Hostname)
		}
	}
	return out
}

// planPrefix names the plan in a message, when there are several.
func planPrefix(op *openedPlan, several bool) string {
	if !several {
		return ""
	}
	return op.spec.Name + " (" + op.spec.ID + "): "
}
