package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"time"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/internal/server"
)

// benchCmd measures how fast this machine decides: the engine alone, and whole PAS requests through the server - read
// the Bundle, find the evidence, decide, write the case to the audit log (synced to disk) and answer. It uses its own
// temporary data directory and invented requests, and touches nothing else.
func benchCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	dir := fs.String("policies", "policies", "the policy directory; the lumbar MRI policy is the one exercised")
	n := fs.Int("n", 2000, "PAS requests to send")
	workers := fs.Int("workers", runtime.NumCPU(), "requests in flight at once")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile of the whole-request run to this file (go tool pprof)")
	seconds := fs.Float64("engine-seconds", 2, "how long to run the engine alone")
	fs.Parse(args)
	policies, err := loadDir(*dir)
	if err != nil {
		return err
	}
	p := policies["lumbar_mri"]
	if p == nil {
		return errors.New("bench exercises the lumbar_mri policy; -policies must hold it")
	}
	fmt.Fprintf(w, "vaglio %s, %s/%s, %d CPUs (GOMAXPROCS %d)\n\n", version, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(),
		runtime.GOMAXPROCS(0))

	// The engine alone: a decision with its proof and its routes to approval, on random evidence.
	ev := p.EvidenceNames()
	reqs := make([]decide.Request, 256)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range reqs {
		for _, name := range ev {
			switch r.IntN(3) {
			case 0:
				reqs[i].Evidence = append(reqs[i].Evidence, decide.Assertion{Argument: name, Finding: decide.Present,
					Citation: decide.Citation{Reference: "QuestionnaireResponse/q", Quote: "Yes"}})
			case 1:
				reqs[i].Evidence = append(reqs[i].Evidence, decide.Assertion{Argument: name, Finding: decide.Absent,
					Citation: decide.Citation{Reference: "QuestionnaireResponse/q", Quote: "No"}})
			}
		}
	}
	one := engineRate(p, reqs, 1, *seconds)
	all := engineRate(p, reqs, runtime.NumCPU(), *seconds)
	fmt.Fprintf(w, "engine alone (%d pieces of evidence, decision + proof + routes to approval)\n", len(ev))
	fmt.Fprintf(w, "  one core     %8.0f decisions/s  (%.1f µs each)\n", one, 1e6/one)
	fmt.Fprintf(w, "  all cores    %8.0f decisions/s\n\n", all)

	// Whole requests, through the server, each recorded in the audit log before it is answered.
	tmp, err := os.MkdirTemp("", "vaglio-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	st, err := openData(tmp)
	if err != nil {
		return err
	}
	defer st.log.Close()
	h := server.New(server.Config{Policies: policies, Token: "bench", Sessions: auth.NewSessions(st.users),
		Audit: st.log, AuditPath: st.auditPath, Cases: st.cases})
	// One Bundle per request, as in real traffic: each is a new file kept beside its case (written, synced, renamed
	// into place, its directory synced), not one of a few the server has already kept.
	bundles := make([][]byte, *n)
	for i := range bundles {
		bundles[i] = benchBundle(r, i)
	}
	lat := make([]time.Duration, *n)
	var next int
	var mu sync.Mutex
	var failed error
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	start := time.Now()
	var wg sync.WaitGroup
	for k := 0; k < *workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= *n {
					return
				}
				req := httptest.NewRequest(http.MethodPost, "/api/pas/decide?item=1", bytes.NewReader(bundles[i%len(bundles)]))
				req.Header.Set("Authorization", "Bearer bench")
				req.Header.Set("X-PAS-Request-Id", fmt.Sprintf("bench-%d", i))
				rec := httptest.NewRecorder()
				t0 := time.Now()
				h.ServeHTTP(rec, req)
				lat[i] = time.Since(t0)
				if rec.Code != http.StatusOK {
					mu.Lock()
					failed = fmt.Errorf("request %d: %d %s", i, rec.Code, rec.Body)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	took := time.Since(start)
	if failed != nil {
		return failed
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	q := func(f float64) time.Duration { return lat[int(f*float64(len(lat)-1))] }
	fmt.Fprintf(w, "whole PAS requests (read the Bundle, find the evidence, decide, write the case to the audit log and\n")
	fmt.Fprintf(w, "sync it to disk, answer), %d requests, %d in flight\n", *n, *workers)
	fmt.Fprintf(w, "  throughput   %8.0f requests/s\n", float64(*n)/took.Seconds())
	fmt.Fprintf(w, "  latency      p50 %v   p95 %v   p99 %v   max %v\n", q(0.50).Round(10*time.Microsecond),
		q(0.95).Round(10*time.Microsecond), q(0.99).Round(10*time.Microsecond), lat[len(lat)-1].Round(10*time.Microsecond))
	fmt.Fprintf(w, "\nNot included: TLS, the network, and the PAS server in front. Every decision is synced to disk before it is\n"+
		"answered, so the disk's sync time bounds the throughput of whole requests.\n")
	return nil
}

// engineRate runs the engine on reqs from workers goroutines for about seconds, and returns decisions per second.
func engineRate(p *policy.Policy, reqs []decide.Request, workers int, seconds float64) float64 {
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	var total int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			var n int64
			for i := k; time.Now().Before(deadline); i++ {
				for j := 0; j < 64; j++ {
					if _, err := decide.Decide(p, reqs[(i*64+j)%len(reqs)]); err != nil {
						panic(err)
					}
				}
				n += 64
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}(k)
	}
	wg.Wait()
	return float64(total) / time.Since(start).Seconds()
}

// benchBundle is an invented lumbar MRI request with its questions answered at random.
func benchBundle(r *rand.Rand, i int) []byte {
	type ans struct {
		LinkID string `json:"linkId"`
		Answer []any  `json:"answer"`
	}
	var items []ans
	for _, link := range []string{"red-flag", "conservative-therapy", "surgical-planning", "prior-imaging-6mo", "new-or-worsening"} {
		if r.IntN(5) == 0 {
			continue
		}
		items = append(items, ans{link, []any{map[string]any{"valueBoolean": r.IntN(2) == 0}}})
	}
	base := "http://provider.example/fhir/"
	b := map[string]any{"resourceType": "Bundle", "type": "collection", "entry": []any{
		map[string]any{"fullUrl": base + "Claim/c", "resource": map[string]any{"resourceType": "Claim", "id": "c",
			"status": "active", "use": "preauthorization", "created": "2026-10-01",
			"identifier": []any{map[string]any{"system": "http://provider.example/claims", "value": fmt.Sprint("bench-", i)}},
			"patient":    map[string]any{"reference": "Patient/p"}, "insurer": map[string]any{"reference": "Organization/plan"},
			"provider": map[string]any{"reference": "Organization/clinic"},
			"item": []any{map[string]any{"sequence": 1, "productOrService": map[string]any{"coding": []any{
				map[string]any{"system": "http://www.ama-assn.org/go/cpt", "code": "72148"}}}}}}},
		map[string]any{"fullUrl": base + "Patient/p", "resource": map[string]any{"resourceType": "Patient", "id": "p"}},
		map[string]any{"fullUrl": base + "Organization/plan", "resource": map[string]any{"resourceType": "Organization", "id": "plan"}},
		map[string]any{"fullUrl": base + "Organization/clinic", "resource": map[string]any{"resourceType": "Organization", "id": "clinic"}},
		map[string]any{"fullUrl": base + "QuestionnaireResponse/q", "resource": map[string]any{
			"resourceType": "QuestionnaireResponse", "id": "q", "status": "completed",
			"questionnaire": "https://vaglio.example/Questionnaire/lumbar-mri", "subject": map[string]any{"reference": "Patient/p"},
			"item": items}},
	}}
	raw, _ := json.Marshal(b)
	return raw
}
