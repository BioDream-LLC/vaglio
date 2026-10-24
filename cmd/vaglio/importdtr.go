package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	cqlengine "github.com/biodream-llc/vaglio/internal/cql"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/terminology"
)

// importDTRCmd starts a policy from content a plan already has: a Da Vinci DTR Questionnaire (or a questionnaire
// package Bundle, with its CQL Libraries), as a draft. Each yes/no question becomes a piece of evidence found in the
// provider's answers (find question LINKID) under the questionnaire's canonical URL, with its words as the clause.
// The package's CQL libraries are written beside the policy and pinned, and each question the questionnaire pre-fills
// from a define is also found by running that define over the request (find cql), so the record and the answer are read
// side by side (internal/cql runs it). Where a question's pre-fill reaches a record lookup of a shape import-dtr reads
// (cql.go), a find line is also suggested beside it, as a comment. The draft has no rules (criteria and decisions): an author writes them, and a clinician signs the policy off. It
// decides nothing until then.
func importDTRCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("import-dtr", flag.ExitOnError)
	id := fs.String("id", "", "the policy's id")
	out := fs.String("out", "", "the .vaglio file to write; it must not exist")
	canonical := fs.String("questionnaire", "", "the questionnaire's canonical URL, when it carries none (answers are read by it)")
	service := fs.String("service", "", "what the policy decides: SYSTEM CODE..., such as \"cpt 72148 72149\"")
	runCQL := fs.Bool("cql", true, "write the package's CQL libraries beside the policy (cql/ID/), pin them, and run each "+
		"yes/no question's own pre-fill as evidence (find cql); -cql=false only lists them")
	libDir := fs.String("libraries", "", "a directory of .cql libraries the package includes but does not carry "+
		"(CDS Connect Commons, a plan's helpers): used for those, written and pinned with the rest")
	tx := fs.String("tx", "", "a FHIR terminology server (https://tx.fhir.org/r4) to expand the value sets the libraries "+
		"name and the package does not carry; the expansions are kept and pinned beside the policy (network, now only)")
	txKey := fs.String("tx-key-env", "", "environment variable holding the terminology server's API key (VSAC's UMLS key)")
	fs.Parse(args)
	if *id == "" || *out == "" || *service == "" || fs.NArg() != 1 {
		return errors.New("usage: vaglio import-dtr -id ID -service \"SYSTEM CODE...\" -out FILE.vaglio QUESTIONNAIRE-OR-BUNDLE.json")
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	dir := ""
	if *runCQL {
		dir = "cql/" + *id
	}
	opt := dtrOptions{}
	if *libDir != "" {
		opt.libraries = map[string][]byte{}
		names, _ := filepath.Glob(filepath.Join(*libDir, "*.cql"))
		for _, f := range names {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if l, err := cqlengine.Compile(string(b)); err == nil && l.Name != "" {
				opt.libraries[l.Name+"\x00"+l.Version], opt.libraries[l.Name] = b, b
			}
		}
	}
	if *tx != "" {
		key := ""
		if *txKey != "" {
			if key, err = secretFrom(*txKey); err != nil {
				return err
			}
		}
		opt.tx = &terminology.Server{Base: *tx, APIKey: key}
	}
	src, notes, files, err := importDTRWith(raw, *id, *service, *canonical, dir, opt)
	if err != nil {
		return err
	}
	// The libraries go beside the policy, under its directory, where the server reads them; one already there is kept
	// only if it is the same text (a different one would not match its pin).
	base := filepath.Dir(*out)
	for name, body := range files {
		path := filepath.Join(base, filepath.FromSlash(name))
		if old, err := os.ReadFile(path); err == nil {
			if string(old) != string(body) {
				return fmt.Errorf("%s exists with other text; move it, or give another -id", path)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s\n", path)
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s, a draft with %s\n", *out, notes)
	return nil
}

type qItem struct {
	LinkID     string  `json:"linkId"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`
	Item       []qItem `json:"item"`
	Extensions []struct {
		URL   string `json:"url"`
		Value *struct {
			Expression string `json:"expression"`
		} `json:"valueExpression"`
	} `json:"extension"`
}

type questionnaire struct {
	ResourceType string  `json:"resourceType"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Version      string  `json:"version"`
	Item         []qItem `json:"item"`
}

var (
	cqlDefine = regexp.MustCompile(`(?m)^define\s+(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*:`)
	notWord   = regexp.MustCompile(`[^a-z0-9]+`)
)

// importDTR makes the draft's source, and says what it holds.
func importDTR(raw []byte, id, service, canonical string) (string, string, error) {
	src, notes, _, err := importDTRFiles(raw, id, service, canonical, "")
	return src, notes, err
}

// importDTRFiles is importDTR with the package's CQL: its libraries (and any ValueSets it carries) as files to write
// under cqlDir, beside the policy, pinned in the draft by their SHA-256, and a find cql line on every yes/no question
// the questionnaire pre-fills from a define of a library that compiles and has every library it includes. cqlDir ""
// keeps the old behaviour: nothing run, find lines only suggested.
func importDTRFiles(raw []byte, id, service, canonical, cqlDir string) (string, string, map[string][]byte, error) {
	return importDTRWith(raw, id, service, canonical, cqlDir, dtrOptions{})
}

// dtrOptions are what import-dtr may add to a package: libraries it includes but lacks, by name (and name\x00version),
// and a terminology server for the value sets it names but does not carry.
type dtrOptions struct {
	libraries map[string][]byte
	tx        *terminology.Server
}

func importDTRWith(raw []byte, id, service, canonical, cqlDir string, opt dtrOptions) (string, string, map[string][]byte, error) {
	return importDTRInner(raw, id, service, canonical, cqlDir, opt)
}

func importDTRInner(raw []byte, id, service, canonical, cqlDir string, opt dtrOptions) (string, string, map[string][]byte, error) {
	var top struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return "", "", nil, fmt.Errorf("not JSON: %w", err)
	}
	var q *questionnaire
	var defines []string
	libs := map[string]*cqlLib{}
	compiled, cqlText := map[string]*cqlengine.Library{}, map[string][]byte{}
	var valueSets []json.RawMessage
	var notCompiled []string
	resources := []json.RawMessage{raw}
	if top.ResourceType == "Bundle" {
		resources = nil
		for _, e := range top.Entry {
			resources = append(resources, e.Resource)
		}
	}
	for _, r := range resources {
		var head struct {
			ResourceType string `json:"resourceType"`
			Content      []struct {
				ContentType string `json:"contentType"`
				Data        string `json:"data"`
			} `json:"content"`
		}
		json.Unmarshal(r, &head)
		switch head.ResourceType {
		case "Questionnaire":
			if q == nil {
				q = &questionnaire{}
				if err := json.Unmarshal(r, q); err != nil {
					return "", "", nil, err
				}
			}
		case "ValueSet":
			if vs, err := cqlengine.NewValueSet(r); err == nil {
				valueSets = append(valueSets, r)
				_ = vs
			}
		case "Library":
			for _, c := range head.Content {
				if c.ContentType != "text/cql" {
					continue
				}
				cql, err := base64.StdEncoding.DecodeString(c.Data)
				if err != nil {
					continue
				}
				for _, m := range cqlDefine.FindAllStringSubmatch(string(cql), -1) {
					defines = append(defines, m[1]+m[2])
				}
				if l := parseCQL(string(cql)); l.name != "" {
					libs[l.name] = l
				}
				if l, err := cqlengine.Compile(string(cql)); err == nil && l.Name != "" {
					compiled[l.Name], cqlText[l.Name] = l, cql
				} else if err != nil {
					notCompiled = append(notCompiled, err.Error())
				}
			}
		}
	}
	if q == nil {
		return "", "", nil, errors.New("no Questionnaire in it")
	}
	// Libraries the package includes but lacks, from -libraries: by name and version, else by name.
	var added []string
	for changed := true; changed && opt.libraries != nil; {
		changed = false
		for _, l := range compiled {
			for _, in := range l.Includes {
				if in.Name == "FHIRHelpers" || compiled[in.Name] != nil {
					continue
				}
				b := opt.libraries[in.Name+"\x00"+in.Version]
				if b == nil {
					b = opt.libraries[in.Name]
				}
				renamed := ""
				if b == nil {
					// Published content does not always agree with itself: a package that includes
					// CDS_Connect_Commons_for_FHIRv400 while the library calls itself CDSConnectCommonsForFHIRv400.
					// Matched ignoring case and underscores, and renamed to what the package includes, said so.
					for name, body := range opt.libraries {
						if !strings.Contains(name, "\x00") && loose(name) == loose(in.Name) {
							b, renamed = renameLibrary(body, name, in.Name), name
						}
					}
				}
				if b == nil {
					continue
				}
				if lib, err := cqlengine.Compile(string(b)); err == nil {
					if renamed != "" {
						added = append(added, fmt.Sprintf("%s (renamed from %s, as the package includes it)", lib.Name, renamed))
					}
					compiled[lib.Name], cqlText[lib.Name] = lib, b
					if pl := parseCQL(string(b)); pl.name != "" {
						libs[pl.name] = pl
					}
					if renamed == "" {
						added = append(added, lib.Name+" "+lib.Version)
					}
					changed = true
				}
			}
		}
	}
	// Value sets the libraries name that the package does not carry: expanded now, through -tx.
	var expanded, unexpanded []string
	if cqlDir != "" {
		have := map[string]bool{}
		for _, v := range valueSets {
			var h struct {
				URL string `json:"url"`
			}
			json.Unmarshal(v, &h)
			have[h.URL] = true
		}
		var want []cqlengine.Def
		for _, l := range compiled {
			for _, d := range l.ValueSets {
				if !have[d.ID] {
					have[d.ID] = true
					want = append(want, d)
				}
			}
		}
		sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
		for _, d := range want {
			if opt.tx == nil {
				unexpanded = append(unexpanded, d.ID)
				continue
			}
			vs, err := opt.tx.Expand(context.Background(), d.ID, d.Version)
			if err != nil {
				unexpanded = append(unexpanded, d.ID+" ("+err.Error()+")")
				continue
			}
			valueSets = append(valueSets, vs)
			expanded = append(expanded, d.ID)
		}
	}
	if canonical != "" {
		q.URL = canonical
	}
	if q.URL == "" {
		return "", "", nil, errors.New("the Questionnaire has no canonical url, and answers are read by it: give one with -questionnaire")
	}
	// The yes/no questions, anywhere in the tree.
	type ev struct{ name, link, text, expr string }
	var evs []ev
	seen := map[string]bool{}
	var walk func([]qItem)
	walk = func(items []qItem) {
		for _, it := range items {
			if it.Type == "boolean" && strings.TrimSpace(it.Text) != "" {
				name := strings.Trim(notWord.ReplaceAllString(strings.ToLower(it.LinkID), "_"), "_")
				if name == "" || (name[0] >= '0' && name[0] <= '9') {
					name = "q_" + name
				}
				for seen[name] {
					name += "_"
				}
				seen[name] = true
				e := ev{name: name, link: it.LinkID, text: strings.TrimSpace(it.Text)}
				for _, x := range it.Extensions {
					if x.Value != nil && strings.Contains(x.URL, "initialExpression") {
						e.expr = x.Value.Expression
					}
				}
				evs = append(evs, e)
			}
			walk(it.Item)
		}
	}
	walk(q.Item)
	if len(evs) == 0 {
		return "", "", nil, errors.New("the Questionnaire has no yes/no questions: there is nothing to read evidence from")
	}
	if len(evs) > policy.MaxEvidence {
		return "", "", nil, fmt.Errorf("%d yes/no questions, more than a policy's %d pieces of evidence", len(evs), policy.MaxEvidence)
	}
	title := q.Title
	if title == "" {
		title = q.Name
	}
	if title == "" {
		title = id
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# DRAFT, imported from the DTR Questionnaire %s", q.URL)
	if q.Version != "" {
		fmt.Fprintf(&b, " version %s", q.Version)
	}
	b.WriteString(".\n#\n# Each yes/no question is a piece of evidence, found in the provider's answers. The rules (criteria and\n" +
		"# decisions) are not imported: until they are written from the plan's coverage policy, the placeholder at the end\n" +
		"# pends everything. Then a clinician signs it off; as a draft it decides nothing.\n")
	// Of the CQL's defines, the ones the questions' expressions use: where the answers come from in the record.
	var used []string
	for _, d := range defines {
		for _, e := range evs {
			if strings.Contains(e.expr, "."+d) || strings.Contains(e.expr, `."`+d+`"`) {
				used = append(used, d)
				break
			}
		}
	}
	sort.Strings(used)
	used = slices.Compact(used)
	if len(used) > 0 {
		b.WriteString("#\n# The package's CQL pre-fills these questions from the record (the defines below): read them for what the\n" +
			"# plan counts as each. Where a question's CQL looks up the record in a shape this reader knows, a find line is\n" +
			"# suggested beside it as a comment: uncomment it only if the plan counts that record as the answer.\n")
		for _, d := range used {
			fmt.Fprintf(&b, "#   %s\n", d)
		}
	}
	b.WriteString("\n")
	// The package's libraries that can run: compiled, with every library they include in the package.
	files := map[string][]byte{}
	runnable := map[string]bool{}
	if cqlDir != "" {
		var ok func(name string, seen map[string]bool) bool
		ok = func(name string, seen map[string]bool) bool {
			l := compiled[name]
			if l == nil || seen[name] {
				return l != nil
			}
			seen[name] = true
			for _, in := range l.Includes {
				if in.Name != "FHIRHelpers" && !ok(in.Name, seen) {
					return false
				}
			}
			return true
		}
		for name := range compiled {
			runnable[name] = ok(name, map[string]bool{})
		}
	}
	runs := func(lib, define string) bool {
		if !runnable[lib] {
			return false
		}
		for _, d := range compiled[lib].Defines() {
			if d == define {
				return true
			}
		}
		return false
	}
	var pins []string
	need := map[string]bool{}
	var mark func(name string)
	mark = func(name string) {
		if need[name] || compiled[name] == nil {
			return
		}
		need[name] = true
		for _, in := range compiled[name].Includes {
			mark(in.Name)
		}
	}
	for _, e := range evs {
		if lib, def := prefill(e.expr); runs(lib, def) {
			mark(lib)
		}
	}
	names := make([]string, 0, len(need))
	for n := range need {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		file := cqlDir + "/" + n + ".cql"
		files[file] = cqlText[n]
		sum := sha256.Sum256(cqlText[n])
		pins = append(pins, fmt.Sprintf("  cql %s sha256 %s\n", file, hex.EncodeToString(sum[:])))
	}
	if len(names) > 0 && len(valueSets) > 0 {
		vb, _ := json.MarshalIndent(map[string]any{"resourceType": "Bundle", "type": "collection",
			"entry": func() []any {
				var out []any
				for _, v := range valueSets {
					out = append(out, map[string]any{"resource": v})
				}
				return out
			}()}, "", " ")
		file := cqlDir + "/valuesets.json"
		files[file] = vb
		sum := sha256.Sum256(vb)
		pins = append(pins, fmt.Sprintf("  cql %s sha256 %s\n", file, hex.EncodeToString(sum[:])))
	}
	fmt.Fprintf(&b, "policy %s\n  title %q\n  version 0.1.0\n  source %q\n  status draft\n  questionnaire %s\n  service %s\n",
		id, title, "DTR Questionnaire "+q.URL, q.URL, service)
	for _, p := range pins {
		b.WriteString(p)
	}
	runCount := 0
	suggested := 0
	for _, e := range evs {
		fmt.Fprintf(&b, "\nevidence %s\n  text %q\n  clause %q\n  find question %s\n", e.name, e.text, e.text, e.link)
		if lib, def := prefill(e.expr); runs(lib, def) {
			// The questionnaire's own pre-fill, run over the request: where it and the provider's answer disagree, the
			// case says so (a contradiction), and never decides on either alone.
			fmt.Fprintf(&b, "  find cql %s %q\n", lib, def)
			runCount++
		}
		if e.expr != "" {
			fmt.Fprintf(&b, "  # the questionnaire pre-fills it with: %s\n", strings.ReplaceAll(e.expr, "\n", " "))
			lib := libs[strings.Trim(strings.SplitN(e.expr, ".", 2)[0], `"`)]
			finds, notes := lib.suggest(e.expr)
			for _, f := range finds {
				fmt.Fprintf(&b, "  # suggested from its CQL: %s\n", f)
				suggested++
			}
			for _, n := range notes {
				fmt.Fprintf(&b, "  # note: %s\n", n)
			}
		}
	}
	// The one rule imported: nothing is approved until an author writes the rules. It keeps the draft a policy the
	// language accepts, and every request under it pends.
	b.WriteString(`
criterion rules_not_written
  text "The rules of this policy have not been written yet: replace this criterion and the decision below."
  clause "IMPORTED: the coverage rules are not in the questionnaire."

decision approve approves
  text "Approve."
  clause "IMPORTED: the coverage rules are not in the questionnaire."
  defeated by rules_not_written
`)
	msg := fmt.Sprintf("%d pieces of evidence (%d pre-filled by CQL; %d find lines suggested from it, as comments", len(evs),
		len(used), suggested)
	if cqlDir != "" {
		msg += fmt.Sprintf("; %d run by their CQL, from %d library file(s)", runCount, len(names))
		var missing []string
		for name, l := range compiled {
			for _, in := range l.Includes {
				if in.Name != "FHIRHelpers" && compiled[in.Name] == nil {
					missing = append(missing, name+" includes "+in.Name)
				}
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			msg += "; not run, as the package lacks a library they include: " + strings.Join(slices.Compact(missing), ", ")
		}
		if len(notCompiled) > 0 {
			msg += "; not compiled: " + strings.Join(notCompiled, "; ")
		}
		if len(added) > 0 {
			sort.Strings(added)
			msg += "; from -libraries: " + strings.Join(added, ", ")
		}
		if len(expanded) > 0 {
			msg += fmt.Sprintf("; %d value set(s) expanded through %s", len(expanded), opt.tx.Base)
		}
		if len(unexpanded) > 0 {
			msg += fmt.Sprintf("; %d value set(s) not expanded, so membership in them is unknown (pass -tx): %s",
				len(unexpanded), strings.Join(unexpanded, ", "))
		}
	}
	return b.String(), msg + "); no rules yet", files, nil
}

// loose is a library name ignoring case and underscores, for matching a package's includes to the libraries given.
func loose(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }

var libraryDecl = regexp.MustCompile(`(?m)^(\s*library\s+)("?)([A-Za-z0-9_]+)("?)`)

// renameLibrary rewrites a library's own declaration of its name.
func renameLibrary(body []byte, from, to string) []byte {
	done := false
	return libraryDecl.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := libraryDecl.FindSubmatch(m)
		if done || string(sub[3]) != from {
			return m
		}
		done = true
		return []byte(string(sub[1]) + to)
	})
}

// prefill is the library and define a DTR initialExpression names: %Lib.Define, "Lib"."Define", Lib.Define.
func prefill(expr string) (string, string) {
	e := strings.TrimPrefix(strings.TrimSpace(expr), "%")
	lib, def, ok := strings.Cut(e, ".")
	if !ok {
		return "", ""
	}
	lib, def = strings.Trim(lib, `"`), strings.Trim(def, `"`)
	if strings.ContainsAny(def, " ()[]") && !strings.HasPrefix(strings.TrimSpace(strings.SplitN(e, ".", 2)[1]), `"`) {
		return "", ""
	}
	return lib, def
}
