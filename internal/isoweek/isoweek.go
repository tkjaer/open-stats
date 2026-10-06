// SPDX-License-Identifier: AGPL-3.0-or-later

// Package isoweek handles ISO 8601 weeks ("2026-W40") in UTC.
package isoweek

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var weekRE = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

// Day is midnight UTC on a calendar day.
type Day = time.Time

// DayOf truncates t to its UTC day.
func DayOf(t time.Time) Day {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ParseDay parses YYYY-MM-DD.
func ParseDay(s string) (Day, error) {
	return time.ParseInLocation(time.DateOnly, s, time.UTC)
}

// Format returns the week id, e.g. 2026-W40.
func Format(year, week int) string { return fmt.Sprintf("%04d-W%02d", year, week) }

// Of returns the ISO week a day belongs to.
func Of(d Day) string { return Format(d.ISOWeek()) }

// Days returns the seven days (Monday to Sunday) of an ISO week.
func Days(week string) ([]Day, error) {
	m := weekRE.FindStringSubmatch(week)
	if m == nil {
		return nil, fmt.Errorf("week must look like 2026-W40, not %q", week)
	}
	year, _ := strconv.Atoi(m[1])
	w, _ := strconv.Atoi(m[2])
	// 4 January is always in week 1.
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)+(w-1)*7)
	if y, n := monday.ISOWeek(); w < 1 || y != year || n != w {
		return nil, fmt.Errorf("no such week: %s", week)
	}
	days := make([]Day, 7)
	for i := range days {
		days[i] = monday.AddDate(0, 0, i)
	}
	return days, nil
}

// LastComplete is the ISO week that ended on the last Sunday before today.
func LastComplete(today Day) string {
	return Of(today.AddDate(0, 0, -isoWeekday(today)))
}

func isoWeekday(d Day) int { return (int(d.Weekday())+6)%7 + 1 }
