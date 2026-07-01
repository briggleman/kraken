// Package cron parses standard 5-field cron expressions and computes the next
// run time. Fields are: minute (0-59), hour (0-23), day-of-month (1-31), month
// (1-12), day-of-week (0-6, Sunday=0; 7 also accepted as Sunday).
//
// Each field supports "*", a single value, a "a-b" range, a "*/step" or
// "a-b/step" step, and comma-separated lists of those. When both day-of-month
// and day-of-week are restricted (neither is "*"), a day matches if EITHER
// field matches — the traditional Vixie-cron rule.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	expr                         string
	min, hour, dom, month, dow   uint64 // bitsets of allowed values
	domRestricted, dowRestricted bool
}

// String returns the original expression.
func (s *Schedule) String() string { return s.expr }

type fieldSpec struct {
	min, max int
}

var (
	minuteField = fieldSpec{0, 59}
	hourField   = fieldSpec{0, 23}
	domField    = fieldSpec{1, 31}
	monthField  = fieldSpec{1, 12}
	dowField    = fieldSpec{0, 6}
)

// Parse compiles a 5-field cron expression.
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}
	s := &Schedule{expr: strings.Join(fields, " ")}
	var err error
	if s.min, _, err = parseField(fields[0], minuteField); err != nil {
		return nil, err
	}
	if s.hour, _, err = parseField(fields[1], hourField); err != nil {
		return nil, err
	}
	if s.dom, s.domRestricted, err = parseField(fields[2], domField); err != nil {
		return nil, err
	}
	if s.month, _, err = parseField(fields[3], monthField); err != nil {
		return nil, err
	}
	if s.dow, s.dowRestricted, err = parseField(fields[4], dowField); err != nil {
		return nil, err
	}
	return s, nil
}

// parseField returns the bitset of allowed values and whether the field is
// restricted (i.e. not a bare "*").
func parseField(field string, spec fieldSpec) (uint64, bool, error) {
	var bits uint64
	restricted := field != "*"
	for _, part := range strings.Split(field, ",") {
		rng := part
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			var err error
			if step, err = strconv.Atoi(part[i+1:]); err != nil || step < 1 {
				return 0, false, fmt.Errorf("cron: bad step in %q", part)
			}
			rng = part[:i]
		}
		lo, hi := spec.min, spec.max
		switch {
		case rng == "*":
			// full range
		case strings.Contains(rng, "-"):
			ab := strings.SplitN(rng, "-", 2)
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil {
				return 0, false, fmt.Errorf("cron: bad range %q", rng)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(rng)
			if err != nil {
				return 0, false, fmt.Errorf("cron: bad value %q", rng)
			}
			lo, hi = v, v
		}
		// Day-of-week accepts 7 as an alias for Sunday (0), including inside
		// ranges like "5-7" (Fri–Sun), so widen the accepted max for validation
		// and fold 7→0 when setting bits (not on the endpoints, which would break
		// ranges).
		maxAllowed := spec.max
		if spec == dowField {
			maxAllowed = 7
		}
		if lo > hi {
			return 0, false, fmt.Errorf("cron: range %q is inverted", rng)
		}
		if lo < spec.min || hi > maxAllowed {
			return 0, false, fmt.Errorf("cron: value out of range %d-%d in %q", spec.min, spec.max, part)
		}
		for v := lo; v <= hi; v += step {
			bv := v
			if spec == dowField {
				bv = v % 7 // 7 → 0 (Sunday)
			}
			bits |= 1 << uint(bv)
		}
	}
	return bits, restricted, nil
}

func bit(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// Next returns the first time strictly after `after` that matches the schedule,
// truncated to the minute. It returns the zero time if no match exists within a
// 5-year horizon (e.g. an impossible date like Feb 31).
func (s *Schedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !bit(s.month, int(t.Month())) {
			// Jump to the first day of next month at 00:00.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).AddDate(0, 1, 0)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
			continue
		}
		if !bit(s.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Add(time.Hour)
			continue
		}
		if !bit(s.min, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches applies the Vixie rule: if both DOM and DOW are restricted, match
// on either; otherwise match on whichever field(s) are restricted.
func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := bit(s.dom, t.Day())
	dowOK := bit(s.dow, int(t.Weekday()))
	switch {
	case s.domRestricted && s.dowRestricted:
		return domOK || dowOK
	case s.domRestricted:
		return domOK
	case s.dowRestricted:
		return dowOK
	default:
		return true
	}
}
