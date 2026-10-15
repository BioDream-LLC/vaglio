package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/biodream-llc/vaglio/internal/audit"
	"github.com/biodream-llc/vaglio/internal/cases"
	"github.com/biodream-llc/vaglio/internal/dtr"
	"github.com/biodream-llc/vaglio/internal/goldcard"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/replay"
	"github.com/biodream-llc/vaglio/internal/report"
	"github.com/biodream-llc/vaglio/internal/server"
	"github.com/biodream-llc/vaglio/internal/tlsserve"
)

// questionnaireCmd prints a policy's DTR Questionnaire, or with -put stores it on a FHIR server (Perfuse serves it to
// DTR apps from there) as Questionnaire/{id}.
func questionnaireCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("questionnaire", flag.ExitOnError)
	put := fs.String("put", "", "a FHIR base URL to PUT the Questionnaire to, such as https://127.0.0.1:8443/fhir")
	tokenEnv := fs.String("token-env", "", "name of an environment variable holding the bearer token for -put")
	caFile := fs.String("ca", "", "a PEM file of certificate authorities to trust for -put, beside the system's")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: vaglio questionnaire [-put FHIR-BASE -token-env VAR [-ca FILE]] FILE.vaglio")
	}
	p, err := policy.LoadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	q, err := dtr.For(p)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(q, "", "  ")
	if *put == "" {
		_, err = fmt.Fprintf(w, "%s\n", out)
		return err
	}
	if err := checkPASURL(*put); err != nil {
		return err
	}
	token, err := secretFrom(*tokenEnv)
	if err != nil {
		return fmt.Errorf("-token-env: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := strings.TrimRight(*put, "/") + "/Questionnaire/" + q.ID
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(out))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	hc, err := tlsserve.Client(*caFile, 30*time.Second)
	if err != nil {
		return fmt.Errorf("-ca: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("PUT %s: %s %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	fmt.Fprintf(w, "stored %s (%d questions), %s\n", url, len(q.Item), resp.Status)
	return nil
}

// openCases opens a data directory's cases for reading, beside a running server or not.
func openCases(dir string) (*cases.Store, func(), error) {
	l, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	cs, err := cases.Open(l, filepath.Join(dir, "bundles"))
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	return cs, func() { l.Close() }, nil
}

func reportCmd(args []string, w io.Writer) error {
	kinds := []string{"metrics", "pends", "overrides", "list"}
	if len(args) == 0 || !slices.Contains(kinds, args[0]) {
		return errors.New("usage: vaglio report metrics|pends|overrides|list -data DIR [-from YYYY-MM-DD] [-to YYYY-MM-DD] " +
			"[-csv] [-policies DIR] [-by clause|provider|state] [-min-requests N] [-min-approved PCT] [-min-reversed PCT]")
	}
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	from := fs.String("from", "", "first day (default: 1 January of -to's year)")
	to := fs.String("to", "", "last day (default: today)")
	csv := fs.Bool("csv", false, "metrics: as CSV in the shape of CMS's template")
	policies := fs.String("policies", "", "pends and overrides: the policy directory, to name questions and clauses")
	by := fs.String("by", "clause", "overrides: group by clause, provider or state")
	crit := report.DefaultListCriteria
	fs.IntVar(&crit.MinRequests, "min-requests", crit.MinRequests, "list: the fewest decided requests for a verdict")
	fs.Float64Var(&crit.MinApprovedPercent, "min-approved", crit.MinApprovedPercent, "list: approved at least this often "+
		"(percent) to be a candidate to stop reviewing")
	fs.Float64Var(&crit.MinReversedPercent, "min-reversed", crit.MinReversedPercent, "list: a candidate with denials had "+
		"at least this share of them reversed (percent)")
	fs.Parse(args[1:])
	p, err := report.ParsePeriod(*from, *to, time.Now().UTC())
	if err != nil {
		return err
	}
	cs, done, err := openCases(*dir)
	if err != nil {
		return err
	}
	defer done()
	all := cs.Select(nil)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	defer tw.Flush()
	if args[0] == "metrics" {
		m := report.Build(all, p)
		if *csv {
			_, err := io.WriteString(w, m.CSV())
			return err
		}
		fmt.Fprintf(tw, "Prior authorization metrics, %s to %s\tstandard\texpedited\n", m.From, m.To)
		for _, line := range strings.Split(strings.TrimSpace(m.CSV()), "\n")[1:] {
			name, rest, _ := strings.Cut(line, `",`)
			fmt.Fprintf(tw, "%s\t%s\n", strings.TrimPrefix(name, `"`), strings.ReplaceAll(rest, ",", "\t"))
		}
		if m.Superseded > 0 {
			fmt.Fprintf(tw, "(%d request(s) replaced by a resubmission before a decision are not counted)\n", m.Superseded)
		}
		return nil
	}
	var ps map[string]*policy.Policy
	if *policies != "" {
		if ps, err = loadDir(*policies); err != nil {
			return err
		}
	}
	if args[0] == "list" {
		l := report.BuildListReview(all, p, ps, crit)
		if *csv {
			_, err := io.WriteString(w, l.CSV())
			return err
		}
		fmt.Fprintf(tw, "Prior authorization list review, %s to %s\trequests\tapproved\tdenials reversed\tverdict\n", l.From, l.To)
		for _, r := range l.Rows {
			rev := "no denials"
			if r.ReversedPercent != nil {
				rev = fmt.Sprintf("%d of %d", r.Reversed, r.EngineDenials)
			}
			fmt.Fprintf(tw, "  %s %s\t%d\t%.1f%%\t%s\t%s\n", r.Code, r.Title, r.Requests, r.ApprovedPercent, rev, r.Verdict)
		}
		if len(l.Rows) == 0 {
			fmt.Fprintln(tw, "  no requests in this period")
		}
		tw.Flush()
		fmt.Fprintf(w, "\n%s\n", l.Note)
		return nil
	}
	if args[0] == "overrides" {
		o, err := report.BuildOverrides(all, p, *by, ps)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "Engine denials reversed, by %s, %s to %s\tdenials\toverridden\tappealed\toverturned\treversed\n", o.By, o.From, o.To)
		for _, r := range o.Rows {
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%.0f%%\n", r.Label, r.Denials, r.Overridden, r.Appealed, r.Overturned, r.ReversalPercent)
		}
		if len(o.Rows) == 0 {
			fmt.Fprintln(tw, "  no denials in this period")
		}
		return nil
	}
	r := report.BuildPends(all, p, ps)
	fmt.Fprintf(tw, "Requests pended for information, %s to %s:", r.From, r.To)
	if len(r.ForInformation) == 0 {
		fmt.Fprintln(tw, " none")
	}
	fmt.Fprintln(tw)
	for id, n := range r.ForInformation {
		fmt.Fprintf(tw, "  %s\t%d\n", id, n)
	}
	if len(r.Missing) > 0 {
		fmt.Fprintln(tw, "\nMissing most often\tpends\tshare\tquestion")
		for _, c := range r.Missing {
			fmt.Fprintf(tw, "  %s/%s\t%d\t%.0f%%\t%s\n", c.Policy, c.Argument, c.Pends, c.Percent, c.Question)
		}
	}
	if len(r.Contradicted) > 0 {
		fmt.Fprintln(tw, "\nContradicted in the record\tpends\tshare")
		for _, c := range r.Contradicted {
			fmt.Fprintf(tw, "  %s/%s\t%d\t%.0f%%\n", c.Policy, c.Argument, c.Pends, c.Percent)
		}
	}
	return nil
}

func goldCardCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("goldcard", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	config := fs.String("config", "", "the gold card programs (JSON), as serve -goldcard reads them")
	fs.Parse(args)
	if *config == "" {
		return errors.New("usage: vaglio goldcard -data DIR -config FILE")
	}
	gc, err := goldcard.Load(*config, nil)
	if err != nil {
		return err
	}
	cs, done, err := openCases(*dir)
	if err != nil {
		return err
	}
	defer done()
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintln(tw, "program\tNPI\tpolicy\tapproved\tdecided\trate\texempt")
	for _, s := range gc.Standings(cs.Select(nil), time.Now().UTC()) {
		ex := "no"
		if s.Exempt {
			ex = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%.0f%%\t%s\n", s.Program, s.NPI, s.Policy, s.Approved, s.Decisions, 100*s.Rate, ex)
	}
	return nil
}

// replayCmd runs a policy over the past cases it covers. It reads every stored request it replays, so it is audited.
func replayCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	dir := fs.String("data", "data", "the server's data directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: vaglio replay -data DIR FILE.vaglio")
	}
	p, err := policy.LoadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	l, err := audit.Open(filepath.Join(*dir, "audit.jsonl"))
	if err != nil {
		return err
	}
	defer l.Close()
	cs, err := cases.Open(l, filepath.Join(*dir, "bundles"))
	if err != nil {
		return err
	}
	rep := replay.Run(cs, p)
	if _, err := l.Append("system", "policy.replayed", "", map[string]any{"policy": p.ID, "version": p.Version,
		"cases": rep.Cases, "changed": rep.Changed, "by": "command line"}); err != nil {
		return err
	}
	check := "passes its check"
	if !rep.CheckOK {
		check = "FAILS its check (vaglio check says why)"
	}
	fmt.Fprintf(w, "%s v%s %s; %d warning(s)\n%d case(s) replayed: %d unchanged, %d changed, %d could not be replayed\n",
		p.ID, p.Version, check, len(rep.Warnings), rep.Cases, rep.Unchanged, rep.Changed, rep.Errors)
	for t, n := range rep.Transitions {
		fmt.Fprintf(w, "  %-18s %d\n", t, n)
	}
	if len(rep.Rows) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintln(tw, "\ncase\treceived\twas\tfinal\tnow\tnote")
	for _, r := range rep.Rows {
		note := r.Note
		if r.Error != "" {
			note = "not replayed: " + r.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Case, r.Created[:min(16, len(r.Created))], r.Was, r.Final, r.Now, note)
	}
	return nil
}

