package export

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
)

// fakeGoatCounter answers the API calls the export makes, for 2026-W40
// (Mon 28 Sep – Sun 4 Oct 2026).
func fakeGoatCounter(t *testing.T, tz string, calls *int) *httptest.Server {
	return fakeGoatCounterSettings(t, tz, goodSettings, calls)
}

const goodSettings = `{"public":"private","secret":"s3cret","data_retention":31,"collect":16,"ignore_ips":["1.2.3.4"],"allow_embed":""}`

func fakeGoatCounterSettings(t *testing.T, tz, settings string, calls *int) *httptest.Server {
	t.Helper()
	locations := map[string]string{
		"2026-09-28": `[{"id":"DE","count":10},{"id":"DK","count":5},{"id":"SE","count":4},{"id":"(unknown)","count":1}]`,
		"2026-10-01": `[{"id":"FR","count":5}]`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Host != "how-the-internet-works.localhost" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/status":
			io.WriteString(w, `{"version":"v2.7.0"}`)
		case "/api/v0/me":
			io.WriteString(w, `{"user":{"settings":{"timezone":"`+tz+`"}}}`)
		case "/api/v0/sites":
			io.WriteString(w, `{"sites":[{"id":1,"cname":"how-the-internet-works.localhost","setttings":`+settings+`}]}`)
		case "/api/v0/stats/total":
			if q.Get("start") != "2026-09-28T00:00:00Z" || q.Get("end") != "2026-10-04T23:59:59Z" {
				t.Errorf("total: %v", q)
			}
			io.WriteString(w, `{"total":25,"stats":[{"day":"2026-09-27","daily":99},
				{"day":"2026-09-28","daily":20},{"day":"2026-10-01","daily":5}]}`)
		case "/api/v0/stats/hits":
			io.WriteString(w, `{"more":false,"hits":[
				{"path":"/en","stats":[{"day":"2026-09-28","daily":15},{"day":"2026-10-01","daily":5}]},
				{"path":"/da","stats":[{"day":"2026-09-28","daily":5}]}]}`)
		case "/api/v0/stats/locations":
			if q.Get("start")[:10] != q.Get("end")[:10] {
				t.Errorf("locations not per day: %v", q)
			}
			l, ok := locations[q.Get("start")[:10]]
			if !ok {
				l = "[]"
			}
			io.WriteString(w, `{"stats":`+l+`}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitRepos makes a bare origin with one commit, and a clone with an
// untracked file in it.
func gitRepos(t *testing.T) (tmp, origin, clone string) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	tmp = t.TempDir()
	origin, clone = filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "clone")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run(t, "git", "init", "--quiet", "--bare", "-b", "main", origin)
	run(t, "git", "clone", "--quiet", origin, clone)
	run(t, "git", "-C", clone, "config", "user.name", "test")
	run(t, "git", "-C", clone, "config", "user.email", "test@example.org")
	os.WriteFile(filepath.Join(clone, "README.md"), []byte("hi\n"), 0o644)
	run(t, "git", "-C", clone, "add", ".")
	run(t, "git", "-C", clone, "commit", "--quiet", "-m", "init")
	run(t, "git", "-C", clone, "push", "--quiet", "origin", "main")
	os.WriteFile(filepath.Join(clone, "junk"), []byte("x"), 0o644)
	return tmp, origin, clone
}

func TestRun(t *testing.T) {
	calls := 0
	srv := fakeGoatCounter(t, "Etc/UTC", &calls)
	defer srv.Close()
	ps, err := project.LoadAll(projects.FS, "how-the-internet-works")
	if err != nil {
		t.Fatal(err)
	}

	tmp, origin, clone := gitRepos(t)

	api := goatcounter.NewAPI(srv.URL, "tok")
	api.Pause = 0
	var log strings.Builder
	o := Options{Projects: ps, Repo: clone, Now: time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), API: api, Log: &log}
	if err := Run(o); err != nil {
		t.Fatalf("Run: %v\n%s", err, &log)
	}
	files := run(t, "git", "--git-dir", origin, "ls-tree", "-r", "--name-only", "main")
	if files != "README.md\ndata/how-the-internet-works/goatcounter-settings.json\ndata/how-the-internet-works/weekly/2026-W40.json\n" {
		t.Errorf("files on origin:\n%s", files)
	}
	if msg := run(t, "git", "--git-dir", origin, "log", "-1", "--format=%s", "main"); msg != "data: 2026-W40\n" {
		t.Errorf("commit message %q", msg)
	}

	data := []byte(run(t, "git", "--git-dir", origin, "show", "main:data/how-the-internet-works/weekly/2026-W40.json"))
	w, err := dataformat.ParseWeek(data, ps[0], "2026-W40")
	if err != nil {
		t.Fatal(err)
	}
	if w.GeneratedAt != "2026-10-05T03:00:00Z" {
		t.Errorf("generated_at %s", w.GeneratedAt)
	}
	got, _ := json.Marshal(w.Tables)
	for _, want := range []string{
		`{"day":"2026-09-28","count":20}`, `{"day":"2026-10-01","count":5}`,
		`{"day":"2026-09-28","counts":{"da":5,"en":15,"other":0}}`,
		`{"day":"2026-09-28","counts":{"DE":10,"DK":5,"other":5}}`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("tables lack %s:\n%s", want, got)
		}
	}
	// Only Monday has 20 page loads or more; the other days are just totals.
	if strings.Contains(string(got), "2026-09-27") || strings.Contains(string(got), "SE") ||
		strings.Count(string(got), `"counts"`) != 2 || strings.Contains(string(got), "FR") {
		t.Errorf("tables leak:\n%s", got)
	}
	settings := run(t, "git", "--git-dir", origin, "show", "main:data/how-the-internet-works/goatcounter-settings.json")
	if strings.Contains(settings, "s3cret") || strings.Contains(settings, "1.2.3.4") {
		t.Errorf("settings leak:\n%s", settings)
	}

	// Second run: nothing new; a file someone fixed by hand stays as it is.
	edited := strings.Replace(string(data), "2026-10-05T03:00:00Z", "2000-01-01T00:00:00Z", 1)
	work := filepath.Join(tmp, "work")
	run(t, "git", "clone", "--quiet", origin, work)
	os.WriteFile(filepath.Join(work, "data/how-the-internet-works/weekly/2026-W40.json"), []byte(edited), 0o644)
	run(t, "git", "-C", work, "-c", "user.name=x", "-c", "user.email=x@example.org", "commit", "--quiet", "-am", "manual")
	run(t, "git", "-C", work, "push", "--quiet", "origin", "main")
	head := run(t, "git", "--git-dir", origin, "rev-parse", "main")
	log.Reset()
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "already published") {
		t.Errorf("log: %s", &log)
	}
	if run(t, "git", "--git-dir", origin, "rev-parse", "main") != head {
		t.Error("second run pushed something")
	}
	if got := run(t, "git", "--git-dir", origin, "show", "main:data/how-the-internet-works/weekly/2026-W40.json"); got != edited {
		t.Error("manual edit was overwritten")
	}

	// Not over yet / too old / wrong time zone / no target.
	for _, tc := range []struct {
		o   func(Options) Options
		err string
	}{
		{func(o Options) Options { o.Week = "2026-W41"; return o }, "not over"},
		{func(o Options) Options { o.Week = "2026-W35"; return o }, "retention"},
		{func(o Options) Options { o.Repo = ""; return o }, "exactly one"},
		{func(o Options) Options { o.Out = tmp; return o }, "exactly one"},
	} {
		if err := Run(tc.o(o)); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("got %v, want %q", err, tc.err)
		}
	}
}

func TestRunNotUTC(t *testing.T) {
	calls := 0
	srv := fakeGoatCounter(t, "Europe/Copenhagen", &calls)
	defer srv.Close()
	ps, _ := project.LoadAll(projects.FS, "how-the-internet-works")
	api := goatcounter.NewAPI(srv.URL, "tok")
	api.Pause = 0
	out := t.TempDir()
	err := Run(Options{Projects: ps, Out: out, Now: time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), API: api, Log: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "UTC") {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

func TestRunOut(t *testing.T) {
	calls := 0
	srv := fakeGoatCounter(t, ".UTC", &calls)
	defer srv.Close()
	ps, _ := project.LoadAll(projects.FS, "how-the-internet-works")
	api := goatcounter.NewAPI(srv.URL, "wrong")
	api.Pause = 0
	o := Options{Projects: ps, Out: t.TempDir(), Week: "2026-W40",
		Now: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), API: api, Log: io.Discard}
	if err := Run(o); err == nil || !goatcounter.IsStatus(err, 403) {
		t.Fatalf("wrong token: %v", err)
	}
	api.Token = "tok"
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.Out, "data/how-the-internet-works/weekly/2026-W40.json")); err != nil {
		t.Error(err)
	}
}

func TestRunSettingsDrift(t *testing.T) {
	ps, _ := project.LoadAll(projects.FS, "how-the-internet-works")
	for _, settings := range []string{
		`{"public":"private","data_retention":31,"collect":17,"allow_embed":""}`,
		`{"public":"private","data_retention":60,"collect":16,"allow_embed":""}`,
		`{"public":"public","data_retention":31,"collect":16,"allow_embed":""}`,
		`{"public":"private","data_retention":31,"collect":16,"allow_counter":true,"allow_embed":""}`,
		`{"public":"private","data_retention":31,"collect":16,"allow_embed":"example.org"}`,
	} {
		calls := 0
		srv := fakeGoatCounterSettings(t, "Etc/UTC", settings, &calls)
		_, origin, clone := gitRepos(t)
		head := run(t, "git", "--git-dir", origin, "rev-parse", "main")
		api := goatcounter.NewAPI(srv.URL, "tok")
		api.Pause = 0
		out := t.TempDir()
		for _, o := range []Options{{Repo: clone}, {Out: out}} {
			o.Projects, o.API, o.Log = ps, api, io.Discard
			o.Now = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
			var se *SettingsError
			if err := Run(o); !errors.As(err, &se) || len(se.Problems) != 1 {
				t.Errorf("%s: got %v", settings, err)
			}
		}
		srv.Close()
		if run(t, "git", "--git-dir", origin, "rev-parse", "main") != head {
			t.Errorf("%s: pushed something", settings)
		}
		if st := run(t, "git", "-C", clone, "status", "--porcelain"); st != "" || run(t, "git", "-C", clone, "rev-parse", "HEAD") != head {
			t.Errorf("%s: wrote or committed something: %q", settings, st)
		}
		if entries, _ := os.ReadDir(out); len(entries) != 0 {
			t.Errorf("%s: wrote %v", settings, entries)
		}
	}
}

// A failed run can leave changed and untracked files in the clone. They must
// not block later runs, even when GitHub has since changed or added the
// same files.
func TestRunLeftovers(t *testing.T) {
	calls := 0
	srv := fakeGoatCounter(t, "Etc/UTC", &calls)
	defer srv.Close()
	ps, _ := project.LoadAll(projects.FS, "how-the-internet-works")
	tmp, origin, clone := gitRepos(t)

	weekly := "data/how-the-internet-works/weekly/2026-W40.json"
	os.MkdirAll(filepath.Join(clone, filepath.Dir(weekly)), 0o755)
	os.WriteFile(filepath.Join(clone, weekly), []byte("left over\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "README.md"), []byte("left over\n"), 0o644)

	work := filepath.Join(tmp, "work")
	run(t, "git", "clone", "--quiet", origin, work)
	os.MkdirAll(filepath.Join(work, filepath.Dir(weekly)), 0o755)
	os.WriteFile(filepath.Join(work, weekly), []byte("published\n"), 0o644)
	os.WriteFile(filepath.Join(work, "README.md"), []byte("changed on GitHub\n"), 0o644)
	run(t, "git", "-C", work, "add", ".")
	run(t, "git", "-C", work, "-c", "user.name=x", "-c", "user.email=x@example.org", "commit", "--quiet", "-m", "remote")
	run(t, "git", "-C", work, "push", "--quiet", "origin", "main")
	head := run(t, "git", "--git-dir", origin, "rev-parse", "main")

	api := goatcounter.NewAPI(srv.URL, "tok")
	api.Pause = 0
	var log strings.Builder
	o := Options{Projects: ps, Repo: clone, Now: time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), API: api, Log: &log}
	if err := Run(o); err != nil {
		t.Fatalf("Run: %v\n%s", err, &log)
	}
	if !strings.Contains(log.String(), "already published") {
		t.Errorf("log: %s", &log)
	}
	// Only the settings file is new.
	if got := run(t, "git", "--git-dir", origin, "rev-parse", "main~1"); got != head {
		t.Errorf("origin is not one commit ahead of %s", head)
	}
	for file, want := range map[string]string{weekly: "published\n", "README.md": "changed on GitHub\n"} {
		if got := run(t, "git", "--git-dir", origin, "show", "main:"+file); got != want {
			t.Errorf("%s on origin: %q", file, got)
		}
		if got, _ := os.ReadFile(filepath.Join(clone, file)); string(got) != want {
			t.Errorf("%s in the clone: %q", file, got)
		}
	}
	if st := run(t, "git", "-C", clone, "status", "--porcelain"); st != "" {
		t.Errorf("clone not clean: %q", st)
	}
}
