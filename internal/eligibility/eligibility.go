// Package eligibility checks a request's member against the plan's member file: covered on the day of the service,
// under which plan and line of business, and whether the benefit leaves the service out. The file is the plan's own
// export, read at start and again whenever it changes.
//
// A failed check never denies: the request pends for a person to look, since a member file is late, partial or wrong
// often enough that "not found" is a question, not an answer.
package eligibility

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Statuses of a check.
const (
	Active   = "active"    // covered on the day, the service not excluded
	Inactive = "inactive"  // the member is known, but not covered on the day
	NotFound = "not-found" // no member with that id (and birth date) in the file
	Excluded = "excluded"  // covered, but the benefit leaves this service out
	Unknown  = "unknown"   // the request names no member id
)

// Coverage is one row of the member file: a member's coverage for a span of days.
type Coverage struct {
	Member    string   `json:"member"`
	BirthDate string   `json:"birthDate,omitempty"` // YYYY-MM-DD; when set, the request's must match
	Plan      string   `json:"plan,omitempty"`
	Line      string   `json:"line,omitempty"` // medicare, medicaid, commercial, exchange ...
	Start     string   `json:"start"`          // YYYY-MM-DD, inclusive
	End       string   `json:"end,omitempty"`  // YYYY-MM-DD, inclusive; empty: still covered
	Excluded  []string `json:"excluded,omitempty"`
}

// Check is the answer for one request: kept on the case, shown to the reviewer.
type Check struct {
	Status   string    `json:"status"`
	Why      string    `json:"why"`
	Member   string    `json:"member,omitempty"`
	Date     string    `json:"date"`
	Coverage *Coverage `json:"coverage,omitempty"`
	Source   string    `json:"source"` // the file and its date
}

// Members is a member file, reloaded when it changes.
type Members struct {
	path string
	mu   sync.Mutex
	mod  time.Time
	by   map[string][]Coverage
	src  string
}

// Open reads a member file: CSV with a header naming its columns, member_id, birth_date, plan, line, start, end and
// excluded (service codes separated by semicolons); member_id and start are required.
func Open(path string) (*Members, error) {
	m := &Members{path: path}
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Members) reload() error {
	info, err := os.Stat(m.path)
	if err != nil {
		return err
	}
	if info.ModTime().Equal(m.mod) && m.by != nil {
		return nil
	}
	f, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer f.Close()
	by, err := parse(f)
	if err != nil {
		return fmt.Errorf("%s: %w", m.path, err)
	}
	m.by, m.mod = by, info.ModTime()
	name := m.path
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	m.src = name + ", " + info.ModTime().UTC().Format(time.DateOnly)
	return nil
}

func parse(r io.Reader) (map[string][]Coverage, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, errors.New("the member file is empty")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"member_id", "start"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the header has no %s column", need)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	by := map[string][]Coverage{}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, err
		}
		c := Coverage{Member: get(rec, "member_id"), BirthDate: get(rec, "birth_date"), Plan: get(rec, "plan"),
			Line: strings.ToLower(get(rec, "line")), Start: get(rec, "start"), End: get(rec, "end")}
		if c.Member == "" {
			continue
		}
		for _, d := range []string{c.Start, c.End, c.BirthDate} {
			if d == "" {
				continue
			}
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return nil, fmt.Errorf("line %d: %q is not YYYY-MM-DD", line, d)
			}
		}
		if c.Start == "" {
			return nil, fmt.Errorf("line %d: member %s has no start date", line, c.Member)
		}
		if c.End != "" && c.End < c.Start {
			return nil, fmt.Errorf("line %d: member %s ends before it starts", line, c.Member)
		}
		for _, x := range strings.Split(get(rec, "excluded"), ";") {
			if x = strings.TrimSpace(x); x != "" {
				c.Excluded = append(c.Excluded, x)
			}
		}
		by[key(c.Member)] = append(by[key(c.Member)], c)
	}
	if len(by) == 0 {
		return nil, errors.New("the member file holds no members")
	}
	return by, nil
}

func key(id string) string { return strings.ToUpper(strings.TrimSpace(id)) }

// Size is how many members the file holds.
func (m *Members) Size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.by)
}

// Check looks a member up for a service (its code, without the system) on a day (YYYY-MM-DD). birthDate, when the
// request has one and the file does, must agree: an id typed wrong is then not taken for someone else.
func (m *Members) Check(member, birthDate, date, service string) Check {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reload(); err != nil && m.by == nil {
		return Check{Status: Unknown, Why: "the member file could not be read: " + err.Error(), Date: date, Source: m.src}
	}
	out := Check{Member: member, Date: date, Source: m.src}
	if strings.TrimSpace(member) == "" {
		out.Status, out.Why = Unknown, "the request names no member id"
		return out
	}
	var rows []Coverage
	for _, c := range m.by[key(member)] {
		if birthDate == "" || c.BirthDate == "" || c.BirthDate == birthDate {
			rows = append(rows, c)
		}
	}
	if len(rows) == 0 {
		out.Status = NotFound
		out.Why = "no member " + member + " in the member file"
		if len(m.by[key(member)]) > 0 {
			out.Why = "member " + member + " is in the member file with another birth date"
		}
		return out
	}
	for i := range rows {
		c := rows[i]
		if date < c.Start || (c.End != "" && date > c.End) {
			continue
		}
		out.Coverage = &c
		for _, x := range c.Excluded {
			if strings.EqualFold(x, service) {
				out.Status, out.Why = Excluded, "the member's benefit ("+c.Plan+") leaves out "+service
				return out
			}
		}
		out.Status, out.Why = Active, "covered on "+date+" under "+nonEmpty(c.Plan, "the plan")
		return out
	}
	last := rows[len(rows)-1]
	out.Coverage = &last
	out.Status = Inactive
	span := "from " + last.Start
	if last.End != "" {
		span += " to " + last.End
	}
	out.Why = "member " + member + " is not covered on " + date + " (covered " + span + ")"
	return out
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}
