//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSystemd runs in the second container, where systemd is PID 1: it
// installs the units from collector/systemd as the setup guide does, and
// checks GoatCounter under its real unit and the hourly restart. test/run.sh
// runs it with -test.run TestSystemd.
func TestSystemd(t *testing.T) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("not running under systemd (test/run.sh runs this in the systemd container)")
	}
	install(t)
	steps := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"unit", testUnit},
		{"restart-only-if-running", testRestartOnlyIfRunning},
		{"restart-clears-rate-limiter", testRestartClearsLimiter},
		{"counts-across-restarts", testCountsAcrossRestarts},
		{"export-cannot-delay-restart", testExportCannotDelayRestart},
		{"restart-schedule", testRestartSchedule},
	}
	for _, s := range steps {
		t.Run(s.name, s.fn)
	}
}

const (
	unitDir      = "/etc/systemd/system"
	timerDropIn  = unitDir + "/goatcounter-restart.timer.d/test.conf"
	gcDropIn     = unitDir + "/goatcounter.service.d/test.conf"
	testInterval = 20 * time.Second
)

func install(t *testing.T) {
	run(t, "install", "-m", "0755", "/repo/test/.bin/open-stats-linux-"+runtime.GOARCH, osBin)
	os.RemoveAll(optDir)
	run(t, "install", "-d", optDir)
	run(t, "cp", "-r", "/repo/collector", "/repo/projects", optDir+"/")
	units, _ := filepath.Glob(filepath.Join(optDir, "collector/systemd/*"))
	run(t, append(append([]string{"install", "-m", "0644"}, units...), unitDir+"/")...)
	run(t, "systemctl", "daemon-reload")
	run(t, "systemctl", "enable", "--now", "goatcounter.service")
	waitUp(t)
	out, err := runE(append(os.Environ(), "OPEN_STATS_ADMIN_PASSWORD=test-password-"+randomHex(8)),
		osBin, "configure", "-admin-email", "admin@example.invalid", "how-the-internet-works")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(indent(out))
	exportToken = envToken(t)
}

