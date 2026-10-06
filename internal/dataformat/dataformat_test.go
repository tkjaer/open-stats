package dataformat

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/internal/isoweek"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
)

func hiw(t *testing.T) *project.Project {
	t.Helper()
	ps, err := project.LoadAll(projects.FS, "how-the-internet-works")
	if err != nil {
		t.Fatal(err)
	}
	return ps[0]
}

func TestFoldCountries(t *testing.T) {
	tests := []struct {
		name  string
		total int
		in    map[string]int
		want  map[string]int
	}{
		{"under 5 folded, unknown in other", 16, map[string]int{"DE": 6, "DK": 5, "SE": 4, "(unknown)": 1},
			map[string]int{"DE": 6, "DK": 5, "other": 5}},
		{"empty day", 0, nil, map[string]int{"other": 0}},
		{"single small country", 4, map[string]int{"DK": 4}, map[string]int{"other": 4}},
		{"exactly 5 is listed", 5, map[string]int{"DK": 5}, map[string]int{"DK": 5, "other": 0}},
		{"other is exact, even when small", 11, map[string]int{"DK": 10, "SE": 1}, map[string]int{"DK": 10, "other": 1}},
		{"bad codes go to other", 30, map[string]int{"FR": 5, "DE": 10, "xx": 5, "<b>": 10},
			map[string]int{"FR": 5, "DE": 10, "other": 15}},
	}
	for _, tt := range tests {
		got, err := FoldCountries(tt.total, tt.in, 5)
		if err != nil || !maps.Equal(got, tt.want) {
			t.Errorf("%s: got %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
	if _, err := FoldCountries(3, map[string]int{"DK": 5}, 5); err == nil {
		t.Error("countries above the total: no error")
	}
}

func TestLanguageCounts(t *testing.T) {
	allowed := []string{"en", "da"}
	tests := []struct {
		total int
		in    map[string]int
		want  map[string]int
	}{
		{10, map[string]int{"/en": 6, "/da": 3, "/xx": 1}, map[string]int{"en": 6, "da": 3, "other": 1}},
		{0, nil, map[string]int{"en": 0, "da": 0, "other": 0}},
	}
	for _, tt := range tests {
		got, err := LanguageCounts(tt.total, tt.in, allowed)
		if err != nil || !maps.Equal(got, tt.want) {
			t.Errorf("%v: got %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	if _, err := LanguageCounts(1, map[string]int{"/en": 2}, allowed); err == nil {
		t.Error("languages above the total: no error")
	}
}

func TestCollectNames(t *testing.T) {
	if got := CollectNames(16); !equal(got, []string{"country"}) {
		t.Errorf("16: %v", got)
	}
	// GoatCounter's default.
	if got := CollectNames(190); !equal(got, []string{"referrer", "user-agent", "screen-size", "country", "region", "sessions"}) {
		t.Errorf("190: %v", got)
	}
}

func equal(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// SampleWeek is a valid week made by the publishing rules themselves.
func SampleWeek(t *testing.T, week string) *Week {
	t.Helper()
	raw := Raw{Totals: map[string]int{}, Paths: map[string]map[string]int{}, Countries: map[string]map[string]int{}}
	// Quiet days (under 20 page loads) on Wednesday, Thursday and Sunday.
	byDay := []map[string]int{{"DE": 101, "SE": 3}, {"DE": 202}, {}, {"DK": 19}, {"DK": 20}, {"DK": 15, "SE": 6}, {"US": 4}}
	monday, _ := time.Parse(time.DateOnly, "2026-09-28")
	if week != "2026-W40" {
		monday = monday.AddDate(0, 0, -7)
	}
	for i, c := range byDay {
		day := monday.AddDate(0, 0, i).Format(time.DateOnly)
		total := 0
		for _, n := range c {
			total += n
		}
		raw.Totals[day] = total
		raw.Paths[day] = map[string]int{"/en": total - total/3, "/da": total / 3}
		raw.Countries[day] = c
	}
	w, err := Build(hiw(t), week, raw, time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC),
		Source{"goatcounter", "v2.7.0", []string{"country"}, 31})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestBuild(t *testing.T) {
	w := SampleWeek(t, "2026-W40")
	if w.Period != (Period{"iso-week", "2026-W40", "2026-09-28", "2026-10-04", "UTC"}) {
		t.Errorf("period %+v", w.Period)
	}
	if r := w.Tables.PageLoads.Rows[0]; r != (DayCount{"2026-09-28", 104}) {
		t.Errorf("page loads %+v", r)
	}
	if c := w.Tables.Country.Rows[0].Counts; !maps.Equal(c, map[string]int{"DE": 101, "other": 3}) {
		t.Errorf("countries %v", c)
	}
	if c := w.Tables.Country.Rows[3].Counts; !maps.Equal(c, map[string]int{"DK": 15, "SE": 6, "other": 0}) {
		t.Errorf("countries %v", c)
	}
	if c := w.Tables.Language.Rows[0].Counts; !maps.Equal(c, map[string]int{"en": 70, "da": 34, "other": 0}) {
		t.Errorf("language %v", c)
	}
	// The three tables, daily, and nothing else.
	var tables struct {
		Tables map[string]map[string]any `json:"tables"`
	}
	if err := json.Unmarshal(Marshal(w), &tables); err != nil {
		t.Fatal(err)
	}
	if len(tables.Tables) != 3 {
		t.Errorf("tables: %v", tables.Tables)
	}
	for name, tbl := range tables.Tables {
		if tbl["granularity"] != "day" {
			t.Errorf("%s: granularity %v", name, tbl["granularity"])
		}
	}
	if _, err := ParseWeek(Marshal(w), hiw(t), "2026-W40"); err != nil {
		t.Errorf("the exporter's own output doesn't pass the site's checks: %v", err)
	}
}

func TestParseWeekRejects(t *testing.T) {
	good := string(Marshal(SampleWeek(t, "2026-W40")))
	edit := func(f func(m map[string]any)) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(good), &m)
		f(m)
		b, _ := json.Marshal(m)
		return string(b)
	}
	tables := func(m map[string]any, name string) map[string]any {
		return m["tables"].(map[string]any)[name].(map[string]any)
	}
	row := func(m map[string]any, name string, i int) map[string]any {
		return tables(m, name)["rows"].([]any)[i].(map[string]any)
	}
	counts := func(m map[string]any, name string, i int) map[string]any {
		return row(m, name, i)["counts"].(map[string]any)
	}
	tests := map[string]string{
		"not json":             "{",
		"trailing data":        good + "{}",
		"trailing brace":       good + "}",
		"trailing bracket":     good + "]garbage",
		"trailing text":        good + "x",
		"not an object":        "[1,2,3]",
		"string count":         edit(func(m map[string]any) { row(m, "page_loads", 0)["count"] = "104" }),
		"negative":             edit(func(m map[string]any) { row(m, "page_loads", 2)["count"] = -1 }),
		"float":                strings.Replace(good, `"count": 0`, `"count": 0.0`, 1),
		"exponent":             strings.Replace(good, `"count": 0`, `"count": 0e0`, 1),
		"bool":                 edit(func(m map[string]any) { row(m, "page_loads", 2)["count"] = false }),
		"null":                 edit(func(m map[string]any) { row(m, "page_loads", 2)["count"] = nil }),
		"huge":                 edit(func(m map[string]any) { row(m, "page_loads", 2)["count"] = 1e12 }),
		"html in country":      edit(func(m map[string]any) { counts(m, "country", 2)["<script>alert(1)</script>"] = 0 }),
		"html in language":     edit(func(m map[string]any) { counts(m, "language", 2)["<img src=x onerror=alert(1)>"] = 0 }),
		"lowercase country":    edit(func(m map[string]any) { counts(m, "country", 2)["de"] = 0 }),
		"extra key":            edit(func(m map[string]any) { m["note"] = "<b>hi</b>" }),
		"missing key":          edit(func(m map[string]any) { delete(m, "generated_at") }),
		"extra table":          edit(func(m map[string]any) { m["tables"].(map[string]any)["country_week"] = map[string]any{} }),
		"weekly country total": edit(func(m map[string]any) { tables(m, "country")["total"] = map[string]any{"DE": 303} }),
		"languages off":        edit(func(m map[string]any) { counts(m, "language", 0)["other"] = 1 }),
		"countries off":        edit(func(m map[string]any) { counts(m, "country", 0)["other"] = 0 }),
		"country under min":    edit(func(m map[string]any) { counts(m, "country", 0)["SE"] = 3; counts(m, "country", 0)["other"] = 0 }),
		"missing other":        edit(func(m map[string]any) { delete(counts(m, "country", 2), "other") }),
		"null counts":          edit(func(m map[string]any) { row(m, "country", 2)["counts"] = nil }),
		"six days":             edit(func(m map[string]any) { tables(m, "page_loads")["rows"] = tables(m, "page_loads")["rows"].([]any)[:6] }),
		"wrong day":            edit(func(m map[string]any) { row(m, "page_loads", 1)["day"] = "2026-09-28" }),
		"wrong period":         edit(func(m map[string]any) { m["period"].(map[string]any)["id"] = "2026-W41" }),
		"wrong project":        edit(func(m map[string]any) { m["project"] = "other" }),
		"version 1":            edit(func(m map[string]any) { m["version"] = 1 }),
		"version 3":            edit(func(m map[string]any) { m["version"] = 3 }),
		"row for a quiet day": edit(func(m map[string]any) {
			tables(m, "language")["rows"] = append(tables(m, "language")["rows"].([]any), map[string]any{"day": "2026-10-04", "counts": map[string]any{"en": 4, "other": 0}})
			tables(m, "country")["rows"] = append(tables(m, "country")["rows"].([]any), map[string]any{"day": "2026-10-04", "counts": map[string]any{"other": 4}})
		}),
		"quiet day relabelled": edit(func(m map[string]any) {
			row(m, "language", 2)["day"] = "2026-10-01"
			row(m, "country", 2)["day"] = "2026-10-01"
		}),
		"busy day missing": edit(func(m map[string]any) { tables(m, "country")["rows"] = tables(m, "country")["rows"].([]any)[:3] }),
		"busy day made quiet": edit(func(m map[string]any) {
			tables(m, "language")["min_day_total"] = 21
			tables(m, "country")["min_day_total"] = 21
		}),
		"min_day_total differs": edit(func(m map[string]any) { tables(m, "language")["min_day_total"] = 19 }),
		"min_day_total under min_count": edit(func(m map[string]any) {
			tables(m, "language")["min_day_total"] = 4
			tables(m, "country")["min_day_total"] = 4
		}),
		"no min_day_total":     edit(func(m map[string]any) { delete(tables(m, "country"), "min_day_total") }),
		"weekly granularity":   edit(func(m map[string]any) { tables(m, "country")["granularity"] = "week" }),
		"html in version":      edit(func(m map[string]any) { m["source"].(map[string]any)["goatcounter_version"] = "<script>" }),
		"html in generated_at": edit(func(m map[string]any) { m["generated_at"] = "<script>" }),
		"min_count 0":          edit(func(m map[string]any) { tables(m, "country")["min_count"] = 0 }),
	}
	for name, data := range tests {
		if _, err := ParseWeek([]byte(data), hiw(t), "2026-W40"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseWeek([]byte(good), hiw(t), "2026-W41"); err == nil {
		t.Error("file name / period mismatch: accepted")
	}
}

func TestParseSettings(t *testing.T) {
	s := Settings{FormatURL, Version, "how-the-internet-works", GoatCounterSettings{"v2.7.0", "how-the-internet-works.localhost", []string{"country"}, 16, 31, "private", false, ""}}
	if _, err := ParseSettings(Marshal(s), "how-the-internet-works"); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Settings){
		"html in version":  func(s *Settings) { s.GoatCounter.Version = "<b>" },
		"bad public":       func(s *Settings) { s.GoatCounter.Public = "<i>" },
		"collect mismatch": func(s *Settings) { s.GoatCounter.Collect = []string{"region"} },
		"html in embed":    func(s *Settings) { s.GoatCounter.AllowEmbed = "<script>" },
		"wrong project":    func(s *Settings) { s.Project = "x" },
	}
	for name, f := range bad {
		c := s
		c.GoatCounter.Collect = append([]string(nil), s.GoatCounter.Collect...)
		f(&c)
		if _, err := ParseSettings(Marshal(c), "how-the-internet-works"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseSettings([]byte(strings.Replace(string(Marshal(s)), `"public"`, `"x": 1, "public"`, 1)), "how-the-internet-works"); err == nil {
		t.Error("extra key: accepted")
	}
	for _, suffix := range []string{"}", "]garbage", "{}", "x"} {
		if _, err := ParseSettings(append(Marshal(s), suffix...), "how-the-internet-works"); err == nil {
			t.Errorf("trailing %q: accepted", suffix)
		}
	}
	if _, err := ParseSettings(append(Marshal(s), " \n\t"...), "how-the-internet-works"); err != nil {
		t.Errorf("trailing whitespace: %v", err)
	}
}

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]int{"other": 99, "FR": 5, "DE": 10, "AT": 10})
	if !equal(got, []string{"AT", "DE", "FR", "other"}) {
		t.Errorf("%v", got)
	}
}

// A day under 20 page loads publishes only its total; at 20 and above, the
// language and country rows are there as before.
func TestQuietDays(t *testing.T) {
	p := hiw(t)
	if p.Publish.BreakdownMinDayTotal != 20 {
		t.Fatalf("breakdown_min_day_total %d, want 20", p.Publish.BreakdownMinDayTotal)
	}
	days, _ := isoweek.Days("2026-W40")
	totals := []int{19, 20, 21, 0, 1, 5, 100}
	raw := Raw{Totals: map[string]int{}, Paths: map[string]map[string]int{}, Countries: map[string]map[string]int{}}
	for i, n := range totals {
		day := days[i].Format(time.DateOnly)
		raw.Totals[day] = n
		raw.Paths[day] = map[string]int{"/da": min(n, 1), "/en": max(n-1, 0)}
		raw.Countries[day] = map[string]int{"DK": n}
	}
	w, err := Build(p, "2026-W40", raw, time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), Source{"goatcounter", "v2.7.0", []string{"country"}, 31})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range totals {
		if r := w.Tables.PageLoads.Rows[i]; r.Count != n {
			t.Errorf("day %d: page loads %d, want %d", i, r.Count, n)
		}
	}
	want := map[string]int{"2026-09-29": 20, "2026-09-30": 21, "2026-10-04": 100} // the days with 20 or more
	var got []string
	for i, r := range w.Tables.Language.Rows {
		c := w.Tables.Country.Rows[i]
		got = append(got, r.Day)
		if c.Day != r.Day || !maps.Equal(c.Counts, map[string]int{"DK": want[r.Day], "other": 0}) ||
			!maps.Equal(r.Counts, map[string]int{"da": 1, "en": want[r.Day] - 1, "other": 0}) {
			t.Errorf("%s: language %v, country %v", r.Day, r.Counts, c.Counts)
		}
	}
	if !equal(got, []string{"2026-09-29", "2026-09-30", "2026-10-04"}) {
		t.Errorf("language and country rows for %v, want only the days with at least 20 page loads", got)
	}
	data := Marshal(w)
	if _, err := ParseWeek(data, hiw(t), "2026-W40"); err != nil {
		t.Error(err)
	}
	if strings.Contains(string(data), `"day": "2026-09-28",
      "counts"`) {
		t.Error("the day with 19 page loads has a breakdown")
	}
}
