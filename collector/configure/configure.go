// SPDX-License-Identifier: AGPL-3.0-or-later

// Package configure creates or updates a project's GoatCounter site,
// reproducibly. It is safe to run again at any time. It
//
//  1. creates the GoatCounter site <project>.localhost if it doesn't exist
//     (the first site also creates the dashboard login; later sites are
//     linked to it, so the same login and export token cover all projects);
//  2. applies the data-collection settings from projects/<project>.yml
//     through GoatCounter's API (collect: country only; data retention 31
//     days; private dashboard), then reads them back to check;
//  3. makes sure the export token in /etc/open-stats/env exists, can read
//     statistics and site settings (and nothing else), and covers every site.
//
// GoatCounter's CLI can't create tokens with the "read statistics"
// permission or change settings, so steps 1 and 3 write to its database with
// `goatcounter db query`; step 2 uses a temporary token that is deleted again.
package configure

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/project"
)

// GoatCounter API token permission bits.
const (
	PermSiteRead   = 8
	PermSiteUpdate = 32
	PermStats      = 64
	ExportPerms    = PermStats | PermSiteRead
	TokenName      = "open-stats export"
)

var tokenRE = regexp.MustCompile(`^[0-9a-f]{20,64}$`)

type Options struct {
	CLI        *goatcounter.CLI
	URL        string
	EnvFile    string
	AdminEmail string
	// Dashboard password for the first site; asked by GoatCounter if empty.
	AdminPassword string
	Stdin         io.Reader
	Log           io.Writer
}

type setup struct {
	Options
}

func (s *setup) logf(format string, args ...any) { fmt.Fprintf(s.Log, format+"\n", args...) }

// siteID returns the site's id and its root (itself or its parent), or 0.
func (s *setup) siteID(vhost string) (int, int, error) {
	rows, err := s.CLI.Query("select site_id, parent from sites where cname = '" + vhost + "' and state = 'a'")
	if err != nil || len(rows) == 0 {
		return 0, 0, err
	}
	id, _ := goatcounter.Int(rows[0], "site_id")
	root, ok := goatcounter.Int(rows[0], "parent")
	if !ok || root == 0 {
		root = id
	}
	return id, root, nil
}

func (s *setup) ensureSite(vhost string) (int, int, error) {
	id, root, err := s.siteID(vhost)
	if err != nil {
		return 0, 0, err
	}
	if id != 0 {
		s.logf("site %s exists (id %d)", vhost, id)
		return id, root, nil
	}

	roots, err := s.CLI.Query("select cname from sites where parent is null and state = 'a' order by site_id limit 1")
	if err != nil {
		return 0, 0, err
	}
	if len(roots) > 0 {
		link, _ := roots[0]["cname"].(string)
		s.logf("creating site %s, linked to %s", vhost, link)
		if _, err := s.CLI.Run("db", "create", "site", "-db", s.CLI.DB, "-vhost", vhost, "-link", link); err != nil {
			return 0, 0, err
		}
	} else {
		email := s.AdminEmail
		if email == "" {
			fmt.Fprint(s.Log, "E-mail address for the dashboard login: ")
			line, _ := bufio.NewReader(s.Stdin).ReadString('\n')
			email = strings.TrimSpace(line)
		}
		if email == "" {
			return 0, 0, fmt.Errorf("no e-mail address for the dashboard login")
		}
		args := []string{"db", "create", "site", "-db", s.CLI.DB, "-vhost", vhost, "-user.email", email}
		s.logf("creating site %s with login %s", vhost, email)
		if s.AdminPassword != "" {
			_, err = s.CLI.Run(append(args, "-user.password", s.AdminPassword)...)
		} else {
			s.logf("GoatCounter will now ask for a dashboard password.")
			err = s.CLI.RunInteractive(args...)
		}
		if err != nil {
			return 0, 0, err
		}
	}
	id, root, err = s.siteID(vhost)
	if err == nil && id == 0 {
		err = fmt.Errorf("creating %s failed", vhost)
	}
	return id, root, err
}

func (s *setup) adminUser(root int) (int, error) {
	rows, err := s.CLI.Query(fmt.Sprintf("select user_id, json_extract(settings, '$.timezone') as tz from users "+
		"where site_id = %d order by user_id limit 1", root))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("no GoatCounter user found")
	}
	if tz, _ := rows[0]["tz"].(string); !goatcounter.IsUTC(tz) {
		return 0, fmt.Errorf("the dashboard user's time zone is %q; set it to UTC in the dashboard "+
			"(Settings → Preferences) first: GoatCounter groups days by it", tz)
	}
	user, _ := goatcounter.Int(rows[0], "user_id")
	return user, nil
}

func sitesJSON(ids []int) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

func (s *setup) insertToken(root, user int, name string, perms int, sites []int) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	err := s.CLI.Exec(fmt.Sprintf("insert into api_tokens (site_id, user_id, name, token, permissions, created_at, sites) "+
		"values (%d, %d, '%s', '%s', '%d', strftime('%%Y-%%m-%%d %%H:%%M:%%S', 'now'), '%s')",
		root, user, name, token, perms, sitesJSON(sites)))
	return token, err
}

