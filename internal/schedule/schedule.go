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

// Package schedule reads the cron schedules of backups, drills and store
// tasks and decides when one is due.
package schedule

import (
	"fmt"
	"strings"
	"time"
	// The operator image has no zoneinfo; embed it.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// lookback bounds how far back a missed run is searched for: a schedule
// whose last handled run is older still runs once, for its latest time in
// this window.
const lookback = 366 * 24 * time.Hour

// Schedule is a cron schedule in a time zone.
type Schedule struct {
	cron cron.Schedule
	loc  *time.Location
}

// Parse reads a standard five-field cron expression, or a descriptor such as
// @daily or @every 6h, in the IANA time zone tz ("" is UTC).
func Parse(expr, tz string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty schedule")
	}
	if strings.HasPrefix(expr, "CRON_TZ=") || strings.HasPrefix(expr, "TZ=") {
		return nil, fmt.Errorf("schedule %q: set the time zone in timeZone, not in the schedule", expr)
	}
	loc, err := Location(tz)
	if err != nil {
		return nil, err
	}
	c, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", expr, err)
	}
	return &Schedule{cron: c, loc: loc}, nil
}

// Location loads an IANA time zone; "" is UTC.
func Location(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || strings.EqualFold(tz, "Local") {
		return nil, fmt.Errorf("unknown time zone %q", tz)
	}
	return loc, nil
}

// Location is the schedule's time zone.
func (s *Schedule) Location() *time.Location { return s.loc }

// Next is the first scheduled time after t. Times are matched against the
// wall clock of the time zone, so on the day clocks spring forward a time in
// the skipped hour runs at the end of the gap (02:30 runs at 03:00), and on
// the day they fall back a time in the repeated hour runs once, the first
// time the clock shows it.
func (s *Schedule) Next(t time.Time) time.Time {
	if _, every := s.cron.(cron.ConstantDelaySchedule); every {
		return s.cron.Next(t.In(s.loc))
	}
	w := wall(t, s.loc)
	for range 1000 {
		w = s.cron.Next(w)
		if w.IsZero() {
			return time.Time{}
		}
		if at := instant(w, s.loc); at.After(t) {
			return at
		}
	}
	return time.Time{}
}

// wall is t's wall clock in loc, written as a UTC time so that cron steps
// through it without daylight saving.
func wall(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// instant is the first moment loc's wall clock shows w, or, when w falls in
// a gap the clock skips, the first moment after the gap.
func instant(w time.Time, loc *time.Location) time.Time {
	guess := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), loc)
	var first time.Time
	for _, d := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 0, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		if c := guess.Add(d); wall(c, loc).Equal(w) && (first.IsZero() || c.Before(first)) {
			first = c
		}
	}
	if !first.IsZero() {
		return first
	}
	// In a gap: the earliest moment whose wall clock is past w.
	lo, hi := guess.Add(-3*time.Hour), guess.Add(3*time.Hour)
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if wall(mid, loc).After(w) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi.Truncate(time.Second)
}

// Step decides the schedule at now. last is the latest scheduled time already
// handled; nil means the schedule is new, and nothing before now is due. due
// is the latest scheduled time after last and at or before now (zero when
// none): however many times were missed, only that one runs. next is the
// first scheduled time after now.
func (s *Schedule) Step(last *time.Time, now time.Time) (due, next time.Time) {
	next = s.Next(now)
	if last == nil {
		return time.Time{}, next
	}
	from := *last
	if earliest := now.Add(-lookback); from.Before(earliest) {
		from = earliest
	}
	for t := s.Next(from); !t.IsZero() && !t.After(now); t = s.Next(t) {
		due = t
	}
	return due, next
}
