package retention

import (
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestSameAsRestic checks the rules against restic's own forget on a real
// repository, in UTC (where restic's periods and ours agree).
func TestSameAsRestic(t *testing.T) {
	bin := os.Getenv("TEST_RESTIC_BIN")
	if bin == "" {
		p, err := exec.LookPath("restic")
		if err != nil {
			t.Skip("no restic binary (set TEST_RESTIC_BIN)")
		}
		bin = p
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+filepath.Join(dir, "repo"),
		"RESTIC_PASSWORD=test",
		"RESTIC_CACHE_DIR="+filepath.Join(dir, "cache"),
		"TZ=UTC",
	)
	restic := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			stderr := ""
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			t.Fatalf("restic %v: %v: %s", args, err, stderr)
		}
		return out
	}
	restic("init")

	// Irregular times over 14 months: gaps, some on the same day.
	rng := rand.New(rand.NewSource(7))
	at := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	for at.Before(end) {
		restic("backup", "--host", "h", "--tag", "kind=scheduled", "--time", at.Format("2006-01-02 15:04:05"), src)
		at = at.Add(time.Duration(1+rng.Intn(250)) * time.Hour)
	}

	var listed []struct {
		ID   string    `json:"id"`
		Time time.Time `json:"time"`
	}
	if err := json.Unmarshal(restic("snapshots", "--json"), &listed); err != nil {
		t.Fatal(err)
	}
	snaps := make([]Snapshot, 0, len(listed))
	for _, s := range listed {
		snaps = append(snaps, Snapshot{ID: s.ID, Time: s.Time, Kind: KindScheduled})
	}

	for _, p := range []Policy{
		{Daily: 7, Weekly: 4, Monthly: 12},
		{Last: 3, Hourly: 10, Daily: 5, Yearly: 2},
		{Within: "1m14d", Monthly: 3},
		{Weekly: 30},
		{Last: 1},
	} {
		args := []string{"forget", "--dry-run", "--json", "--host", "h"}
		for _, kv := range []struct {
			flag string
			n    int
		}{{"--keep-last", p.Last}, {"--keep-hourly", p.Hourly}, {"--keep-daily", p.Daily},
			{"--keep-weekly", p.Weekly}, {"--keep-monthly", p.Monthly}, {"--keep-yearly", p.Yearly}} {
			if kv.n > 0 {
				args = append(args, kv.flag, strconv.Itoa(kv.n))
			}
		}
		if p.Within != "" {
			args = append(args, "--keep-within", p.Within)
		}
		var groups []struct {
			Keep []struct {
				ID string `json:"id"`
			} `json:"keep"`
		}
		if err := json.Unmarshal(restic(args...), &groups); err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, g := range groups {
			for _, k := range g.Keep {
				want = append(want, k.ID)
			}
		}
		p.Now = time.Now()
		_, decisions, err := Apply(p, snaps, Protect{})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, d := range decisions {
			if d.Keep {
				got = append(got, d.Snapshot.ID)
			}
		}
		sort.Strings(want)
		sort.Strings(got)
		if len(want) == 0 || !equal(got, want) {
			t.Errorf("%+v: we keep %d, restic keeps %d", p, len(got), len(want))
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
