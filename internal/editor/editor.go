// Package editor is how a policy changes: drafted, checked, signed off by a clinician, and only then put in force.
//
// A server's policy directory holds the policies in force; its drafts/ subdirectory holds work in progress. A draft can
// be saved however unfinished it is, since writing a policy takes many sittings, but it decides nothing. Signing it off
// is the one way into force, and it refuses a policy that fails its check, carries warnings, reuses the version of the
// policy it replaces, or claims a service another policy decides. The policy in force keeps deciding until its successor
// is signed: an edit never takes a service offline.
//
// The command line (vaglio signoff) and the console's editor share this package, so the rules cannot drift apart.
package editor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/decide"
	"github.com/biodream-llc/vaglio/internal/policy"
)

// Head is a policy source's leading comment block. Rewriting a policy (Format) keeps it and drops nothing else, so a
// source with comments further down is refused rather than silently stripped of them.
func Head(file string, src []byte) (string, error) {
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(src), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		b.WriteString(line)
	}
	if strings.Contains(string(src[b.Len():]), "#") {
		return "", fmt.Errorf("%s: has comments below its header, which rewriting would drop; move them to the top", file)
	}
	return b.String(), nil
}

// Ready is why a parsed policy may not be signed off, or nil: it must be a draft that passes its check with no warning
// but its draft status.
func Ready(p *policy.Policy) error {
	if p.Status != policy.Draft {
		return errors.New("it is not a draft: it is signed already, or was never drafted")
	}
	if rep := decide.Check(p); !rep.OK() {
		return errors.New("it fails its check; a policy that decides badly is not signed")
	}
	for _, warn := range policy.Lint(p) {
		if !strings.HasPrefix(warn, "status draft") {
			return fmt.Errorf("%s; settle the warnings before signing", warn)
		}
	}
	return nil
}

// SignOff returns a draft's source signed: its draft status cleared and the clinician's name, credential and the date
// recorded, its header comments kept.
func SignOff(file string, src []byte, by, date string) ([]byte, *policy.Policy, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, nil, errors.New("a sign-off names who signs, with their credential")
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return nil, nil, fmt.Errorf("the date is YYYY-MM-DD, not %q", date)
	}
	p, err := policy.Parse(file, string(src))
	if err != nil {
		return nil, nil, err
	}
	if err := Ready(p); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	h, err := Head(file, src)
	if err != nil {
		return nil, nil, err
	}
	p.Status, p.Signed = "", &policy.SignOff{By: by, Date: date}
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	// The header may still call it a draft awaiting sign-off; say, where a reader of the file looks first, that it no
	// longer is. The signed line below is what the engine reads; this is for people.
	h += fmt.Sprintf("#\n# Signed off by %s on %s: in force from then, whatever the comments above say of its draft status.\n", by, date)
	return []byte(h + policy.Format(p)), p, nil
}

