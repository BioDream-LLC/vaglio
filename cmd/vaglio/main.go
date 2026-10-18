// Command vaglio is the prior authorisation decision engine.
//
//	vaglio serve  [-addr ADDR] [-policies DIR] [-data DIR] [-token-env VAR] [-pas-url URL ...]   the API and web console
//	vaglio user   add|reset|remove|list -data DIR [-role ROLE] [NAME]    console accounts
//	vaglio audit  verify -data DIR                                       check the audit log's chain
//	vaglio receipt [-data DIR | -log FILE] RECEIPT                       check a decision's receipt against the log
//	vaglio decide -policy FILE REQUEST.json                one decision, as JSON
//	vaglio check  FILE...                                  every combination of evidence, per policy, and its warnings
//	vaglio compile FILE.vaglio                             the policy as the JSON the engine reads
//	vaglio fmt    FILE...                                  rewrite .vaglio files in the standard layout; print .json ones in it
//	vaglio draft  -id ID -out FILE.vaglio DOCUMENT.txt      draft a policy from a coverage document with a language model
//	vaglio questionnaire [-put URL] FILE.vaglio            the DTR Questionnaire the policy reads, made from it
//	vaglio report metrics|pends -data DIR [-from -to]      CMS prior authorisation metrics; what pends requests most
//	vaglio goldcard -data DIR -config FILE                 every provider's standing under the gold card programs
//	vaglio readings A.vaglio B.vaglio                     where two readings of one coverage document disagree
//	vaglio replay -data DIR FILE.vaglio                    which past decisions a new policy version would change
//	vaglio signoff -by "NAME, CREDENTIAL" FILE.vaglio      a clinician's sign-off: the draft becomes usable
//	vaglio version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/biodream-llc/vaglio/internal/acme"
	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/editor"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/precedence"
	"github.com/biodream-llc/vaglio/internal/reader"
	"github.com/biodream-llc/vaglio/internal/seal"
	"github.com/biodream-llc/vaglio/internal/server"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
	"github.com/biodream-llc/vaglio/library"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	// The data key, when the deployment sets one, seals every command's writes and opens its reads.
	if _, err := seal.FromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "vaglio:", err)
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "data":
		err = dataCmd(os.Args[2:], os.Stdout)
	case "standby":
		err = standbyCmd(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	case "decide":
		err = decideCmd(os.Args[2:], os.Stdout)
	case "check":
		var ok bool
		ok, err = check(os.Args[2:], os.Stdout)
		if err == nil && !ok {
			os.Exit(1)
		}
	case "user":
		err = userCmd(os.Args[2:], os.Stdout)
	case "audit":
		err = auditCmd(os.Args[2:], os.Stdout)
	case "compile":
		err = compile(os.Args[2:], os.Stdout)
	case "draft":
		err = draftCmd(os.Args[2:], os.Stdout, os.Stderr)
	case "fmt":
		err = format(os.Args[2:], os.Stdout)
	case "questionnaire":
		err = questionnaireCmd(os.Args[2:], os.Stdout)
	case "report":
		err = reportCmd(os.Args[2:], os.Stdout)
	case "goldcard":
		err = goldCardCmd(os.Args[2:], os.Stdout)
	case "replay":
		err = replayCmd(os.Args[2:], os.Stdout)
	case "signoff":
		err = signoffCmd(os.Args[2:], os.Stdout, time.Now)
	case "appeals":
		err = appealsCmd(os.Args[2:], os.Stdout)
	case "tls":
		err = tlsCmd(os.Args[2:], os.Stdout)
	case "receipt":
		err = receiptCmd(os.Args[2:], os.Stdout)
	case "readings":
		err = readingsCmd(os.Args[2:], os.Stdout)
	case "bench":
		err = benchCmd(os.Args[2:], os.Stdout)
	case "shadow":
		err = shadowCmd(os.Args[2:], os.Stdout)
	case "import-dtr":
		err = importDTRCmd(os.Args[2:], os.Stdout)
	case "version":
		fmt.Println("vaglio", version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vaglio:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  vaglio serve  [-addr 127.0.0.1:8740] [-policies DIR] [-data DIR] [-token-env VAR]
                [-pas-url URL] [-pas-token-env VAR] [-pas-ca FILE] [-tls-cert FILE -tls-key FILE] [-hostname NAME]
                [-goldcard FILE] [-clinician-denials] [-pas-api-url URL]
                [-document-model-url URL -document-model M [-document-model-key-env VAR] [-document-model-remote]]
                [-edit-policies] [-oidc-issuer URL -oidc-client-id ID -oidc-client-secret-env VAR
                 -oidc-redirect-url URL [-oidc-name-claim C] [-oidc-group G] [-oidc-label TEXT]]
                [-saml-metadata URL|FILE -saml-entity-id ID -saml-acs-url URL [-saml-name-attribute A] [-saml-group G]]
                [-extension-days N] [-appeals-token-env VAR] [-extract pdftotext,tesseract,model [-extract-model M]]
                [-draft-model-url URL [-draft-model M] [-draft-key-env VAR]] [-demo-account NAME:PASSWORD]
                [-fax-token-env VAR] [-acme [-acme-email ADDR] [-acme-directory URL]] [-plans FILE]
                [-read-documents [-approve-from-documents [-approve-sample PCT]] [-read-workers N]]
                [-criteria NAME=URL [-criteria-token-env NAME=VAR]] [-network FILE] [-npi-registry nppes|FILE] [-members FILE]
                [-webhook URL -webhook-secret-env VAR] [-fax-connectors FILE]
                (with VAGLIO_DATA_KEY set, every command seals patient data on disk; see vaglio data)
  vaglio user   add|reset|remove|list|provider -data DIR [-role viewer|reviewer|admin|provider] [-npi NPIs] [NAME]
  vaglio data   seal|status -data DIR
  vaglio standby -from https://PRIMARY -token-env VAR -data DIR [-ca FILE] [-every 2s]
  vaglio user   sso -data DIR -role ROLE NAME          an account that signs in only through single sign-on
  vaglio user   scopes -data DIR -scopes author,audit,appeals,intake,policy:ID NAME   (empty to clear)
  vaglio user   clinician -data DIR -npi NPI -credential "MD, specialty" NAME   (-clear to remove)
  vaglio audit  verify -data DIR
  vaglio receipt [-data DIR | -log FILE] [-json] VG-XXXX-XXXX-XXXX-XXXX   check a decision's receipt against the log
  vaglio decide -policy FILE REQUEST.json
  vaglio check  FILE...
  vaglio compile FILE.vaglio
  vaglio fmt    FILE...
  vaglio draft  -id ID -out FILE.vaglio [-model M] [-url URL] [-key-env VAR] DOCUMENT.txt
  vaglio questionnaire [-put FHIR-BASE -token-env VAR [-ca FILE]] FILE.vaglio
  vaglio report metrics|pends|overrides -data DIR [-from YYYY-MM-DD] [-to YYYY-MM-DD] [-csv] [-by clause|provider|state]
  vaglio goldcard -data DIR -config FILE
  vaglio replay -data DIR FILE.vaglio
  vaglio bench  [-policies DIR] [-n N] [-workers W]   how fast this machine decides (temporary data, invented requests)
  vaglio shadow -requests DIR [-outcomes FILE.csv] [-policies DIR] [-csv OUT.csv]
                                  a shadow pilot: past requests decided beside what the plan decided
  vaglio import-dtr -id ID -service "SYSTEM CODE..." -out FILE.vaglio QUESTIONNAIRE.json
                                  a draft from a DTR Questionnaire the plan already has (its yes/no questions)
  vaglio readings [-json] A.vaglio B.vaglio    where two readings of one coverage document decide a record differently
  vaglio signoff -by "NAME, CREDENTIAL" [-date YYYY-MM-DD] FILE.vaglio
  vaglio appeals import -url SERVER -token-env VAR [-ca FILE] FILE.csv    appeal outcomes, to the server's appeals feed
  vaglio tls    show|init -data DIR [-addr ADDR] [-hostname NAME]   Vaglio's own certificate authority, and how to trust it
  vaglio version
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8740", "listen address (always https); anything but loopback needs -hostname")
	dir := fs.String("policies", "policies", "directory of policy files (.vaglio, or .json)")
	data := fs.String("data", "data", "directory for the audit log, the cases' request Bundles and the user accounts")
	tokenEnv := fs.String("token-env", "", "name of an environment variable holding the bearer token /api/pas/ requires "+
		"(the token itself is never a flag)")
	pasURL := fs.String("pas-url", "", "the PAS server's FHIR base (such as https://127.0.0.1:8443/fhir): reviewers' decisions "+
		"on pended cases are sent there as Claim/$decide")
	pasTokenEnv := fs.String("pas-token-env", "", "name of an environment variable holding the bearer token for -pas-url")
	pasCA := fs.String("pas-ca", "", "a PEM file of certificate authorities to trust for -pas-url, beside the system's "+
		"(the PAS server's own or the plan's internal authority)")
	tlsCert := fs.String("tls-cert", "", "the server's certificate (PEM, with its chain): the plan's own or a public one; "+
		"reread when it changes. Without it, Vaglio's own authority in DATA/tls issues one")
	tlsKey := fs.String("tls-key", "", "the private key of -tls-cert (PEM)")
	var ac acmeOptions
	fs.BoolVar(&ac.On, "acme", false, "get a public certificate for -hostname from Let's Encrypt (or -acme-directory), kept "+
		"in DATA/tls/acme and renewed 30 days before it runs out; Vaglio's own authority serves until it arrives")
	fs.StringVar(&ac.Email, "acme-email", "", "the address the authority writes to about the certificate (optional)")
	fs.StringVar(&ac.Directory, "acme-directory", "", "another ACME authority's directory URL (default Let's Encrypt; "+
		acme.LetsEncryptStaging+" to test)")
	fs.StringVar(&ac.CA, "acme-ca", "", "a PEM file of authorities to trust for -acme-directory, beside the system's (a test authority)")
	fs.IntVar(&ac.HTTPPort, "acme-http-port", 80, "where the authority's http-01 check is answered (it asks on port 80; the "+
		"rest of that port redirects to https). 0 with Vaglio on 443: tls-alpn-01 only")
	hostname := fs.String("hostname", "", "the name browsers reach this server by, required off loopback (Host headers "+
		"naming anything else are refused)")
	goldCardFile := fs.String("goldcard", "", "gold card programs (JSON): providers whose record qualifies are approved "+
		"without review")
	docURL := fs.String("document-model-url", "", "an OpenAI-compatible chat API that reads attached documents for reviewers; "+
		"documents are patient records, so it must be on this machine unless -document-model-remote")
	docModel := fs.String("document-model", "", "the model -document-model-url serves")
	docKeyEnv := fs.String("document-model-key-env", "", "name of an environment variable holding its API key")
	docRemote := fs.Bool("document-model-remote", false, "allow a -document-model-url off this machine: only for a "+
		"provider the organisation has a business associate agreement with")
	pasAPI := fs.String("pas-api-url", "", "the PAS server's own API, for documents sent after a request (default: -pas-url "+
		"with /fhir replaced by /api, as Perfuse serves it)")
	clinicianDenials := fs.Bool("clinician-denials", false, "the engine never denies on its own: it proposes a denial, "+
		"and only an account recorded as a clinician may make it (for states that require a clinician to review AI denials)")
	draftURL := fs.String("draft-model-url", "", "an OpenAI-compatible chat completions API (https://openrouter.ai/api/v1, "+
		"a local server): the console's policy editor drafts policies from uploaded coverage documents with it. The "+
		"document is sent there: published coverage documents only, never patient records")
	draftModel := fs.String("draft-model", "meta-llama/llama-3.3-70b-instruct", "the model for -draft-model-url")
	draftKeyEnv := fs.String("draft-key-env", "", "name of an environment variable holding -draft-model-url's API key")
	demo := fs.String("demo-account", "", "NAME:PASSWORD - a public demonstration on invented data: a viewer account with "+
		"this published password, shown on the sign-in page, and a banner on every screen. Never on real patient data")
	useLibrary := fs.Bool("library", true, "decide with the policy library built into the program, as published, beside "+
		"-policies; a policy of the same id in -policies overrides the library's (the console's Override, with -edit-policies)")
	editPolicies := fs.Bool("edit-policies", false, "turn on the console's policy editor: drafts are saved in -policies/drafts, "+
		"and a draft a clinician signs off is written into -policies and put in force")
	oidcIssuer := fs.String("oidc-issuer", "", "single sign-on: the OpenID Connect provider's issuer URL")
	oidcClient := fs.String("oidc-client-id", "", "single sign-on: the client id registered with the provider")
	oidcSecretEnv := fs.String("oidc-client-secret-env", "", "single sign-on: name of an environment variable holding the client secret")
	oidcRedirect := fs.String("oidc-redirect-url", "", "single sign-on: this server's callback, https://HOST/api/sso/callback, as registered")
	oidcClaim := fs.String("oidc-name-claim", "preferred_username", "single sign-on: the ID token claim naming the account here")
	oidcGroup := fs.String("oidc-group", "", "single sign-on: a group the person must be in (read from the groups claim; "+
		"Microsoft Entra ID sends groups by object id, not name, and only when the application is set to send them)")
	oidcGroupsClaim := fs.String("oidc-groups-claim", "", "single sign-on: the claim -oidc-group is read from, groups by default "+
		"(roles for an Entra ID app role)")
	samlMetadata := fs.String("saml-metadata", "", "SAML sign-on: the identity provider's metadata, an https URL or a file "+
		"(Entra ID: the App Federation Metadata Url); instead of -oidc-*")
	samlEntity := fs.String("saml-entity-id", "", "SAML sign-on: this server's entity id, as registered (Entra ID: Identifier)")
	samlACS := fs.String("saml-acs-url", "", "SAML sign-on: https://HOST/api/sso/saml, as registered (Entra ID: Reply URL)")
	samlNameAttr := fs.String("saml-name-attribute", "", "SAML sign-on: the attribute naming the account here; default an email, "+
		"then the user principal name (Entra ID's), then the NameID")
	samlGroup := fs.String("saml-group", "", "SAML sign-on: a group the person must be in (Entra ID: its object id)")
	samlGroupsAttr := fs.String("saml-groups-attribute", "", "SAML sign-on: the attribute -saml-group is read from, groups by default")
	samlLabel := fs.String("saml-label", "", "SAML sign-on: the sign-in button's words")
	oidcLabel := fs.String("oidc-label", "", `single sign-on: the sign-in button's words, such as "Sign in with Example Health"`)
	extract := fs.String("extract", "", "what makes text of documents that are not text, tried in this order: pdftotext "+
		"(PDFs with text in them), tesseract (scans and faxes: images, TIFF and PDF), model (the document model reads each "+
		"page's picture: no program to install), comma-separated")
	extractModel := fs.String("extract-model", "", "for -extract model: a model that reads images, when -document-model does not")
	extensionDays := fs.Int("extension-days", 14, "the longest extension of a decision timeframe a reviewer may record (1 to 14)")
	faxTokenEnv := fs.String("fax-token-env", "", "name of an environment variable holding the bearer token the plan's "+
		"fax service sends with each fax it posts to /api/feeds/fax; set, the console's fax inbox is on (faxes kept in DATA/faxes)")
	appealsTokenEnv := fs.String("appeals-token-env", "", "name of an environment variable holding the bearer token the "+
		"appeals system sends to /api/feeds/appeals; unset, the feed is off")
	readDocs := fs.Bool("read-documents", false, "read a pended request's documents with -document-model-url as soon as it "+
		"is pended (and documents received later), so reviewers find the proposals waiting")
	approveDocs := fs.Bool("approve-from-documents", false, "with -read-documents: approve, with no person, a request the "+
		"policy approves on the readings' quote-checked findings. Never denies or pends: those wait for a reviewer")
	approveSample := fs.Int("approve-sample", 10, "the percent of automatic approvals set aside for a reviewer to check "+
		"after the fact (0 to 100)")
	readWorkers := fs.Int("read-workers", 2, "documents -read-documents reads at once")
	var criteria, criteriaTokens multiFlag
	fs.Var(&criteria, "criteria", "NAME=URL: a criteria service the plan runs or licenses, named by its policies' "+
		"\"find criteria NAME ...\" (repeatable). It is sent the request: https, or http on this machine")
	fs.Var(&criteriaTokens, "criteria-token-env", "NAME=VAR: the environment variable holding criteria service NAME's bearer token")
	faxConnectorsFile := fs.String("fax-connectors", "", "a JSON file naming where else faxes come from: sinch, documo "+
		"(webhooks), srfax, ringcentral (polled), folder (a scan folder)")
	replicaTokenEnv := fs.String("replica-token-env", "", "name of an environment variable holding the token a standby "+
		"copies this server's data with (vaglio standby)")
	notices := fs.String("notices", "", "the plan's notices file (JSON): mail the member their letter of a denial, an approval, an "+
		"extension or an appeal's outcome, as PDFs into a folder for a mail house or through Lob")
	webhook := fs.String("webhook", "", "push every record about a case, as signed JSON events, to this https URL")
	webhookSecretEnv := fs.String("webhook-secret-env", "", "name of an environment variable holding the webhook's signing secret (16+ bytes)")
	letters := fs.String("letters", "", "the plan's letter settings (JSON): its own appeal deadline as a date, appeals address, fax, "+
		"phone and portal, member services, and its receipt page; without it letters give the generic words")
	members := fs.String("members", "", "the plan's member file (CSV: member_id, birth_date, plan, line, start, end, excluded): "+
		"a request is approved or denied only for a member covered on the day; otherwise it pends")
	network := fs.String("network", "", "the plan's provider network: a file of NPIs (one per line, or a CSV's first column)")
	npiRegistry := fs.String("npi-registry", "", `where providers' specialties are read: "nppes" (the public NPI Registry API; `+
		`it is sent the provider's NPI only), or a CSV of npi,taxonomy... extracted from the NPPES download`)
	shadow := fs.Bool("shadow", false, "a shadow pilot: decide and record every request the PAS server sends, and answer "+
		"each \"decides nothing\" (404), so the plan's own process answers it as before; letters, webhooks, advice, eligibility, "+
		"NCPDP, the provider portal and intake are off. Needs a data directory of its own")
	outcomesTokenEnv := fs.String("outcomes-token-env", "", "for -shadow: name of an environment variable holding the bearer "+
		"token the plan's systems send its own decisions with, to /api/feeds/outcomes")
	plansFile := fs.String("plans", "", "serve several health plans from one server: a JSON file listing each plan's id, "+
		"name, hostname, policies and data directories, and its tokens and PAS server; each plan is its own console, accounts, "+
		"cases and audit log at its own hostname. The per-plan flags (-policies, -hostname, -token-env, -pas-*, -goldcard, "+
		"-fax-token-env, -appeals-token-env) are then the file's")
	fs.Parse(args)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *plansFile != "" {
		for _, f := range []string{"policies", "hostname", "token-env", "pas-url", "pas-token-env", "pas-ca", "pas-api-url",
			"goldcard", "fax-token-env", "appeals-token-env", "demo-account", "oidc-issuer", "oidc-client-id", "oidc-client-secret-env", "oidc-redirect-url", "oidc-group", "oidc-groups-claim", "oidc-label", "saml-metadata", "saml-entity-id", "saml-acs-url", "saml-name-attribute", "saml-group", "saml-groups-attribute", "saml-label", "criteria", "criteria-token-env",
			"network", "npi-registry", "members", "letters", "notices", "webhook", "webhook-secret-env", "fax-connectors", "replica-token-env"} {
			if set[f] {
				return fmt.Errorf("-%s is set per plan in the -plans file (the demonstration is for one plan only)", f)
			}
		}
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("-tls-cert and -tls-key go together")
	}
	var plans []planSpec
	if *plansFile != "" {
		var err error
		if plans, err = readPlans(*plansFile, *data); err != nil {
			return err
		}
	} else {
		crit := map[string]criteriaSpec{}
		for _, c := range criteria {
			name, u, ok := strings.Cut(c, "=")
			if !ok || name == "" || u == "" {
				return fmt.Errorf("-criteria %q is NAME=URL", c)
			}
			if _, dup := crit[name]; dup {
				return fmt.Errorf("-criteria %s is given twice", name)
			}
			crit[name] = criteriaSpec{URL: u}
		}
		for _, c := range criteriaTokens {
			name, v, ok := strings.Cut(c, "=")
			cs, known := crit[name]
			if !ok || !known {
				return fmt.Errorf("-criteria-token-env %q is NAME=VAR, for a NAME given with -criteria", c)
			}
			cs.TokenEnv = v
			crit[name] = cs
		}
		plans = []planSpec{{Hostname: strings.ToLower(*hostname), Policies: *dir, Data: *data, TokenEnv: *tokenEnv,
			PASURL: *pasURL, PASAPI: *pasAPI, PASTokenEnv: *pasTokenEnv, PASCA: *pasCA, GoldCard: *goldCardFile,
			FaxTokenEnv: *faxTokenEnv, AppealsTokenEnv: *appealsTokenEnv, Criteria: crit, Network: *network, Members: *members, Letters: *letters,
			Webhook: *webhook, Notices: *notices, WebhookSecretEnv: *webhookSecretEnv, FaxConnectors: *faxConnectorsFile,
			ReplicaTokenEnv: *replicaTokenEnv, Shadow: *shadow, OutcomesTokenEnv: *outcomesTokenEnv,
			NPIRegistry: *npiRegistry}}
		if *oidcIssuer != "" {
			plans[0].SSO = &ssoSpec{Issuer: *oidcIssuer, ClientID: *oidcClient, ClientSecretEnv: *oidcSecretEnv,
				RedirectURL: *oidcRedirect, NameClaim: *oidcClaim, Group: *oidcGroup, GroupsClaim: *oidcGroupsClaim,
				Label: *oidcLabel}
		}
		if *samlMetadata != "" {
			plans[0].SAML = &samlSpec{Metadata: *samlMetadata, EntityID: *samlEntity, ACSURL: *samlACS,
				NameAttribute: *samlNameAttr, Group: *samlGroup, GroupsAttribute: *samlGroupsAttr, Label: *samlLabel}
		}
	}
	names := planNames(plans)
	if err := ac.check(names, *tlsCert); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	loopback := false
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	allowed := []string{}
	switch {
	case loopback && len(plans) == 1:
		allowed = append(allowed, *addr, "localhost:"+port)
	case len(names) == 0:
		return errors.New("-hostname is required off loopback: it is the only Host header the server will accept")
	}
	for i := range plans {
		if h := plans[i].Hostname; h != "" {
			plans[i].allowed = append(plans[i].allowed, h, net.JoinHostPort(h, port))
		}
		if len(plans) == 1 {
			plans[i].allowed = append(plans[i].allowed, allowed...)
		}
		allowed = append(allowed, plans[i].allowed...)
	}
	slices.Sort(allowed)
	allowed = slices.Compact(allowed)
	// The console carries patient data and passwords: it is served over https only, on every address, loopback too.
	var certs *tlsserve.Source
	var authority *tlsserve.Authority
	if *tlsCert != "" {
		if certs, err = tlsserve.FileSource(*tlsCert, *tlsKey); err != nil {
			return fmt.Errorf("-tls-cert: %w", err)
		}
	} else {
		tn, err := tlsserve.NamesFor(*addr, "")
		if err != nil {
			return err
		}
		for _, n := range names {
			tn = tn.With(n)
		}
		if authority, err = tlsserve.Open(filepath.Join(*data, "tls"), tn); err != nil {
			return fmt.Errorf("vaglio's certificate authority: %w", err)
		}
		if certs, err = tlsserve.LocalSource(authority, tn); err != nil {
			return fmt.Errorf("vaglio's certificate authority: %w", err)
		}
	}
	var docs *server.DocumentModel
	var err2 error
	if *docURL != "" {
		if docs, err2 = documentModel(*docURL, *docModel, *docKeyEnv, *docRemote); err2 != nil {
			return err2
		}
	}
	sh := shared{docs: docs, clinicianDenials: *clinicianDenials, editPolicies: *editPolicies, demo: *demo}
	if *useLibrary {
		if sh.library, err2 = library.Policies(); err2 != nil {
			return err2
		}
	}
	if *draftURL != "" {
		if !*editPolicies && !slices.ContainsFunc(plans, func(p planSpec) bool { return p.EditPolicies }) {
			return errors.New("-draft-model-url drafts into the policy editor: it needs -edit-policies")
		}
		key := ""
		if *draftKeyEnv != "" {
			if key, err2 = secretFrom(*draftKeyEnv); err2 != nil {
				return fmt.Errorf("-draft-key-env: %w", err2)
			}
		}
		sh.drafter = &server.Drafter{Model: &draft.OpenAI{Base: *draftURL, Key: key, Model: *draftModel}, Name: *draftModel}
	}
	if *extract != "" {
		if docs == nil && *draftURL == "" && *faxTokenEnv == "" && *plansFile == "" {
			return errors.New("-extract makes text for the document model, drafting or the fax inbox: it needs " +
				"-document-model-url, -draft-model-url or -fax-token-env")
		}
		for _, name := range strings.Split(*extract, ",") {
			name = strings.TrimSpace(name)
			if name == "model" {
				// Pages read by the document model's own API, as pictures: the same endpoint, and the same rule that
				// patient records go off this machine only with -document-model-remote.
				if *docURL == "" {
					return errors.New("-extract model reads pages with the document model: it needs -document-model-url")
				}
				vm := *extractModel
				if vm == "" {
					vm = *docModel
				}
				key, err := secretFrom(*docKeyEnv)
				if err != nil {
					return fmt.Errorf("-document-model-key-env: %w", err)
				}
				sh.extractors = append(sh.extractors, reader.Vision(*docURL, key, vm))
				continue
			}
			x, err := reader.Lookup(name)
			if err != nil {
				return fmt.Errorf("-extract: %w", err)
			}
			sh.extractors = append(sh.extractors, x)
		}
	}
	if *extensionDays < 1 || *extensionDays > cases.MaxExtensionDays {
		return fmt.Errorf("-extension-days is 1 to %d", cases.MaxExtensionDays)
	}
	sh.extensionDays = *extensionDays
	switch {
	case *readDocs && docs == nil:
		return errors.New("-read-documents reads with the document model: it needs -document-model-url")
	case *approveDocs && !*readDocs:
		return errors.New("-approve-from-documents approves on what -read-documents reads: it needs -read-documents")
	case *approveSample < 0 || *approveSample > 100:
		return errors.New("-approve-sample is a percent, 0 to 100")
	case *readWorkers < 1 || *readWorkers > 16:
		return errors.New("-read-workers is 1 to 16")
	}
	sh.readDocs, sh.approveDocs, sh.approveSample, sh.readWorkers = *readDocs, *approveDocs, *approveSample, *readWorkers
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var opened []*openedPlan
	defer func() {
		for _, op := range opened {
			op.st.log.Close()
		}
	}()
	for _, sp := range plans {
		op, err := openPlan(ctx, sp, sh)
		if err != nil {
			return err
		}
		opened = append(opened, op)
	}
	for _, op := range opened {
		if op.spec.SAML != nil {
			sso, err := openSAML(ctx, op.spec, len(opened) > 1)
			if err != nil {
				return fmt.Errorf("%s%w", planPrefix(op, len(opened) > 1), err)
			}
			op.cfg.SSO = sso
			log.Printf("%sSAML sign-on: %s", planPrefix(op, len(opened) > 1), op.spec.SAML.Metadata)
			continue
		}
		if op.spec.SSO == nil {
			continue
		}
		sso, err := openSSO(ctx, op.spec, len(opened) > 1)
		if err != nil {
			return fmt.Errorf("%s%w", planPrefix(op, len(opened) > 1), err)
		}
		op.cfg.SSO = sso
		log.Printf("%ssingle sign-on: %s", planPrefix(op, len(opened) > 1), op.spec.SSO.Issuer)
	}
	var handler http.Handler
	if len(opened) == 1 {
		handler = server.New(opened[0].cfg)
	} else {
		hosts := map[string]http.Handler{}
		for _, op := range opened {
			hosts[op.spec.Hostname] = server.New(op.cfg)
		}
		handler = byHost(hosts)
	}
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	shown := *addr
	if len(names) == 1 {
		shown = net.JoinHostPort(names[0], port)
	}
	tlsCfg := tlsserve.Config(certs)
	var acmeMgr *acme.Manager
	if ac.On {
		p, _ := strconv.Atoi(port)
		if acmeMgr, err = startACME(ac, *data, names, p, certs); err != nil {
			return err
		}
		tlsCfg.GetConfigForClient = acmeMgr.ConfigForClient(tlsCfg)
	}
	ln, err := tlsserve.Listen(*addr, tlsCfg, allowed)
	if err != nil {
		return err
	}
	if acmeMgr != nil {
		p, _ := strconv.Atoi(port)
		go runACME(ctx, acmeMgr, ac, names, p, certs.Local())
	}
	if len(opened) == 1 {
		log.Printf("vaglio %s: https://%s/", version, shown)
	} else {
		log.Printf("vaglio %s: %d plans on %s", version, len(opened), *addr)
	}
	switch {
	case ac.On && !certs.Local():
		log.Printf("tls: certificate from %s, expires %s", certs.Leaf().Issuer.CommonName, certs.Leaf().NotAfter.Format(time.DateOnly))
	case ac.On:
		// Nobody needs to trust Vaglio's own authority: it only covers the minutes until the public certificate arrives.
	case authority != nil && authority.Created:
		describeAuthority(os.Stdout, authority, *data)
	case authority != nil:
		log.Printf("tls: certificate from Vaglio's own authority %s (SHA-256 %s); vaglio tls show for how to trust it",
			filepath.Join(*data, "tls", tlsserve.CAFile), authority.Fingerprint())
	default:
		log.Printf("tls: certificate from %s, expires %s", *tlsCert, certs.Leaf().NotAfter.Format(time.DateOnly))
	}
	for _, op := range opened {
		origin := "https://" + shown
		if op.spec.Hostname != "" {
			origin = "https://" + net.JoinHostPort(op.spec.Hostname, port)
		}
		op.report(origin)
	}
	if docs != nil {
		log.Printf("document model: %s at %s reads attachments for reviewers", docs.Name, *docURL)
	}
	if sh.readDocs {
		if sh.approveDocs {
			log.Printf("documents: read on arrival; approved without a person when the quote-checked findings approve "+
				"(%d%% set aside for a check after the fact; never a denial)", sh.approveSample)
		} else {
			log.Printf("documents: read on arrival, %d at once; reviewers confirm the proposals", sh.readWorkers)
		}
	}
	for _, x := range sh.extractors {
		log.Printf("document text: %s (%s) for %s", x.Name, x.Path, strings.Join(x.Types, ", "))
	}
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// documentModel is the model that reads attached documents. They are patient records: plain http only to this machine,
// and a model elsewhere only when the operator says the provider may hold them.
func documentModel(raw, model, keyEnv string, remote bool) (*server.DocumentModel, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("-document-model-url %q is not an http(s) URL", raw)
	}
	host := u.Hostname()
	local := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		local = true
	}
	switch {
	case !local && !remote:
		return nil, fmt.Errorf("-document-model-url %s is not on this machine, and documents are patient records: run the "+
			"model here, or pass -document-model-remote if the provider is covered by a business associate agreement", host)
	case !local && u.Scheme != "https":
		return nil, errors.New("-document-model-url off this machine must be https")
	case model == "":
		return nil, errors.New("-document-model-url needs -document-model")
	}
	key, err := secretFrom(keyEnv)
	if err != nil {
		return nil, fmt.Errorf("-document-model-key-env: %w", err)
	}
	return &server.DocumentModel{Name: model, Model: &draft.OpenAI{Base: raw, Key: key, Model: model}}, nil
}

