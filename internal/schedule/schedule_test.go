/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package schedule

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, expr, tz string) *Schedule {
	t.Helper()
	s, err := Parse(expr, tz)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", expr, tz, err)
	}
	return s
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseRejects(t *testing.T) {
	for _, c := range []struct{ expr, tz string }{
		{"", "UTC"},
		{"   ", "UTC"},
		{"0 3 * *", "UTC"},
		{"61 3 * * *", "UTC"},
		{"0 3 * * * *", "UTC"},
		{"@fortnightly", "UTC"},
		{"CRON_TZ=Europe/Berlin 0 3 * * *", "UTC"},
		{"TZ=Europe/Berlin 0 3 * * *", "UTC"},
		{"0 3 * * *", "Mars/Olympus"},
		{"0 3 * * *", "Local"},
	} {
		if _, err := Parse(c.expr, c.tz); err == nil {
			t.Errorf("Parse(%q, %q) accepted it", c.expr, c.tz)
		}
	}
}

func TestParseAccepts(t *testing.T) {
	for _, c := range []struct{ expr, tz string }{
		{"0 3 * * *", ""},
		{"0 3 * * *", "UTC"},
		{"*/15 * * * *", "Asia/Tokyo"},
		{"@daily", "Europe/Berlin"},
		{"@every 6h", "UTC"},
		{"0 4 * * SUN", "America/New_York"},
	} {
		if _, err := Parse(c.expr, c.tz); err != nil {
			t.Errorf("Parse(%q, %q): %v", c.expr, c.tz, err)
		}
	}
}

func TestStepNewScheduleRunsNothingBeforeNow(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "UTC")
	now := at(t, "2026-10-18T10:00:00Z")
	due, next := s.Step(nil, now)
	if !due.IsZero() {
		t.Errorf("due = %v, want none", due)
	}
	if want := at(t, "2026-10-19T03:00:00Z"); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestStepDueAtTheScheduledTime(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "UTC")
	last := at(t, "2026-10-17T03:00:00Z")
	for _, c := range []struct {
		now, due, next string
	}{
		{"2026-10-18T02:59:59Z", "", "2026-10-18T03:00:00Z"},
		{"2026-10-18T03:00:00Z", "2026-10-18T03:00:00Z", "2026-10-19T03:00:00Z"},
		{"2026-10-18T03:00:01Z", "2026-10-18T03:00:00Z", "2026-10-19T03:00:00Z"},
	} {
		due, next := s.Step(&last, at(t, c.now))
		if c.due == "" && !due.IsZero() || c.due != "" && !due.Equal(at(t, c.due)) {
			t.Errorf("now %s: due = %v, want %q", c.now, due, c.due)
		}
		if !next.Equal(at(t, c.next)) {
			t.Errorf("now %s: next = %v, want %s", c.now, next, c.next)
		}
	}
}

func TestStepMissedRunsRunOnceForTheLatest(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "UTC")
	last := at(t, "2026-10-10T03:00:00Z")
	due, _ := s.Step(&last, at(t, "2026-10-18T12:00:00Z"))
	if want := at(t, "2026-10-18T03:00:00Z"); !due.Equal(want) {
		t.Errorf("due = %v, want the latest missed time %v", due, want)
	}
	// Handled: the next step finds nothing more until the next time.
	due, _ = s.Step(&due, at(t, "2026-10-18T12:00:00Z"))
	if !due.IsZero() {
		t.Errorf("due again = %v", due)
	}
}

func TestStepVeryOldLastStillRunsOnce(t *testing.T) {
	s := mustParse(t, "* * * * *", "UTC")
	last := at(t, "2020-01-01T00:00:00Z")
	now := at(t, "2026-10-18T12:00:30Z")
	start := time.Now()
	due, _ := s.Step(&last, now)
	if want := at(t, "2026-10-18T12:00:00Z"); !due.Equal(want) {
		t.Errorf("due = %v, want %v", due, want)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("Step took %v", time.Since(start))
	}
}

