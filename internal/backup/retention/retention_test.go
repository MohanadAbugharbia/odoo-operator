package retention

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

func snap(id, at, kind string) Snapshot {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return Snapshot{ID: id, Time: t, Kind: kind, Backup: "b-" + id}
}

func ids(snaps []Snapshot) []string {
	out := make([]string, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, s.ID)
	}
	return out
}

// daily returns one scheduled snapshot a day at 03:00Z for n days ending on end.
func daily(n int, end time.Time) []Snapshot {
	var out []Snapshot
	for i := 0; i < n; i++ {
		t := end.AddDate(0, 0, -i)
		out = append(out, Snapshot{ID: t.Format("20060102"), Time: t, Kind: KindScheduled, Backup: "b-" + t.Format("20060102")})
	}
	return out
}

func TestDailyKeepsTheNewestOfEachDay(t *testing.T) {
	snaps := []Snapshot{
		snap("a1", "2026-03-01T03:00:00Z", KindScheduled),
		snap("a2", "2026-03-01T15:00:00Z", KindScheduled),
		snap("b1", "2026-03-02T03:00:00Z", KindScheduled),
		snap("c1", "2026-03-03T03:00:00Z", KindScheduled),
		snap("c2", "2026-03-03T15:00:00Z", KindScheduled),
	}
	forget, _, err := Apply(Policy{Daily: 2}, snaps, Protect{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(forget), []string{"a1", "a2", "c1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forget %v, want %v", got, want)
	}
}

func TestDaysAreThoseOfTheTimeZone(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	// 23:30Z on the 1st is 00:30 on the 2nd in Berlin: the same day as b.
	snaps := []Snapshot{
		snap("z", "2026-02-27T10:00:00Z", KindScheduled),
		snap("a", "2026-03-01T23:30:00Z", KindScheduled),
		snap("b", "2026-03-02T10:00:00Z", KindScheduled),
		snap("c", "2026-03-03T10:00:00Z", KindScheduled),
	}
	forget, _, _ := Apply(Policy{Daily: 3}, snaps, Protect{})
	if got := ids(forget); !reflect.DeepEqual(got, []string{"z"}) {
		t.Fatalf("in UTC a, b and c are three days, forget %v", got)
	}
	forget, _, _ = Apply(Policy{Daily: 3, Location: berlin}, snaps, Protect{})
	if got := ids(forget); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("in Berlin a and b share a day, forget %v", got)
	}
}

func TestARuleWithCountLeftKeepsTheOldest(t *testing.T) {
	snaps := []Snapshot{
		snap("a1", "2026-03-01T03:00:00Z", KindScheduled),
		snap("a2", "2026-03-01T15:00:00Z", KindScheduled),
		snap("b", "2026-03-02T03:00:00Z", KindScheduled),
	}
	forget, _, _ := Apply(Policy{Daily: 5}, snaps, Protect{})
	if len(forget) != 0 {
		t.Fatalf("forget %v: like restic, the oldest is kept while daily has count left", ids(forget))
	}
}

func TestWithinIgnoresSnapshotsInTheFuture(t *testing.T) {
	snaps := []Snapshot{
		snap("old", "2026-01-01T00:00:00Z", KindScheduled),
		snap("past", "2026-03-01T00:00:00Z", KindScheduled),
		snap("future", "2026-09-01T00:00:00Z", KindScheduled),
	}
	now, _ := time.Parse(time.RFC3339, "2026-03-02T00:00:00Z")
	forget, _, _ := Apply(Policy{Within: "10d", Now: now}, snaps, Protect{})
	if got := ids(forget); !reflect.DeepEqual(got, []string{"old"}) {
		t.Fatalf("forget %v, want [old]: within counts from past, not future", got)
	}
}

func TestDefaultsOverAYear(t *testing.T) {
	end := time.Date(2026, 12, 31, 3, 0, 0, 0, time.UTC)
	snaps := daily(400, end)
	forget, decisions, err := Apply(Policy{Daily: 7, Weekly: 4, Monthly: 12}, snaps, Protect{})
	if err != nil {
		t.Fatal(err)
	}
	if len(forget) != MaxForget {
		t.Fatalf("forget %d, want the cap %d", len(forget), MaxForget)
	}
	if !forget[0].Time.Before(forget[len(forget)-1].Time) {
		t.Fatal("forget is not oldest first")
	}
	kept := 0
	for _, d := range decisions {
		if d.Keep {
			kept++
		}
	}
	// 7 days, then 4 weeks less those already covered, then 12 months: restic
	// keeps 7 + 2 + 11 here (the newest weeks and month are also kept daily).
	if kept != 20 {
		t.Fatalf("kept %d snapshots, want about 7 daily + weekly + monthly", kept)
	}
	// Running again on what is left converges to the same kept set.
	left := snaps
	for range 20 {
		forget, _, _ := Apply(Policy{Daily: 7, Weekly: 4, Monthly: 12}, left, Protect{})
		if len(forget) == 0 {
			break
		}
		gone := map[string]bool{}
		for _, f := range forget {
			gone[f.ID] = true
		}
		var next []Snapshot
		for _, s := range left {
			if !gone[s.ID] {
				next = append(next, s)
			}
		}
		left = next
	}
	if len(left) != kept {
		t.Fatalf("after thinning %d remain, want %d", len(left), kept)
	}
}

