// SPDX-License-Identifier: AGPL-3.0-or-later

// Package site renders data/ as one static HTML page with inline SVG charts.
//
// No JavaScript, no external resources. Everything under data/ is treated
// as untrusted: each file is checked against the format in docs/data-format.md
// and the build fails on anything unexpected, so a bad or tampered file never
// reaches the published page (which shares an origin with the apps on
// github.io). html/template escapes everything that is printed.
package site

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/isoweek"
	"github.com/tkjaer/open-stats/internal/project"
)

const (
	chartDays   = 84 // 12 weeks
	maxFileSize = 64 << 10
)

var (
	//go:embed style.css
	style string
	//go:embed page.html
	pageHTML string
	page     = template.Must(template.New("page").Parse(pageHTML))

	weekFileRE = regexp.MustCompile(`^(\d{4}-W\d{2})\.json$`)
	palette    = []string{"#3b6ea5", "#d0782a", "#4a9b5f", "#8e5ea2", "#b84a4a", "#6b7f8e", "#a08a2b"}
)

const (
	otherColour = "#9aa0a6"
	quietColour = "#d5d8dc"
	quietKey    = "not broken down"
)

// Project is a project with its checked data.
type Project struct {
	*project.Project
	Weeks    []*dataformat.Week // oldest first
	Settings *dataformat.Settings
}

func readSmall(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Size() > maxFileSize {
		return nil, fmt.Errorf("%s: too large", path)
	}
	return os.ReadFile(path)
}