func waitUp(t *testing.T) {
	t.Helper()
	for range 50 {
		if resp, err := http.Get("http://127.0.0.1:8081/status"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("GoatCounter did not start")
}

func mainPID(t *testing.T) string {
	return strings.TrimSpace(run(t, "systemctl", "show", "-p", "MainPID", "--value", "goatcounter.service"))
}

func isActive(unit string) string {
	out, _ := runE(nil, "systemctl", "is-active", unit)
	return strings.TrimSpace(out)
}

// setTestTimer makes goatcounter-restart.timer fire every testInterval instead
// of hourly, and starts it; or, with on false, removes that and stops it.
func setTestTimer(t *testing.T, on bool) {
	if on {
		os.MkdirAll(filepath.Dir(timerDropIn), 0o755)
		os.WriteFile(timerDropIn, []byte(fmt.Sprintf("[Timer]\nOnCalendar=\nOnCalendar=*-*-* *:*:00/%d UTC\n", int(testInterval.Seconds()))), 0o644)
	} else {
		os.RemoveAll(filepath.Dir(timerDropIn))
	}
	run(t, "systemctl", "daemon-reload")
	if on {
		run(t, "systemctl", "restart", "goatcounter-restart.timer")
	} else {
		run(t, "systemctl", "stop", "goatcounter-restart.timer")
	}
}

// directCount sends one count straight to GoatCounter as nginx would, and
// returns the HTTP status, or 0 if the request failed.
func directCount(ip string) int {
	r, _ := http.NewRequest("GET", "http://127.0.0.1:8081/count?p=/en", nil)
	r.Host = "how-the-internet-works.localhost"
	r.Header.Set("X-Real-IP", ip)
	r.Header.Set("User-Agent", forwardUA)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(r)
	if err != nil {
		return 0
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func todayTotal(t *testing.T) int {
	today := time.Now().UTC().Format(time.DateOnly)
	return num(mustAPI(t, "GET", "how-the-internet-works.localhost",
		"/api/v0/stats/total?start="+today+"T00:00:00Z&end="+today+"T23:59:59Z", exportToken, nil)["total"])
}

func testUnit(t *testing.T) {
	check(t, isActive("goatcounter.service") == "active", "goatcounter.service runs with its sandboxing")
	env, _ := os.ReadFile("/proc/" + mainPID(t) + "/environ")
	check(t, strings.Contains("\x00"+string(env)+"\x00", "\x00GOMEMLIMIT=100MiB\x00"), "GoatCounter runs with GOMEMLIMIT=100MiB")
	for prop, want := range map[string]string{"MemoryHigh": "125829120", "MemoryMax": "157286400", "User": "goatcounter"} {
		got := strings.TrimSpace(run(t, "systemctl", "show", "-p", prop, "--value", "goatcounter.service"))
		check(t, got == want, "goatcounter.service %s=%s (got %s)", prop, want, got)
	}
}

func testRestartOnlyIfRunning(t *testing.T) {
	run(t, "systemctl", "stop", "goatcounter.service")
	run(t, "systemctl", "start", "goatcounter-restart.service")
	check(t, isActive("goatcounter.service") == "inactive", "goatcounter-restart.service leaves a stopped GoatCounter stopped")
	run(t, "systemctl", "start", "goatcounter.service")
	waitUp(t)
	pid := mainPID(t)
	run(t, "systemctl", "start", "goatcounter-restart.service")
	waitUp(t)
	check(t, mainPID(t) != pid, "goatcounter-restart.service restarts a running GoatCounter")
}

func testRestartClearsLimiter(t *testing.T) {
	// The limiter's keys can't be seen from outside, so make it bite: with a
	// test-only limit of 2 counts an hour per IP address, a third count from
	// the same address is refused until the timer restarts GoatCounter.
	unit, err := os.ReadFile(filepath.Join(unitDir, "goatcounter.service"))
	if err != nil {
		t.Fatal(err)
	}
	var exec []string
	in := false
	for _, ln := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(ln, "ExecStart=") {
			in = true
		}
		if in {
			exec = append(exec, ln)
			if !strings.HasSuffix(ln, "\\") {
				break
			}
		}
	}
	start := strings.Join(exec, "\n")
	if !strings.Contains(start, "-ratelimit=count:1000/1") {
		t.Fatalf("no -ratelimit=count:1000/1 in %s", start)
	}
	os.MkdirAll(filepath.Dir(gcDropIn), 0o755)
	os.WriteFile(gcDropIn, []byte("[Service]\nExecStart=\n"+strings.Replace(start, "count:1000/1", "count:2/3600", 1)+"\n"), 0o644)
	defer func() {
		os.RemoveAll(filepath.Dir(gcDropIn))
		run(t, "systemctl", "daemon-reload")
		run(t, "systemctl", "restart", "goatcounter.service")
		waitUp(t)
	}()
	run(t, "systemctl", "daemon-reload")
	run(t, "systemctl", "restart", "goatcounter.service")
	waitUp(t)

	const ip = "80.62.117.20"
	var codes []int
	for range 3 {
		codes = append(codes, directCount(ip))
	}
	check(t, codes[0] == 200 && codes[1] == 200 && codes[2] == 429,
		"test limit of 2 an hour: the third count from %s is refused (got %v)", ip, codes)

	pid := mainPID(t)
	setTestTimer(t, true)
	defer setTestTimer(t, false)
	restarted := waitFor(t, "the timer to restart GoatCounter", func() bool {
		p := mainPID(t)
		return p != pid && p != "0"
	})
	check(t, restarted, "goatcounter-restart.timer restarted GoatCounter")
	waitUp(t)
	code := directCount(ip)
	check(t, code == 200, "after the restart, %s can count again: its rate-limiter key is gone (got %d)", ip, code)
}

func testCountsAcrossRestarts(t *testing.T) {
	// 20 counts a second from different addresses for three timer intervals,
	// so the timer restarts GoatCounter at least twice in the middle.
	const rate = 20
	dur := 3*testInterval - 2*time.Second
	before := todayTotal(t)
	setTestTimer(t, true)
	defer setTestTimer(t, false)

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		ok    int
		fails []time.Time
		pids  = map[string]bool{mainPID(t): true}
	)
	stop, watched := make(chan struct{}), make(chan struct{})
	go func() { // watch for restarts
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				out, err := runE(nil, "systemctl", "show", "-p", "MainPID", "--value", "goatcounter.service")
				if p := strings.TrimSpace(out); err == nil && p != "0" {
					mu.Lock()
					pids[p] = true
					mu.Unlock()
				}
			}
		}
	}()
	tick := time.NewTicker(time.Second / rate)
	end := time.Now().Add(dur)
	for i := 0; time.Now().Before(end); i++ {
		<-tick.C
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sent := time.Now()
			code := directCount(fmt.Sprintf("11.1.%d.%d", i/250, i%250+1))
			mu.Lock()
			defer mu.Unlock()
			if code == 200 {
				ok++
			} else {
				fails = append(fails, sent)
			}
		}(i)
	}
	tick.Stop()
	wg.Wait()
	close(stop)
	<-watched
	setTestTimer(t, false)
	waitUp(t)
	restarts, failed := len(pids)-1, len(fails)
	// Refused counts, grouped by restart: how long each restart refused counts.
	slices.SortFunc(fails, func(a, b time.Time) int { return a.Compare(b) })
	var gaps []time.Duration
	for i, f := range fails {
		if i == 0 || f.Sub(fails[i-1]) > 2*time.Second {
			gaps = append(gaps, 0)
			start := f
			for _, g := range fails[i:] {
				if g.Sub(start) > 5*time.Second {
					break
				}
				gaps[len(gaps)-1] = g.Sub(start) + time.Second/rate
			}
		}
	}

	var after int
	waitFor(t, "GoatCounter to store the counts", func() bool {
		after = todayTotal(t)
		return after-before >= ok
	})
	time.Sleep(11 * time.Second) // one more store, in case more is counted than accepted
	after = todayTotal(t)
	t.Logf("sent %d at %d a second, %d accepted, %d refused while GoatCounter restarted %d times; counts refused for about %v per restart",
		ok+failed, rate, ok, failed, restarts, gaps)
	check(t, restarts >= 2, "the timer restarted GoatCounter at least twice (got %d)", restarts)
	check(t, after-before == ok, "every accepted count was stored, across the restarts: %d (got %d)", ok, after-before)
	check(t, failed <= restarts*rate, "at most one second of counts lost per restart: %d over %d restarts", failed, restarts)
}

