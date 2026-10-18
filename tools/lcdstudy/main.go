// Command lcdstudy asks whether Vaglio's policy language fits real coverage policy: it drafts Medicare LCDs, a
// spread sample of the ones in force, through the same drafting loop as `vaglio draft` (a model writes the policy;
// Vaglio's gates check that it compiles, that every clause is quoted word for word from the LCD, that every code is in
// the LCD, that it decides every combination of evidence without contradicting itself, and that it has no lint
// warnings), and writes a CSV of what passed and why the rest did not.
//
// What it measures is fit: a draft that passes every gate is one the language could express without breaking its own
// rules. It does not measure correctness: nobody here has checked that a draft says what its LCD means. That is a
// clinician's job, as for every draft.
//
//	go run ./tools/lcdstudy -n 60 -out study.csv -drafts DIR
//
// The LCDs come from the CMS Coverage API (api.coverage.cms.gov), their "Coverage Indications, Limitations, and/or
// Medical Necessity" section. The model is any OpenAI-compatible API; the LCD text is sent to it.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/draft"
)

const api = "https://api.coverage.cms.gov/v1"

type lcd struct {
	ID      int    `json:"document_id"`
	Version int    `json:"document_version"`
	Display string `json:"document_display_id"`
	Title   string `json:"title"`
	Note    string `json:"note"`
}

type row struct {
	LCD, Title      string
	Chars           int
	Passed          bool
	Rounds          int
	Evidence, Rules int
	Kinds           []string // the kinds of failure in the last round, when it did not pass
	FirstKinds      []string // the kinds of failure in the first round
	Error           string
}

func main() {
	n := flag.Int("n", 60, "how many LCDs to draft")
	out := flag.String("out", "lcd-study.csv", "the CSV to write")
	drafts := flag.String("drafts", "", "a directory to write each draft and its rounds into (they quote the LCDs)")
	url := flag.String("url", "https://openrouter.ai/api/v1", "an OpenAI-compatible chat API")
	model := flag.String("model", "meta-llama/llama-3.3-70b-instruct", "the model")
	keyEnv := flag.String("key-env", "OPENROUTER_API_KEY", "the environment variable holding the API key")
	workers := flag.Int("workers", 4, "LCDs drafted at once")
	maxChars := flag.Int("max-chars", 40000, "LCDs longer than this are left out (the model's context)")
	flag.Parse()

	token, err := licenseToken()
	if err != nil {
		log.Fatal(err)
	}
	all, err := listLCDs()
	if err != nil {
		log.Fatal(err)
	}
	sample := spread(all, *n)
	m := &draft.OpenAI{Base: *url, Key: os.Getenv(*keyEnv), Model: *model}
	if *drafts != "" {
		if err := os.MkdirAll(*drafts, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	rows := make([]row, len(sample))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				rows[i] = study(m, token, sample[i], *maxChars, *drafts)
				r := rows[i]
				log.Printf("%s passed=%v rounds=%d %s %s", r.LCD, r.Passed, r.Rounds, strings.Join(r.Kinds, ","), r.Error)
			}
		}()
	}
	for i := range sample {
		next <- i
	}
	close(next)
	wg.Wait()
	if err := write(*out, rows); err != nil {
		log.Fatal(err)
	}
}

func study(m draft.Model, token string, l lcd, maxChars int, dir string) row {
	r := row{LCD: l.Display, Title: l.Title}
	text, err := indication(token, l)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if codes := articleCodes(token, l.ID); codes != "" {
		text += "\n\n" + codes
	}
	r.Chars = len(text)
	if r.Chars > maxChars {
		r.Error = "too long for the model's context"
		return r
	}
	if r.Chars < 200 {
		r.Error = "no coverage text in this section"
		return r
	}
	id := strings.ToLower(l.Display)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res, err := draft.Draft(ctx, m, draft.Request{ID: id, Document: text, Name: l.Display + " " + l.Title})
	if res != nil {
		r.Rounds = len(res.Attempts)
		if len(res.Attempts) > 0 {
			r.FirstKinds = kinds(res.Attempts[0].Failures)
			r.Kinds = kinds(res.Attempts[len(res.Attempts)-1].Failures)
		}
		if dir != "" {
			b, _ := json.MarshalIndent(res.Attempts, "", " ")
			_ = os.WriteFile(filepath.Join(dir, id+".rounds.json"), b, 0o600)
		}
	}
	switch {
	case err == nil:
		r.Passed, r.Kinds = true, nil
		r.Evidence = len(res.Policy.EvidenceNames())
		r.Rules = len(res.Policy.Arguments) - r.Evidence
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, id+".vaglio"), []byte(res.Source), 0o600)
		}
	case errors.Is(err, draft.ErrNoDraft):
	default:
		r.Error = err.Error()
	}
	return r
}