func (s *setup) applySettings(p *project.Project, siteID, root, user int) (err error) {
	tmp, err := s.insertToken(root, user, "open-stats configure (temporary)", PermSiteRead|PermSiteUpdate, []int{siteID})
	if err != nil {
		return err
	}
	defer func() {
		if derr := s.CLI.Exec("delete from api_tokens where token = '" + tmp + "'"); err == nil {
			err = derr
		}
	}()

	host := p.GoatCounter.VHost
	api := goatcounter.NewAPI(s.URL, tmp)
	path := "/api/v0/sites/" + strconv.Itoa(siteID)
	var cur goatcounter.Site
	if err := api.Get(host, path, nil, &cur); err != nil {
		return err
	}
	ignoreIPs := cur.Settings.IgnoreIPs
	if len(ignoreIPs) == 0 || string(ignoreIPs) == "null" {
		ignoreIPs = json.RawMessage(`""`)
	}
	// POST replaces all settings; PATCH can only add bits to "collect".
	body := map[string]any{
		"cname":       host,
		"link_domain": "",
		"settings": map[string]any{
			"public":          "private",
			"secret":          "",
			"allow_counter":   false,
			"allow_bosmang":   false,
			"data_retention":  p.GoatCounter.DataRetentionDays,
			"ignore_ips":      ignoreIPs,
			"collect":         p.GoatCounter.Collect,
			"collect_regions": "",
			"allow_embed":     "",
		},
	}
	if err := api.Do("POST", host, path, nil, body, nil); err != nil {
		return err
	}
	var after goatcounter.Site
	if err := api.Get(host, path, nil, &after); err != nil {
		return err
	}
	a := after.Settings
	if a.Collect != p.GoatCounter.Collect || a.DataRetention != p.GoatCounter.DataRetentionDays ||
		a.Public != "private" || a.AllowCounter || a.Secret != "" || a.AllowEmbedString() != "" {
		return fmt.Errorf("settings did not stick: %+v", a)
	}
	s.logf("settings for %s: collect %v, data retention %d days, private dashboard",
		host, dataformat.CollectNames(p.GoatCounter.Collect), p.GoatCounter.DataRetentionDays)
	return nil
}

// readEnv returns the env file's lines and its GOATCOUNTER_TOKEN.
func readEnv(path string) ([]string, string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var lines []string
	token := ""
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if v, ok := strings.CutPrefix(ln, "GOATCOUNTER_TOKEN="); ok {
			token = strings.TrimSpace(v)
			continue
		}
		lines = append(lines, ln)
	}
	if token != "" && !tokenRE.MatchString(token) {
		return nil, "", fmt.Errorf("unexpected GOATCOUNTER_TOKEN format in %s", path)
	}
	return lines, token, nil
}

// writeEnv replaces the file atomically, readable by its owner only.
func writeEnv(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".env-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *setup) ensureExportToken(root, user int) error {
	rows, err := s.CLI.Query(fmt.Sprintf("select site_id from sites where (site_id = %d or parent = %d) "+
		"and state = 'a' order by site_id", root, root))
	if err != nil {
		return err
	}
	var all []int
	for _, r := range rows {
		id, _ := goatcounter.Int(r, "site_id")
		all = append(all, id)
	}
	lines, token, err := readEnv(s.EnvFile)
	if err != nil {
		return err
	}
	if token != "" {
		rows, err := s.CLI.Query("select api_token_id from api_tokens where token = '" + token + "'")
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			err := s.CLI.Exec(fmt.Sprintf("update api_tokens set permissions = '%d', sites = '%s', name = '%s' "+
				"where token = '%s'", ExportPerms, sitesJSON(all), TokenName, token))
			if err == nil {
				s.logf("export token in %s covers sites %v", s.EnvFile, all)
			}
			return err
		}
	}
	token, err = s.insertToken(root, user, TokenName, ExportPerms, all)
	if err != nil {
		return err
	}
	if err := writeEnv(s.EnvFile, append(lines, "GOATCOUNTER_TOKEN="+token)); err != nil {
		return err
	}
	s.logf("new export token written to %s (mode 600), covering sites %v", s.EnvFile, all)
	return nil
}

// Run configures one project.
func Run(p *project.Project, o Options) error {
	if !project.SlugRE.MatchString(p.Slug) || p.GoatCounter.VHost != p.Slug+".localhost" {
		return fmt.Errorf("unexpected vhost %q", p.GoatCounter.VHost)
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Log == nil {
		o.Log = os.Stdout
	}
	s := &setup{Options: o}
	siteID, root, err := s.ensureSite(p.GoatCounter.VHost)
	if err != nil {
		return err
	}
	user, err := s.adminUser(root)
	if err != nil {
		return err
	}
	if err := s.applySettings(p, siteID, root, user); err != nil {
		return err
	}
	if err := s.ensureExportToken(root, user); err != nil {
		return err
	}
	s.logf("done. Dashboard: ssh -L 8081:127.0.0.1:8081 <server>, then http://%s:8081/", p.GoatCounter.VHost)
	return nil
}
