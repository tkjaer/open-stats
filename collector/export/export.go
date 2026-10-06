// SPDX-License-Identifier: AGPL-3.0-or-later

// Package export is the weekly export: it reads one complete ISO week (UTC)
// per project from GoatCounter, applies the publishing rules and writes
//
//	data/<project>/weekly/YYYY-Www.json      (written once, never rewritten)
//	data/<project>/goatcounter-settings.json (the settings it found)
//
// in a clone of the data repository (tkjaer/open-stats-data), then commits
// and pushes them. If GoatCounter's settings differ from
// projects/*.yml, it publishes nothing. See docs/data-format.md for the format.
package export

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/isoweek"
	"github.com/tkjaer/open-stats/internal/project"
)

// SafeDays: GoatCounter deletes everything older than its data retention (31
// days), once a day. Only weeks entirely younger than this are exported.
const SafeDays = 30

type Options struct {
	Week     string // default: the last complete week
	Projects []*project.Project
	Repo     string // git clone to write into, commit and push
	Out      string // or: plain directory, no git
	Branch   string
	Now      time.Time
	API      *goatcounter.API
	Log      io.Writer
	// Pause between push attempts.
	RetryPause time.Duration
}

// CheckExportable returns the week's days, or why it can't be exported today.
func CheckExportable(week string, today isoweek.Day) ([]isoweek.Day, error) {
	days, err := isoweek.Days(week)
	if err != nil {
		return nil, err
	}
	if !days[6].Before(today) {
		return nil, fmt.Errorf("%s is not over yet (UTC)", week)
	}
	if days[0].Before(today.AddDate(0, 0, -SafeDays)) {
		return nil, fmt.Errorf("%s starts more than %d days ago; GoatCounter may already have deleted part "+
			"of it (data retention), so it can't be exported reliably", week, SafeDays)
	}
	return days, nil
}

// Snapshot is the published copy of a site's settings (without secrets or
// ignored IPs).
func Snapshot(p *project.Project, site *goatcounter.Site, version string) dataformat.Settings {
	s := site.Settings
	return dataformat.Settings{
		Format: dataformat.FormatURL, Version: dataformat.Version, Project: p.Slug,
		GoatCounter: dataformat.GoatCounterSettings{
			Version: version, Site: site.Cname,
			Collect: dataformat.CollectNames(s.Collect), CollectBitmask: s.Collect,
			DataRetentionDays: s.DataRetention, Public: s.Public,
			AllowCounter: s.AllowCounter, AllowEmbed: s.AllowEmbedString(),
		},
	}
}

