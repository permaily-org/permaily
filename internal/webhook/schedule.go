package webhook

import (
	"strings"
	"time"

	// The zones are the image's own: the enclave has no zoneinfo on disk,
	// so the image's Go toolchain pins every rule a schedule follows, and a
	// change of a zone's rules reaches schedules with a release (ADR 0039,
	// "Schedules").
	_ "time/tzdata"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Weekdays are the names a weekly schedule's On may hold, Sunday first as
// time.Weekday counts them.
var Weekdays = [7]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

// CheckSchedule says whether s is one of the fixed choices: every day, or
// every week on one weekday, at "HH:MM" on a quarter hour.
func CheckSchedule(s contracts.WebhookSchedule) error {
	if _, _, ok := clock(s.At); !ok {
		return ErrSchedule
	}
	switch s.Every {
	case contracts.EveryDay:
		if s.On != "" {
			return ErrSchedule
		}
	case contracts.EveryWeek:
		if weekday(s.On) < 0 {
			return ErrSchedule
		}
	default:
		return ErrSchedule
	}
	return nil
}

func clock(at string) (hour, minute int, ok bool) {
	if len(at) != 5 || at[2] != ':' {
		return 0, 0, false
	}
	digits := func(s string) (int, bool) {
		if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
			return 0, false
		}
		return int(s[0]-'0')*10 + int(s[1]-'0'), true
	}
	h, okh := digits(at[:2])
	m, okm := digits(at[3:])
	if !okh || !okm || h > 23 || m%15 != 0 || m > 45 {
		return 0, 0, false
	}
	return h, m, true
}

func weekday(on string) int {
	for i, w := range Weekdays {
		if w == on {
			return i
		}
	}
	return -1
}

// ZoneNameBytesMax bounds a time zone's name.
const ZoneNameBytesMax = 64

// Zone is the person's time zone by its IANA name, from the zones compiled
// into the image: never "Local" (the machine's), never a path, never an
// empty name.
func Zone(name string) (*time.Location, error) {
	if name == "" || len(name) > ZoneNameBytesMax || name == "Local" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return nil, ErrTimeZone
	}
	for _, r := range name {
		if !zoneRune(r) {
			return nil, ErrTimeZone
		}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrTimeZone
	}
	return loc, nil
}

// zoneRune says whether r may be in a zone's name: ASCII letters, digits,
// "/", "_", "-" and "+".
func zoneRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("/_-+", r)
}

// Next is the first time s falls strictly after after, in loc. A wall time
// that a change of the clocks skips (02:30 on the night the clocks go
// forward, or a whole day a zone skipped) falls at the instant the clocks
// changed, the first one at or after it on the wall; a wall time that
// happens twice (the night they go back) falls at the first of the two.
// s must pass CheckSchedule.
func Next(s contracts.WebhookSchedule, loc *time.Location, after time.Time) time.Time {
	hour, minute, _ := clock(s.At)
	on := weekday(s.On)
	local := after.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	// A weekly schedule falls within seven days; a day before covers a
	// candidate a skipped day moves past midnight, and two after a day
	// skipped itself.
	for i := -1; i <= 9; i++ {
		d := day.AddDate(0, 0, i)
		if s.Every == contracts.EveryWeek && int(d.Weekday()) != on {
			continue
		}
		if t := wallInstant(time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.UTC), loc); t.After(after) {
			return t
		}
	}
	// Unreachable for a schedule CheckSchedule passes.
	return after.Add(7 * 24 * time.Hour)
}

// wall is t's local clock in loc, read as if it were UTC.
func wall(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// wallInstant is the instant whose clock in loc reads w (w given as UTC),
// the first of two when the clocks went back, and the instant the clocks
// changed when they skipped it.
func wallInstant(w time.Time, loc *time.Location) time.Time {
	// The offsets in force around w: a zone changes its clocks at most a
	// few times a year, and never twice within these probes.
	var offsets []int
	for _, h := range []int{-36, -12, 0, 12, 36} {
		_, off := w.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		if !containsInt(offsets, off) {
			offsets = append(offsets, off)
		}
	}
	var best time.Time
	for _, off := range offsets {
		t := w.Add(-time.Duration(off) * time.Second)
		if wall(t, loc).Equal(w) && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	if !best.IsZero() {
		return best
	}
	// Skipped: the change of the clocks whose wall time jumps over w.
	for _, off := range offsets {
		start, _ := w.Add(-time.Duration(off) * time.Second).In(loc).ZoneBounds()
		if start.IsZero() {
			continue
		}
		if !wall(start, loc).Before(w) && wall(start.Add(-time.Second), loc).Before(w) && (best.IsZero() || start.Before(best)) {
			best = start
		}
	}
	if best.IsZero() {
		// No rule of the zone explains it; the first reading is kept.
		return w.Add(-time.Duration(offsets[0]) * time.Second)
	}
	return best
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Due is the latest time s fell in (last, now], in loc, and whether a
// notice for it is late: sent more than LateAfter after it, or standing for
// more than one run missed. ok is false when none fell. A service that was
// down for weeks sends one notice, late, never one per run missed. What the
// notice tells the vendor to fetch from is not the run before at but the
// coverage boundary the queue keeps (Coverage; design review, finding 6),
// so every missed run's mail is fetched.
func Due(s contracts.WebhookSchedule, loc *time.Location, last, now time.Time) (at time.Time, late, ok bool) {
	const window = 9 * 24 * time.Hour
	missedMore := false
	if now.Sub(last) > window {
		last, missedMore = now.Add(-window), true
	}
	runs := 0
	for t := Next(s, loc, last); !t.After(now); t = Next(s, loc, t) {
		at, runs = t, runs+1
	}
	if runs == 0 {
		return time.Time{}, false, false
	}
	return at, runs > 1 || missedMore || now.Sub(at) > LateAfter, true
}

// Coverage is a schedule's first coverage boundary: when its connection was
// made, rounded down to its UTC minute (a notice's Since is on a minute).
// The queue keeps the boundary, moves it to a scheduled notice's
// ScheduledAt only when the vendor took that notice (a 2xx), and keeps it
// across runs missed, skipped and given up; every scheduled notice's Since
// is the boundary, so a late notice that stands for many runs tells the
// vendor to fetch everything since the last one it took.
func Coverage(madeAt time.Time) time.Time {
	return madeAt.UTC().Truncate(time.Minute)
}