func TestWithinIsCountedFromTheNewest(t *testing.T) {
	end := time.Date(2026, 3, 31, 3, 0, 0, 0, time.UTC)
	snaps := daily(30, end)
	forget, _, err := Apply(Policy{Within: "7d"}, snaps, Protect{})
	if err != nil {
		t.Fatal(err)
	}
	// Kept: the newest and the six days before it (more than 7 days back is out).
	if len(forget) != 23 {
		t.Fatalf("forget %d, want 23", len(forget))
	}
}

func TestProtectedAndNewestAreKept(t *testing.T) {
	snaps := []Snapshot{
		snap("old", "2026-01-01T03:00:00Z", KindScheduled),
		snap("kept", "2026-01-02T03:00:00Z", KindScheduled),
		snap("restore", "2026-01-03T03:00:00Z", KindScheduled),
		snap("mid", "2026-01-04T03:00:00Z", KindScheduled),
		snap("new", "2026-01-05T03:00:00Z", KindScheduled),
	}
	p := Protect{Backups: map[string]bool{"b-kept": true}, SnapshotIDs: []string{"resto"}, Current: "mid"}
	forget, _, err := Apply(Policy{Last: 1}, snaps, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(forget); !reflect.DeepEqual(got, []string{"old"}) {
		t.Fatalf("forget %v, want [old]", got)
	}
}

func TestProtectedDoNotUseUpARule(t *testing.T) {
	snaps := []Snapshot{
		snap("a", "2026-01-01T03:00:00Z", KindScheduled),
		snap("b", "2026-01-02T03:00:00Z", KindScheduled),
		snap("c", "2026-01-03T03:00:00Z", KindScheduled),
	}
	forget, _, _ := Apply(Policy{Last: 2}, snaps, Protect{Backups: map[string]bool{"b-c": true}})
	if len(forget) != 0 {
		t.Fatalf("forget %v: c is protected, so last 2 keeps a and b", ids(forget))
	}
}

func TestManualAndPreUpgradeHaveTheirOwnCount(t *testing.T) {
	snaps := []Snapshot{
		snap("m1", "2026-01-01T03:00:00Z", KindManual),
		snap("m2", "2026-01-02T03:00:00Z", KindManual),
		snap("m3", "2026-01-03T03:00:00Z", KindManual),
		snap("u1", "2026-01-01T04:00:00Z", KindPreUpgrade),
		snap("u2", "2026-01-02T04:00:00Z", KindPreUpgrade),
		snap("s1", "2026-01-04T03:00:00Z", KindScheduled),
	}
	forget, _, _ := Apply(Policy{Daily: 7}, snaps, Protect{})
	if len(forget) != 0 {
		t.Fatalf("manual 0 keeps every manual backup, forget %v", ids(forget))
	}
	forget, _, _ = Apply(Policy{Daily: 7, Manual: 1}, snaps, Protect{})
	got := ids(forget)
	sort.Strings(got)
	if want := []string{"m1", "m2", "u1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forget %v, want %v", got, want)
	}
}

func TestNoRulesKeepsEverything(t *testing.T) {
	snaps := daily(50, time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC))
	snaps = append(snaps, Snapshot{ID: "x", Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: "unknown"})
	forget, _, err := Apply(Policy{}, snaps, Protect{})
	if err != nil || len(forget) != 0 {
		t.Fatalf("forget %v, err %v", ids(forget), err)
	}
}

func TestUnknownKindIsNeverForgotten(t *testing.T) {
	snaps := []Snapshot{
		{ID: "hand", Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		snap("s1", "2026-01-04T03:00:00Z", KindScheduled),
	}
	forget, _, _ := Apply(Policy{Last: 1}, snaps, Protect{})
	if len(forget) != 0 {
		t.Fatalf("forget %v", ids(forget))
	}
}

func TestWithinParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want within
		bad  bool
	}{
		{in: "", want: within{}},
		{in: "1y6m", want: within{years: 1, months: 6}},
		{in: "17d12h", want: within{days: 17, hours: 12}},
		{in: "2w", bad: true},
		{in: "3x", bad: true},
		{in: "d", bad: true},
		{in: "1.5d", bad: true},
	} {
		got, err := parseWithin(tc.in)
		if (err != nil) != tc.bad {
			t.Errorf("%q: err %v", tc.in, err)
			continue
		}
		if !tc.bad && got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
	if _, _, err := Apply(Policy{Within: "soon"}, nil, Protect{}); err == nil {
		t.Fatal("an invalid within is refused")
	}
}

func TestEmptyRepository(t *testing.T) {
	forget, decisions, err := Apply(Policy{Daily: 1}, nil, Protect{})
	if err != nil || len(forget) != 0 || len(decisions) != 0 {
		t.Fatal(fmt.Sprint(forget, decisions, err))
	}
}
