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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/deliver"
	"github.com/biodream-llc/vaglio/internal/draft"
	"github.com/biodream-llc/vaglio/internal/editor"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/oidc"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/reader"
	"github.com/biodream-llc/vaglio/internal/server"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
	"github.com/biodream-llc/vaglio/internal/versions"
	"github.com/biodream-llc/vaglio/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
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
                [-extension-days N] [-appeals-token-env VAR] [-extract pdftotext,tesseract]
  vaglio user   add|reset|remove|list -data DIR [-role viewer|reviewer|admin] [NAME]
  vaglio user   sso -data DIR -role ROLE NAME          an account that signs in only through single sign-on
  vaglio user   scopes -data DIR -scopes author,audit,appeals,policy:ID NAME   (empty to clear)
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
	editPolicies := fs.Bool("edit-policies", false, "turn on the console's policy editor: drafts are saved in -policies/drafts, "+
		"and a draft a clinician signs off is written into -policies and put in force")
	oidcIssuer := fs.String("oidc-issuer", "", "single sign-on: the OpenID Connect provider's issuer URL")
	oidcClient := fs.String("oidc-client-id", "", "single sign-on: the client id registered with the provider")
	oidcSecretEnv := fs.String("oidc-client-secret-env", "", "single sign-on: name of an environment variable holding the client secret")
	oidcRedirect := fs.String("oidc-redirect-url", "", "single sign-on: this server's callback, https://HOST/api/sso/callback, as registered")
	oidcClaim := fs.String("oidc-name-claim", "preferred_username", "single sign-on: the ID token claim naming the account here")
	oidcGroup := fs.String("oidc-group", "", "single sign-on: a group the person must be in (read from the groups claim)")
	oidcLabel := fs.String("oidc-label", "", `single sign-on: the sign-in button's words, such as "Sign in with Example Health"`)
	extract := fs.String("extract", "", "programs on this machine that make text of documents for the document model: "+
		"pdftotext (PDFs), tesseract (scanned images), comma-separated")
	extensionDays := fs.Int("extension-days", 14, "the longest extension of a decision timeframe a reviewer may record (1 to 14)")
	appealsTokenEnv := fs.String("appeals-token-env", "", "name of an environment variable holding the bearer token the "+
		"appeals system sends to /api/feeds/appeals; unset, the feed is off")
	fs.Parse(args)

	token, err := secretFrom(*tokenEnv)
	if err != nil {
		return fmt.Errorf("-token-env: %w", err)
	}
	pasToken, err := secretFrom(*pasTokenEnv)
	if err != nil {
		return fmt.Errorf("-pas-token-env: %w", err)
	}
	if *pasTokenEnv != "" && *pasURL == "" {
		return errors.New("-pas-token-env needs -pas-url")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("-tls-cert and -tls-key go together")
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
	case loopback:
		allowed = append(allowed, *addr, "localhost:"+port)
	case *hostname == "":
		return errors.New("-hostname is required off loopback: it is the only Host header the server will accept")
	}
	if *hostname != "" {
		h := strings.ToLower(*hostname)
		allowed = append(allowed, h, net.JoinHostPort(h, port))
	}
	// The console carries patient data and passwords: it is served over https only, on every address, loopback too.
	var certs *tlsserve.Source
	var authority *tlsserve.Authority
	if *tlsCert != "" {
		if certs, err = tlsserve.FileSource(*tlsCert, *tlsKey); err != nil {
			return fmt.Errorf("-tls-cert: %w", err)
		}
	} else {
		names, err := tlsserve.NamesFor(*addr, *hostname)
		if err != nil {
			return err
		}
		if authority, err = tlsserve.Open(filepath.Join(*data, "tls"), names); err != nil {
			return fmt.Errorf("vaglio's certificate authority: %w", err)
		}
		if certs, err = tlsserve.LocalSource(authority, names); err != nil {
			return fmt.Errorf("vaglio's certificate authority: %w", err)
		}
	}
	if *pasURL != "" {
		if err := checkPASURL(*pasURL); err != nil {
			return err
		}
	}

	policies, err := loadDir(*dir)
	if err != nil {
		return err
	}
	var docs *server.DocumentModel
	if *docURL != "" {
		if docs, err = documentModel(*docURL, *docModel, *docKeyEnv, *docRemote); err != nil {
			return err
		}
	}
	var gold *goldcard.Config
	if *goldCardFile != "" {
		if gold, err = goldcard.Load(*goldCardFile, policies); err != nil {
			return err
		}
	}
	st, err := openData(*data)
	if err != nil {
		return err
	}
	defer st.log.Close()
	if n, err := st.users.Count(); err != nil {
		return err
	} else if n == 0 {
		// Nobody can sign in to make the first account, so the first start makes it, and shows its password once.
		pw, err := st.users.Set("admin", auth.Admin)
		if err != nil {
			return err
		}
		st.log.Append("system", "user.created", "", map[string]string{"name": "admin", "role": "admin", "by": "first start"})
		fmt.Printf("  No users existed, so an administrator has been created.\n    username:  admin\n    password:  %s\n"+
			"  It is shown only now. Add reviewers with: vaglio user add -data %s -role reviewer <name>\n", pw, *data)
	}

	cfg := server.Config{
		Policies:     policies,
		Assets:       web.Assets(),
		AllowedHosts: allowed,
		Token:        token,
		Sessions:     auth.NewSessions(st.users),
		Audit:        st.log,
		AuditPath:    st.auditPath,
		Cases:        st.cases,
		Secure:       true,
		GoldCard:     gold,
		// Set on the command line, never in the console: who may deny care is the deployment's to decide.
		ClinicianDenials: *clinicianDenials,
		Reader:           docs,
	}
	if cfg.Versions, err = versions.Open(filepath.Join(*data, "versions")); err != nil {
		return err
	}
	if *editPolicies {
		cfg.PolicyDir = *dir
	}
	if *extract != "" {
		if docs == nil {
			return errors.New("-extract makes text for the document model: it needs -document-model-url")
		}
		for _, name := range strings.Split(*extract, ",") {
			x, err := reader.Lookup(strings.TrimSpace(name))
			if err != nil {
				return fmt.Errorf("-extract: %w", err)
			}
			cfg.Extractors = append(cfg.Extractors, x)
		}
	}
	if *extensionDays < 1 || *extensionDays > cases.MaxExtensionDays {
		return fmt.Errorf("-extension-days is 1 to %d", cases.MaxExtensionDays)
	}
	cfg.ExtensionDays = *extensionDays
	if *appealsTokenEnv != "" {
		if cfg.AppealsToken, err = secretFrom(*appealsTokenEnv); err != nil {
			return fmt.Errorf("-appeals-token-env: %w", err)
		}
		if cfg.AppealsToken == "" {
			return fmt.Errorf("-appeals-token-env: %s is empty", *appealsTokenEnv)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *oidcIssuer != "" {
		secret, err := secretFrom(*oidcSecretEnv)
		if err != nil {
			return fmt.Errorf("-oidc-client-secret-env: %w", err)
		}
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		prov, err := oidc.Discover(dctx, oidc.Config{Issuer: *oidcIssuer, ClientID: *oidcClient, ClientSecret: secret,
			RedirectURL: *oidcRedirect, NameClaim: *oidcClaim, Group: *oidcGroup})
		cancel()
		if err != nil {
			return fmt.Errorf("single sign-on: %w", err)
		}
		cfg.SSO = server.NewSSO(prov, *oidcLabel)
	}
	if *pasURL != "" {
		hc, err := tlsserve.Client(*pasCA, 20*time.Second)
		if err != nil {
			return fmt.Errorf("-pas-ca: %w", err)
		}
		client := &deliver.Client{Base: *pasURL, API: *pasAPI, Token: pasToken, HTTP: hc}
		cfg.Deliver = func(c cases.Case) { go client.Deliver(ctx, st.cases, c) }
		cfg.FetchDocuments = client.Documents
		go client.Run(ctx, st.cases, 30*time.Second)
	}
	srv := &http.Server{Addr: *addr, Handler: server.New(cfg), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	shown := *addr
	if *hostname != "" {
		shown = net.JoinHostPort(*hostname, port)
	}
	ln, err := tlsserve.Listen(*addr, tlsserve.Config(certs), allowed)
	if err != nil {
		return err
	}
	log.Printf("vaglio %s: %d policies, https://%s/", version, len(policies), shown)
	switch {
	case authority != nil && authority.Created:
		describeAuthority(os.Stdout, authority, *data)
	case authority != nil:
		log.Printf("tls: certificate from Vaglio's own authority %s (SHA-256 %s); vaglio tls show for how to trust it",
			filepath.Join(*data, "tls", tlsserve.CAFile), authority.Fingerprint())
	default:
		log.Printf("tls: certificate from %s, expires %s", *tlsCert, certs.Leaf().NotAfter.Format(time.DateOnly))
	}
	if gold != nil {
		log.Printf("gold card: %d program(s) from %s", len(gold.Programs), *goldCardFile)
	}
	if docs != nil {
		log.Printf("document model: %s at %s reads attachments for reviewers", docs.Name, *docURL)
	}
	for _, x := range cfg.Extractors {
		log.Printf("document text: %s (%s) for %s", x.Name, x.Path, strings.Join(x.Types, ", "))
	}
	if cfg.PolicyDir != "" {
		log.Printf("policy editor: drafts in %s", filepath.Join(cfg.PolicyDir, "drafts"))
	}
	if cfg.SSO.Provider != nil {
		log.Printf("single sign-on: %s", *oidcIssuer)
	}
	if *clinicianDenials {
		log.Printf("clinician denials: the engine proposes denials; only clinicians make them")
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
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o077 != 0 {
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
	var files []string
	for _, ext := range []string{policy.Ext, ".json"} {
		found, err := filepath.Glob(filepath.Join(dir, "*"+ext))
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no policies in %s", dir)
	}
	out := map[string]*policy.Policy{}
	claimed := map[policy.Service]string{} // service -> policy id
	for _, f := range files {
		p, err := policy.LoadFile(f)
		if err != nil {
			return nil, err
		}
		if _, dup := out[p.ID]; dup {
			return nil, fmt.Errorf("%s: policy id %q is already loaded", f, p.ID)
		}
		for _, sv := range p.Services {
			if other, dup := claimed[sv]; dup {
				return nil, fmt.Errorf("%s: %s %s is decided by policy %q as well; a service has one policy", f, sv.System, sv.Code, other)
			}
			claimed[sv] = p.ID
		}
		if p.Status == policy.Draft {
			return nil, fmt.Errorf("%s: policy %s is a draft; it decides nothing until a clinician signs it off "+
				"(vaglio signoff -by \"NAME, CREDENTIAL\", or the console with -edit-policies)", f, p.ID)
		}
		if r := decide.Check(p); !r.OK() {
			return nil, fmt.Errorf("%s: fails its check (run vaglio check %s)", f, f)
		}
		out[p.ID] = p
	}
	return out, nil
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
		return errors.New("usage: vaglio user add|reset|sso|scopes|clinician|remove|list -data DIR [-role ROLE] [NAME]")
	}
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	role := fs.String("role", "", "viewer, reviewer or admin")
	npi := fs.String("npi", "", "clinician: the person's NPI")
	credential := fs.String("credential", "", `clinician: the licence and specialty, such as "MD, orthopaedic surgery"`)
	clear := fs.Bool("clear", false, "clinician: remove the NPI and credential")
	scopes := fs.String("scopes", "", "scopes: a comma-separated list (author, audit, appeals, policy:ID); empty clears them")
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
			return errors.New("add needs -role viewer, reviewer or admin")
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
