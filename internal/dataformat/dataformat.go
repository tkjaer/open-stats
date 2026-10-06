// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dataformat is the published file format (docs/data-format.md,
// version 2), the publishing rules that produce it, and the strict checks that the
// site runs on every file before rendering it.
package dataformat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/tkjaer/open-stats/internal/isoweek"
	"github.com/tkjaer/open-stats/internal/project"
)

const (
	Version   = 2
	FormatURL = "https://github.com/tkjaer/open-stats/blob/main/docs/data-format.md"
	// Other is the catch-all key in the language and country tables.
	Other    = "other"
	maxCount = 1_000_000_000
)

var (
	CountryRE  = regexp.MustCompile(`^[A-Z]{2}$`)
	timeRE     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	versionRE  = regexp.MustCompile(`^v?[0-9][0-9A-Za-z.+-]{0,30}$`)
	siteRE     = regexp.MustCompile(`^[a-z0-9-]+\.localhost$`)
	embedRE    = regexp.MustCompile(`^[A-Za-z0-9.:/ -]{0,200}$`)
	collectAll = []struct {
		Bit  int
		Name string
	}{
		{2, "referrer"}, {4, "user-agent"}, {8, "screen-size"}, {16, "country"}, {32, "region"},
		{64, "language"}, {128, "sessions"}, {256, "individual-pageviews"},
	}
)

// CollectNames turns GoatCounter's "collect" bitmask into names.
func CollectNames(mask int) []string {
	names := []string{}
	for _, f := range collectAll {
		if mask&f.Bit != 0 {
			names = append(names, f.Name)
		}
	}
	return names
}

func knownCollectName(n string) bool {
	return slices.ContainsFunc(collectAll, func(f struct {
		Bit  int
		Name string
	}) bool {
		return f.Name == n
	})
}

// ---- types ------------------------------------------------------------------------

type Week struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	Project     string `json:"project"`
	Period      Period `json:"period"`
	GeneratedAt string `json:"generated_at"`
	Source      Source `json:"source"`
	Tables      Tables `json:"tables"`
}

type Period struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	FirstDay string `json:"first_day"`
	LastDay  string `json:"last_day"`
	Timezone string `json:"timezone"`
}

type Source struct {
	Collector          string   `json:"collector"`
	GoatCounterVersion string   `json:"goatcounter_version"`
	Collect            []string `json:"collect"`
	DataRetentionDays  int      `json:"data_retention_days"`
}

type Tables struct {
	PageLoads PageLoads `json:"page_loads"`
	Language  Breakdown `json:"language"`
	Country   Countries `json:"country"`
}

type PageLoads struct {
	Granularity string     `json:"granularity"`
	Rows        []DayCount `json:"rows"`
}

type Breakdown struct {
	Granularity string      `json:"granularity"`
	MinDayTotal int         `json:"min_day_total"`
	Rows        []DayCounts `json:"rows"`
}

type Countries struct {
	Granularity string      `json:"granularity"`
	MinDayTotal int         `json:"min_day_total"`
	MinCount    int         `json:"min_count"`
	Rows        []DayCounts `json:"rows"`
}

type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type DayCounts struct {
	Day    string         `json:"day"`
	Counts map[string]int `json:"counts"`
}

type Settings struct {
	Format      string              `json:"format"`
	Version     int                 `json:"version"`
	Project     string              `json:"project"`
	GoatCounter GoatCounterSettings `json:"goatcounter"`
}

type GoatCounterSettings struct {
	Version           string   `json:"version"`
	Site              string   `json:"site"`
	Collect           []string `json:"collect"`
	CollectBitmask    int      `json:"collect_bitmask"`
	DataRetentionDays int      `json:"data_retention_days"`
	Public            string   `json:"public"`
	AllowCounter      bool     `json:"allow_counter"`
	AllowEmbed        string   `json:"allow_embed"`
}

