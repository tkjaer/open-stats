package project

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tkjaer/open-stats/projects"
)

func TestEmbeddedProjects(t *testing.T) {
	ps, err := LoadAll(projects.FS)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ps, func(p *Project) bool { return p.Slug == "how-the-internet-works" }) {
		t.Fatal("how-the-internet-works missing")
	}
	for _, p := range ps {
		if p.GoatCounter.Collect != 16 {
			t.Errorf("%s: collect %d, want 16 (country only)", p.Slug, p.GoatCounter.Collect)
		}
		if p.Publish.CountryMinPerDay < 5 {
			t.Errorf("%s: country_min_per_day below 5", p.Slug)
		}
		if p.Publish.BreakdownMinDayTotal < 20 {
			t.Errorf("%s: breakdown_min_day_total below 20", p.Slug)
		}
	}
	if _, err := LoadAll(projects.FS, "nope"); err == nil {
		t.Error("unknown project: no error")
	}
}

func TestBadProjects(t *testing.T) {
	good, err := os.ReadFile("../../projects/how-the-internet-works.yml")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][2]string{
		"slug mismatch":   {"slug: how-the-internet-works", "slug: other"},
		"bad language":    {"[en, da, de]", `[en, "d'a"]`},
		"long language":   {"[en, da, de]", "[en, deu]"},
		"upper language":  {"[en, da, de]", "[en, DA]"},
		"other language":  {"[en, da, de]", "[en, other]"},
		"duplicate":       {"[en, da, de]", "[en, en]"},
		"no languages":    {"[en, da, de]", "[]"},
		"other values":    {"values: two-letter-code", "values: any"},
		"missing values":  {"values: two-letter-code", ""},
		"bad vhost":       {"vhost: how-the-internet-works.localhost", `vhost: "x'; drop table sites; --"`},
		"missing min":     {"country_min_per_day: 5", ""},
		"zero min":        {"country_min_per_day: 5", "country_min_per_day: 0"},
		"missing quiet":   {"breakdown_min_day_total: 20", ""},
		"quiet below min": {"breakdown_min_day_total: 20", "breakdown_min_day_total: 4"},
		"short retention": {"data_retention_days: 31", "data_retention_days: 7"},
		"unknown key":     {"slug: how-the-internet-works", "slug: how-the-internet-works\nextra: 1"},
		"bad path":        {"path: /how-the-internet-works/count", "path: /how-the-internet-works/x"},
		"http url":        {"url: https://", "url: http://"},
		"other parameter": {"parameter: lang", "parameter: locale"},
	}
	for name, r := range tests {
		data := strings.Replace(string(good), r[0], r[1], 1)
		if data == string(good) {
			t.Fatalf("%s: replacement didn't apply", name)
		}
		if _, err := Parse("how-the-internet-works.yml", []byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The nginx allow-list must have exactly one line per project, matching
// projects/*.yml.
func TestNginxAllowList(t *testing.T) {
	conf, err := os.ReadFile("../../collector/nginx/stats.irq.dk.conf")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := LoadAll(projects.FS)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, p := range ps {
		want = append(want, `"~^GET `+p.Request.Path+`\?`+p.Request.Parameter+`=[a-z]{2}$" `+p.GoatCounter.VHost+`;`)
	}
	lines := mapBlock(t, string(conf), "openstats_site")
	got := slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, "default ") })
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("collector/nginx/stats.irq.dk.conf and projects/*.yml disagree:\n got  %q\n want %q", got, want)
	}

	// The patterns behave as intended (RE2 and PCRE agree on these).
	site := func(s string) string {
		for _, l := range got {
			m := regexp.MustCompile(`^"~(.*)" (\S+);$`).FindStringSubmatch(l)
			if regexp.MustCompile(m[1]).MatchString(s) {
				return m[2]
			}
		}
		return ""
	}
	const hiw = "how-the-internet-works.localhost"
	for s, want := range map[string]string{
		"GET /how-the-internet-works/count?lang=en":                          hiw,
		"GET /how-the-internet-works/count?lang=da":                          hiw,
		"GET /how-the-internet-works/count?lang=de":                          hiw,
		"GET /how-the-internet-works/count?lang=fr":                          hiw, // counted, published under "other"
		"GET /how-the-internet-works/count?lang=zz":                          hiw,
		"GET /how-the-internet-works/count?lang=deu":                         "",
		"GET /how-the-internet-works/count?lang=EN":                          "",
		"GET /how-the-internet-works/count?lang=En":                          "",
		"GET /how-the-internet-works/count?lang=e1":                          "",
		"GET /how-the-internet-works/count?lang=e":                           "",
		"GET /how-the-internet-works/count?lang=":                            "",
		"GET /how-the-internet-works/count?lang":                             "",
		"GET /how-the-internet-works/count":                                  "",
		"GET /how-the-internet-works/count?lang=en-GB":                       "",
		"GET /how-the-internet-works/count?lang=en_GB":                       "",
		"GET /how-the-internet-works/count?lang=e-":                          "",
		"GET /how-the-internet-works/count?lang=" + strings.Repeat("a", 300): "",
		"GET /how-the-internet-works/count?lang=en%0A":                       "",
		"GET /how-the-internet-works/count?lang=en\n":                        "",
		"POST /how-the-internet-works/count?lang=en":                         "",
		"GET /how-the-internet-works/count?lang=en&x":                        "",
		"GET /how-the-internet-works/countXlang=en":                          "",
		"GET //how-the-internet-works/count?lang=en":                         "",
		"XGET /how-the-internet-works/count?lang=en":                         "",
		"GET /how-the-internet-works/count?lang=enn":                         "",
		"GET /how-the-internet-works/count?lang=%65n":                        "",
		"GET /how-the-internet-works/count/?lang=en":                         "",
		"GET /HIW/count?lang=en":                                             "",
		"GET /how-the-internet-works/count?x=1&lang=en":                      "",
	} {
		if got := site(s); got != want {
			t.Errorf("%q: got %q, want %q", s, got, want)
		}
	}

	// Every request a project accepts is translated to GoatCounter's
	// /count?p=/<code>, and nothing else is.
	up := mapBlock(t, string(conf), "openstats_upstream_uri")
	m := regexp.MustCompile(`^"~(.*)"\s+"(.*)";$`).FindStringSubmatch(up[1])
	re, repl := regexp.MustCompile(m[1]), strings.ReplaceAll(m[2], "$1", "${1}")
	for _, p := range ps {
		for _, v := range append([]string{"fr", "zz"}, p.Publish.Languages...) {
			req := "GET " + p.Request.Path + "?" + p.Request.Parameter + "=" + v
			if got := re.ReplaceAllString(req, repl); !re.MatchString(req) || got != "/count?p=/"+v {
				t.Errorf("upstream URI for %q: %q", req, got)
			}
		}
		for _, v := range []string{"deu", "EN", "e1", "", "en-GB"} {
			if req := "GET " + p.Request.Path + "?" + p.Request.Parameter + "=" + v; re.MatchString(req) {
				t.Errorf("upstream URI for %q: matched", req)
			}
		}
	}
}

func mapBlock(t *testing.T, conf, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^map "\$request_method \$request_uri" \$` + name + ` \{\n(.*?)^\}`).FindStringSubmatch(conf)
	if m == nil {
		t.Fatalf("map $%s not found", name)
	}
	var lines []string
	for _, l := range strings.Split(m[1], "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestPinnedGoatCounter(t *testing.T) {
	v, err := os.ReadFile("../../collector/goatcounter.version")
	if err != nil {
		t.Fatal(err)
	}
	for _, re := range []string{
		`(?m)^GOATCOUNTER_VERSION=v\d+\.\d+\.\d+$`,
		`(?m)^GOATCOUNTER_SHA256_LINUX_AMD64_GZ=[0-9a-f]{64}$`, `(?m)^GOATCOUNTER_SHA256_LINUX_AMD64=[0-9a-f]{64}$`,
		`(?m)^GOATCOUNTER_SHA256_LINUX_ARM64_GZ=[0-9a-f]{64}$`, `(?m)^GOATCOUNTER_SHA256_LINUX_ARM64=[0-9a-f]{64}$`,
	} {
		if !regexp.MustCompile(re).Match(v) {
			t.Errorf("collector/goatcounter.version: no match for %s", re)
		}
	}
}
