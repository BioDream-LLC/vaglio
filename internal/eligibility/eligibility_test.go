package eligibility

import (
	"os"
	"path/filepath"
	"testing"
)

func file(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "members.csv")
	os.WriteFile(f, []byte(body), 0o600)
	return f
}

func TestCheck(t *testing.T) {
	m, err := Open(file(t, "member_id,birth_date,plan,line,start,end,excluded\n"+
		"M1,1960-02-03,Gold PPO,commercial,2026-01-01,2026-06-30,\n"+
		"m1,1960-02-03,Gold PPO,commercial,2026-08-01,,97110;S9083\n"+
		"M2,,Medicare Advantage,medicare-advantage,2025-01-01,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ member, birth, date, service, want string }{
		{"M1", "1960-02-03", "2026-03-01", "72148", Active},
		{"m1", "", "2026-07-15", "72148", Inactive}, // the gap between two coverages
		{"M1", "1960-02-03", "2026-09-01", "97110", Excluded},
		{"M1", "1999-01-01", "2026-03-01", "72148", NotFound}, // another birth date: not taken for this member
		{"M2", "1950-01-01", "2026-03-01", "72148", Active},   // the file has no birth date: not compared
		{"M3", "", "2026-03-01", "72148", NotFound},
		{"", "", "2026-03-01", "72148", Unknown},
	} {
		if got := m.Check(tc.member, tc.birth, tc.date, tc.service); got.Status != tc.want || got.Why == "" {
			t.Errorf("%+v: %+v", tc, got)
		}
	}
	for name, body := range map[string]string{
		"no member column": "id,start\nM1,2026-01-01\n",
		"bad date":         "member_id,start\nM1,01/01/2026\n",
		"ends early":       "member_id,start,end\nM1,2026-02-01,2026-01-01\n",
		"empty":            "member_id,start\n",
	} {
		if _, err := Open(file(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReloads(t *testing.T) {
	f := file(t, "member_id,start\nM1,2026-01-01\n")
	m, _ := Open(f)
	if m.Check("M9", "", "2026-03-01", "x").Status != NotFound {
		t.Fatal("M9 before")
	}
	os.WriteFile(f, []byte("member_id,start\nM1,2026-01-01\nM9,2026-01-01\n"), 0o600)
	future := filepath.Join(filepath.Dir(f), "members.csv")
	info, _ := os.Stat(future)
	os.Chtimes(f, info.ModTime().Add(2e9), info.ModTime().Add(2e9))
	if m.Check("M9", "", "2026-03-01", "x").Status != Active {
		t.Error("the changed file was not read")
	}
}
