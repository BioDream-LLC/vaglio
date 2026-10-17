package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/eligibility"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/server"
	"github.com/biodream-llc/vaglio/library"
)

// shadowCmd is a shadow pilot: the plan's past requests decided by Vaglio, beside what the plan decided, before Vaglio
// decides anything for real. Each request goes through the same path as in serve (a PAS request, decided on the day it
// was made, recorded in a temporary audit log that is removed afterwards); nothing is sent anywhere. The report says
// where Vaglio would have agreed, where it would have approved what the plan denied, where it would have denied what
// the plan approved, and what it would have asked for instead of deciding.
func shadowCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("shadow", flag.ExitOnError)
	reqDir := fs.String("requests", "", "a directory of the plan's past PAS request Bundles, one JSON file each")
	outcomes := fs.String("outcomes", "", "CSV of what the plan decided: claim, item, decision (approve or deny), and optionally "+
		"by (who decided) and appeal (overturned or upheld)")
	dir := fs.String("policies", "policies", "the plan's policy directory")
	useLib := fs.Bool("library", true, "decide with the built-in library too, as serve does")
	members := fs.String("members", "", "the plan's member file, as serve -members")
	csvOut := fs.String("csv", "", "also write one row per item decided to this CSV file")
	fs.Parse(args)
	if *reqDir == "" {
		return errors.New("shadow needs -requests DIR, the plan's past requests")
	}
	var lib map[string]*policy.Policy
	if *useLib {
		var err error
		if lib, err = library.Policies(); err != nil {
			return err
		}
	}
	live, _, err := loadPolicies(*dir, lib)
	if err != nil {
		return err
	}
	plan, err := readOutcomes(*outcomes)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "vaglio-shadow-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // the requests are patient records: nothing of them is kept
	st, err := openData(tmp)
	if err != nil {
		return err
	}
	defer st.log.Close()
	day := time.Now().Format(time.DateOnly)
	cfg := server.Config{Policies: live, Library: lib, Token: "shadow", Sessions: auth.NewSessions(st.users), Audit: st.log,
		AuditPath: st.auditPath, Cases: st.cases, Today: func() string { return day }}
	if *members != "" {
		if cfg.Members, err = eligibility.Open(*members); err != nil {
			return err
		}
	}
	h := server.New(cfg)

	files, err := filepath.Glob(filepath.Join(*reqDir, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	var rows []shadowRow
	unread := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		claim, created, items, ok := claimOf(raw)
		if !ok {
			unread++
			continue
		}
		if created != "" {
			day = created
		}
		for _, item := range items {
			req := httptest.NewRequest(http.MethodPost, "/api/pas/decide?item="+item, bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer shadow")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			row := shadowRow{File: filepath.Base(f), Claim: claim, Item: item}
			var a server.PASAnswer
			switch {
			case rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &a) == nil:
				row.Vaglio, row.Why = a.Decision, a.Why
				if a.Result != nil {
					row.Policy = a.Result.Policy
				}
				if row.Policy == "" && a.Decision == "pend" {
					row.Vaglio = "none" // no policy here decides this service
				}
				for _, m := range a.Requested {
					row.Missing = append(row.Missing, m)
				}
			default:
				row.Vaglio, row.Why = "error", strings.TrimSpace(rec.Body.String())
			}
			if o, ok := plan.find(claim, item); ok {
				row.Plan, row.By, row.Appeal = o.decision, o.by, o.appeal
			}
			rows = append(rows, row)
		}
	}
	if *csvOut != "" {
		if err := writeShadowCSV(*csvOut, rows); err != nil {
			return err
		}
	}
	shadowReport(w, rows, len(files), unread)
	return nil
}

type shadowRow struct {
	File, Claim, Item   string
	Policy, Vaglio, Why string
	Missing             []string
	Plan, By, Appeal    string
}

type outcome struct{ decision, by, appeal string }

type outcomeSet map[string]outcome

func (o outcomeSet) find(claim, item string) (outcome, bool) {
	if v, ok := o[claim+"#"+item]; ok {
		return v, true
	}
	if _, value, ok := strings.Cut(claim, "|"); ok { // the CSV may give the identifier's value alone
		v, ok := o[value+"#"+item]
		return v, ok
	}
	return outcome{}, false
}

func readOutcomes(path string) (outcomeSet, error) {
	out := outcomeSet{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"claim", "decision"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("%s: no %q column (claim, item, decision, by, appeal)", path, need)
		}
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		d := strings.ToLower(get(rec, "decision"))
		switch d {
		case "approve", "approved", "a1":
			d = "approve"
		case "deny", "denied", "a3":
			d = "deny"
		default:
			return nil, fmt.Errorf("%s line %d: decision %q is approve or deny", path, line, d)
		}
		item := get(rec, "item")
		if item == "" {
			item = "1"
		}
		out[get(rec, "claim")+"#"+item] = outcome{d, get(rec, "by"), strings.ToLower(get(rec, "appeal"))}
	}
	return out, nil
}