// secretFrom reads a secret from the environment variable a flag names; no flag is no secret.
func secretFrom(env string) (string, error) {
	if env == "" {
		return "", nil
	}
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return "", fmt.Errorf("names %s, which is empty", env)
	}
	return v, nil
}

// checkPASURL refuses to send reviewers' decisions, and a token, anywhere but over TLS or to this machine.
func checkPASURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("-pas-url %q is not a URL", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("-pas-url must be https, or http to this machine: it carries patient decisions and a token")
}

// data is the server's data directory, opened.
type data struct {
	log       *audit.Log
	auditPath string
	cases     *cases.Store
	users     *auth.Users
}

func openData(dir string) (*data, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Windows reports every directory as mode 0777: its access is by ACL, which the operator sets (the service account
	// only). There is nothing here to check it against.
	if info, err := os.Stat(dir); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by others (mode %v); it holds patient data: chmod 700 %s", dir, info.Mode().Perm(), dir)
	}
	path := filepath.Join(dir, "audit.jsonl")
	l, err := audit.Open(path)
	if err != nil {
		return nil, err
	}
	cs, err := cases.Open(l, filepath.Join(dir, "bundles"))
	if err != nil {
		l.Close()
		return nil, err
	}
	users, err := auth.OpenUsers(filepath.Join(dir, "users.json"))
	if err != nil {
		l.Close()
		return nil, err
	}
	return &data{log: l, auditPath: path, cases: cs, users: users}, nil
}

