// SPDX-License-Identifier: AGPL-3.0-or-later

// Package goatcounter talks to the local GoatCounter: its API (with a token)
// and, for setup, its CLI (`goatcounter db query`).
package goatcounter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/isoweek"
)

// API is GoatCounter's HTTP API on localhost. The Host header picks the site.
type API struct {
	BaseURL string
	Token   string
	// GoatCounter allows 4 API requests a second.
	Pause time.Duration
	// How long to keep trying while the connection is refused, e.g. while
	// GoatCounter restarts (it does every hour, for about half a second).
	RefusedFor time.Duration
	HTTP       *http.Client
}

func NewAPI(baseURL, token string) *API {
	return &API{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Pause: 300 * time.Millisecond, RefusedFor: 20 * time.Second,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// HTTPError is a non-2xx answer.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

// Do sends a request and decodes the JSON answer into out (if not nil).
func (a *API) Do(method, host, path string, params url.Values, body, out any) error {
	u := a.BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var (
		resp     *http.Response
		err      error
		deadline = time.Now().Add(a.RefusedFor)
	)
	for {
		var rd io.Reader
		if b != nil {
			rd = bytes.NewReader(b)
		}
		req, rerr := http.NewRequest(method, u, rd)
		if rerr != nil {
			return rerr
		}
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+a.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		time.Sleep(a.Pause)
		resp, err = a.HTTP.Do(req)
		// A refused connection never reached GoatCounter, so trying again is
		// safe for any method.
		if err != nil && errors.Is(err, syscall.ECONNREFUSED) && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		break
	}
	if err != nil {
		return fmt.Errorf("GoatCounter at %s not reachable: %w", a.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GoatCounter %s %s for %s: %w", method, path, host,
			&HTTPError{resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 500)]))})
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (a *API) Get(host, path string, params url.Values, out any) error {
	return a.Do(http.MethodGet, host, path, params, nil, out)
}

// IsStatus reports whether err is an HTTP error with this status.
func IsStatus(err error, status int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == status
}

// Version is the running GoatCounter's version.
func (a *API) Version(host string) (string, error) {
	var s struct {
		Version string `json:"version"`
	}
	if err := a.Get(host, "/status", nil, &s); err != nil {
		return "", err
	}
	if s.Version == "" {
		return "unknown", nil
	}
	return s.Version, nil
}

// IsUTC reports whether a time zone name from GoatCounter is UTC.
func IsUTC(tz string) bool { return tz == ".UTC" || tz == "UTC" || tz == "Etc/UTC" }

// CheckUTC fails unless the token's user has the UTC time zone: GoatCounter
// groups days in that zone.
func (a *API) CheckUTC(host string) error {
	var me struct {
		User struct {
			Settings struct {
				Timezone string `json:"timezone"`
			} `json:"settings"`
		} `json:"user"`
	}
	if err := a.Get(host, "/api/v0/me", nil, &me); err != nil {
		return err
	}
	if tz := me.User.Settings.Timezone; !IsUTC(tz) {
		return fmt.Errorf("the GoatCounter user that owns the token uses time zone %q; "+
			"GoatCounter groups days in that zone, so set it to UTC first", tz)
	}
	return nil
}

// SiteSettings is the part of GoatCounter's site settings open-stats manages.
type SiteSettings struct {
	Public         string          `json:"public"`
	Secret         string          `json:"secret"`
	AllowCounter   bool            `json:"allow_counter"`
	AllowBosmang   bool            `json:"allow_bosmang"`
	DataRetention  int             `json:"data_retention"`
	IgnoreIPs      json.RawMessage `json:"ignore_ips"`
	Collect        int             `json:"collect"`
	CollectRegions json.RawMessage `json:"collect_regions"`
	AllowEmbed     json.RawMessage `json:"allow_embed"`
}

// Site is a GoatCounter site as the API returns it.
type Site struct {
	ID       int    `json:"id"`
	Cname    string `json:"cname"`
	Settings SiteSettings
}

func (s *Site) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID        int           `json:"id"`
		Cname     *string       `json:"cname"`
		Settings  *SiteSettings `json:"settings"`
		Setttings *SiteSettings `json:"setttings"` // sic, GoatCounter 2.7
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.ID = raw.ID
	if raw.Cname != nil {
		s.Cname = *raw.Cname
	}
	switch {
	case raw.Setttings != nil:
		s.Settings = *raw.Setttings
	case raw.Settings != nil:
		s.Settings = *raw.Settings
	}
	return nil
}

// AllowEmbedString flattens allow_embed (a string or a list) for publishing.
func (s SiteSettings) AllowEmbedString() string {
	var str string
	if json.Unmarshal(s.AllowEmbed, &str) == nil {
		return str
	}
	var list []string
	if json.Unmarshal(s.AllowEmbed, &list) == nil {
		return strings.Join(list, ",")
	}
	return string(s.AllowEmbed)
}

