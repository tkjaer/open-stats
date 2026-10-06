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

var (
	SlugRE  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	ValueRE = regexp.MustCompile(`^[a-z0-9-]{1,16}$`)
	vhostRE = regexp.MustCompile(`^[a-z0-9-]+\.localhost$`)
	paramRE = regexp.MustCompile(`^[a-z]{1,16}$`)
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
		Path      string   `yaml:"path"`
		Parameter string   `yaml:"parameter"`
		Allowed   []string `yaml:"allowed"`
	} `yaml:"request"`
	GoatCounter struct {
		VHost             string `yaml:"vhost"`
		Collect           int    `yaml:"collect"`
		DataRetentionDays int    `yaml:"data_retention_days"`
	} `yaml:"goatcounter"`
	Publish struct {
		BreakdownMinDayTotal int `yaml:"breakdown_min_day_total"`
		CountryMinPerDay     int `yaml:"country_min_per_day"`
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
	case !paramRE.MatchString(p.Request.Parameter):
		return fmt.Errorf("request.parameter must match %s", paramRE)
	case len(p.Request.Allowed) == 0:
		return fmt.Errorf("request.allowed is empty")
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
	for i, v := range p.Request.Allowed {
		if !ValueRE.MatchString(v) || v == "other" || slices.Contains(p.Request.Allowed[:i], v) {
			return fmt.Errorf("request.allowed: %q must be unique, match %s and not be \"other\"", v, ValueRE)
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