// Marshal is the one way files are written: indented, with a final newline.
func Marshal(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// ---- publishing rules --------------------------------------------------------------

// FoldCountries keeps the countries with at least min page loads on the day;
// everything else, including unknown locations, is "other" (the day's total
// minus the listed countries).
func FoldCountries(dayTotal int, byCountry map[string]int, min int) (map[string]int, error) {
	out := map[string]int{}
	listed := 0
	for c, n := range byCountry {
		if CountryRE.MatchString(c) && n >= min {
			out[c] = n
			listed += n
		}
	}
	if listed > dayTotal {
		return nil, fmt.Errorf("country counts (%d) exceed the day's total (%d)", listed, dayTotal)
	}
	out[Other] = dayTotal - listed
	return out, nil
}

// LanguageCounts counts the project's languages (GoatCounter paths "/<code>")
// and puts the rest of the day's total under "other": every other code, by
// subtraction, so their own counts are never needed or published.
func LanguageCounts(dayTotal int, byPath map[string]int, languages []string) (map[string]int, error) {
	out := map[string]int{}
	sum := 0
	for _, v := range languages {
		out[v] = byPath["/"+v]
		sum += out[v]
	}
	if sum > dayTotal {
		return nil, fmt.Errorf("language counts (%d) exceed the day's total (%d)", sum, dayTotal)
	}
	out[Other] = dayTotal - sum
	return out, nil
}

// Raw is one week as read from GoatCounter, keyed by day (YYYY-MM-DD).
type Raw struct {
	Totals    map[string]int            // page loads
	Paths     map[string]map[string]int // "/en" -> n
	Countries map[string]map[string]int // "DK" -> n
}

// Build applies the publishing rules to one week. Days with fewer page loads
// than publish.breakdown_min_day_total get no language or country row.
func Build(p *project.Project, week string, raw Raw, generatedAt time.Time, src Source) (*Week, error) {
	days, err := isoweek.Days(week)
	if err != nil {
		return nil, err
	}
	min, quiet := p.Publish.CountryMinPerDay, p.Publish.BreakdownMinDayTotal
	w := &Week{
		Format: FormatURL, Version: Version, Project: p.Slug,
		Period: Period{
			Kind: "iso-week", ID: week, Timezone: "UTC",
			FirstDay: days[0].Format(time.DateOnly), LastDay: days[6].Format(time.DateOnly),
		},
		GeneratedAt: generatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Source:      src,
		Tables: Tables{
			PageLoads: PageLoads{Granularity: "day", Rows: []DayCount{}},
			Language:  Breakdown{Granularity: "day", MinDayTotal: quiet, Rows: []DayCounts{}},
			Country:   Countries{Granularity: "day", MinDayTotal: quiet, MinCount: min, Rows: []DayCounts{}},
		},
	}
	for _, d := range days {
		day := d.Format(time.DateOnly)
		total := raw.Totals[day]
		lang, err := LanguageCounts(total, raw.Paths[day], p.Publish.Languages)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", day, err)
		}
		countries, err := FoldCountries(total, raw.Countries[day], min)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", day, err)
		}
		w.Tables.PageLoads.Rows = append(w.Tables.PageLoads.Rows, DayCount{day, total})
		if total < quiet {
			continue
		}
		w.Tables.Language.Rows = append(w.Tables.Language.Rows, DayCounts{day, lang})
		w.Tables.Country.Rows = append(w.Tables.Country.Rows, DayCounts{day, countries})
	}
	return w, nil
}

// ---- strict reading (the site treats every file as untrusted) -----------------------

// strictDecode decodes data into v, rejecting unknown or missing keys, wrong
// types, non-integer numbers and trailing data: the file must be exactly what
// v marshals back to.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return fmt.Errorf("trailing data after the JSON value")
	}
	var orig, back any
	if err := useNumber(data, &orig); err != nil {
		return err
	}
	if err := useNumber(Marshal(v), &back); err != nil {
		return err
	}
	if !reflect.DeepEqual(orig, back) {
		return fmt.Errorf("missing keys, null values or numbers in an unexpected form")
	}
	return nil
}

func useNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

func checkCount(n int, where string) error {
	if n < 0 || n > maxCount {
		return fmt.Errorf("%s: not a plausible count: %d", where, n)
	}
	return nil
}

func checkCounts(counts map[string]int, where string, key func(string) bool) (int, error) {
	if _, ok := counts[Other]; !ok {
		return 0, fmt.Errorf(`%s: counts must include "other"`, where)
	}
	sum := 0
	for k, n := range counts {
		if k != Other && !key(k) {
			return 0, fmt.Errorf("%s: bad key %q", where, k)
		}
		if err := checkCount(n, where+"."+k); err != nil {
			return 0, err
		}
		sum += n
	}
	return sum, nil
}