// claimOf is a request Bundle's Claim identifier (system|value), its creation day, and its items' sequence numbers.
func claimOf(raw []byte) (claim, day string, items []string, ok bool) {
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Created      string `json:"created"`
				Identifier   []struct {
					System, Value string
				} `json:"identifier"`
				Item []struct {
					Sequence int `json:"sequence"`
				} `json:"item"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return "", "", nil, false
	}
	for _, e := range b.Entry {
		r := e.Resource
		if r.ResourceType != "Claim" {
			continue
		}
		if len(r.Identifier) > 0 {
			claim = r.Identifier[0].System + "|" + r.Identifier[0].Value
		}
		if len(r.Created) >= 10 {
			day = r.Created[:10]
		}
		for _, it := range r.Item {
			items = append(items, fmt.Sprint(it.Sequence))
		}
		if len(items) == 0 {
			items = []string{"1"}
		}
		return claim, day, items, true
	}
	return "", "", nil, false
}

func writeShadowCSV(path string, rows []shadowRow) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"file", "claim", "item", "policy", "vaglio", "plan", "by", "appeal", "missing", "why"})
	for _, r := range rows {
		w.Write([]string{r.File, r.Claim, r.Item, r.Policy, r.Vaglio, r.Plan, r.By, r.Appeal, strings.Join(r.Missing, "; "),
			csvSafe(r.Why)})
	}
	w.Flush()
	return errors.Join(w.Error(), f.Close())
}

// csvSafe keeps a cell from being read as a formula by a spreadsheet.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

func shadowReport(w io.Writer, rows []shadowRow, files, unread int) {
	fmt.Fprintf(w, "# Shadow pilot\n\n%d request files, %d items", files, len(rows))
	if unread > 0 {
		fmt.Fprintf(w, " (%d files had no Claim and were not read)", unread)
	}
	fmt.Fprint(w, ". Vaglio decided each as it would have on the day it was made; nothing was sent anywhere, and nothing of them is kept.\n\n")
	count := map[string]int{}
	type key struct{ v, p string }
	grid := map[key]int{}
	var known []shadowRow
	for _, r := range rows {
		count[r.Vaglio]++
		if r.Plan != "" && r.Vaglio != "none" && r.Vaglio != "error" {
			grid[key{r.Vaglio, r.Plan}]++
			known = append(known, r)
		}
	}
	fmt.Fprintf(w, "Vaglio would have approved %d, denied %d and pended %d (asked for evidence); %d were for services no policy "+
		"here decides, and %d could not be read.\n\n", count["approve"], count["deny"], count["pend"], count["none"], count["error"])
	if len(known) == 0 {
		fmt.Fprint(w, "No outcomes were given (-outcomes), so there is nothing to compare with.\n")
		return
	}
	agree := grid[key{"approve", "approve"}] + grid[key{"deny", "deny"}]
	decided := agree + grid[key{"approve", "deny"}] + grid[key{"deny", "approve"}]
	fmt.Fprintf(w, "## Beside the plan's decisions (%d items with an outcome)\n\n", len(known))
	fmt.Fprint(w, "| Vaglio \\ the plan | approved | denied |\n|---|---|---|\n")
	for _, v := range []string{"approve", "deny", "pend"} {
		fmt.Fprintf(w, "| %s | %d | %d |\n", v, grid[key{v, "approve"}], grid[key{v, "deny"}])
	}
	if decided > 0 {
		fmt.Fprintf(w, "\nWhere both decided, they agreed on %d of %d (%.1f%%). A pend is not a disagreement: it is Vaglio "+
			"asking for evidence the record did not have, where the plan decided without it.\n", agree, decided,
			100*float64(agree)/float64(decided))
	}
	list := func(title, note string, keep func(shadowRow) bool) {
		var sel []shadowRow
		for _, r := range known {
			if keep(r) {
				sel = append(sel, r)
			}
		}
		if len(sel) == 0 {
			return
		}
		fmt.Fprintf(w, "\n## %s (%d)\n\n%s\n\n| Claim | Item | Policy | The plan | Appeal | Vaglio said |\n|---|---|---|---|---|---|\n",
			title, len(sel), note)
		for _, r := range sel {
			by := r.Plan
			if r.By != "" {
				by += " (" + r.By + ")"
			}
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", cell(r.Claim), r.Item, r.Policy, cell(by), r.Appeal, cell(r.Why))
		}
	}
	list("Vaglio would have approved what the plan denied",
		"Look at these first: a denial Vaglio's policy finds no ground for, which an appeal may already have overturned.",
		func(r shadowRow) bool { return r.Vaglio == "approve" && r.Plan == "deny" })
	list("Vaglio would have denied what the plan approved",
		"Either the policy reads stricter than the plan's practice, or the record shows something the reviewer weighed differently.",
		func(r shadowRow) bool { return r.Vaglio == "deny" && r.Plan == "approve" })
	missing := map[string]int{}
	for _, r := range known {
		if r.Vaglio == "pend" {
			for _, m := range r.Missing {
				missing[m]++
			}
		}
	}
	if len(missing) > 0 {
		var names []string
		for m := range missing {
			names = append(names, m)
		}
		sort.Slice(names, func(i, j int) bool { return missing[names[i]] > missing[names[j]] })
		fmt.Fprint(w, "\n## What Vaglio would have asked for instead of deciding\n\n| Evidence | Requests |\n|---|---|\n")
		for _, m := range names {
			fmt.Fprintf(w, "| %s | %d |\n", cell(m), missing[m])
		}
	}
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