func TestStepYearlyAfterALongOutage(t *testing.T) {
	s := mustParse(t, "@yearly", "UTC")
	last := at(t, "2024-01-01T00:00:00Z")
	due, next := s.Step(&last, at(t, "2026-10-18T00:00:00Z"))
	if want := at(t, "2026-01-01T00:00:00Z"); !due.Equal(want) {
		t.Errorf("due = %v, want %v", due, want)
	}
	if want := at(t, "2027-01-01T00:00:00Z"); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestStepInTimeZone(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "Asia/Tokyo") // 03:00 JST = 18:00 UTC the day before
	last := at(t, "2026-10-16T18:00:00Z")
	due, next := s.Step(&last, at(t, "2026-10-17T18:30:00Z"))
	if want := at(t, "2026-10-17T18:00:00Z"); !due.Equal(want) {
		t.Errorf("due = %v, want %v", due, want)
	}
	if want := at(t, "2026-10-18T18:00:00Z"); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

// On the day clocks spring forward, a time in the skipped hour still runs
// that day, and on the day they fall back, a time in the repeated hour runs
// once.
func TestStepAcrossDaylightSaving(t *testing.T) {
	s := mustParse(t, "30 2 * * *", "Europe/Berlin")
	runs := func(from, to string) []time.Time {
		var out []time.Time
		last := at(t, from)
		end := at(t, to)
		for now := last; !now.After(end); now = now.Add(time.Minute) {
			if due, _ := s.Step(&last, now); !due.IsZero() {
				out = append(out, due)
				last = due
			}
		}
		return out
	}
	// 2026-03-29: 02:00 CET jumps to 03:00 CEST; 02:30 does not exist.
	spring := runs("2026-03-27T12:00:00Z", "2026-03-30T12:00:00Z")
	if len(spring) != 3 {
		t.Fatalf("spring forward: %d runs over three days: %v", len(spring), spring)
	}
	if want := at(t, "2026-03-29T01:00:00Z"); !spring[1].Equal(want) { // 03:00 CEST
		t.Errorf("skipped time ran at %v, want the end of the gap %v", spring[1], want)
	}
	// 2026-10-25: 03:00 CEST falls back to 02:00 CET; 02:30 happens twice.
	fall := runs("2026-10-23T12:00:00Z", "2026-10-26T12:00:00Z")
	if len(fall) != 3 {
		t.Fatalf("fall back: %d runs over three days: %v", len(fall), fall)
	}
	if want := at(t, "2026-10-25T00:30:00Z"); !fall[1].Equal(want) { // the first 02:30, CEST
		t.Errorf("repeated time ran at %v, want its first occurrence %v", fall[1], want)
	}
}

func TestStepLastInTheFuture(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "UTC")
	last := at(t, "2026-10-20T03:00:00Z")
	due, next := s.Step(&last, at(t, "2026-10-18T12:00:00Z"))
	if !due.IsZero() {
		t.Errorf("due = %v", due)
	}
	if want := at(t, "2026-10-19T03:00:00Z"); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextAcrossDaylightSavingHourly(t *testing.T) {
	s := mustParse(t, "0 * * * *", "Europe/Berlin")
	// Spring forward: 01:00 CET, then 03:00 CEST (02:00 does not exist).
	got := s.Next(at(t, "2026-03-29T00:00:00Z")) // 01:00 CET
	if want := at(t, "2026-03-29T01:00:00Z"); !got.Equal(want) {
		t.Errorf("after 01:00 CET: %v, want %v (03:00 CEST)", got, want)
	}
	// Fall back: 02:00 CEST, then 02:00 CET would repeat the wall clock; it
	// is not run twice, so the hour after 02:00 CEST is 03:00 CET.
	got = s.Next(at(t, "2026-10-25T00:00:00Z")) // 02:00 CEST
	if want := at(t, "2026-10-25T02:00:00Z"); !got.Equal(want) {
		t.Errorf("after 02:00 CEST: %v, want %v (03:00 CET)", got, want)
	}
}

func TestNextEvery(t *testing.T) {
	s := mustParse(t, "@every 6h", "Europe/Berlin")
	from := at(t, "2026-03-29T00:00:00Z")
	if got, want := s.Next(from), from.Add(6*time.Hour); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestNextIsAlwaysLater(t *testing.T) {
	for _, tz := range []string{"UTC", "Europe/Berlin", "America/New_York", "Australia/Lord_Howe", "Asia/Kolkata"} {
		for _, expr := range []string{"*/7 * * * *", "30 2 * * *", "0 0 1 * *", "15 1-3 * * *"} {
			s := mustParse(t, expr, tz)
			cur := at(t, "2026-01-01T00:00:00Z")
			for i := 0; i < 3000; i++ {
				next := s.Next(cur)
				if !next.After(cur) {
					t.Fatalf("%s %s: Next(%v) = %v, not later", tz, expr, cur, next)
				}
				cur = next
			}
		}
	}
}

// TestStepFindsWhatAFullScanFinds checks the windowed search against walking
// every scheduled time from last.
func TestStepFindsWhatAFullScanFinds(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	exprs := []string{"* * * * *", "*/7 * * * *", "0 3 * * *", "30 2 * * 0", "0 0 1 * *", "@yearly", "@every 90m", "15 4 29 2 *"}
	base := at(t, "2026-01-01T00:00:00Z")
	for _, expr := range exprs {
		for _, tz := range []string{"UTC", "Europe/Berlin", "Asia/Jerusalem"} {
			s := mustParse(t, expr, tz)
			for i := 0; i < 40; i++ {
				now := base.Add(time.Duration(rng.Int63n(int64(400 * 24 * time.Hour))))
				// Gaps up to 3 days for the dense schedules keep the full
				// scan quick; the sparse ones also get gaps past lookback.
				gap := 3 * 24 * time.Hour
				if i%4 == 0 && !strings.HasPrefix(expr, "*") {
					gap = 500 * 24 * time.Hour
				}
				last := now.Add(-time.Duration(rng.Int63n(int64(gap))))
				var want time.Time
				from := last
				if earliest := now.Add(-lookback); from.Before(earliest) {
					from = earliest
				}
				for x := s.Next(from); !x.IsZero() && !x.After(now); x = s.Next(x) {
					want = x
				}
				if got, _ := s.Step(&last, now); !got.Equal(want) {
					t.Fatalf("%s %s last %v now %v: due %v, want %v", expr, tz, last, now, got, want)
				}
			}
		}
	}
}
