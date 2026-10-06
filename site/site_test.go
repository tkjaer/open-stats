package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/isoweek"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
)

var now = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func hiw(t *testing.T) []*project.Project {
	t.Helper()
	ps, err := project.LoadAll(projects.FS, "how-the-internet-works")
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// weekJSON is a valid weekly file, made by the exporter's own rules. DE has
// 101 and 202 on two days, SE 3 and US 4; Sunday (US 4) and the empty days
// are quiet days.
func weekJSON(t *testing.T, week string) []byte {
	t.Helper()
	return weekJSONFor(t, hiw(t)[0], week)
}

func weekJSONFor(t *testing.T, p *project.Project, week string) []byte {
	t.Helper()
	countries := []map[string]int{{"DE": 101, "SE": 3}, {"DE": 202}, {}, {}, {}, {}, {"US": 4}}
	days := []string{}
	w, err := dataformat.Build(p, week, dataformat.Raw{}, now, dataformat.Source{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range w.Tables.PageLoads.Rows {
		days = append(days, r.Day)
	}
	raw := dataformat.Raw{Totals: map[string]int{}, Paths: map[string]map[string]int{}, Countries: map[string]map[string]int{}}
	for i, d := range days {
		total := 0
		for _, n := range countries[i] {
			total += n
		}
		raw.Totals[d] = total
		raw.Paths[d] = map[string]int{"/en": total - total/3, "/da": total / 3}
		raw.Countries[d] = countries[i]
	}
	w, err = dataformat.Build(p, week, raw, time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), dataformat.Source{
		Collector: "goatcounter", GoatCounterVersion: "v2.7.0", Collect: []string{"country"}, DataRetentionDays: 31})
	if err != nil {
		t.Fatal(err)
	}
	return dataformat.Marshal(w)
}

var settingsJSON = `{"format":"` + dataformat.FormatURL + `","version":` + strconv.Itoa(dataformat.Version) + `,"project":"how-the-internet-works","goatcounter":{
	"version":"v2.7.0","site":"how-the-internet-works.localhost","collect":["country"],"collect_bitmask":16,
	"data_retention_days":31,"public":"private","allow_counter":false,"allow_embed":""}}`

type fixture struct {
	t    *testing.T
	data string
}

func newFixture(t *testing.T) *fixture {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(data, "how-the-internet-works", "weekly"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(data, "how-the-internet-works", "weekly", ".gitkeep"), nil, 0o644)
	return &fixture{t, data}
}

func (f *fixture) put(name string, data []byte) string {
	p := filepath.Join(f.data, "how-the-internet-works", "weekly", name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) build() (string, error) {
	ps, err := Load(f.data, hiw(f.t))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = Render(&buf, ps, now)
	return buf.String(), err
}

var unsafe = regexp.MustCompile(`(?i)<script|<iframe|<object|<embed|<img|<link|\s(src|srcset|action|formaction|href\s*=\s*"javascript|on\w+)\s*=`)

func TestRenders(t *testing.T) {
	f := newFixture(t)
	f.put("2026-W40.json", weekJSON(t, "2026-W40"))
	f.put("2026-W39.json", weekJSON(t, "2026-W39"))
	os.WriteFile(filepath.Join(f.data, "how-the-internet-works", "goatcounter-settings.json"), []byte(settingsJSON), 0o644)
	page, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if m := unsafe.FindString(page); m != "" {
		t.Errorf("page contains %q", m)
	}
	for _, want := range []string{"default-src &#39;none&#39;", "2026-W40", "2026-W39", "<td>101</td>", "<td>202</td>",
		"GoatCounter v2.7.0; collects: country", "21 Sep – 4 Oct 2026", "<details open><summary>2026-W40"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// No weekly totals per country: DE's week (101 + 202) appears nowhere.
	if regexp.MustCompile(`>\s*303\s*<`).MatchString(page) {
		t.Error("page has a weekly country total")
	}
	// Countries under the threshold are not named.
	if regexp.MustCompile(`>(SE|US)<`).MatchString(page) {
		t.Error("page names a country under the threshold")
	}
	if strings.Index(page, "2026-W40") > strings.Index(page, "2026-W39") {
		t.Error("newest week should come first")
	}
}

func TestStyleHash(t *testing.T) {
	f := newFixture(t)
	f.put("2026-W40.json", weekJSON(t, "2026-W40"))
	page, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no style")
	}
	sum := sha256.Sum256([]byte(m[1]))
	if !strings.Contains(html.UnescapeString(page), "sha256-"+base64.StdEncoding.EncodeToString(sum[:])) {
		t.Error("style does not match the CSP hash")
	}
}

func TestNoData(t *testing.T) {
	page, err := newFixture(t).build()
	if err != nil || !strings.Contains(page, "No data published yet") {
		t.Fatal(err)
	}
	page, err = (&fixture{t, filepath.Join(t.TempDir(), "missing")}).build()
	if err != nil || !strings.Contains(page, "No data published yet") {
		t.Fatal(err)
	}
}

// mutate applies fn to a decoded copy of a valid week.
func mutate(t *testing.T, fn func(map[string]any)) []byte {
	var doc map[string]any
	if err := json.Unmarshal(weekJSON(t, "2026-W40"), &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	b, _ := json.Marshal(doc)
	return b
}

func table(d map[string]any, name string) map[string]any {
	return d["tables"].(map[string]any)[name].(map[string]any)
}

func row(d map[string]any, name string, i int) map[string]any {
	return table(d, name)["rows"].([]any)[i].(map[string]any)
}

func counts(d map[string]any, name string, i int) map[string]any {
	return row(d, name, i)["counts"].(map[string]any)
}

func TestRejectsBadData(t *testing.T) {
	bad := map[string]func(map[string]any){
		"string count":         func(d map[string]any) { row(d, "page_loads", 0)["count"] = "104" },
		"negative":             func(d map[string]any) { row(d, "page_loads", 2)["count"] = -1 },
		"float":                func(d map[string]any) { row(d, "page_loads", 2)["count"] = 0.5 },
		"bool":                 func(d map[string]any) { row(d, "page_loads", 2)["count"] = true },
		"null":                 func(d map[string]any) { row(d, "page_loads", 2)["count"] = nil },
		"huge":                 func(d map[string]any) { row(d, "page_loads", 2)["count"] = 1e12 },
		"html in country":      func(d map[string]any) { counts(d, "country", 0)["<script>alert(1)</script>"] = 5 },
		"html in language":     func(d map[string]any) { counts(d, "language", 0)["<img src=x onerror=alert(1)>"] = 0 },
		"lowercase country":    func(d map[string]any) { counts(d, "country", 0)["de"] = 0 },
		"extra key":            func(d map[string]any) { d["note"] = "<b>hi</b>" },
		"extra table":          func(d map[string]any) { d["tables"].(map[string]any)["country_week"] = map[string]any{} },
		"weekly country total": func(d map[string]any) { table(d, "country")["total"] = map[string]any{"DE": 303} },
		"languages don't add":  func(d map[string]any) { counts(d, "language", 0)["other"] = 1 },
		"countries don't add":  func(d map[string]any) { counts(d, "country", 0)["other"] = 0 },
		"country under min":    func(d map[string]any) { counts(d, "country", 0)["SE"] = 3; counts(d, "country", 0)["other"] = 0 },
		"missing other":        func(d map[string]any) { delete(counts(d, "country", 1), "other") },
		"six days":             func(d map[string]any) { table(d, "page_loads")["rows"] = table(d, "page_loads")["rows"].([]any)[:6] },
		"wrong day":            func(d map[string]any) { row(d, "page_loads", 1)["day"] = "2026-09-28" },
		"wrong period":         func(d map[string]any) { d["period"].(map[string]any)["id"] = "2026-W41" },
		"wrong project":        func(d map[string]any) { d["project"] = "other" },
		"version 1":            func(d map[string]any) { d["version"] = 1 },
		"row for a quiet day": func(d map[string]any) {
			table(d, "country")["rows"] = append(table(d, "country")["rows"].([]any), map[string]any{"day": "2026-10-04", "counts": map[string]any{"US": 4, "other": 0}})
			table(d, "language")["rows"] = append(table(d, "language")["rows"].([]any), map[string]any{"day": "2026-10-04", "counts": map[string]any{"en": 3, "da": 1, "other": 0}})
		},
		"min_day_total lowered": func(d map[string]any) {
			table(d, "country")["min_day_total"] = 1
			table(d, "language")["min_day_total"] = 1
		},
		"weekly granularity":    func(d map[string]any) { table(d, "country")["granularity"] = "week" },
		"html in version":       func(d map[string]any) { d["source"].(map[string]any)["goatcounter_version"] = "<script>" },
		"html in generated_at":  func(d map[string]any) { d["generated_at"] = "<script>" },
		"min_count 0":           func(d map[string]any) { table(d, "country")["min_count"] = 0 },
		"missing source":        func(d map[string]any) { delete(d, "source") },
		"missing language rows": func(d map[string]any) { delete(table(d, "language"), "rows") },
	}
	for why, fn := range bad {
		f := newFixture(t)
		f.put("2026-W40.json", mutate(t, fn))
		if _, err := f.build(); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}
	f := newFixture(t)
	f.put("2026-W40.json", []byte("[1,2,3]"))
	if _, err := f.build(); err == nil {
		t.Error("not an object: accepted")
	}
}

func TestRejectsBadFiles(t *testing.T) {
	good := weekJSON(t, "2026-W40")
	for why, c := range map[string]struct {
		name string
		data []byte
	}{
		"not json":             {"2026-W40.json", []byte("{")},
		"bad name":             {"index.html", []byte("<script>alert(1)</script>")},
		"bad week":             {"2026-W60.json", good},
		"name/period mismatch": {"2026-W41.json", good},
		"too large":            {"2026-W40.json", append(slicesClone(good), bytes.Repeat([]byte(" "), 70000)...)},
		"trailing data":        {"2026-W40.json", append(slicesClone(good), []byte("{}")...)},
	} {
		f := newFixture(t)
		f.put(c.name, c.data)
		if _, err := f.build(); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}
	f := newFixture(t)
	target := f.put("2026-W40.json", good)
	os.Rename(target, filepath.Join(f.data, "real.json"))
	os.Symlink(filepath.Join(f.data, "real.json"), target)
	if _, err := f.build(); err == nil {
		t.Error("symlink: accepted")
	}
	f = newFixture(t)
	os.Mkdir(filepath.Join(f.data, "how-the-internet-works", "weekly", "2026-W40.json"), 0o755)
	if _, err := f.build(); err == nil {
		t.Error("directory: accepted")
	}
}

func slicesClone(b []byte) []byte { return append([]byte(nil), b...) }

func TestRejectsBadSettings(t *testing.T) {
	for why, s := range map[string]string{
		"html in version": strings.Replace(settingsJSON, `"v2.7.0"`, `"<b>"`, 1),
		"extra key":       strings.Replace(settingsJSON, `"allow_embed":""`, `"allow_embed":"","x":1`, 1),
		"bad public":      strings.Replace(settingsJSON, `"private"`, `"<i>"`, 1),
		"bad embed":       strings.Replace(settingsJSON, `"allow_embed":""`, `"allow_embed":"<script>"`, 1),
	} {
		f := newFixture(t)
		f.put("2026-W40.json", weekJSON(t, "2026-W40"))
		os.WriteFile(filepath.Join(f.data, "how-the-internet-works", "goatcounter-settings.json"), []byte(s), 0o644)
		if _, err := f.build(); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}
}

func TestEscapesProjectFields(t *testing.T) {
	p := hiw(t)[0]
	evil := *p
	evil.Name = "<script>alert(1)</script>"
	evil.Privacy = `https://example.org/"onmouseover=x`
	evil.URL = "javascript:alert(1)"
	var buf bytes.Buffer
	if err := Render(&buf, []Project{{Project: &evil}}, now); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if m := unsafe.FindString(page); m != "" {
		t.Errorf("page contains %q", m)
	}
	if !strings.Contains(page, "&lt;script&gt;") || strings.Contains(page, "javascript:") {
		t.Errorf("not escaped:\n%s", page)
	}
}

func TestNiceMax(t *testing.T) {
	for in, want := range map[int]int{0: 4, 4: 4, 5: 8, 8: 8, 9: 20, 20: 20, 21: 40, 41: 80, 81: 100, 101: 200, 1999: 2000, 2001: 4000} {
		if got := niceMax(in); got != want {
			t.Errorf("niceMax(%d) = %d, want %d", in, got, want)
		}
	}
	for v := range 100000 {
		top := niceMax(v)
		if top < v || top%4 != 0 {
			t.Fatalf("niceMax(%d) = %d", v, top)
		}
	}
}

// Each gridline's label is the value at its height.
func TestGridLabelsMatchLines(t *testing.T) {
	days, _ := isoweek.Days("2026-W40")
	for _, most := range []int{1, 5, 7, 10, 13, 30, 99, 450} {
		c := barChart("t", "t", days, []series{{"x", "#000", map[string]int{days[0].Format(time.DateOnly): most}}})
		top, bottom := c.Grid[4], c.Grid[0]
		ymax, _ := strconv.Atoi(top.Label)
		y0, _ := strconv.ParseFloat(bottom.Y, 64)
		y4, _ := strconv.ParseFloat(top.Y, 64)
		for _, g := range c.Grid {
			y, _ := strconv.ParseFloat(g.Y, 64)
			want := float64(ymax) * (y0 - y) / (y0 - y4)
			if g.Label != strconv.FormatFloat(want, 'f', -1, 64) {
				t.Errorf("most %d: label %s at %s, want %v", most, g.Label, g.Y, want)
			}
		}
	}
}

// Quiet days show only their total, and each week's thresholds come from that
// week's file.
func TestQuietDaysAndThresholdsPerWeek(t *testing.T) {
	f := newFixture(t)
	f.put("2026-W40.json", weekJSON(t, "2026-W40"))
	stricter := *hiw(t)[0]
	stricter.Publish.CountryMinPerDay, stricter.Publish.BreakdownMinDayTotal = 6, 25
	f.put("2026-W39.json", weekJSONFor(t, &stricter, "2026-W39"))
	page, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	w40 := page[strings.Index(page, "<summary>2026-W40"):strings.Index(page, "<summary>2026-W39")]
	w39 := page[strings.Index(page, "<summary>2026-W39"):]
	for _, c := range []struct{ html, want string }{
		{w40, "fewer than 5 page loads on a day"}, {w40, "fewer\nthan 20 page loads"},
		{w39, "fewer than 6 page loads on a day"}, {w39, "fewer\nthan 25 page loads"},
		{w40, `<th scope="row">Sun 4 Oct</th><td colspan="2" class="quiet">4 page loads: not broken down</td>`},
		{w40, `<th scope="row">Wed 30 Sep</th><td colspan="2" class="quiet">0 page loads: not broken down</td>`},
		{w40, `<th scope="row">Mon 28 Sep</th><td>101</td><td>3</td>`},
		{page, `<th>not broken down</th>`},
		{page, `<tr><th scope="row">2026-W40</th><td>28 Sep</td><td>310</td><td>205</td><td>101</td><td>4</td></tr>`},
		{page, "2026-10-04 not broken down: 4"},
	} {
		if !strings.Contains(c.html, c.want) {
			t.Errorf("page lacks %q", c.want)
		}
	}
	if strings.Contains(page, "2026-10-04 en") || strings.Contains(page, "2026-10-04 da") {
		t.Error("the language chart breaks down a quiet day")
	}
}