// Problems lists where the running settings differ from projects/<slug>.yml.
func Problems(p *project.Project, snap dataformat.Settings) []string {
	g := snap.GoatCounter
	var out []string
	if g.CollectBitmask != p.GoatCounter.Collect {
		out = append(out, fmt.Sprintf("collect is %d %v, expected %d", g.CollectBitmask, g.Collect, p.GoatCounter.Collect))
	}
	if g.DataRetentionDays != p.GoatCounter.DataRetentionDays {
		out = append(out, fmt.Sprintf("data retention is %d days, expected %d", g.DataRetentionDays,
			p.GoatCounter.DataRetentionDays))
	}
	if g.Public != "private" || g.AllowCounter || g.AllowEmbed != "" {
		out = append(out, "the dashboard is not private")
	}
	return out
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// prepare resets the clone to what's on GitHub: it only ever holds data
// written here, and nothing local (e.g. from a failed run) may be pushed.
// The checkout is forced so that leftovers can't block it.
func prepare(repo, branch string) error {
	for _, args := range [][]string{
		{"fetch", "--quiet", "origin", branch},
		{"checkout", "--quiet", "--force", "-B", branch, "origin/" + branch},
		{"reset", "--quiet", "--hard", "origin/" + branch},
		{"clean", "--quiet", "-fdx"},
	} {
		if _, err := git(repo, args...); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SettingsError means GoatCounter's settings differ from projects/*.yml.
// Nothing was published.
type SettingsError struct{ Problems []string }

func (e *SettingsError) Error() string {
	return "GoatCounter's settings differ from projects/*.yml, so nothing was published: " +
		strings.Join(e.Problems, "; ")
}

// live is what the export found running for one project.
type live struct {
	host    string
	version string
	snap    dataformat.Settings
}

// inspect reads a project's site settings and returns them, with how they
// differ from projects/<slug>.yml.
func inspect(o *Options, p *project.Project) (live, []string, error) {
	host := p.GoatCounter.VHost
	if err := o.API.CheckUTC(host); err != nil {
		return live{}, nil, err
	}
	site, err := o.API.Site(host)
	if err != nil {
		return live{}, nil, err
	}
	version, err := o.API.Version(host)
	if err != nil {
		return live{}, nil, err
	}
	l := live{host, version, Snapshot(p, site, version)}
	var problems []string
	for _, pr := range Problems(p, l.snap) {
		problems = append(problems, p.Slug+": "+pr)
	}
	return l, problems, nil
}

// exportProject writes one project's files and returns the paths written
// (relative to dir).
func exportProject(o *Options, p *project.Project, l live, week string, days []isoweek.Day, dir string) ([]string, error) {
	var written []string
	rel := filepath.Join("data", p.Slug, "goatcounter-settings.json")
	if old, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || !bytes.Equal(old, dataformat.Marshal(l.snap)) {
		if err := writeAtomic(filepath.Join(dir, rel), dataformat.Marshal(l.snap)); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}

	rel = filepath.Join("data", p.Slug, "weekly", week+".json")
	if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
		fmt.Fprintf(o.Log, "%s: %s already published, leaving it as it is\n", p.Slug, week)
		return written, nil
	}
	raw, err := o.API.Week(l.host, days)
	if err != nil {
		return nil, err
	}
	doc, err := dataformat.Build(p, week, raw, o.Now, dataformat.Source{
		Collector: "goatcounter", GoatCounterVersion: l.version,
		Collect: l.snap.GoatCounter.Collect, DataRetentionDays: l.snap.GoatCounter.DataRetentionDays,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Slug, err)
	}
	if err := writeAtomic(filepath.Join(dir, rel), dataformat.Marshal(doc)); err != nil {
		return nil, err
	}
	total := 0
	for _, n := range raw.Totals {
		total += n
	}
	fmt.Fprintf(o.Log, "%s: wrote %s (%d page loads)\n", p.Slug, rel, total)
	return append(written, rel), nil
}

// Run exports one week for all projects. If any project's GoatCounter
// settings differ from projects/*.yml, it writes nothing and returns a
// *SettingsError.
func Run(o Options) error {
	if (o.Repo == "") == (o.Out == "") {
		return fmt.Errorf("give exactly one of -repo or -out")
	}
	if o.Branch == "" {
		o.Branch = "main"
	}
	today := isoweek.DayOf(o.Now)
	week := o.Week
	if week == "" {
		week = isoweek.LastComplete(today)
	}
	days, err := CheckExportable(week, today)
	if err != nil {
		return err
	}
	dir, useGit := o.Out, false
	if o.Repo != "" {
		dir, useGit = o.Repo, true
	}

	for attempt := 1; ; attempt++ {
		if useGit {
			if err := prepare(dir, o.Branch); err != nil {
				return err
			}
		}
		lives := make([]live, len(o.Projects))
		var problems []string
		for i, p := range o.Projects {
			l, pr, err := inspect(&o, p)
			if err != nil {
				return err
			}
			lives[i] = l
			problems = append(problems, pr...)
		}
		if len(problems) > 0 {
			return &SettingsError{problems}
		}
		var written []string
		for i, p := range o.Projects {
			w, err := exportProject(&o, p, lives[i], week, days, dir)
			if err != nil {
				return err
			}
			written = append(written, w...)
		}
		if !useGit || len(written) == 0 {
			return nil
		}
		msg := "data: GoatCounter settings changed"
		for _, w := range written {
			if filepath.Base(filepath.Dir(w)) == "weekly" {
				msg = "data: " + week
			}
		}
		if _, err := git(dir, append([]string{"add", "--"}, written...)...); err != nil {
			return err
		}
		if _, err := git(dir, "commit", "--quiet", "-m", msg); err != nil {
			return err
		}
		_, err := git(dir, "push", "--quiet", "origin", "HEAD:"+o.Branch)
		if err == nil {
			fmt.Fprintf(o.Log, "pushed %d file(s) for %s\n", len(written), week)
			return nil
		}
		if attempt == 3 {
			return fmt.Errorf("could not push: %w", err)
		}
		fmt.Fprintf(o.Log, "push failed (attempt %d): %v\n", attempt, err)
		time.Sleep(o.RetryPause)
	}
}