// Site finds the site with this vhost (cname).
func (a *API) Site(host string) (*Site, error) {
	var resp struct {
		Sites []Site `json:"sites"`
	}
	if err := a.Get(host, "/api/v0/sites", nil, &resp); err != nil {
		return nil, err
	}
	for _, s := range resp.Sites {
		if s.Cname == host {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("no GoatCounter site %s", host)
}

// maxCountries is far more than there are country codes (about 250).
const maxCountries = 1000

type dayStat struct {
	Day   string `json:"day"`
	Daily int    `json:"daily"`
}

// Week reads the raw daily numbers of one week from GoatCounter.
func (a *API) Week(host string, days []isoweek.Day) (dataformat.Raw, error) {
	raw := dataformat.Raw{Totals: map[string]int{}, Paths: map[string]map[string]int{},
		Countries: map[string]map[string]int{}}
	inWeek := map[string]bool{}
	for _, d := range days {
		day := d.Format(time.DateOnly)
		inWeek[day] = true
		raw.Paths[day] = map[string]int{}
	}
	span := func(first, last isoweek.Day) url.Values {
		return url.Values{
			"start": {first.Format(time.DateOnly) + "T00:00:00Z"},
			"end":   {last.Format(time.DateOnly) + "T23:59:59Z"},
		}
	}

	var total struct {
		Stats []dayStat `json:"stats"`
	}
	if err := a.Get(host, "/api/v0/stats/total", span(days[0], days[6]), &total); err != nil {
		return raw, err
	}
	for _, s := range total.Stats {
		if inWeek[s.Day] {
			raw.Totals[s.Day] = s.Daily
		}
	}

	p := span(days[0], days[6])
	p.Set("daily", "true")
	p.Set("limit", "100")
	var hits struct {
		More bool `json:"more"`
		Hits []struct {
			Path  string    `json:"path"`
			Stats []dayStat `json:"stats"`
		} `json:"hits"`
	}
	if err := a.Get(host, "/api/v0/stats/hits", p, &hits); err != nil {
		return raw, err
	}
	if hits.More {
		return raw, fmt.Errorf("%s has more than 100 paths; expected only the allow-listed ones", host)
	}
	for _, h := range hits.Hits {
		for _, s := range h.Stats {
			if inWeek[s.Day] {
				raw.Paths[s.Day][h.Path] += s.Daily
			}
		}
	}

	for _, d := range days {
		day := d.Format(time.DateOnly)
		raw.Countries[day] = map[string]int{}
		// Up to 100 countries a page; GoatCounter sorts by count and code,
		// so offsets are stable for a finished day.
		for offset := 0; ; offset += 100 {
			if offset >= maxCountries {
				return raw, fmt.Errorf("%s: more than %d countries on %s", host, maxCountries, day)
			}
			p := span(d, d)
			p.Set("limit", "100")
			p.Set("offset", strconv.Itoa(offset))
			var locs struct {
				More  bool `json:"more"`
				Stats []struct {
					ID    string `json:"id"`
					Count int    `json:"count"`
				} `json:"stats"`
			}
			if err := a.Get(host, "/api/v0/stats/locations", p, &locs); err != nil {
				return raw, err
			}
			for _, s := range locs.Stats {
				if _, dup := raw.Countries[day][s.ID]; dup {
					return raw, fmt.Errorf("%s: country %q listed twice on %s", host, s.ID, day)
				}
				raw.Countries[day][s.ID] = s.Count
			}
			if !locs.More {
				break
			}
			if len(locs.Stats) == 0 {
				return raw, fmt.Errorf("%s: empty page of countries with more to come on %s", host, day)
			}
		}
	}
	return raw, nil
}

// CLI runs the goatcounter binary, as another user when running as root.
type CLI struct {
	Binary string
	DB     string
	RunAs  string
}

func (c *CLI) command(args ...string) *exec.Cmd {
	argv := append([]string{c.Binary}, args...)
	if c.RunAs != "" && os.Geteuid() == 0 {
		argv = append([]string{"runuser", "-u", c.RunAs, "--"}, argv...)
	}
	return exec.Command(argv[0], argv[1:]...)
}

// Run runs a goatcounter command and returns its output.
func (c *CLI) Run(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := c.command(args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String() + stdout.String())
		return "", fmt.Errorf("goatcounter %s: %w: %s", strings.Join(args[:min(len(args), 3)], " "), err, msg)
	}
	return stdout.String(), nil
}

// RunInteractive runs a goatcounter command attached to the terminal.
func (c *CLI) RunInteractive(args ...string) error {
	cmd := c.command(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Query runs a SELECT with `goatcounter db query` and returns the rows.
func (c *CLI) Query(sql string) ([]map[string]any, error) {
	out, err := c.Run("db", "query", "-db", c.DB, "-format", "json", sql)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("goatcounter db query: %w", err)
	}
	return rows, nil
}

// Exec runs a statement with `goatcounter db query -format exec`.
func (c *CLI) Exec(sql string) error {
	_, err := c.Run("db", "query", "-db", c.DB, "-format", "exec", sql)
	return err
}

// Int reads an integer column from a Query row.
func Int(row map[string]any, col string) (int, bool) {
	switch v := row[col].(type) {
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	case string:
		var n int
		_, err := fmt.Sscan(v, &n)
		return n, err == nil
	}
	return 0, false
}
