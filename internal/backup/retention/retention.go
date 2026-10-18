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

// Package retention decides which of an instance's snapshots are forgotten.
// The keep rules are restic's (keep-last, keep-hourly, ... keep-within), with
// the same meaning, but they are applied here rather than by restic forget so
// that hours, days, weeks, months and years are those of the instance's time
// zone (restic buckets by the offset each snapshot was stored with), and so
// that a protected backup is kept outside the rules instead of using up one
// of their slots.
package retention

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backup kinds, as in the snapshots' kind= tag.
const (
	KindScheduled  = "scheduled"
	KindManual     = "manual"
	KindPreUpgrade = "pre-upgrade"
)

// MaxForget is the most snapshots one run forgets, oldest first. It bounds
// the list the backup Job reports back in its 4 KiB termination message; a
// repository with more to forget is thinned over the next backups.
const MaxForget = 24

// Policy is an instance's retention.
type Policy struct {
	// Last, Hourly, Daily, Weekly, Monthly and Yearly keep the newest
	// snapshot of each of the last N periods that have one (Last: the newest
	// N snapshots). 0 turns a rule off.
	Last, Hourly, Daily, Weekly, Monthly, Yearly int
	// Within keeps every snapshot taken less than this before the newest
	// one, in restic's format (1y6m, 30d, 12h).
	Within string
	// Manual keeps the newest N manual backups, and separately the newest N
	// pre-upgrade backups; 0 keeps them all.
	Manual int
	// Location is the time zone periods are counted in; UTC when nil.
	Location *time.Location
	// Now is the clock Within ignores snapshots after (time.Now when zero).
	Now time.Time
}

// Snapshot is what the policy needs to know of one snapshot.
type Snapshot struct {
	ID   string
	Time time.Time
	// Kind and Backup are the snapshot's kind= and backup= tags.
	Kind   string
	Backup string
	// Original is the snapshot a copy was made from; protections by ID
	// match it too.
	Original string
}

// Protect lists what is never forgotten.
type Protect struct {
	// Backups are OdooBackups that are kept (spec.keep). They stand outside
	// the rules: they do not take up a slot of daily, last and the rest.
	Backups map[string]bool
	// SnapshotIDs are snapshots something still needs (a restore not yet
	// run), as IDs or ID prefixes. Current is the snapshot the running
	// backup just wrote. Both count under the rules like any other, and are
	// kept whatever the rules decide.
	SnapshotIDs []string
	Current     string
}

// pinned reports a snapshot that is kept whatever the rules decide.
func (p Protect) pinned(s Snapshot) bool {
	if p.Current != "" && s.ID == p.Current {
		return true
	}
	for _, id := range p.SnapshotIDs {
		if id != "" && (strings.HasPrefix(s.ID, id) || (s.Original != "" && strings.HasPrefix(s.Original, id))) {
			return true
		}
	}
	return false
}

var withinRE = regexp.MustCompile(`^([0-9]+[ymdh])*$`)

// within is a parsed keep-within duration.
type within struct{ years, months, days, hours int }

