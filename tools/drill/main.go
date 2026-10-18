// Command drill is a recovery drill, run against real Vaglio processes in a temporary directory, with invented
// requests. It measures what recovery promises instead of stating it:
//
//  1. Crash: requests arrive from several clients at once; the server is killed (SIGKILL, no shutdown) in the middle;
//     it is started again on the same data. Recovery point: every decision the server answered must be in the log
//     afterwards (none lost), and the log's hash chain must verify. Recovery time: from the restart to the first
//     request answered.
//
//  2. Standby: a standby (vaglio standby) follows the primary under the same load; the primary is killed; a server is
//     started on the standby's data. Recovery point: the answered decisions the standby did not have yet. Recovery
//     time: from the kill to the first request the new server answered.
//
//     go build -o bin/vaglio ./cmd/vaglio && go run ./tools/drill -vaglio bin/vaglio
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	bin     = flag.String("vaglio", "bin/vaglio", "the vaglio program")
	bundle  = flag.String("request", "testdata/pas/approve.json", "the PAS request sent, again and again")
	clients = flag.Int("clients", 8, "requests in flight at once")
	before  = flag.Int("before", 300, "decisions answered before the kill")
	steady  = flag.Duration("steady", 5*time.Second, "for the standby: how long the load runs before the kill")
)