// loadDir loads every policy in a directory, and refuses to start if any of them fails its exhaustive check: a policy
// that contradicts itself on some evidence must not be in front of a request.
func loadDir(dir string) (map[string]*policy.Policy, error) {
	live, _, err := loadPolicies(dir, nil)
	return live, err
}

// loadPolicies reads the policies in force in dir and those signed and waiting in dir/scheduled for their effective
// date. Every one must be signed and pass its check, and no two may tie for a request (package precedence). The library
// policies in lib (serve -library) decide too, each as published unless the directory holds the plan's own policy of the
// same id: that one overrides it.
func loadPolicies(dir string, lib map[string]*policy.Policy) (live, scheduled map[string]*policy.Policy, err error) {
	var files []string
	for _, ext := range []string{policy.Ext, ".json"} {
		found, err := filepath.Glob(filepath.Join(dir, "*"+ext))
		if err != nil {
			return nil, nil, err
		}
		files = append(files, found...)
	}
	if len(files) == 0 && len(lib) == 0 {
		return nil, nil, fmt.Errorf("no policies in %s", dir)
	}
	live = map[string]*policy.Policy{}
	var all []*policy.Policy
	for _, f := range files {
		p, err := policy.LoadFile(f)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := live[p.ID]; dup {
			return nil, nil, fmt.Errorf("%s: policy id %q is already loaded", f, p.ID)
		}
		if p.Status == policy.Draft {
			return nil, nil, fmt.Errorf("%s: policy %s is a draft; it decides nothing until a clinician signs it off "+
				"(vaglio signoff -by \"NAME, CREDENTIAL\", or the console with -edit-policies)", f, p.ID)
		}
		if r := decide.Check(p); !r.OK() {
			return nil, nil, fmt.Errorf("%s: fails its check (run vaglio check %s)", f, f)
		}
		live[p.ID] = p
		all = append(all, p)
	}
	for id, p := range lib {
		if _, overridden := live[id]; !overridden {
			live[id] = p
			all = append(all, p)
		}
	}
	if scheduled, err = (editor.Dir{Path: dir}).ScheduledPolicies(); err != nil {
		return nil, nil, err
	}
	for _, p := range scheduled {
		if r := decide.Check(p); !r.OK() {
			return nil, nil, fmt.Errorf("scheduled policy %s fails its check (run vaglio check)", p.ID)
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if why := precedence.Conflicts(all); why != "" {
		return nil, nil, errors.New(why)
	}
	return live, scheduled, nil
}

func decideCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("decide", flag.ExitOnError)
	file := fs.String("policy", "", "policy file")
	fs.Parse(args)
	if *file == "" || fs.NArg() != 1 {
		return errors.New("usage: vaglio decide -policy FILE REQUEST.json")
	}
	p, err := policy.LoadFile(*file)
	if err != nil {
		return err
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var req decide.Request
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
	res, err := decide.Decide(p, req)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func check(files []string, w io.Writer) (bool, error) {
	if len(files) == 0 {
		return false, errors.New("usage: vaglio check FILE...")
	}
	ok := true
	for _, f := range files {
		p, err := policy.LoadFile(f)
		if err != nil {
			return false, err
		}
		r := decide.Check(p)
		status := "PASS"
		if !r.OK() {
			status, ok = "FAIL", false
		}
		fmt.Fprintf(w, "%s  %s  (%d combinations of evidence)\n", status, p.ID, r.Combinations)
		keys := make([]string, 0, len(r.Outcomes))
		for k := range r.Outcomes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "      %-34s %d\n", k, r.Outcomes[k])
		}
		for _, u := range r.Unreachable {
			fmt.Fprintf(w, "      unreachable decision: %s\n", u)
		}
		for _, d := range r.Defects {
			fmt.Fprintf(w, "      defective on evidence: %v\n", d)
		}
		for _, l := range policy.Lint(p) {
			fmt.Fprintf(w, "      warning: %s\n", l)
		}
	}
	return ok, nil
}

func compile(args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: vaglio compile FILE.vaglio")
	}
	p, err := policy.LoadFile(args[0])
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// format rewrites each .vaglio file in place, only when it parses, and prints a .json policy in the language.
// head is a .vaglio file's leading comments, which fmt and signoff keep; it refuses a file with comments further down,
// which rewriting would drop.
// signoffCmd records a clinician's sign-off on a draft: the person who read every rule against the source says so, by
// name and credential, and the policy becomes one the server will use. A policy that fails its check, or has warnings
// other than its draft status, is not signed.
func signoffCmd(args []string, w io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("signoff", flag.ExitOnError)
	by := fs.String("by", "", `who signs, with their credential: "Dr A. Example, MD"`)
	date := fs.String("date", "", "the date signed, YYYY-MM-DD (default today)")
	fs.Parse(args)
	if fs.NArg() != 1 || strings.TrimSpace(*by) == "" || !strings.HasSuffix(fs.Arg(0), policy.Ext) {
		return errors.New(`usage: vaglio signoff -by "NAME, CREDENTIAL" [-date YYYY-MM-DD] FILE.vaglio`)
	}
	f := fs.Arg(0)
	if *date == "" {
		*date = now().Format(time.DateOnly)
	}
	src, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	signed, p, err := editor.SignOff(f, src, *by, *date)
	if err != nil {
		return err
	}
	if err := os.WriteFile(f, signed, 0o644); err != nil {
		return err
	}
	if editor.Scheduled(p, *date) {
		fmt.Fprintf(w, "%s signed off by %s on %s, effective %s: copy it into the policy directory's scheduled/ folder, and "+
			"the server puts it in force on that day\n", f, p.Signed.By, p.Signed.Date, p.Effective)
		return nil
	}
	fmt.Fprintf(w, "%s signed off by %s on %s; copy it into the policy directory and restart the server to use it\n", f, p.Signed.By, p.Signed.Date)
	return nil
}

func format(files []string, w io.Writer) error {
	if len(files) == 0 {
		return errors.New("usage: vaglio fmt FILE...")
	}
	for _, f := range files {
		p, err := policy.LoadFile(f)
		if err != nil {
			return err
		}
		out := policy.Format(p)
		if !strings.HasSuffix(f, policy.Ext) {
			fmt.Fprint(w, out)
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		// Comments are the author's: keep the ones at the head of the file, which is where a policy's notes go.
		h, err := editor.Head(f, src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(h+out), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// userCmd manages console accounts. It writes the users file the server reads, and the audit log, which it opens as the
// server does; run it while the server is up and the server picks the change up at the next request.
func userCmd(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: vaglio user add|reset|sso|scopes|clinician|provider|remove|list -data DIR [-role ROLE] [NAME]")
	}
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	role := fs.String("role", "", "viewer, reviewer or admin; or provider, for the provider portal")
	npi := fs.String("npi", "", "clinician: the person's NPI; provider: the NPIs the account acts for, comma-separated")
	credential := fs.String("credential", "", `clinician: the licence and specialty, such as "MD, orthopaedic surgery"`)
	clear := fs.Bool("clear", false, "clinician: remove the NPI and credential")
	scopes := fs.String("scopes", "", "scopes: a comma-separated list (author, audit, appeals, intake, policy:ID); empty clears them")
	fs.Parse(args[1:])
	users, err := auth.OpenUsers(filepath.Join(*dir, "users.json"))
	if err != nil {
		return err
	}
	if args[0] == "list" {
		list, err := users.List()
		if err != nil {
			return err
		}
		for _, u := range list {
			clin := ""
			if u.Clinician() {
				clin = fmt.Sprintf("  clinician NPI %s, %s", u.NPI, u.Credential)
			}
			extra := clin
			if u.SSO {
				extra += "  single sign-on"
			}
			if len(u.Scopes) > 0 {
				extra += "  scopes " + strings.Join(u.Scopes, ",")
			}
			if len(u.ProviderNPIs) > 0 {
				extra += "  acts for NPI " + strings.Join(u.ProviderNPIs, ",")
			}
			fmt.Fprintf(w, "%-24s %-9s created %s%s\n", u.Name, u.Role, u.Created, extra)
		}
		return nil
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: vaglio user %s -data DIR [-role ROLE] NAME", args[0])
	}
	name := fs.Arg(0)
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	l, err := audit.Open(filepath.Join(*dir, "audit.jsonl"))
	if err != nil {
		return err
	}
	defer l.Close()
	switch args[0] {
	case "add", "reset":
		existing, err := users.List()
		if err != nil {
			return err
		}
		var current *auth.User
		for i := range existing {
			if existing[i].Name == name {
				current = &existing[i]
			}
		}
		r := auth.Role(*role)
		switch {
		case args[0] == "add" && current != nil:
			return fmt.Errorf("user %s exists; vaglio user reset gives them a new password", name)
		case args[0] == "add" && r == "":
			return errors.New("add needs -role viewer, reviewer, admin or provider")
		case args[0] == "reset" && current == nil:
			return fmt.Errorf("no user %s", name)
		case args[0] == "reset" && r == "":
			r = current.Role
		}
		pw, err := users.Set(name, r)
		if err != nil {
			return err
		}
		action := "user.created"
		if args[0] == "reset" {
			action = "user.reset"
		}
		if _, err := l.Append("system", action, "", map[string]string{"name": name, "role": string(r), "by": "command line"}); err != nil {
			return err
		}
		fmt.Fprintf(w, "user %s (%s), password: %s\nIt is shown only now.\n", name, r, pw)
		return nil
	case "provider":
		var npis []string
		for _, n := range strings.Split(*npi, ",") {
			if n = strings.TrimSpace(n); n != "" {
				npis = append(npis, n)
			}
		}
		if err := users.SetProviderNPIs(name, npis); err != nil {
			return err
		}
		if _, err := l.Append("system", "user.provider", "", map[string]any{"name": name, "npis": npis, "by": "command line"}); err != nil {
			return err
		}
		fmt.Fprintf(w, "provider account %s acts for NPI %s in the provider portal\n", name, strings.Join(npis, ", "))
		return nil
	case "clinician":
		if *clear {
			*npi, *credential = "", ""
		} else if *npi == "" || *credential == "" {
			return errors.New("clinician needs -npi and -credential, or -clear")
		}
		if err := users.SetClinician(name, *npi, *credential); err != nil {
			return err
		}
		if _, err := l.Append("system", "user.clinician", "", map[string]string{"name": name, "npi": *npi,
			"credential": *credential, "by": "command line"}); err != nil {
			return err
		}
		if *clear {
			fmt.Fprintf(w, "user %s is no longer recorded as a clinician\n", name)
		} else {
			fmt.Fprintf(w, "user %s is recorded as a clinician: NPI %s, %s\n", name, *npi, *credential)
		}
		return nil
	case "sso":
		existing, ok, err := users.Get(name)
		if err != nil {
			return err
		}
		r := auth.Role(*role)
		if r == "" && ok {
			r = existing.Role
		}
		if r == "" {
			return errors.New("sso needs -role viewer, reviewer or admin")
		}
		if err := users.SetSSO(name, r); err != nil {
			return err
		}
		if _, err := l.Append("system", "user.sso", "", map[string]string{"name": name, "role": string(r), "by": "command line"}); err != nil {
			return err
		}
		fmt.Fprintf(w, "user %s (%s) signs in only through single sign-on, as %s at the identity provider; it has no password\n", name, r, name)
		return nil
	case "scopes":
		var list []string
		if *scopes != "" {
			list = strings.Split(*scopes, ",")
		}
		if err := users.SetScopes(name, list); err != nil {
			return err
		}
		u, _, _ := users.Get(name)
		if _, err := l.Append("system", "user.scopes", "", map[string]string{"name": name, "scopes": strings.Join(u.Scopes, ","),
			"by": "command line"}); err != nil {
			return err
		}
		fmt.Fprintf(w, "user %s scopes: %s\n", name, strings.Join(append([]string{}, u.Scopes...), ", "))
		return nil
	case "remove":
		if err := users.Remove(name); err != nil {
			return err
		}
		_, err := l.Append("system", "user.removed", "", map[string]string{"name": name, "by": "command line"})
		return err
	}
	return fmt.Errorf("vaglio user %s: unknown; add, reset, sso, scopes, clinician, remove or list", args[0])
}

func auditCmd(args []string, w io.Writer) error {
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: vaglio audit verify -data DIR")
	}
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	fs.Parse(args[1:])
	n, head, err := audit.Verify(filepath.Join(*dir, "audit.jsonl"))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "audit log intact: %d records, latest hash %s\nKeep that hash somewhere else: the log up to it can then "+
		"never be rewritten unnoticed.\n", n, head)
	return nil
}

// draftCmd drafts a policy from a coverage document. The document goes to the model's provider: use it for published
// coverage policies, never for anything holding patient data.
func draftCmd(args []string, w, errw io.Writer) error {
	fs := flag.NewFlagSet("draft", flag.ExitOnError)
	id := fs.String("id", "", "the policy's id")
	out := fs.String("out", "", "the .vaglio file to write; it must not exist")
	model := fs.String("model", "meta-llama/llama-3.3-70b-instruct", "the model")
	base := fs.String("url", "https://openrouter.ai/api/v1", "an OpenAI-compatible chat completions API; the document is sent there")
	keyEnv := fs.String("key-env", "OPENROUTER_API_KEY", "name of an environment variable holding the API key")
	rounds := fs.Int("rounds", 4, "drafts to try before giving up")
	fs.Parse(args)
	if *id == "" || *out == "" || fs.NArg() != 1 {
		return errors.New("usage: vaglio draft -id ID -out FILE.vaglio [-model M] [-url URL] [-key-env VAR] DOCUMENT.txt")
	}
	if !strings.HasSuffix(*out, policy.Ext) {
		return fmt.Errorf("-out is a %s file", policy.Ext)
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s exists; a draft never overwrites a policy", *out)
	}
	doc, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	key, err := secretFrom(*keyEnv)
	if err != nil {
		return fmt.Errorf("-key-env: %w", err)
	}
	m := &draft.OpenAI{Base: *base, Key: key, Model: *model}
	res, err := draft.Draft(context.Background(), m, draft.Request{ID: *id, Document: string(doc),
		Name: filepath.Base(fs.Arg(0)) + " with " + *model, Rounds: *rounds})
	for i, a := range res.Attempts {
		if len(a.Failures) == 0 {
			fmt.Fprintf(errw, "round %d: passed every check\n", i+1)
			continue
		}
		fmt.Fprintf(errw, "round %d: %d failure(s)\n", i+1, len(a.Failures))
		for _, f := range a.Failures {
			fmt.Fprintf(errw, "  - %s\n", f)
		}
	}
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(res.Source), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (status draft). Read every rule against the document before signing it off.\n", *out)
	return nil
}

// multiFlag is a flag that may be given more than once.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