// Hash is a source's SHA-256, as the audit log records it.
func Hash(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ValidID is whether id may name a policy file.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Draft is one policy in progress.
type Draft struct {
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Saved  time.Time `json:"saved"`
	// Policy is the draft parsed, or nil when it does not parse yet (Error says where).
	Policy *policy.Policy `json:"-"`
	Error  string         `json:"error,omitempty"`
}

// Dir is a policy directory: the policies in force, and drafts/ beneath it.
type Dir struct{ Path string }

func (d Dir) drafts() string { return filepath.Join(d.Path, "drafts") }

func (d Dir) draftFile(id string) string { return filepath.Join(d.drafts(), id+policy.Ext) }

// LiveFile is the file a policy in force lives in.
func (d Dir) LiveFile(id string) string { return filepath.Join(d.Path, id+policy.Ext) }

// Drafts lists the drafts, by id.
func (d Dir) Drafts() ([]Draft, error) {
	files, err := filepath.Glob(filepath.Join(d.drafts(), "*"+policy.Ext))
	if err != nil {
		return nil, err
	}
	out := []Draft{}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), policy.Ext)
		if dr, ok, err := d.Draft(id); err != nil {
			return nil, err
		} else if ok {
			out = append(out, dr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Draft reads one draft.
func (d Dir) Draft(id string) (Draft, bool, error) {
	if !ValidID(id) {
		return Draft{}, false, nil
	}
	f := d.draftFile(id)
	info, err := os.Stat(f)
	if errors.Is(err, os.ErrNotExist) {
		return Draft{}, false, nil
	}
	if err != nil {
		return Draft{}, false, err
	}
	src, err := os.ReadFile(f)
	if err != nil {
		return Draft{}, false, err
	}
	dr := Draft{ID: id, Source: string(src), Saved: info.ModTime().UTC()}
	if p, err := policy.Parse(id+policy.Ext, dr.Source); err != nil {
		dr.Error = err.Error()
	} else {
		dr.Policy = p
	}
	return dr, true, nil
}

// MaxSource bounds a policy source. The largest library policy is a few kilobytes.
const MaxSource = 256 << 10

// SaveDraft writes a draft. Whatever it says, it is kept as a draft: a status line is made "draft", and a sign-off line
// is refused, since a signature is given by signing off, not typed. A source that does not parse is saved all the same,
// so work is never lost, as long as it names no other policy's id.
func (d Dir) SaveDraft(id string, src []byte) (Draft, error) {
	if !ValidID(id) {
		return Draft{}, fmt.Errorf("%q is not a policy id: lower case letters, digits and _, starting with a letter", id)
	}
	if len(src) > MaxSource {
		return Draft{}, fmt.Errorf("a policy source is at most %d bytes", MaxSource)
	}
	if p, err := policy.Parse(id+policy.Ext, string(src)); err == nil {
		if p.ID != id {
			return Draft{}, fmt.Errorf("the source is policy %q; save it as that, or change its id", p.ID)
		}
		if p.Signed != nil {
			return Draft{}, errors.New("a draft carries no signed line: a signature is given by signing off, not typed")
		}
		if p.Status != policy.Draft {
			return Draft{}, errors.New(`a draft says so: give it the line "status draft" under its version`)
		}
	}
	if err := os.MkdirAll(d.drafts(), 0o755); err != nil {
		return Draft{}, err
	}
	if err := writeAtomic(d.draftFile(id), src); err != nil {
		return Draft{}, err
	}
	dr, _, err := d.Draft(id)
	return dr, err
}

// RemoveDraft deletes a draft.
func (d Dir) RemoveDraft(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("no draft %q", id)
	}
	err := os.Remove(d.draftFile(id))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no draft %q", id)
	}
	return err
}

// Promote signs a draft off and puts it in force beside the policies in force now (live), returning the policy and its
// signed source. The draft is removed once the policy is written.
func (d Dir) Promote(id, by, date string, live map[string]*policy.Policy) (*policy.Policy, []byte, error) {
	dr, ok, err := d.Draft(id)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("no draft %q", id)
	}
	if dr.Error != "" {
		return nil, nil, errors.New("the draft does not parse: " + dr.Error)
	}
	signed, p, err := SignOff(id+policy.Ext, []byte(dr.Source), by, date)
	if err != nil {
		return nil, nil, err
	}
	if err := Fits(p, live); err != nil {
		return nil, nil, err
	}
	if err := writeAtomic(d.LiveFile(id), signed); err != nil {
		return nil, nil, err
	}
	if err := d.RemoveDraft(id); err != nil {
		return nil, nil, err
	}
	return p, signed, nil
}

// Fits is why a policy may not join the policies in force, or nil. It may replace the policy of its own id, with a new
// version: a version names one text, and replays, reports and the audit log rely on that. It may not claim a service
// another policy decides.
func Fits(p *policy.Policy, live map[string]*policy.Policy) error {
	if old, ok := live[p.ID]; ok && old.Version == p.Version {
		return fmt.Errorf("version %s of %s is in force already; a changed policy is a new version", p.Version, p.ID)
	}
	for _, o := range live {
		if o.ID == p.ID {
			continue
		}
		for _, a := range o.Services {
			for _, b := range p.Services {
				if a == b {
					return fmt.Errorf("%s %s is decided by policy %s already; a service has one policy", b.System, b.Code, o.ID)
				}
			}
		}
	}
	return nil
}

// writeAtomic replaces a file in one step, so a reader never sees half a policy.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
