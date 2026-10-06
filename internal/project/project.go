// SPDX-License-Identifier: AGPL-3.0-or-later

// Package project reads and checks projects/<slug>.yml.
package project

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Parameter is the one query parameter a count carries. nginx's maps in
// collector/nginx/stats.irq.dk.conf handle only this name.
const Parameter = "lang"

// TwoLetterCode is the one kind of value a count may carry: two lowercase
// letters (CodeRE), so at most 676 values and never free text or an ID.
// nginx checks the same pattern and drops anything else.
const TwoLetterCode = "two-letter-code"

// maxLanguages bounds publish.languages (the export reads their counts from
// GoatCounter in one page of 100).
const maxLanguages = 50

var (
	SlugRE  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	CodeRE  = regexp.MustCompile(`^[a-z]{2}$`)
	vhostRE = regexp.MustCompile(`^[a-z0-9-]+\.localhost$`)
	httpsRE = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._~/%-]*)?$`)
)

// Project is one site that is counted.
type Project struct {
	Slug    string `yaml:"slug"`
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	Source  string `yaml:"source"`
	Privacy string `yaml:"privacy"`
	Request struct {
		Path      string `yaml:"path"`
		Parameter string `yaml:"parameter"`
		Values    string `yaml:"values"`
	} `yaml:"request"`
	GoatCounter struct {
		VHost             string `yaml:"vhost"`
		Collect           int    `yaml:"collect"`
		DataRetentionDays int    `yaml:"data_retention_days"`
	} `yaml:"goatcounter"`
	Publish struct {
		BreakdownMinDayTotal int `yaml:"breakdown_min_day_total"`
		CountryMinPerDay     int `yaml:"country_min_per_day"`
		// Languages get their own column; every other code is under "other".
		Languages []string `yaml:"languages"`
	} `yaml:"publish"`
}

// Parse reads one project file; name is its file name (<slug>.yml).
func Parse(name string, data []byte) (*Project, error) {
	var p Project
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := p.check(strings.TrimSuffix(path.Base(name), ".yml")); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &p, nil
}

func (p *Project) check(stem string) error {
	switch {
	case !SlugRE.MatchString(p.Slug) || p.Slug != stem:
		return fmt.Errorf("slug %q must match the file name and %s", p.Slug, SlugRE)
	case p.Name == "" || len(p.Name) > 100:
		return fmt.Errorf("name missing or too long")
	case !httpsRE.MatchString(p.URL) || !httpsRE.MatchString(p.Source) || !httpsRE.MatchString(p.Privacy):
		return fmt.Errorf("url, source and privacy must be plain https URLs")
	case p.Request.Path != "/"+p.Slug+"/count":
		return fmt.Errorf("request.path must be /%s/count", p.Slug)
	case p.Request.Parameter != Parameter:
		return fmt.Errorf("request.parameter must be %q (nginx's maps only handle that name)", Parameter)
	case p.Request.Values != TwoLetterCode:
		return fmt.Errorf("request.values must be %q (the only kind nginx's maps handle)", TwoLetterCode)
	case len(p.Publish.Languages) == 0 || len(p.Publish.Languages) > maxLanguages:
		return fmt.Errorf("publish.languages must list 1 to %d languages", maxLanguages)
	case !vhostRE.MatchString(p.GoatCounter.VHost):
		return fmt.Errorf("goatcounter.vhost must look like <name>.localhost")
	case p.GoatCounter.Collect < 1 || p.GoatCounter.Collect >= 512:
		return fmt.Errorf("goatcounter.collect must be a GoatCounter collect bitmask")
	case p.GoatCounter.DataRetentionDays < 31:
		return fmt.Errorf("goatcounter.data_retention_days must be at least 31 (GoatCounter's minimum)")
	case p.Publish.CountryMinPerDay < 1:
		return fmt.Errorf("publish.country_min_per_day must be at least 1")
	case p.Publish.BreakdownMinDayTotal < p.Publish.CountryMinPerDay:
		return fmt.Errorf("publish.breakdown_min_day_total must be at least publish.country_min_per_day")
	}
	for i, v := range p.Publish.Languages {
		if !CodeRE.MatchString(v) || slices.Contains(p.Publish.Languages[:i], v) {
			return fmt.Errorf("publish.languages: %q must be unique and match %s", v, CodeRE)
		}
	}
	return nil
}

// LoadAll reads every *.yml in fsys, sorted by slug. With only set, it
// returns just those projects (and fails on unknown ones).
func LoadAll(fsys fs.FS, only ...string) ([]*Project, error) {
	names, err := fs.Glob(fsys, "*.yml")
	if err != nil {
		return nil, err
	}
	var out []*Project
	for _, n := range names {
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		p, err := Parse(n, data)
		if err != nil {
			return nil, err
		}
		if len(only) == 0 || slices.Contains(only, p.Slug) {
			out = append(out, p)
		}
	}
	for _, o := range only {
		if !slices.ContainsFunc(out, func(p *Project) bool { return p.Slug == o }) {
			return nil, fmt.Errorf("unknown project %q", o)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no projects found")
	}
	return out, nil
}
