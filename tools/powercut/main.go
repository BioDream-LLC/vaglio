// Command powercut tests what a power cut leaves of Vaglio's data, on Linux, as root.
//
// A kill -9 (tools/drill) loses what the process held, never what the operating system held; a power cut loses what
// the disk had not been told to keep. This replays exactly what a disk had been told, at every point it was told it:
//
//  1. Vaglio (anchoring every minute, the run long enough for checkpoints to be written too) runs on a filesystem over dm-log-writes, which records every write and every flush the disk is sent.
//     Clients send requests (a new Bundle each); after each answer, a mark is put in that record ("a<n>": n decisions
//     had been answered by here).
//  2. The record is replayed onto a fresh device up to every flush (replay-log --check flush), as if the power had
//     gone just after it: each state must mount, start a server, verify (the log's chain and every case's request),
//     and keep working.
//  3. It is replayed up to marks: everything answered before a mark must be there, with its request.
//
// It models a disk that keeps what it acknowledged; one that acknowledges a flush and then loses it defeats any
// software. replay-log comes from xfstests (src/log-writes); powercut does not ship it.
//
//	sudo powercut -vaglio ./vaglio -policies ./policies -replay-log ./replay-log [-answers 120] [-clients 4] [-fs xfs]
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	bin      = flag.String("vaglio", "vaglio", "the vaglio program")
	policies = flag.String("policies", "policies", "the policy directory")
	replay   = flag.String("replay-log", "replay-log", "xfstests' replay-log")
	request  = flag.String("request", "testdata/pas/approve.json", "the PAS request sent (made new each time)")
	work     = flag.String("work", "/var/tmp/vaglio-powercut", "a scratch directory (emptied)")
	answers  = flag.Int("answers", 120, "decisions to answer before the power goes")
	clients  = flag.Int("clients", 4, "requests in flight at once")
	fstype   = flag.String("fs", "ext4", "the filesystem: ext4 or xfs")
	marks    = flag.Int("mark-every", 10, "check every this many marks with the decisions they promise")
	spread   = flag.Duration("over", 70*time.Second, "spread the decisions over this long (past the first anchor checkpoint)")
)

const token = "powercut-token-0123456789"

