package server

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/biodream-llc/vaglio/internal/auth"
	"github.com/biodream-llc/vaglio/internal/editor"
	"github.com/biodream-llc/vaglio/internal/policy"
	"github.com/biodream-llc/vaglio/library"
)

// LibraryState is a policy's place in the library: "published", the library's own text deciding, or "overridden", the
// plan's own version of a library policy deciding instead (Published is then the library version it replaced).
type LibraryState struct {
	State     string `json:"state"`
	Published string `json:"published"`
}

func (cfg Config) libraryState(p *policy.Policy) *LibraryState {
	lib := cfg.Library[p.ID]
	switch {
	case lib == nil:
		return nil
	case lib == p:
		return &LibraryState{State: "published", Published: lib.Version}
	default:
		return &LibraryState{State: "overridden", Published: lib.Version}
	}
}

// sourceOf is a policy's source: the library's file as published, header comments and all, for a library policy.
func (cfg Config) sourceOf(p *policy.Policy) string {
	if lib := cfg.Library[p.ID]; lib != nil && lib == p {
		if src, err := library.Source(p.ID); err == nil {
			return string(src)
		}
	}
	return policy.Format(p)
}

// nextVersion is a version one patch on (0.1.0 to 0.1.1); a version not of that shape gets ".1" added.
func nextVersion(v string) string {
	parts := strings.Split(v, ".")
	if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && len(parts) > 1 {
		parts[len(parts)-1] = strconv.Itoa(n + 1)
		return strings.Join(parts, ".")
	}
	return v + ".1"
}

// removeOverride takes the plan's override of a library policy out of force and puts the library's published version
// back. A clinician's act, as signing the override was: it changes what decides requests.
func (cfg Config) removeOverride(w http.ResponseWriter, r *http.Request, s auth.Session) {
	if !cfg.editing(w) {
		return
	}
	u, ok, err := cfg.Sessions.Account(s.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || !u.Clinician() {
		writeError(w, http.StatusForbidden, "only a clinician removes an override: an account with an NPI and a credential "+
			"(vaglio user clinician)")
		return
	}
	id := r.PathValue("id")
	lib := cfg.Library[id]
	if lib == nil {
		writeError(w, http.StatusNotFound, "no library policy "+id)
		return
	}
	cfg.edit.Lock()
	defer cfg.edit.Unlock()
	cur := cfg.set.get(id)
	if cur == lib {
		writeError(w, http.StatusConflict, id+" is the library's as published already")
		return
	}
	dir := editor.Dir{Path: cfg.PolicyDir}
	file := dir.LiveFile(id)
	src, err := os.ReadFile(file)
	if err != nil {
		writeError(w, http.StatusConflict, "the override of "+id+" is not a file in the policy directory: "+err.Error())
		return
	}
	from := ""
	if cur != nil {
		from = cur.Version
	}
	if _, err := cfg.Audit.Append("user:"+s.User, ActOverrideRemoved, "", map[string]string{"policy": id,
		"by": u.Name + ", " + u.Credential, "removed": from, "sha256": editor.Hash(src), "published": lib.Version}); err != nil {
		writeError(w, http.StatusInternalServerError, "the change could not be recorded, so it is not made: "+err.Error())
		return
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.set.put(lib)
	if cfg.Versions != nil {
		_, _ = cfg.Versions.Keep(lib)
	}
	writeJSON(w, http.StatusOK, struct {
		*policy.Policy
		Library *LibraryState `json:"library,omitempty"`
	}{lib, cfg.libraryState(lib)})
}