func testRestartSchedule(t *testing.T) {
	setTestTimer(t, false)
	run(t, "systemctl", "enable", "--now", "goatcounter-restart.timer")
	cal := strings.TrimSpace(run(t, "systemctl", "show", "-p", "TimersCalendar", "--value", "goatcounter-restart.timer"))
	check(t, strings.Contains(cal, "OnCalendar=*-*-* *:30:00 UTC"), "the timer fires every hour at half past (%s)", cal)
	next := strings.TrimSpace(run(t, "systemctl", "show", "-p", "NextElapseUSecRealtime", "--value", "goatcounter-restart.timer"))
	at, err := time.Parse("Mon 2006-01-02 15:04:05 MST", next)
	in := time.Until(at)
	check(t, err == nil && in > 0 && in <= time.Hour, "next restart within the hour (%s, in %v)", next, in.Round(time.Second))
	acc := strings.TrimSpace(run(t, "systemctl", "show", "-p", "AccuracyUSec", "--value", "goatcounter-restart.timer"))
	check(t, acc == "1s", "timer accuracy 1s (got %s)", acc)
}

// testExportCannotDelayRestart starts the export with a remote that never
// answers (a fake ssh that hangs, as a stalled connection to GitHub would),
// and checks that the hourly restart still happens at once, and that the
// export fails by itself within its git deadline.
func testExportCannotDelayRestart(t *testing.T) {
	const (
		repo     = "/var/lib/open-stats/repo"
		fakeSSH  = "/usr/local/bin/hang-ssh"
		exportIn = unitDir + "/open-stats-export.service.d/test.conf"
		deadline = 5 * time.Second
	)
	got := strings.TrimSpace(run(t, "systemctl", "show", "-p", "TimeoutStartUSec", "--value", "open-stats-export.service"))
	check(t, got == "15min", "open-stats-export.service TimeoutStartSec=15min (got %s)", got)
	order := run(t, "systemctl", "show", "-p", "After", "--value", "goatcounter-restart.service")
	check(t, !strings.Contains(order, "open-stats-export"), "goatcounter-restart.service isn't ordered after the export (After=%s)", strings.TrimSpace(order))

	os.RemoveAll(repo)
	run(t, "runuser", "-u", "open-stats", "--", "git", "init", "--quiet", "-b", "main", repo)
	run(t, "runuser", "-u", "open-stats", "--", "git", "-C", repo, "remote", "add", "origin", "ssh://fake/srv/origin.git")
	os.WriteFile(fakeSSH, []byte("#!/bin/sh\nsleep 600\n"), 0o755)
	os.MkdirAll(filepath.Dir(exportIn), 0o755)
	os.WriteFile(exportIn, []byte(fmt.Sprintf("[Service]\nEnvironment=GIT_SSH_COMMAND=%s GIT_SSH_VARIANT=simple\n"+
		"ExecStart=\nExecStart=%s export --repo %s --git-timeout %s\n", fakeSSH, osBin, repo, deadline)), 0o644)
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(exportIn))
		os.RemoveAll(repo)
		os.Remove(fakeSSH)
		runE(nil, "systemctl", "daemon-reload")
		runE(nil, "systemctl", "reset-failed", "open-stats-export.service")
	})
	run(t, "systemctl", "daemon-reload")

	start := time.Now()
	run(t, "systemctl", "start", "--no-block", "open-stats-export.service")
	if !check(t, waitFor(t, "the export to hang on its remote", func() bool {
		_, err := runE(nil, "pgrep", "-f", fakeSSH)
		return err == nil
	}), "the export is waiting on a remote that never answers") {
		return
	}

	before := mainPID(t)
	t0 := time.Now()
	_, err := runE(nil, "timeout", "30", "systemctl", "start", "goatcounter-restart.service")
	took := time.Since(t0)
	exporting := isActive("open-stats-export.service")
	after := mainPID(t)
	check(t, err == nil && took < 5*time.Second, "the restart ran while the export hung, in %v (%v)", took.Round(time.Millisecond), err)
	check(t, exporting == "activating", "the export was still running during the restart (%s)", exporting)
	check(t, after != before && after != "0", "GoatCounter restarted (MainPID %s -> %s)", before, after)
	waitUp(t)

	waitFor(t, "the export to give up", func() bool { return isActive("open-stats-export.service") != "activating" })
	took = time.Since(start)
	result := strings.TrimSpace(run(t, "systemctl", "show", "-p", "Result", "--value", "open-stats-export.service"))
	log := run(t, "journalctl", "-u", "open-stats-export.service", "--since", "@"+fmt.Sprint(start.Unix()-1), "-o", "cat")
	check(t, result == "exit-code", "the export failed (Result=%s)", result)
	check(t, strings.Contains(log, "timed out after "+deadline.String()), "it failed because git timed out:\n%s", indent(log))
	check(t, took < deadline+15*time.Second, "within its git deadline (%v after it started)", took.Round(time.Second))
	_, err = runE(nil, "pgrep", "-f", fakeSSH)
	check(t, err != nil, "nothing the export started is left running")
}