func main() {
	flag.Parse()
	raw, err := os.ReadFile(*bundle)
	if err != nil {
		log.Fatal(err)
	}
	root, err := os.MkdirTemp("", "vaglio-drill-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(root)
	fmt.Printf("Recovery drill, %d clients, kill after %d decisions answered\n\n", *clients, *before)
	crash(root, raw)
	standby(root, raw)
}

type server struct {
	cmd  *exec.Cmd
	addr string
	data string
	cli  *http.Client
}

func start(data, addr string, env ...string) *server {
	if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(*bin, "serve", "-data", data, "-policies", "policies", "-addr", addr, "-token-env", "DRILL_TOKEN",
		"-replica-token-env", "DRILL_REPLICA")
	cmd.Env = append(os.Environ(), append([]string{"DRILL_TOKEN=drill", "DRILL_REPLICA=replica-drill-token"}, env...)...)
	logf, _ := os.Create(data + ".log")
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	return &server{cmd: cmd, addr: addr, data: data}
}

// ready waits until the server answers, and returns when it did.
func (s *server) ready(deadline time.Duration) time.Time {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if s.cli == nil {
			if pem, err := os.ReadFile(filepath.Join(s.data, "tls", "ca.crt")); err == nil {
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(pem)
				s.cli = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
					TLSClientConfig: &tls.Config{RootCAs: pool}, MaxIdleConnsPerHost: 64}}
			}
		}
		if s.cli != nil {
			if resp, err := s.cli.Get("https://" + s.addr + "/api/me"); err == nil {
				resp.Body.Close()
				return time.Now()
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	log.Fatalf("%s did not answer in %v; see %s.log", s.addr, deadline, s.data)
	return time.Time{}
}

// decide sends one request; it returns the case the server answered with, or "" if it did not answer.
func (s *server) decide(raw []byte, id string) string {
	req, _ := http.NewRequest(http.MethodPost, "https://"+s.addr+"/api/pas/decide?item=1", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer drill")
	req.Header.Set("X-PAS-Request-Id", id)
	resp, err := s.cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var a struct{ Case string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&a) != nil {
		return ""
	}
	return a.Case
}

// load sends requests until stop, and returns the cases answered.
func load(s *server, raw []byte, stop *atomic.Bool, answered *atomic.Int64) []string {
	var mu sync.Mutex
	var cases []string
	var n atomic.Int64
	var wg sync.WaitGroup
	for c := 0; c < *clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if id := s.decide(raw, fmt.Sprintf("drill-%d", n.Add(1))); id != "" {
					mu.Lock()
					cases = append(cases, id)
					mu.Unlock()
					answered.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	return cases
}

// opened is the cases the data directory's log records.
func opened(data string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(data, "audit.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		var r struct{ Action, Case string }
		if json.Unmarshal([]byte(line), &r) == nil && r.Action == "case.opened" {
			out[r.Case] = true
		}
	}
	return out
}

func verify(data string) string {
	out, err := exec.Command(*bin, "audit", "verify", "-data", data).CombinedOutput()
	if err != nil {
		return "FAILED: " + strings.TrimSpace(string(out))
	}
	return "verifies"
}

func killUnderLoad(s *server, raw []byte, after time.Duration) (answered []string, killedAt time.Time) {
	var stop atomic.Bool
	var n atomic.Int64
	done := make(chan []string)
	go func() { done <- load(s, raw, &stop, &n) }()
	for n.Load() < int64(*before) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(after)
	s.cmd.Process.Kill() // SIGKILL: no shutdown, no flush
	killedAt = time.Now()
	s.cmd.Wait()
	stop.Store(true)
	return <-done, killedAt
}

func crash(root string, raw []byte) {
	data := filepath.Join(root, "crash", "data")
	s := start(data, "127.0.0.1:18791")
	s.ready(30 * time.Second)
	answered, _ := killUnderLoad(s, raw, 0)
	t0 := time.Now()
	s2 := start(data, "127.0.0.1:18791")
	s2.cli = s.cli
	up := s2.ready(30 * time.Second)
	first := s2.decide(raw, "after-restart")
	rto := time.Since(t0)
	have := opened(data)
	lost := 0
	for _, c := range answered {
		if !have[c] {
			lost++
		}
	}
	fmt.Printf("1. Killed under load, restarted on the same data\n")
	fmt.Printf("   decisions answered before the kill   %d\n", len(answered))
	fmt.Printf("   of them missing from the log after   %d (recovery point)\n", lost)
	fmt.Printf("   log hash chain                       %s\n", verify(data))
	fmt.Printf("   restart to listening                 %v\n", up.Sub(t0).Round(time.Millisecond))
	fmt.Printf("   restart to first decision answered   %v (recovery time)  [%v]\n\n", rto.Round(time.Millisecond), first != "")
	s2.cmd.Process.Kill()
	s2.cmd.Wait()
	if lost > 0 {
		defer os.Exit(1)
	}
}

func standby(root string, raw []byte) {
	pdata := filepath.Join(root, "standby", "primary")
	sdata := filepath.Join(root, "standby", "standby")
	p := start(pdata, "127.0.0.1:18792")
	p.ready(30 * time.Second)
	os.MkdirAll(sdata, 0o700)
	sb := exec.Command(*bin, "standby", "-from", "https://127.0.0.1:18792", "-token-env", "DRILL_REPLICA", "-data", sdata,
		"-ca", filepath.Join(pdata, "tls", "ca.crt"), "-every", "200ms")
	sb.Env = append(os.Environ(), "DRILL_REPLICA=replica-drill-token")
	sblog, _ := os.Create(sdata + ".standby.log")
	sb.Stdout, sb.Stderr = sblog, sblog
	if err := sb.Start(); err != nil {
		log.Fatal(err)
	}
	answered, killedAt := killUnderLoad(p, raw, *steady)
	time.Sleep(time.Second) // the standby's last poll fails: the primary is gone
	sb.Process.Signal(os.Interrupt)
	sb.Wait()
	have := opened(sdata)
	lost := 0
	for _, c := range answered {
		if !have[c] {
			lost++
		}
	}
	n := start(sdata, "127.0.0.1:18793")
	n.ready(30 * time.Second)
	first := n.decide(raw, "after-failover")
	rto := time.Since(killedAt)
	fmt.Printf("2. Primary killed after %v of steady load, a server started on the standby's data (polling every 200 ms)\n", *steady)
	fmt.Printf("   decisions the primary answered       %d\n", len(answered))
	fmt.Printf("   of them the standby did not have     %d (recovery point: the standby is asynchronous)\n", lost)
	fmt.Printf("   standby's log hash chain             %s\n", verify(sdata))
	fmt.Printf("   kill to first decision answered      %v (recovery time; includes the 1 s wait and stopping the standby)  [%v]\n",
		rto.Round(time.Millisecond), first != "")
	n.cmd.Process.Kill()
	n.cmd.Wait()
}