// Load reads and checks every project's files under dataDir.
func Load(dataDir string, ps []*project.Project) ([]Project, error) {
	var out []Project
	for _, p := range ps {
		sp := Project{Project: p}
		dir := filepath.Join(dataDir, p.Slug, "weekly")
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range entries { // sorted by name, so oldest week first
			if e.Name() == ".gitkeep" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			m := weekFileRE.FindStringSubmatch(e.Name())
			if m == nil {
				return nil, fmt.Errorf("%s: unexpected file", path)
			}
			data, err := readSmall(path)
			if err != nil {
				return nil, err
			}
			w, err := dataformat.ParseWeek(data, p.Slug, m[1])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			sp.Weeks = append(sp.Weeks, w)
		}
		path := filepath.Join(dataDir, p.Slug, "goatcounter-settings.json")
		if _, err := os.Lstat(path); err == nil {
			data, err := readSmall(path)
			if err != nil {
				return nil, err
			}
			if sp.Settings, err = dataformat.ParseSettings(data, p.Slug); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		out = append(out, sp)
	}
	return out, nil
}

// ---- view model ------------------------------------------------------------------

type (
	pageView struct {
		CSP      string
		Style    template.CSS
		Projects []projectView
		Built    string
	}
	projectView struct {
		Slug, Name, Privacy string
		URL                 string
		Settings            *settingsView
		Weeks               []weekRow
		Range               string
		PageLoads, Language chart
		Legend              []legendItem
		LangKeys            []string
		Quiet               bool // the language chart has days that are not broken down
		QuietCol            bool // the weeks table has page loads that are not broken down
		Countries           []countryWeek
	}
	settingsView struct {
		Version, Collect, Public string
		Retention                int
	}
	weekRow struct {
		ID, From  string
		PageLoads int
		Language  []int
		Quiet     int // page loads on days that are not broken down
	}
	legendItem  struct{ Key, Colour string }
	countryWeek struct {
		ID, Span              string
		Open                  bool
		MinCount, MinDayTotal int
		Codes                 []string
		Rows                  []countryRow
	}
	countryRow struct {
		Label string
		Cells []string
		Other int
		Quiet bool // fewer than MinDayTotal page loads: only Total is published
		Total int
		Span  int // columns to span when Quiet
	}
	chart struct {
		ID, Title string
		W, H      int
		Grid      []gridLine
		Bars      []bar
		Labels    []label
	}
	gridLine struct {
		X1, X2       int
		Y, TextX     string
		TextY, Label string
	}
	bar   struct{ X, Y, W, H, Fill, Title string }
	label struct{ X, Y, Text string }
)

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

func fmtDay(d time.Time) string { return d.Format("2 Jan") }

// niceMax rounds a chart's top up to four times a round step (1, 2, 2.5 or 5
// times a power of ten), so all four gridline labels are exact whole numbers.
// For single digits 5*mag/2 is 2 again, which skips the step 2.5.
func niceMax(v int) int {
	for mag := 1; ; mag *= 10 {
		for _, step := range []int{mag, 2 * mag, 5 * mag / 2, 5 * mag} {
			if 4*step >= v {
				return 4 * step
			}
		}
	}
}

type series struct {
	key    string
	colour string
	counts map[string]int
}

// barChart draws stacked bars, one per day.
func barChart(id, title string, days []time.Time, ss []series) chart {
	const w, h, left, bottom, top = 720, 220, 44, 28, 10
	plotW, plotH := float64(w-left-8), float64(h-bottom-top)
	most := 0
	for _, d := range days {
		sum := 0
		for _, s := range ss {
			sum += s.counts[d.Format(time.DateOnly)]
		}
		most = max(most, sum)
	}
	ymax := niceMax(most)
	bw := plotW / float64(max(len(days), 1))
	c := chart{ID: id, Title: title, W: w, H: h}
	for i := range 5 {
		y := float64(top) + plotH - plotH*float64(i)/4
		c.Grid = append(c.Grid, gridLine{X1: left, X2: w - 8, Y: f1(y), TextX: strconv.Itoa(left - 4),
			TextY: f1(y + 4), Label: strconv.Itoa(ymax * i / 4)})
	}
	for i, d := range days {
		x := float64(left) + float64(i)*bw
		y := float64(top) + plotH
		day := d.Format(time.DateOnly)
		for _, s := range ss {
			n := s.counts[day]
			if n == 0 {
				continue
			}
			bh := plotH * float64(n) / float64(ymax)
			y -= bh
			c.Bars = append(c.Bars, bar{X: f1(x + bw*0.1), Y: f1(y), W: f1(bw * 0.8), H: f1(bh), Fill: s.colour,
				Title: fmt.Sprintf("%s %s: %d", day, s.key, n)})
		}
		if d.Weekday() == time.Monday {
			c.Labels = append(c.Labels, label{X: f1(x), Y: strconv.Itoa(h - 10), Text: fmtDay(d)})
		}
	}
	return c
}

func colours(keys []string) map[string]string {
	m := map[string]string{}
	i := 0
	for _, k := range keys {
		if k == dataformat.Other {
			m[k] = otherColour
		} else {
			m[k] = palette[i%len(palette)]
			i++
		}
	}
	return m
}

func weekDays(w *dataformat.Week) []time.Time {
	days, _ := isoweek.Days(w.Period.ID) // checked by ParseWeek
	return days
}

func view(p Project) projectView {
	v := projectView{Slug: p.Slug, Name: p.Name, URL: p.URL, Privacy: p.Privacy}
	if s := p.Settings; s != nil {
		collect := strings.Join(s.GoatCounter.Collect, ", ")
		if collect == "" {
			collect = "nothing"
		}
		v.Settings = &settingsView{Version: s.GoatCounter.Version, Collect: collect, Public: s.GoatCounter.Public,
			Retention: s.GoatCounter.DataRetentionDays}
	}
	if len(p.Weeks) == 0 {
		return v
	}

	pageLoads, language := map[string]int{}, map[string]map[string]int{}
	quiet := map[string]int{} // page loads on days without a breakdown
	seen := map[string]bool{}
	for _, w := range p.Weeks {
		for _, r := range w.Tables.PageLoads.Rows {
			pageLoads[r.Day] = r.Count
			if r.Count < w.Tables.Language.MinDayTotal {
				quiet[r.Day] = r.Count
			}
		}
		for _, r := range w.Tables.Language.Rows {
			language[r.Day] = r.Counts
			for k, n := range r.Counts {
				if n > 0 {
					seen[k] = true
				}
			}
		}
	}
	first, last := weekDays(p.Weeks[0])[0], weekDays(p.Weeks[len(p.Weeks)-1])[6]
	first = later(first, last.AddDate(0, 0, -(chartDays-1)))
	var days []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	v.Range = fmt.Sprintf("%s – %s %d", fmtDay(first), fmtDay(last), last.Year())
	v.PageLoads = barChart(p.Slug+"-page-loads", p.Name+": page loads per day", days,
		[]series{{"page loads", palette[0], pageLoads}})

	keys := slices.Clone(p.Request.Allowed)
	var extra []string
	for k := range seen {
		if k != dataformat.Other && !slices.Contains(keys, k) {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	if seen[dataformat.Other] {
		keys = append(keys, dataformat.Other)
	}
	cmap := colours(keys)
	var ss []series
	for _, k := range keys {
		counts := map[string]int{}
		for day, c := range language {
			counts[day] = c[k]
		}
		ss = append(ss, series{k, cmap[k], counts})
		v.Legend = append(v.Legend, legendItem{k, cmap[k]})
	}
	for _, d := range days {
		v.Quiet = v.Quiet || quiet[d.Format(time.DateOnly)] > 0
	}
	if v.Quiet {
		ss = append(ss, series{quietKey, quietColour, quiet})
		v.Legend = append(v.Legend, legendItem{quietKey, quietColour})
	}
	v.Language = barChart(p.Slug+"-language", p.Name+": language per day", days, ss)
	v.LangKeys = keys

	for i := len(p.Weeks) - 1; i >= 0; i-- {
		w := p.Weeks[i]
		wd := weekDays(w)
		row := weekRow{ID: w.Period.ID, From: fmtDay(wd[0])}
		for _, r := range w.Tables.PageLoads.Rows {
			row.PageLoads += r.Count
			if r.Count < w.Tables.Language.MinDayTotal {
				row.Quiet += r.Count
			}
		}
		for _, k := range keys {
			sum := 0
			for _, r := range w.Tables.Language.Rows {
				sum += r.Counts[k]
			}
			row.Language = append(row.Language, sum)
		}
		v.QuietCol = v.QuietCol || row.Quiet > 0
		v.Weeks = append(v.Weeks, row)

		// Columns: countries named on any day, by their highest daily count.
		best := map[string]int{}
		for _, r := range w.Tables.Country.Rows {
			for c, n := range r.Counts {
				if c != dataformat.Other {
					best[c] = max(best[c], n)
				}
			}
		}
		cw := countryWeek{ID: w.Period.ID, Span: fmtDay(wd[0]) + " – " + fmtDay(wd[6]), Open: i == len(p.Weeks)-1,
			MinCount: w.Tables.Country.MinCount, MinDayTotal: w.Tables.Country.MinDayTotal, Codes: dataformat.SortedKeys(best)}
		byDay := map[string]dataformat.DayCounts{}
		for _, r := range w.Tables.Country.Rows {
			byDay[r.Day] = r
		}
		for j, pl := range w.Tables.PageLoads.Rows {
			cr := countryRow{Label: wd[j].Format("Mon 2 Jan")}
			r, ok := byDay[pl.Day]
			if !ok {
				cr.Quiet, cr.Total, cr.Span = true, pl.Count, len(cw.Codes)+1
				cw.Rows = append(cw.Rows, cr)
				continue
			}
			cr.Other = r.Counts[dataformat.Other]
			for _, c := range cw.Codes {
				if n, ok := r.Counts[c]; ok {
					cr.Cells = append(cr.Cells, strconv.Itoa(n))
				} else {
					cr.Cells = append(cr.Cells, "–")
				}
			}
			cw.Rows = append(cw.Rows, cr)
		}
		v.Countries = append(v.Countries, cw)
	}
	return v
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// CSP is the page's Content-Security-Policy: nothing but its own style.
func CSP() string {
	sum := sha256.Sum256([]byte(style))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; img-src 'none'; base-uri 'none'; form-action 'none'"
}

// Render writes the page.
func Render(w io.Writer, ps []Project, now time.Time) error {
	pv := pageView{CSP: CSP(), Style: template.CSS(style), Built: now.UTC().Format("2006-01-02 15:04")}
	for _, p := range ps {
		pv.Projects = append(pv.Projects, view(p))
	}
	return page.Execute(w, pv)
}

// Build loads dataDir and writes outDir/index.html.
func Build(dataDir, outDir string, ps []*project.Project, now time.Time) (string, error) {
	loaded, err := Load(dataDir, ps)
	if err != nil {
		return "", fmt.Errorf("refusing to build: %w", err)
	}
	var buf bytes.Buffer
	if err := Render(&buf, loaded, now); err != nil {
		return "", err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, "index.html")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	weeks := 0
	for _, p := range loaded {
		weeks += len(p.Weeks)
	}
	return fmt.Sprintf("wrote %s (%d project(s), %d week(s))", out, len(loaded), weeks), nil
}
