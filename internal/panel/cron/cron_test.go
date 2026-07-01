package cron

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func TestParseErrors(t *testing.T) {
	bad := []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "5-1 * * * *", "a * * * *"}
	for _, expr := range bad {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q): expected error, got nil", expr)
		}
	}
}

func TestNext(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{ // every minute
			"* * * * *",
			time.Date(2026, 6, 25, 10, 30, 15, 0, utc),
			time.Date(2026, 6, 25, 10, 31, 0, 0, utc),
		},
		{ // top of every hour
			"0 * * * *",
			time.Date(2026, 6, 25, 10, 30, 0, 0, utc),
			time.Date(2026, 6, 25, 11, 0, 0, 0, utc),
		},
		{ // daily at 04:00
			"0 4 * * *",
			time.Date(2026, 6, 25, 10, 0, 0, 0, utc),
			time.Date(2026, 6, 26, 4, 0, 0, 0, utc),
		},
		{ // every 15 minutes — step
			"*/15 * * * *",
			time.Date(2026, 6, 25, 10, 7, 0, 0, utc),
			time.Date(2026, 6, 25, 10, 15, 0, 0, utc),
		},
		{ // every 6 hours
			"0 */6 * * *",
			time.Date(2026, 6, 25, 5, 0, 0, 0, utc),
			time.Date(2026, 6, 25, 6, 0, 0, 0, utc),
		},
		{ // Mondays at 09:30 (2026-06-25 is a Thursday → next Monday is the 29th)
			"30 9 * * 1",
			time.Date(2026, 6, 25, 12, 0, 0, 0, utc),
			time.Date(2026, 6, 29, 9, 30, 0, 0, utc),
		},
		{ // list of minutes
			"0,30 * * * *",
			time.Date(2026, 6, 25, 10, 15, 0, 0, utc),
			time.Date(2026, 6, 25, 10, 30, 0, 0, utc),
		},
		{ // DOW range ending in 7 (Sunday): Fri–Sun. From Thu 2026-06-25 → Fri 26th.
			"0 0 * * 5-7",
			time.Date(2026, 6, 25, 12, 0, 0, 0, utc),
			time.Date(2026, 6, 26, 0, 0, 0, 0, utc),
		},
		{ // "7" alone is Sunday. From Thu 2026-06-25 → Sun 28th.
			"0 0 * * 7",
			time.Date(2026, 6, 25, 12, 0, 0, 0, utc),
			time.Date(2026, 6, 28, 0, 0, 0, 0, utc),
		},
	}
	for _, c := range cases {
		got := mustParse(t, c.expr).Next(c.from)
		if !got.Equal(c.want) {
			t.Errorf("Next(%q, %v) = %v, want %v", c.expr, c.from, got, c.want)
		}
	}
}

func TestDayOfMonthOrWeek(t *testing.T) {
	utc := time.UTC
	// Both DOM and DOW restricted → match on either. The 1st of the month OR any
	// Monday. From 2026-06-25 (Thu), the next match is Mon 2026-06-29.
	s := mustParse(t, "0 0 1 * 1")
	got := s.Next(time.Date(2026, 6, 25, 0, 0, 0, 0, utc))
	want := time.Date(2026, 6, 29, 0, 0, 0, 0, utc)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}