// ParseWeek reads and checks data/<slug>/weekly/<id>.json for project p. The
// file's thresholds may be stricter than p's, never looser: they are checked
// against p (trusted, built in), not only against the file's own declarations.
// Likewise its language keys must be p's languages (or "other"), so no other
// code's own count can reach the page.
func ParseWeek(data []byte, p *project.Project, id string) (*Week, error) {
	slug := p.Slug
	isLanguage := func(k string) bool { return slices.Contains(p.Publish.Languages, k) }
	var w Week
	if err := strictDecode(data, &w); err != nil {
		return nil, err
	}
	days, err := isoweek.Days(id)
	if err != nil {
		return nil, err
	}
	switch {
	case w.Version != Version:
		return nil, fmt.Errorf("unsupported version %d", w.Version)
	case w.Format != FormatURL:
		return nil, fmt.Errorf("unexpected format URL")
	case w.Project != slug:
		return nil, fmt.Errorf("project %q does not match the directory", w.Project)
	case !timeRE.MatchString(w.GeneratedAt):
		return nil, fmt.Errorf("bad generated_at")
	case w.Period != Period{Kind: "iso-week", ID: id, Timezone: "UTC",
		FirstDay: days[0].Format(time.DateOnly), LastDay: days[6].Format(time.DateOnly)}:
		return nil, fmt.Errorf("period does not match the file name")
	case w.Source.Collector != "goatcounter" || !versionRE.MatchString(w.Source.GoatCounterVersion):
		return nil, fmt.Errorf("bad source")
	case !allKnown(w.Source.Collect):
		return nil, fmt.Errorf("bad source.collect")
	}
	if err := checkCount(w.Source.DataRetentionDays, "source.data_retention_days"); err != nil {
		return nil, err
	}
	t := w.Tables
	if t.PageLoads.Granularity != "day" || t.Language.Granularity != "day" || t.Country.Granularity != "day" {
		return nil, fmt.Errorf("unsupported granularity")
	}
	if t.Country.MinCount < 1 || t.Country.MinCount > maxCount {
		return nil, fmt.Errorf("min_count must be at least 1")
	}
	quiet := t.Language.MinDayTotal
	if quiet < t.Country.MinCount || quiet > maxCount || t.Country.MinDayTotal != quiet {
		return nil, fmt.Errorf("min_day_total must be the same in both tables and at least min_count")
	}
	if t.Country.MinCount < p.Publish.CountryMinPerDay || quiet < p.Publish.BreakdownMinDayTotal {
		return nil, fmt.Errorf("min_count %d and min_day_total %d must be at least the project's %d and %d",
			t.Country.MinCount, quiet, p.Publish.CountryMinPerDay, p.Publish.BreakdownMinDayTotal)
	}
	if len(t.PageLoads.Rows) != 7 {
		return nil, fmt.Errorf("page_loads needs 7 daily rows")
	}
	n := 0 // rows in the language and country tables so far
	for i, d := range days {
		day := d.Format(time.DateOnly)
		pl := t.PageLoads.Rows[i]
		if pl.Day != day {
			return nil, fmt.Errorf("rows must be the week's days in order (%s)", day)
		}
		if err := checkCount(pl.Count, day); err != nil {
			return nil, err
		}
		if pl.Count < quiet {
			continue // a quiet day: only its total
		}
		if n >= len(t.Language.Rows) || n >= len(t.Country.Rows) {
			return nil, fmt.Errorf("%s: language and country rows missing", day)
		}
		lang, ctry := t.Language.Rows[n], t.Country.Rows[n]
		n++
		if lang.Day != day || ctry.Day != day {
			return nil, fmt.Errorf("language and country rows must be exactly the days with at least min_day_total page loads, in order (%s)", day)
		}
		ls, err := checkCounts(lang.Counts, day+" language", isLanguage)
		if err != nil {
			return nil, err
		}
		cs, err := checkCounts(ctry.Counts, day+" country", CountryRE.MatchString)
		if err != nil {
			return nil, err
		}
		if ls != pl.Count || cs != pl.Count {
			return nil, fmt.Errorf("%s: languages (%d) and countries (%d) must add up to the page loads (%d)",
				day, ls, cs, pl.Count)
		}
		for c, cnt := range ctry.Counts {
			if c != Other && cnt < t.Country.MinCount {
				return nil, fmt.Errorf("%s: %s is below min_count", day, c)
			}
		}
	}
	if n != len(t.Language.Rows) || n != len(t.Country.Rows) {
		return nil, fmt.Errorf("language or country rows for days with fewer than min_day_total page loads")
	}
	return &w, nil
}

// ParseSettings reads and checks data/<slug>/goatcounter-settings.json.
func ParseSettings(data []byte, slug string) (*Settings, error) {
	var s Settings
	if err := strictDecode(data, &s); err != nil {
		return nil, err
	}
	g := s.GoatCounter
	switch {
	case s.Version != Version || s.Format != FormatURL || s.Project != slug:
		return nil, fmt.Errorf("bad version, format or project")
	case !versionRE.MatchString(g.Version) || !siteRE.MatchString(g.Site) || !allKnown(g.Collect):
		return nil, fmt.Errorf("bad version, site or collect")
	case !slices.Equal(g.Collect, CollectNames(g.CollectBitmask)):
		return nil, fmt.Errorf("collect does not match collect_bitmask")
	case g.Public != "private" && g.Public != "secret" && g.Public != "public":
		return nil, fmt.Errorf("bad public")
	case !embedRE.MatchString(g.AllowEmbed):
		return nil, fmt.Errorf("bad allow_embed")
	}
	if err := checkCount(g.DataRetentionDays, "data_retention_days"); err != nil {
		return nil, err
	}
	return &s, nil
}

func allKnown(names []string) bool {
	for _, n := range names {
		if !knownCollectName(n) {
			return false
		}
	}
	return names != nil
}

// SortedKeys returns the keys of m with "other" last; countries by count
// (highest first), then name.
func SortedKeys(m map[string]int) []string {
	keys := slices.Collect(maps.Keys(m))
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if (a == Other) != (b == Other) {
			return b == Other
		}
		if m[a] != m[b] {
			return m[a] > m[b]
		}
		return a < b
	})
	return keys
}