// appealsCmd sends appeal outcomes exported from an appeals system (a CSV file) to a running server's appeals feed. It
// goes through the server, not the data directory, so the server stays the one writer of its log and sees every
// outcome at once. The CSV has a header row naming its columns: reference, outcome and reason, then claim (and item) or
// case, and optionally decided and by.
func appealsCmd(args []string, w io.Writer) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New("usage: vaglio appeals import -url SERVER -token-env VAR [-ca FILE] FILE.csv")
	}
	fs := flag.NewFlagSet("appeals", flag.ExitOnError)
	base := fs.String("url", "https://127.0.0.1:8740", "the Vaglio server")
	caFile := fs.String("ca", "", "a PEM file of certificate authorities to trust beside the system's: the server's "+
		"DATA/tls/ca.crt when it uses Vaglio's own authority")
	tokenEnv := fs.String("token-env", "", "name of an environment variable holding the server's -appeals-token-env token")
	fs.Parse(args[1:])
	if fs.NArg() != 1 || *tokenEnv == "" {
		return errors.New("usage: vaglio appeals import -url SERVER -token-env VAR [-ca FILE] FILE.csv")
	}
	token, err := secretFrom(*tokenEnv)
	if err != nil {
		return err
	}
	if u, err := url.Parse(*base); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("-url %q: the Vaglio server answers on https only", *base)
	}
	hc, err := tlsserve.Client(*caFile, time.Minute)
	if err != nil {
		return fmt.Errorf("-ca: %w", err)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	outcomes, err := readAppealsCSV(f)
	if err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
	recorded, again, refused := 0, 0, 0
	for start := 0; start < len(outcomes); start += 500 {
		end := min(start+500, len(outcomes))
		body, _ := json.Marshal(outcomes[start:end])
		req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(*base, "/")+"/api/feeds/appeals", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		var res []server.AppealResult
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			return fmt.Errorf("the server answered %s", resp.Status)
		}
		for _, r := range res {
			switch r.Status {
			case "recorded":
				recorded++
			case "already-recorded":
				again++
			default:
				refused++
				fmt.Fprintf(w, "refused %s: %s\n", r.Reference, r.Error)
			}
		}
	}
	fmt.Fprintf(w, "%d recorded, %d already recorded, %d refused\n", recorded, again, refused)
	if refused > 0 {
		return fmt.Errorf("%d outcome(s) refused", refused)
	}
	return nil
}

// readAppealsCSV reads appeal outcomes from a CSV with a header row. Unknown columns are refused rather than ignored, so
// a misspelt column is not silently a missing value.
func readAppealsCSV(r io.Reader) ([]server.AppealOutcome, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, errors.New("no outcomes: a header row and at least one row")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		switch h {
		case "claim", "item", "case", "reference", "outcome", "reason", "decided", "by":
			col[h] = i
		default:
			return nil, fmt.Errorf("column %q is not one of claim, item, case, reference, outcome, reason, decided, by", h)
		}
	}
	for _, need := range []string{"reference", "outcome", "reason"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("no %s column", need)
		}
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	var out []server.AppealOutcome
	for _, row := range rows[1:] {
		out = append(out, server.AppealOutcome{Claim: get(row, "claim"), Item: get(row, "item"), Case: get(row, "case"),
			Reference: get(row, "reference"), Outcome: strings.ToLower(get(row, "outcome")), Reason: get(row, "reason"),
			Decided: get(row, "decided"), By: get(row, "by")})
	}
	return out, nil
}