func kinds(failures []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range failures {
		if k := draft.Kind(f); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func get(url, token string, v any) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

// licenseToken accepts the CMS Coverage API's licence agreement (AMA CPT, ADA CDT, AHA UB-04), as its data endpoints
// require.
func licenseToken() (string, error) {
	var out struct {
		Data []struct{ Token string } `json:"data"`
	}
	if err := get(api+"/metadata/license-agreement", "", &out); err != nil || len(out.Data) == 0 {
		return "", fmt.Errorf("the licence token: %v", err)
	}
	return out.Data[0].Token, nil
}

func listLCDs() ([]lcd, error) {
	var out struct {
		Data []lcd `json:"data"`
	}
	if err := get(api+"/reports/local-coverage-final-lcds/", "", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// spread is n LCDs in force, one per title (several contractors publish the same LCD), evenly spaced through them in
// order of document number: the same sample for the same list, and no choosing.
func spread(all []lcd, n int) []lcd {
	byTitle := map[string]lcd{}
	for _, l := range all {
		if l.Note != "" { // retired, or future
			continue
		}
		t := strings.ToLower(strings.TrimSpace(l.Title))
		if cur, ok := byTitle[t]; !ok || l.ID < cur.ID {
			byTitle[t] = l
		}
	}
	var in []lcd
	for _, l := range byTitle {
		in = append(in, l)
	}
	sort.Slice(in, func(i, j int) bool { return in[i].ID < in[j].ID })
	if n >= len(in) {
		return in
	}
	out := make([]lcd, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, in[i*len(in)/n])
	}
	return out
}

var (
	tags   = regexp.MustCompile(`(?s)<[^>]*>`)
	blanks = regexp.MustCompile(`[ \t]+`)
	lines  = regexp.MustCompile(`\n\s*\n+`)
)

func indication(token string, l lcd) (string, error) {
	var out struct {
		Data []struct {
			Indication string `json:"indication"`
		} `json:"data"`
	}
	if err := get(fmt.Sprintf("%s/data/lcd/?lcdid=%d&ver=%d", api, l.ID, l.Version), token, &out); err != nil {
		return "", err
	}
	if len(out.Data) == 0 {
		return "", errors.New("no LCD in the answer")
	}
	// The API gives the section's HTML entity-encoded, sometimes twice (&amp;nbsp;): decoded until it no longer changes,
	// then its tags are removed.
	s := out.Data[0].Indication
	for i := 0; i < 3; i++ {
		s = html.UnescapeString(s)
	}
	s = strings.NewReplacer("<br>", "\n", "<br />", "\n", "</p>", "\n", "</li>", "\n", "<li>", "- ", "\u00a0", " ").Replace(s)
	s = html.UnescapeString(tags.ReplaceAllString(s, ""))
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = blanks.ReplaceAllString(s, " ")
	return strings.TrimSpace(lines.ReplaceAllString(s, "\n\n")), nil
}

func write(path string, rows []row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"lcd", "title", "chars", "passed", "rounds", "evidence", "rules", "first_round_failures", "last_round_failures", "error"})
	for _, r := range rows {
		w.Write([]string{r.LCD, r.Title, fmt.Sprint(r.Chars), fmt.Sprint(r.Passed), fmt.Sprint(r.Rounds), fmt.Sprint(r.Evidence),
			fmt.Sprint(r.Rules), strings.Join(r.FirstKinds, ";"), strings.Join(r.Kinds, ";"), r.Error})
	}
	w.Flush()
	return errors.Join(w.Error(), f.Close())
}

// articleCodes lists the codes the LCD's billing and coding articles give, as LCDs carry none themselves: the code
// numbers only (CPT descriptions are the AMA's), as the library's policies were drafted.
func articleCodes(token string, id int) string {
	var rel struct {
		Data []struct {
			Article *int `json:"r_article_id"`
			Version *int `json:"r_article_version"`
		} `json:"data"`
	}
	if get(fmt.Sprintf("%s/data/lcd/related-documents?lcdid=%d", api, id), token, &rel) != nil {
		return ""
	}
	seen := map[string]bool{}
	var hcpcs, icd []string
	for _, a := range rel.Data {
		if a.Article == nil || a.Version == nil {
			continue
		}
		var h struct {
			Data []struct {
				Code string `json:"hcpc_code_id"`
			} `json:"data"`
		}
		if get(fmt.Sprintf("%s/data/article/hcpc-code?articleid=%d&ver=%d", api, *a.Article, *a.Version), token, &h) == nil {
			for _, c := range h.Data {
				if !seen["h"+c.Code] {
					seen["h"+c.Code] = true
					hcpcs = append(hcpcs, c.Code)
				}
			}
		}
		var d struct {
			Data []struct {
				Code string `json:"icd10_code_id"`
			} `json:"data"`
		}
		if get(fmt.Sprintf("%s/data/article/icd10-covered?articleid=%d&ver=%d", api, *a.Article, *a.Version), token, &d) == nil {
			for _, c := range d.Data {
				if !seen["i"+c.Code] && len(icd) < 200 {
					seen["i"+c.Code] = true
					icd = append(icd, c.Code)
				}
			}
		}
	}
	var b strings.Builder
	if len(hcpcs) > 0 {
		b.WriteString("Codes from the LCD's billing and coding article. CPT/HCPCS codes: " + strings.Join(hcpcs, " ") + ".")
	}
	if len(icd) > 0 {
		b.WriteString(" ICD-10-CM codes that support medical necessity: " + strings.Join(icd, " ") + ".")
	}
	return strings.TrimSpace(b.String())
}