func parseWithin(s string) (within, error) {
	var w within
	if !withinRE.MatchString(s) {
		return w, fmt.Errorf("invalid duration %q: use numbers followed by y, m, d or h, such as 1y6m", s)
	}
	for _, part := range regexp.MustCompile(`[0-9]+[ymdh]`).FindAllString(s, -1) {
		n, err := strconv.Atoi(part[:len(part)-1])
		if err != nil {
			return w, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		switch part[len(part)-1] {
		case 'y':
			w.years = n
		case 'm':
			w.months = n
		case 'd':
			w.days = n
		case 'h':
			w.hours = n
		}
	}
	return w, nil
}

func (w within) zero() bool { return w == within{} }

// before returns t minus the duration, as restic computes it.
func (w within) before(t time.Time) time.Time {
	return t.AddDate(-w.years, -w.months, -w.days).Add(-time.Duration(w.hours) * time.Hour)
}

// Validate reports a policy that cannot be applied.
func (p Policy) Validate() error {
	for _, n := range []int{p.Last, p.Hourly, p.Daily, p.Weekly, p.Monthly, p.Yearly, p.Manual} {
		if n < 0 {
			return fmt.Errorf("keep counts must not be negative")
		}
	}
	_, err := parseWithin(p.Within)
	return err
}

// keepsAll reports whether the scheduled rules are all off, which keeps
// every scheduled snapshot (restic refuses a forget without rules for the
// same reason: it would delete everything).
func (p Policy) keepsAll() bool {
	return p.Last == 0 && p.Hourly == 0 && p.Daily == 0 && p.Weekly == 0 &&
		p.Monthly == 0 && p.Yearly == 0 && p.Within == ""
}

// Decision is what Apply decided for one snapshot.
type Decision struct {
	Snapshot Snapshot
	Keep     bool
	// Reasons name the rules that keep it, such as "daily" or "protected".
	Reasons []string
}

// Apply decides every snapshot and returns the ones to forget, oldest
// first, at most MaxForget of them. The newest snapshot of the instance and
// every protected one are always kept; a snapshot of an unknown kind is
// never touched.
func Apply(p Policy, snaps []Snapshot, protect Protect) ([]Snapshot, []Decision, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	w, _ := parseWithin(p.Within)
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}

	sorted := append([]Snapshot(nil), snaps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.After(sorted[j].Time) })
	decisions := make([]Decision, len(sorted))
	for i, s := range sorted {
		decisions[i] = Decision{Snapshot: s}
	}
	keep := func(i int, why string) {
		decisions[i].Keep = true
		decisions[i].Reasons = append(decisions[i].Reasons, why)
	}
	if len(sorted) > 0 {
		keep(0, "newest")
	}

	groups := map[string][]int{}
	for i, s := range sorted {
		switch {
		case protect.Backups[s.Backup]:
			keep(i, "keep")
		case s.Kind == KindScheduled || s.Kind == KindManual || s.Kind == KindPreUpgrade:
			groups[s.Kind] = append(groups[s.Kind], i)
		default:
			keep(i, "unknown kind")
		}
	}

	if idx := groups[KindScheduled]; len(idx) > 0 {
		if p.keepsAll() {
			for _, i := range idx {
				keep(i, "no rules")
			}
		} else {
			applyRules(p, w, loc, sorted, idx, keep)
		}
	}
	for _, kind := range []string{KindManual, KindPreUpgrade} {
		for n, i := range groups[kind] {
			if p.Manual == 0 || n < p.Manual {
				keep(i, "manual")
			}
		}
	}

	for i, s := range sorted {
		if protect.pinned(s) {
			keep(i, "protected")
		}
	}

	var forget []Snapshot
	for i := len(decisions) - 1; i >= 0 && len(forget) < MaxForget; i-- {
		if !decisions[i].Keep {
			forget = append(forget, decisions[i].Snapshot)
		}
	}
	return forget, decisions, nil
}

// applyRules runs restic's bucket rules over the snapshots at idx (newest
// first): each rule keeps a snapshot whose period differs from the period of
// the last one it kept, until its count is used up; a rule with count left
// also keeps the oldest snapshot. Within counts back from the newest
// snapshot that is not in the future. This is restic's ApplyPolicy (0.18).
func applyRules(p Policy, w within, loc *time.Location, sorted []Snapshot, idx []int, keep func(int, string)) {
	type rule struct {
		name   string
		count  int
		bucket func(time.Time) int
		last   int
		seen   bool
	}
	n := 0
	rules := []*rule{
		{name: "last", count: p.Last, bucket: func(time.Time) int { n++; return n }},
		{name: "hourly", count: p.Hourly, bucket: func(t time.Time) int {
			return t.Year()*1000000 + int(t.Month())*10000 + t.Day()*100 + t.Hour()
		}},
		{name: "daily", count: p.Daily, bucket: func(t time.Time) int {
			return t.Year()*10000 + int(t.Month())*100 + t.Day()
		}},
		{name: "weekly", count: p.Weekly, bucket: func(t time.Time) int {
			y, wk := t.ISOWeek()
			return y*100 + wk
		}},
		{name: "monthly", count: p.Monthly, bucket: func(t time.Time) int { return t.Year()*100 + int(t.Month()) }},
		{name: "yearly", count: p.Yearly, bucket: func(t time.Time) int { return t.Year() }},
	}
	var cutoff time.Time
	if !w.zero() {
		now := p.Now
		if now.IsZero() {
			now = time.Now()
		}
		var latest time.Time
		for _, i := range idx {
			if t := sorted[i].Time; t.After(latest) && t.Before(now) {
				latest = t
			}
		}
		cutoff = w.before(latest)
	}
	for k, i := range idx {
		t := sorted[i].Time.In(loc)
		oldest := k == len(idx)-1
		for _, r := range rules {
			if r.count == 0 {
				continue
			}
			b := r.bucket(t)
			if !r.seen || b != r.last || oldest {
				keep(i, r.name)
				r.last, r.seen = b, true
				r.count--
			}
		}
		if !w.zero() && sorted[i].Time.After(cutoff) {
			keep(i, "within")
		}
	}
}
