// SPDX-License-Identifier: AGPL-3.0-or-later

// Command open-stats runs the collector's weekly export, sets up GoatCounter
// sites, and builds the static site. See README.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/tkjaer/open-stats/collector/configure"
	"github.com/tkjaer/open-stats/collector/export"
	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
	"github.com/tkjaer/open-stats/site"
)

const usage = `usage: open-stats <command> [flags]

commands:
  export      publish the last complete ISO week (UTC) from GoatCounter to data/
  configure   create or update a project's GoatCounter site and the export token
  site        render data/ as a static HTML page
  version     print the version

Run "open-stats <command> -h" for the flags of a command.
`

// exitSettings: GoatCounter's settings differ from projects/*.yml; nothing exported.
const exitSettings = 3

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	code := 0
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "export":
		code, err = runExport(args)
	case "configure":
		err = runConfigure(args)
	case "site":
		err = runSite(args)
	case "version", "-version", "--version":
		fmt.Println(version())
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "open-stats: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "open-stats %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	os.Exit(code)
}

// buildVersion is set by collector/build.sh (-ldflags -X main.buildVersion=...).
var buildVersion string

func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "open-stats (unknown version)"
	}
	if buildVersion != "" {
		return "open-stats " + buildVersion + " " + bi.GoVersion
	}
	v := bi.Main.Version
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			v += " " + s.Value
		}
		if s.Key == "vcs.modified" && s.Value == "true" {
			v += " (modified)"
		}
	}
	return "open-stats " + v + " " + bi.GoVersion
}

// loadProjects reads the projects built into the binary, or those in dir.
func loadProjects(dir string, only ...string) ([]*project.Project, error) {
	var fsys fs.FS = projects.FS
	if dir != "" {
		fsys = os.DirFS(dir)
	}
	return project.LoadAll(fsys, only...)
}

func newFlags(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: open-stats %s %s\n\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

func runExport(args []string) (int, error) {
	fs := newFlags("export", "[flags]\n\nReads GOATCOUNTER_TOKEN and GOATCOUNTER_URL from the environment.")
	week := fs.String("week", "", "ISO week, e.g. 2026-W40 (default: the last complete week)")
	var only list
	fs.Var(&only, "project", "export only this project (repeatable; default: all)")
	repo := fs.String("repo", "", "git clone of the data repository (tkjaer/open-stats-data): write, commit and push")
	out := fs.String("out", "", "plain directory to write data/ into, without git")
	branch := fs.String("branch", "main", "branch to push to")
	dir := fs.String("projects", "", "read projects/*.yml from this directory instead of the built-in ones")
	gitTimeout := fs.Duration("git-timeout", export.DefaultGitTimeout, "kill any single git command that runs longer than this")
	fs.Parse(args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	token := os.Getenv("GOATCOUNTER_TOKEN")
	if token == "" {
		return 0, errors.New("GOATCOUNTER_TOKEN is not set (see /etc/open-stats/env)")
	}
	url := os.Getenv("GOATCOUNTER_URL")
	if url == "" {
		url = "http://127.0.0.1:8081"
	}
	ps, err := loadProjects(*dir, only...)
	if err != nil {
		return 0, err
	}
	err = export.Run(export.Options{
		Week: *week, Projects: ps, Repo: *repo, Out: *out, Branch: *branch, Now: time.Now().UTC(),
		API: goatcounter.NewAPI(url, token), Log: os.Stdout, RetryPause: 10 * time.Second, GitTimeout: *gitTimeout,
	})
	var se *export.SettingsError
	if errors.As(err, &se) {
		for _, p := range se.Problems {
			fmt.Fprintf(os.Stderr, "GoatCounter settings differ from projects/*.yml: %s\n", p)
		}
		fmt.Fprintln(os.Stderr, "nothing was published; run `open-stats configure <project>` to restore them, "+
			"then run the export again")
		return exitSettings, nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

func runConfigure(args []string) error {
	fs := newFlags("configure", "[flags] <project>\n\nRun as root on the server, with GoatCounter running. "+
		"Safe to run again.\nThe first site's dashboard password is read from OPEN_STATS_ADMIN_PASSWORD, or asked for.")
	db := fs.String("db", "sqlite+/var/lib/goatcounter/db.sqlite3", "GoatCounter database")
	url := fs.String("url", "http://127.0.0.1:8081", "GoatCounter's local address")
	bin := fs.String("goatcounter", "/usr/local/bin/goatcounter", "goatcounter binary")
	runAs := fs.String("run-as", "goatcounter", "user to run the goatcounter CLI as when root (empty: current user)")
	envFile := fs.String("env-file", "/etc/open-stats/env", "file holding the export token")
	email := fs.String("admin-email", "", "dashboard login for the first site (asked for if empty)")
	dir := fs.String("projects", "", "read projects/*.yml from this directory instead of the built-in ones")
	// Accept flags after the project name too.
	var rest []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(rest) != 1 {
		fs.Usage()
		os.Exit(2)
	}
	ps, err := loadProjects(*dir, rest[0])
	if err != nil {
		return err
	}
	return configure.Run(ps[0], configure.Options{
		CLI: &goatcounter.CLI{Binary: *bin, DB: *db, RunAs: *runAs}, URL: *url, EnvFile: *envFile,
		AdminEmail: *email, AdminPassword: os.Getenv("OPEN_STATS_ADMIN_PASSWORD"),
	})
}

func runSite(args []string) error {
	fs := newFlags("site", "[flags]")
	data := fs.String("data", "data", "data directory")
	out := fs.String("out", "_site", "output directory")
	dir := fs.String("projects", "", "read projects/*.yml from this directory instead of the built-in ones")
	fs.Parse(args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	ps, err := loadProjects(*dir)
	if err != nil {
		return err
	}
	msg, err := site.Build(*data, *out, ps, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}