func main() {
	flag.Parse()
	if len(flag.Args()) == 2 && flag.Arg(0) == "check" {
		// Called by replay-log at each flush, with the replay device: check a snapshot of it.
		if err := checkDevice(flag.Arg(1), 0); err != nil {
			fmt.Fprintln(os.Stderr, "powercut check:", err)
			os.Exit(1)
		}
		return
	}
	if os.Geteuid() != 0 {
		log.Fatal("powercut needs root (loop devices, device-mapper, mount)")
	}
	for _, p := range []*string{bin, policies, replay, request} {
		a, err := filepath.Abs(*p)
		if err != nil {
			log.Fatal(err)
		}
		*p = a
	}
	leftovers()
	os.RemoveAll(*work)
	must(os.MkdirAll(*work, 0o700))
	byMark := record()
	fmt.Printf("recorded: %d decisions answered, each followed by a mark\n", len(byMark))
	everyFlush()
	atMarks(byMark)
	fmt.Println("POWER CUT TEST PASSED")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func run(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		log.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func loop(file string, size int64) string {
	f, err := os.Create(file)
	must(err)
	must(f.Truncate(size))
	f.Close()
	return run("losetup", "-f", "--show", file)
}

func sectors(dev string) string { return run("blockdev", "--getsz", dev) }

func mkfs(dev string) {
	if *fstype == "xfs" {
		run("mkfs.xfs", "-q", "-f", dev)
		return
	}
	run("mkfs.ext4", "-q", "-F", dev)
}

// record runs Vaglio on a logged device until enough decisions are answered, then cuts it off.
func record() map[int]string {
	data := loop(filepath.Join(*work, "data.img"), 512<<20)
	logdev := loop(filepath.Join(*work, "log.img"), 4<<30)
	os.WriteFile(filepath.Join(*work, "devices"), []byte(data+"\n"+logdev+"\n"), 0o600)
	run("dmsetup", "create", "vgpc", "--table", fmt.Sprintf("0 %s log-writes %s %s", sectors(data), data, logdev))
	mkfs("/dev/mapper/vgpc")
	run("dmsetup", "message", "vgpc", "0", "mark", "mkfs")
	mnt := filepath.Join(*work, "mnt")
	must(os.MkdirAll(mnt, 0o700))
	run("mount", "/dev/mapper/vgpc", mnt)
	dir := filepath.Join(mnt, "data")
	must(os.MkdirAll(dir, 0o700))
	s := serve(dir, "127.0.0.1:18795")
	raw, err := os.ReadFile(*request)
	must(err)
	var mu sync.Mutex
	byMark := map[int]string{}
	n := 0
	var wg sync.WaitGroup
	for c := 0; c < *clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for i := 0; ; i++ {
				mu.Lock()
				done := n >= *answers
				mu.Unlock()
				if done {
					return
				}
				// Spread out, so the run outlasts the first anchor checkpoint without thousands of flushes.
				time.Sleep(*spread * time.Duration(*clients) / time.Duration(*answers))
				id := s.decide(raw, fmt.Sprintf("pc-%d-%d", c, i))
				if id == "" {
					continue
				}
				// After the answer: every write it waited for is before this mark in the record.
				mu.Lock()
				n++
				byMark[n] = id
				run("dmsetup", "message", "vgpc", "0", "mark", "a"+strconv.Itoa(n))
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	// The power goes: the process dies with whatever it had, and nothing after this point counts.
	s.cmd.Process.Signal(syscall.SIGKILL)
	s.cmd.Wait()
	run("dmsetup", "message", "vgpc", "0", "mark", "end")
	run("umount", mnt)
	run("dmsetup", "remove", "vgpc")
	b, _ := json.Marshal(byMark)
	must(os.WriteFile(filepath.Join(*work, "answered.json"), b, 0o600))
	return byMark
}

type server struct {
	cmd  *exec.Cmd
	addr string
	data string
	cli  *http.Client
}

func serve(data, addr string) *server {
	cmd := exec.Command(*bin, "serve", "-data", data, "-policies", *policies, "-addr", addr, "-token-env", "PC_TOKEN",
		"-anchor-every", "1m")
	cmd.Env = append(os.Environ(), "PC_TOKEN="+token)
	logf, _ := os.Create(filepath.Join(filepath.Dir(*work), filepath.Base(*work)+"-"+strings.ReplaceAll(addr, ":", "_")+".log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	must(cmd.Start())
	s := &server{cmd: cmd, addr: addr, data: data}
	if err := s.ready(30 * time.Second); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		log.Fatal(err)
	}
	return s
}

func (s *server) ready(deadline time.Duration) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if s.cmd.ProcessState != nil {
			return fmt.Errorf("the server exited")
		}
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
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("%s did not answer in %v", s.addr, deadline)
}

func (s *server) decide(raw []byte, id string) string {
	raw = bytes.Replace(raw, []byte("{"), []byte(`{"id":"`+id+`",`), 1)
	req, _ := http.NewRequest(http.MethodPost, "https://"+s.addr+"/api/pas/decide?item=1", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
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

// everyFlush replays the record onto a fresh device, stopping at every flush to check a snapshot of it.
func everyFlush() {
	logdev := devices()[1]
	dev := loop(filepath.Join(*work, "replay.img"), 512<<20)
	defer run("losetup", "-d", dev)
	self, _ := os.Executable()
	check := fmt.Sprintf("%s -vaglio %s -policies %s -work %s -fs %s check %s", self, *bin, *policies, *work, *fstype, dev)
	t0 := time.Now()
	// The filesystem as mkfs left it first (nothing to check before there is one), then on from there.
	run(*replay, "--log", logdev, "--replay", dev, "--end-mark", "mkfs")
	out, err := exec.Command(*replay, "--log", logdev, "--replay", dev, "--start-mark", "mkfs", "--check", "flush",
		"--fsck", check, "--end-mark", "end").CombinedOutput()
	n, _ := os.ReadFile(filepath.Join(*work, "checked"))
	if err != nil {
		log.Fatalf("FAILED at a flush (after %s states): %v\n%s", strings.TrimSpace(string(n)), err, tail(out))
	}
	fmt.Printf("every flush: %s states after a flush checked, all start, verify and take a new decision (%v)\n",
		strings.TrimSpace(string(n)), time.Since(t0).Round(time.Second))
}

func tail(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return strings.Join(lines, "\n")
}

func devices() []string {
	b, err := os.ReadFile(filepath.Join(*work, "devices"))
	must(err)
	return strings.Fields(string(b))
}

// atMarks replays to every few marks onto a fresh device: the decisions answered by then must all be there.
func atMarks(byMark map[int]string) {
	logdev := devices()[1]
	var ns []int
	for n := range byMark {
		if n%*marks == 0 || n == len(byMark) {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	for _, n := range ns {
		dev := loop(filepath.Join(*work, "mark.img"), 512<<20)
		run(*replay, "--log", logdev, "--replay", dev, "--end-mark", "a"+strconv.Itoa(n))
		err := checkDevice(dev, n)
		run("losetup", "-d", dev)
		if err != nil {
			log.Fatalf("FAILED at mark a%d: %v", n, err)
		}
	}
	fmt.Printf("at %d marks: every decision answered before the mark is there, with its request\n", len(ns))
}

// checkDevice checks the state on dev without changing it: on a snapshot, it mounts, starts a server, verifies the
// log and every request, takes one new decision, and (when want > 0) finds the first want decisions answered.
func checkDevice(dev string, want int) error {
	cow := filepath.Join(*work, "cow.img")
	cdev := loop(cow, 256<<20)
	defer func() { run("losetup", "-d", cdev); os.Remove(cow) }()
	run("dmsetup", "create", "vgsnap", "--table", fmt.Sprintf("0 %s snapshot %s %s N 8", sectors(dev), dev, cdev))
	defer exec.Command("dmsetup", "remove", "vgsnap").Run()
	if *fstype == "ext4" {
		// Journal replay is what mounting does; fsck first finds what it would not mend.
		if out, err := exec.Command("e2fsck", "-fy", "/dev/mapper/vgsnap").CombinedOutput(); err != nil {
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() > 1 {
				return fmt.Errorf("e2fsck: %v\n%s", err, out)
			}
		}
	}
	mnt := filepath.Join(*work, "check")
	os.MkdirAll(mnt, 0o700)
	if out, err := exec.Command("mount", "/dev/mapper/vgsnap", mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("mount: %v %s", err, out)
	}
	defer exec.Command("umount", mnt).Run()
	data := filepath.Join(mnt, "data")
	if _, err := os.Stat(filepath.Join(data, "audit.jsonl")); err != nil {
		count()
		return nil // before the server first wrote anything
	}
	s := &server{addr: "127.0.0.1:18796", data: data}
	s.cmd = exec.Command(*bin, "serve", "-data", data, "-policies", *policies, "-addr", s.addr, "-token-env", "PC_TOKEN",
		"-anchor-every", "1m")
	s.cmd.Env = append(os.Environ(), "PC_TOKEN="+token)
	var out bytes.Buffer
	s.cmd.Stdout, s.cmd.Stderr = &out, &out
	if err := s.cmd.Start(); err != nil {
		return err
	}
	go s.cmd.Wait()
	if err := s.ready(30 * time.Second); err != nil {
		s.cmd.Process.Kill()
		return fmt.Errorf("the server would not start on what the power cut left: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile(*request)
	if s.decide(raw, "after-the-cut") == "" {
		s.cmd.Process.Kill()
		return fmt.Errorf("the restarted server did not answer a new request\n%s", out.String())
	}
	s.cmd.Process.Signal(syscall.SIGTERM)
	for i := 0; i < 100 && s.cmd.ProcessState == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if s.cmd.ProcessState == nil {
		s.cmd.Process.Kill()
		time.Sleep(200 * time.Millisecond)
	}
	v, err := exec.Command(*bin, "audit", "verify", "-data", data).CombinedOutput()
	if err != nil {
		return fmt.Errorf("audit verify after the restart: %v\n%s", err, v)
	}
	if want > 0 {
		b, err := os.ReadFile(filepath.Join(*work, "answered.json"))
		if err != nil {
			return err
		}
		var byMark map[int]string
		json.Unmarshal(b, &byMark)
		have := opened(filepath.Join(data, "audit.jsonl"))
		for n := 1; n <= want; n++ {
			if !have[byMark[n]] {
				return fmt.Errorf("decision %d (case %s) was answered before mark a%d but is not in the log", n, byMark[n], want)
			}
		}
	}
	count()
	return nil
}

func count() {
	p := filepath.Join(*work, "checked")
	b, _ := os.ReadFile(p)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	os.WriteFile(p, []byte(strconv.Itoa(n+1)), 0o600)
}

func opened(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
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

// leftovers undoes what an earlier run that stopped half-way left: its servers, mounts, devices.
func leftovers() {
	exec.Command("pkill", "-9", "-x", filepath.Base(*bin)).Run()
	time.Sleep(200 * time.Millisecond)
	for _, m := range []string{"check", "mnt"} {
		exec.Command("umount", filepath.Join(*work, m)).Run()
	}
	for _, d := range []string{"vgsnap", "vgpc"} {
		exec.Command("dmsetup", "remove", d).Run()
	}
	for _, img := range []string{"data.img", "log.img", "replay.img", "mark.img", "cow.img"} {
		b, _ := exec.Command("losetup", "-j", filepath.Join(*work, img)).Output()
		for _, line := range strings.Split(string(b), "\n") {
			if dev, _, ok := strings.Cut(line, ":"); ok {
				exec.Command("losetup", "-d", dev).Run()
			}
		}
	}
}
