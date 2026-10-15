package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/biodream-llc/vaglio/internal/seal"
)

// What a standby (vaglio standby) copies from this server, behind the replica token (serve -replica-token-env): the
// audit log's records as they are, the request Bundles they name, and the accounts file. Everything else a case holds
// is derived from these. Each read is not itself recorded: the standby reads continuously, and its token is for that
// machine alone.

var bundleNameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (cfg Config) replicaOK(w http.ResponseWriter, r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if cfg.ReplicaToken == "" || !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.ReplicaToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "the replica token is needed")
		return false
	}
	return true
}

// replicaRecords streams the records after a sequence number, at most 2000, as NDJSON, with the log's head.
func (cfg Config) replicaRecords(w http.ResponseWriter, r *http.Request) {
	if !cfg.replicaOK(w, r) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	recs := cfg.Audit.Records(after, 2000)
	n, head := cfg.Audit.Head()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Vaglio-Head-Seq", strconv.Itoa(n))
	w.Header().Set("Vaglio-Head-Hash", head)
	enc := json.NewEncoder(w)
	for _, rec := range recs {
		_ = enc.Encode(rec)
	}
}

func (cfg Config) replicaBundle(w http.ResponseWriter, r *http.Request) {
	if !cfg.replicaOK(w, r) {
		return
	}
	name := r.PathValue("sha")
	if !bundleNameRe.MatchString(name) || cfg.BundleDir == "" {
		writeError(w, http.StatusNotFound, "no such bundle")
		return
	}
	raw, err := seal.ReadFile(filepath.Join(cfg.BundleDir, name+".json"), "bundle")
	if err != nil {
		writeError(w, http.StatusNotFound, "no such bundle")
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	_, _ = w.Write(raw)
}

func (cfg Config) replicaUsers(w http.ResponseWriter, r *http.Request) {
	if !cfg.replicaOK(w, r) {
		return
	}
	if cfg.UsersPath == "" {
		writeError(w, http.StatusNotFound, "no accounts file")
		return
	}
	raw, err := os.ReadFile(cfg.UsersPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "no accounts file")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
