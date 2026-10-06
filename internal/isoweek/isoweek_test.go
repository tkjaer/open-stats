package isoweek

import (
	"testing"
	"time"
)

func day(s string) Day {
	d, err := ParseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestDays(t *testing.T) {
	tests := []struct{ week, first, last string }{
		{"2026-W40", "2026-09-28", "2026-10-04"},
		{"2026-W01", "2025-12-29", "2026-01-04"},
		{"2026-W53", "2026-12-28", "2027-01-03"},
		{"2020-W53", "2020-12-28", "2021-01-03"},
	}
	for _, tt := range tests {
		days, err := Days(tt.week)
		if err != nil {
			t.Fatalf("%s: %v", tt.week, err)
		}
		if len(days) != 7 || days[0].Format(time.DateOnly) != tt.first || days[6].Format(time.DateOnly) != tt.last {
			t.Errorf("%s: got %v .. %v", tt.week, days[0], days[6])
		}
		if Of(days[0]) != tt.week || Of(days[6]) != tt.week {
			t.Errorf("%s: Of() disagrees", tt.week)
		}
	}
}

func TestBadWeeks(t *testing.T) {
	for _, w := range []string{"2026-W00", "2026-W54", "2025-W53", "2026-40", "2026-W4", "../../etc", "", "2026-W40\n"} {
		if _, err := Days(w); err == nil {
			t.Errorf("%q: no error", w)
		}
	}
}

func TestLastComplete(t *testing.T) {
	tests := map[string]string{
		"2026-10-05": "2026-W40", // Monday, when the timer runs
		"2026-10-06": "2026-W40",
		"2026-10-11": "2026-W40", // Sunday: W41 isn't over yet
		"2026-10-04": "2026-W39",
		"2027-01-04": "2026-W53",
	}
	for today, want := range tests {
		if got := LastComplete(day(today)); got != want {
			t.Errorf("%s: got %s, want %s", today, got, want)
		}
	}
}
