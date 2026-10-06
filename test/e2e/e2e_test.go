//go:build e2e

// End-to-end checks, run inside the test VPS container by test/run.sh as
// root (see test/README.md).
//
// They talk to nginx over TLS on 127.0.0.1 and ::1 exactly like a browser
// would, and look at what reaches the upstream: first a recording stand-in
// to see the exact forwarded requests, then the real GoatCounter to see what
// it counts and stores. Then they run `open-stats configure`, `open-stats
// export` (into a local bare git repository, as the systemd unit does) and
// `open-stats site` on the exported data.
package e2e

import (
	"crypto/tls"
	"debug/buildinfo"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/internal/dataformat"
	"github.com/tkjaer/open-stats/internal/isoweek"
)

func TestE2E(t *testing.T) {
	gcctl(t, "stop") // the recording stand-in takes GoatCounter's port first
	steps := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"binary", testBinary},
		{"forwarding", testForwarding},
		{"rate-limits", testRateLimits},
		{"start-goatcounter", func(t *testing.T) { gcctl(t, "start") }},
		{"configure", testConfigure},
		{"counting", testCounting},
		{"goatcounter-internals", testGoatCounterInternals},
		{"export", testExport},
		{"site", testSite},
		{"logs", testLogs},
		{"units", testUnits},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.fn) && (s.name == "configure" || s.name == "start-goatcounter") {
			t.Fatal("cannot continue")
		}
	}
}

func testBinary(t *testing.T) {
	f, err := elf.Open(osBin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	static := true
	for _, p := range f.Progs {
		static = static && p.Type != elf.PT_INTERP && p.Type != elf.PT_DYNAMIC
	}
	check(t, static, "%s is a static binary", osBin)
	out := run(t, osBin, "version")
	check(t, strings.HasPrefix(out, "open-stats "), "open-stats version: %s", strings.TrimSpace(out))

	bi, err := buildinfo.ReadFile(gcBin)
	if err != nil {
		t.Fatal(err)
	}
	isbot := "(none)"
	for _, d := range bi.Deps {
		if d.Path == "zgo.at/isbot" && d.Replace == nil {
			isbot = d.Version
		}
	}
	check(t, isbot == isbotChecked, "GoatCounter is built with isbot %s, whose bot detection collector/README.md describes (got %s)",
		isbotChecked, isbot)
}

// isbotChecked is the version of GoatCounter's bot detection library that
// collector/README.md ("Bot detection") was checked against.
const isbotChecked = "v1.0.0"

// cloudIPs are in cloud and hosting ranges that isbot knows (AWS, Digital
// Ocean, servers.com, Google Cloud, Hetzner).
var cloudIPs = []string{"35.180.0.1", "2600:1f15::1", "104.131.0.1", "2604:a880::1", "142.234.32.1",
	"104.154.0.1", "2600:1900::1", "116.202.0.1"}

func testForwarding(t *testing.T) {
	cap := startCapture(t, 0)
	// Valid requests, over IPv4 and IPv6, with every header GoatCounter
	// could take a client IP from set to something else.
	spoof := [][2]string{
		{"X-Forwarded-For", "8.8.8.8"}, {"X-Real-IP", "8.8.8.8"}, {"Cf-Connecting-Ip", "8.8.8.8"},
		{"Fly-Client-Ip", "8.8.8.8"}, {"X-Azure-Socketip", "8.8.8.8"}, {"Forwarded", "for=8.8.8.8"},
		{"Referer", "https://tkjaer.github.io/how-the-internet-works/secret-page"},
		{"Cookie", "a=b"}, {"DNT", "0"}, {"Sec-GPC", "0"},
	}
	for _, c := range []struct {
		target, ip, addr string
		extra            [][2]string
		want             string
	}{
		{"/how-the-internet-works/count?lang=en", "203.0.113.5", "127.0.0.1", nil, "/count?p=/en"},
		{"/how-the-internet-works/count?lang=da", "203.0.113.6", "127.0.0.1", nil, "/count?p=/da"},
		{"/how-the-internet-works/count?lang=en", "203.0.113.7", "127.0.0.1", spoof, "/count?p=/en"},
		{"/how-the-internet-works/count?lang=da", "", "::1", nil, "/count?p=/da"},
	} {
		r := send(t, req{target: c.target, ip: c.ip, addr: c.addr, headers: c.extra})
		got := cap.take(400 * time.Millisecond)
		client := c.ip
		if client == "" {
			client = c.addr
		}
		label := c.target + " from " + client
		if c.extra != nil {
			label += " with spoofed headers"
		}
		check(t, r.silent204(500*time.Millisecond), "%s: empty 204 (%s)", label, r)
		if !check(t, len(got) == 1, "%s: forwarded once (got %d)", label, len(got)) {
			continue
		}
		g := got[0]
		check(t, g.method == "GET" && g.uri == c.want, "%s: forwarded as GET %s (got %s %s)", label, c.want, g.method, g.uri)
		var names []string
		for k := range g.headers {
			names = append(names, k)
		}
		only := true
		for _, k := range names {
			only = only && (k == "host" || k == "x-real-ip" || k == "user-agent" || k == "connection")
		}
		check(t, only, "%s: only Host, X-Real-IP, User-Agent, Connection forwarded (got %v)", label, names)
		check(t, g.headers["host"] == "how-the-internet-works.localhost", "%s: Host how-the-internet-works.localhost (got %s)", label, g.headers["host"])
		check(t, g.headers["x-real-ip"] == client, "%s: X-Real-IP is the client IP (got %s)", label, g.headers["x-real-ip"])
		check(t, g.headers["user-agent"] == forwardUA, "%s: fixed User-Agent, not the browser's", label)
		check(t, g.body == "", "%s: no body forwarded", label)
	}

	t.Log("-- everything else is answered with an empty 204 and dropped")
	for _, target := range []string{
		"/how-the-internet-works/count?lang=fr", "/how-the-internet-works/count?lang=EN", "/how-the-internet-works/count?lang=En", "/how-the-internet-works/count?lang=en&x=1",
		"/how-the-internet-works/count?x=1&lang=en", "/how-the-internet-works/count?lang=da&lang=en", "/how-the-internet-works/count?lang=en&",
		"/how-the-internet-works/count?lang=en%00", "/how-the-internet-works/count?lang=%65n", "/how-the-internet-works/count?lang=en%0d%0aX-Real-IP:%201.1.1.1",
		"/how-the-internet-works/count?lang=", "/how-the-internet-works/count?lang", "/how-the-internet-works/count?", "/how-the-internet-works/count", "/how-the-internet-works/count/?lang=en",
		"/how-the-internet-works/count?lang=en/../../x", "/How-The-Internet-Works/count?lang=en", "/how-the-internet-works//count?lang=en", "//how-the-internet-works/count?lang=en",
		"/how-the-internet-works/./count?lang=en", "/x/../how-the-internet-works/count?lang=en", "/%68ow-the-internet-works/count?lang=en", "/how-the-internet-works/count;x?lang=en",
		"/how-the-internet-works/count?LANG=en", "/nope/count?lang=en", "/how-the-internet-work/count?lang=en", "/count?p=/en", "/count",
		"/_openstats_upstream", "/_openstats_204", "/", "/favicon.ico", "/robots.txt", "/.env",
		"/how-the-internet-works/count?lang=" + strings.Repeat("e", 5000), "/how-the-internet-works/count?lang=en%20", "/how-the-internet-works/count?lang=en#x",
	} {
		r := send(t, req{target: target, ip: freshIP()})
		got := cap.take(50 * time.Millisecond)
		check(t, r.silent204(500*time.Millisecond) && len(got) == 0, "%.60s: empty 204, not forwarded (%s; forwarded %d)",
			target, r, len(got))
	}
	for _, method := range []string{"POST", "HEAD", "PUT", "DELETE", "OPTIONS", "PATCH", "TRACE", "CONNECT", "PROPFIND"} {
		var body []byte
		if method == "POST" || method == "PUT" || method == "PATCH" {
			body = []byte(`{"p":"/en"}`)
		}
		r := send(t, req{method: method, ip: freshIP(), body: body})
		got := cap.take(50 * time.Millisecond)
		check(t, r.status == 204 && len(r.body) == 0 && len(got) == 0,
			"%s /how-the-internet-works/count?lang=en: 204, not forwarded (%s; forwarded %d)", method, r, len(got))
	}
	for _, c := range []struct {
		headers [][2]string
		drop    []string
	}{
		{headers: [][2]string{{"Sec-GPC", "1"}}}, {headers: [][2]string{{"DNT", "1"}}},
		{headers: [][2]string{{"Sec-GPC", "1"}, {"DNT", "1"}}},
		{drop: []string{"User-Agent"}}, {headers: [][2]string{{"User-Agent", ""}}},
		{headers: [][2]string{{"User-Agent", "curl/8.5.0"}}},
		{headers: [][2]string{{"User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}}},
		{headers: [][2]string{{"User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 HeadlessChrome/131.0 Safari/537.36"}}},
		{headers: [][2]string{{"User-Agent", "python-requests/2.32"}}},
		{headers: [][2]string{{"Purpose", "prefetch"}}}, {headers: [][2]string{{"Sec-Purpose", "prefetch"}}},
		{headers: [][2]string{{"Sec-Purpose", "prefetch;prerender"}}}, {headers: [][2]string{{"X-Moz", "prefetch"}}},
		{headers: [][2]string{{"X-Purpose", "preview"}}},
	} {
		r := send(t, req{ip: freshIP(), headers: c.headers, drop: c.drop})
		got := cap.take(50 * time.Millisecond)
		check(t, r.silent204(500*time.Millisecond) && len(got) == 0,
			"valid request with %v (without %v): empty 204, not forwarded (%s; forwarded %d)", c.headers, c.drop, r, len(got))
	}
	// Not even valid HTTP.
	for _, raw := range []string{
		"GARBAGE\r\n\r\n",
		"GET /how-the-internet-works/count?lang=en HTTP/9.9\r\nHost: stats.irq.dk\r\n\r\n",
		"GET /how-the-internet-works/count?lang=en HTTP/1.1\r\nHost: stats.irq.dk\r\nX-Big: " + strings.Repeat("a", 20000) + "\r\n\r\n",
	} {
		r := send(t, req{raw: []byte(raw)})
		got := cap.take(50 * time.Millisecond)
		check(t, len(got) == 0 && r.status == 204 && len(r.body) == 0,
			"malformed request %q: no body, not forwarded (%s)", raw[:min(len(raw), 40)], r)
	}
	// A request with no Host, or another site's, isn't one for stats.irq.dk even
	// over a TLS connection made to that name: nginx gives it to the default
	// site for port 443 (see collector/README.md). Browsers never send these.
	for _, c := range []req{
		{drop: []string{"Host"}}, {headers: [][2]string{{"Host", "other.example"}}},
	} {
		c.ip = otherSiteIP
		r := send(t, c)
		got := cap.take(50 * time.Millisecond)
		check(t, len(got) == 0 && r.status != 204, "Host %v: handled by the default site, not forwarded (%s)", c.headers, r)
	}
	cap.stop()

	t.Log("-- a slow or stopped upstream never delays or changes the answer")
	slow := startCapture(t, 5*time.Second)
	r := send(t, req{ip: freshIP()})
	check(t, r.silent204(300*time.Millisecond), "upstream answering after 5 s: empty 204 at once (%s)", r)
	check(t, len(slow.take(300*time.Millisecond)) == 1, "the slow upstream still got the request")
	slow.stop()
	r = send(t, req{ip: freshIP()})
	check(t, r.silent204(300*time.Millisecond), "upstream down: empty 204 at once (%s)", r)
}

func testRateLimits(t *testing.T) {
	t.Log("no per-IP limit; one global cap (10/s, burst 200)")
	time.Sleep(21 * time.Second) // let the global bucket refill after the tests above (200 at 10/s)
	cap := startCapture(t, 0)
	defer cap.stop()
	all204 := true
	for range 60 {
		all204 = send(t, req{ip: "198.51.100.77"}).silent204(500*time.Millisecond) && all204
	}
	got := cap.take(400 * time.Millisecond)
	check(t, all204, "all 60 answered with an empty 204")
	check(t, len(got) == 60, "60 quick requests from one IP (a class behind one NAT): all forwarded (got %d)", len(got))

	time.Sleep(21 * time.Second)
	start := time.Now()
	all204 = true
	for range 300 {
		all204 = send(t, req{ip: freshIP()}).silent204(500*time.Millisecond) && all204
	}
	took := time.Since(start)
	got = cap.take(time.Second)
	most := 201 + int(10*took.Seconds()) + 1 // 200 burst + 1, plus 10 per second while sending
	check(t, all204, "all 300 answered with an empty 204")
	check(t, len(got) >= 201 && len(got) <= most, "300 requests from 300 IPs in %.1f s: between 201 and %d forwarded (got %d)",
		took.Seconds(), most, len(got))
}

var exportToken string

func testConfigure(t *testing.T) {
	// A second project next to how-the-internet-works, to check that adding projects works.
	dir := "/tmp/projects"
	os.MkdirAll(dir, 0o755)
	hiw, err := os.ReadFile(filepath.Join(optDir, "projects/how-the-internet-works.yml"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "how-the-internet-works.yml"), hiw, 0o644)
	demo := strings.NewReplacer("slug: how-the-internet-works", "slug: demo", "vhost: how-the-internet-works.localhost", "vhost: demo.localhost",
		"path: /how-the-internet-works/count", "path: /demo/count").Replace(string(hiw))
	os.WriteFile(filepath.Join(dir, "demo.yml"), []byte(demo), 0o644)
	env := append(os.Environ(), "OPEN_STATS_ADMIN_PASSWORD=test-password-"+randomHex(8))
	for _, slug := range []string{"how-the-internet-works", "demo", "how-the-internet-works"} {
		out, err := runE(env, osBin, "configure", "-projects", dir, "-admin-email", "admin@example.invalid", slug)
		t.Log(indent(out))
		check(t, err == nil, "open-stats configure %s succeeded (%v)", slug, err)
	}

	fi, err := os.Stat(envFile)
	check(t, err == nil && fi.Mode().Perm() == 0o600, "%s is mode 600", envFile)
	exportToken = envToken(t)
	check(t, regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(exportToken), "export token written")
	rows := gcQuery(t, "select name, permissions, sites from api_tokens")
	check(t, len(rows) == 1, "exactly one API token left (temporary ones deleted): %v", rows)
	sites := gcQuery(t, "select site_id, cname, parent, cast(settings as text) as settings from sites order by site_id")
	var cnames []string
	var ids []int
	for _, s := range sites {
		cnames = append(cnames, s["cname"].(string))
		ids = append(ids, num(s["site_id"]))
	}
	check(t, reflect.DeepEqual(cnames, []string{"how-the-internet-works.localhost", "demo.localhost"}), "two sites: %v", cnames)
	if len(rows) == 1 && len(sites) == 2 {
		check(t, num(rows[0]["permissions"]) == 72, "token can read stats and sites, nothing else (72): %v", rows[0]["permissions"])
		var tokSites []int
		json.Unmarshal([]byte(rows[0]["sites"].(string)), &tokSites)
		check(t, reflect.DeepEqual(tokSites, ids), "token covers both sites: %v", tokSites)
	}
	for _, s := range sites {
		var cfg struct {
			Collect       int    `json:"collect"`
			DataRetention int    `json:"data_retention"`
			Public        string `json:"public"`
		}
		json.Unmarshal([]byte(s["settings"].(string)), &cfg)
		check(t, cfg.Collect == 16 && cfg.DataRetention == 31 && cfg.Public == "private",
			"%s: collect 16 (country only), retention 31 days, private (got %+v)", s["cname"], cfg)
	}
	for _, host := range []string{"how-the-internet-works.localhost", "demo.localhost"} {
		_, err := api("GET", host, "/api/v0/stats/total?start=2026-01-01T00:00:00Z&end=2026-01-01T23:59:59Z", exportToken, nil)
		check(t, err == nil, "export token can read %s (%v)", host, err)
	}
	_, err = api("POST", "how-the-internet-works.localhost", "/api/v0/count", exportToken,
		map[string]any{"no_sessions": true, "hits": []any{map[string]any{"path": "/en"}}})
	var ae *apiError
	check(t, errors.As(err, &ae) && (ae.status == 401 || ae.status == 403), "export token cannot count (%v)", err)
}

func testCounting(t *testing.T) {
	today := time.Now().UTC().Format(time.DateOnly)
	for _, c := range []struct {
		lang, ip string
		extra    [][2]string
	}{
		{"en", "8.8.8.8", nil}, {"en", "8.8.8.8", nil}, {"da", "193.0.6.139", nil},
		{"en", "130.225.0.1", [][2]string{{"X-Forwarded-For", "8.8.8.8"}, {"X-Real-IP", "8.8.8.8"}, {"Cf-Connecting-Ip", "8.8.8.8"}}},
	} {
		r := send(t, req{target: "/how-the-internet-works/count?lang=" + c.lang, ip: c.ip, headers: c.extra})
		check(t, r.silent204(500*time.Millisecond), "lang=%s from %s: empty 204", c.lang, c.ip)
		// nginx answers before GoatCounter does, and GoatCounter v2.7.0 records
		// "unknown" when two counts from a country it hasn't seen before
		// race to add that country (locations.go, Lookup), so space them out.
		time.Sleep(300 * time.Millisecond)
	}
	for _, target := range []string{"/how-the-internet-works/count?lang=fr", "/how-the-internet-works/count?lang=en&x=1", "/demo/count?lang=en", "/how-the-internet-works/count/?lang=en"} {
		send(t, req{target: target, ip: "8.8.4.4"})
	}
	send(t, req{ip: "8.8.4.4", headers: [][2]string{{"Sec-GPC", "1"}}})
	send(t, req{target: "/how-the-internet-works/count?lang=da", ip: "8.8.4.4", headers: [][2]string{{"DNT", "1"}}})

	q := "start=" + today + "T00:00:00Z&end=" + today + "T23:59:59Z"
	var total map[string]any
	waitFor(t, "GoatCounter to store the hits", func() bool {
		total = mustAPI(t, "GET", "how-the-internet-works.localhost", "/api/v0/stats/total?"+q, exportToken, nil)
		return num(total["total"]) >= 4
	})
	check(t, num(total["total"]) == 4, "4 page loads counted today (got %v)", total["total"])
	hits := mustAPI(t, "GET", "how-the-internet-works.localhost", "/api/v0/stats/hits?"+q+"&daily=true", exportToken, nil)
	paths := map[string]int{}
	for _, h := range hits["hits"].([]any) {
		h := h.(map[string]any)
		paths[h["path"].(string)] = num(h["count"])
	}
	check(t, reflect.DeepEqual(paths, map[string]int{"/en": 3, "/da": 1}), "paths /en 3, /da 1 (got %v)", paths)
	locs := mustAPI(t, "GET", "how-the-internet-works.localhost", "/api/v0/stats/locations?"+q, exportToken, nil)
	countries := map[string]int{}
	for _, s := range locs["stats"].([]any) {
		s := s.(map[string]any)
		countries[s["id"].(string)] = num(s["count"])
	}
	check(t, reflect.DeepEqual(countries, map[string]int{"US": 2, "NL": 1, "DK": 1}),
		"countries from the client IP, spoofed headers ignored: US 2, NL 1, DK 1 (got %v)", countries)
	demo := mustAPI(t, "GET", "demo.localhost", "/api/v0/stats/total?"+q, exportToken, nil)
	check(t, num(demo["total"]) == 0, "nothing counted for the demo project (not in nginx's allow-list): %v", demo["total"])

	t.Log("-- GoatCounter stores no IPs, User-Agents, individual pageviews or bot records")
	check(t, num(gcQuery(t, "select count(*) as n from hits")[0]["n"]) == 0, "no individual pageviews stored")
	check(t, num(gcQuery(t, "select count(*) as n from bots")[0]["n"]) == 0, "no bot records")
	var all []string
	for _, r := range gcQuery(t, "select path from paths order by path") {
		all = append(all, r["path"].(string))
	}
	check(t, reflect.DeepEqual(all, []string{"/da", "/en"}), "only /da and /en were ever recorded (paths: %v)", all)
	gcctl(t, "stop") // flush everything to disk
	var blob []byte
	entries, _ := os.ReadDir("/var/lib/goatcounter")
	for _, e := range entries {
		if e.Type().IsRegular() {
			b, _ := os.ReadFile(filepath.Join("/var/lib/goatcounter", e.Name()))
			blob = append(blob, b...)
		}
	}
	b, _ := os.ReadFile("/var/log/goatcounter.log")
	blob = append(blob, b...)
	for _, needle := range []string{"8.8.8.8", "8.8.4.4", "193.0.6.139", "130.225.0.1", "Firefox/131", "open-stats-nginx", "Linux x86_64"} {
		check(t, !strings.Contains(string(blob), needle), "%q appears nowhere in GoatCounter's database or log", needle)
	}
	r := send(t, req{ip: freshIP()})
	check(t, r.silent204(300*time.Millisecond), "GoatCounter stopped: still an empty 204 at once (%s)", r)
	gcctl(t, "start")
}

// countDirect sends a count straight to GoatCounter, the way nginx does but
// with a path or User-Agent that nginx never sends.
func countDirect(t *testing.T, path, ip, ua string) {
	r, _ := http.NewRequest("GET", "http://127.0.0.1:8081/count?p="+path, nil)
	r.Host = "how-the-internet-works.localhost"
	r.Header.Set("X-Real-IP", ip)
	r.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// testGoatCounterInternals checks what GoatCounter logs on errors and what
// its bot detection does with the requests nginx forwards.
func testGoatCounterInternals(t *testing.T) {
	t.Log("-- an error while processing a count is logged without the IP address")
	// GoatCounter v2.7.0 panics on the path "/web/20" (it slices it as an
	// Internet Archive URL) and logs the whole count it was processing. The
	// address must be public: GoatCounter ignores X-Real-IP otherwise.
	const ip = "80.62.117.12"
	panicLine := func(logFile string) string {
		var line string
		waitFor(t, "GoatCounter to process the count", func() bool {
			b, _ := os.ReadFile(logFile)
			if i := strings.Index(string(b), "slice bounds out of range"); i >= 0 {
				line = string(b[strings.LastIndexByte(string(b[:i]), '\n')+1:])
				return true
			}
			return false
		})
		return line
	}
	countDirect(t, "/web/20", ip, forwardUA)
	jsonLog := panicLine("/var/log/goatcounter.log")
	jsonLog = jsonLog[:strings.IndexByte(jsonLog+"\n", '\n')]
	var entry struct {
		Hit map[string]any `json:"hit"`
	}
	check(t, json.Unmarshal([]byte(jsonLog), &entry) == nil && entry.Hit["p"] == "/web/20",
		"the error is logged as JSON, with the count: %.200s", jsonLog)
	b, _ := os.ReadFile("/var/log/goatcounter.log")
	check(t, !strings.Contains(string(b), ip), "%s does not appear in GoatCounter's log (count logged as %v)", ip, entry.Hit)

	// Control: the same without -json logs the address.
	const textLog = "/tmp/goatcounter-text.log"
	gcctl(t, "stop")
	os.Remove(textLog)
	if _, err := runE(append(os.Environ(), "GCCTL_DROP_FLAG=-json", "GCCTL_LOG="+textLog), "gcctl", "start"); err != nil {
		t.Fatal(err)
	}
	countDirect(t, "/web/20", ip, forwardUA)
	panicLine(textLog)
	gcctl(t, "stop")
	b, _ = os.ReadFile(textLog)
	check(t, !strings.HasPrefix(string(b), "{") && strings.Contains(string(b), " "+ip+" "),
		"control: without -json, GoatCounter logs %s with the count", ip)
	os.Remove(textLog)
	gcctl(t, "start")

	today := time.Now().UTC().Format(time.DateOnly)
	q := "/api/v0/stats/total?start=" + today + "T00:00:00Z&end=" + today + "T23:59:59Z"
	total := func() int {
		return num(mustAPI(t, "GET", "how-the-internet-works.localhost", q, exportToken, nil)["total"])
	}

	t.Log("-- many counts at once from one IP address are all counted (-ratelimit)")
	// GoatCounter's own default limit is 4 counts a second per IP address and
	// User-Agent, and nginx sends one fixed User-Agent.
	const natIP = "80.62.117.13"
	burst := func() int {
		before := total()
		for range 12 {
			send(t, req{ip: natIP})
		}
		gcctl(t, "stop") // stores all counts
		gcctl(t, "start")
		return total() - before
	}
	n := burst()
	check(t, n == 12, "12 counts from %s within a second: 12 counted (got %d)", natIP, n)
	gcctl(t, "stop")
	if _, err := runE(append(os.Environ(), "GCCTL_DROP_FLAG=-ratelimit=count:1000/1"), "gcctl", "start"); err != nil {
		t.Fatal(err)
	}
	n = burst() // restarts GoatCounter with the unit's flags
	check(t, n < 12, "control: without the -ratelimit flag, %d of the 12 are counted", n)

	t.Log("-- requests from cloud and hosting ranges are counted, not recorded as bots")
	before := total()
	for _, ip := range cloudIPs {
		send(t, req{ip: ip})
	}
	var after int
	waitFor(t, "GoatCounter to store the hits", func() bool {
		after = num(mustAPI(t, "GET", "how-the-internet-works.localhost", q, exportToken, nil)["total"])
		return after >= before+len(cloudIPs)
	})
	check(t, after == before+len(cloudIPs), "%d requests from %v counted (got %d)", len(cloudIPs), cloudIPs, after-before)
	check(t, num(gcQuery(t, "select count(*) as n from bots")[0]["n"]) == 0, "no bot records")

	// For one of the few User-Agents that isbot checks the IP for, GoatCounter
	// does record it as a bot. nginx never sends that User-Agent.
	countDirect(t, "/en", cloudIPs[0], "Mozilla/5.0 (Linux; Android 10; CUBOT_X30) AppleWebKit/537.36 Chrome/131.0 Mobile Safari/537.36")
	waitFor(t, "GoatCounter to record the bot", func() bool {
		return num(gcQuery(t, "select count(*) as n from bots")[0]["n"]) > 0
	})
	bots := gcQuery(t, "select path, bot from bots")
	check(t, len(bots) == 1, "control: a request sent around nginx with that User-Agent is in the bots table (got %v)", bots)
	gcExec(t, "delete from bots")
}

// seed puts one complete week into GoatCounter through its API and returns
// the week and the tables the export should publish for it (worked out by
// hand here).
func seed(t *testing.T) (string, []time.Time, dataformat.Tables) {
	today := isoweek.DayOf(time.Now())
	week := isoweek.LastComplete(today)
	d, _ := isoweek.Days(week)

	token, done := tempToken(t, "test seed", 2)

	var hits []any
	add := func(day time.Time, path, country string, n int, at string) {
		for range n {
			h := map[string]any{"path": path, "created_at": day.Format(time.DateOnly) + "T" + at + "Z"}
			if country != "" {
				h["location"] = country
			} else {
				h["ip"] = "10.1.2.3" // private: no country
			}
			hits = append(hits, h)
		}
	}
	// Days with fewer than 20 page loads publish only their total.
	// Mon (20): DE 10, DK 5, SE 4 (under 5), 1 unknown. -> DE 10, DK 5, other 5
	add(d[0], "/en", "DE", 10, "12:00:00")
	add(d[0], "/da", "DK", 5, "12:00:00")
	add(d[0], "/en", "SE", 4, "12:00:00")
	add(d[0], "/en", "", 1, "12:00:00")
	// Tue: nothing. Wed (5): all from DK, one in Danish: without the quiet-day
	// rule, the tables together would single out that visit. -> total only
	add(d[2], "/en", "DK", 4, "12:00:00")
	add(d[2], "/da", "DK", 1, "12:00:00")
	// Thu (19): FR 18 + 1.                               -> total only
	add(d[3], "/en", "FR", 18, "12:00:00")
	add(d[3], "/da", "FR", 1, "12:00:00")
	// Fri (21): DE 9, FR 8 (en 4 + da 4), NO 4.          -> DE 9, FR 8, other 4
	add(d[4], "/en", "DE", 9, "12:00:00")
	add(d[4], "/en", "FR", 4, "12:00:00")
	add(d[4], "/da", "FR", 4, "12:00:00")
	add(d[4], "/en", "NO", 4, "12:00:00")
	// Sat (20): DK 17, US 2, plus a path outside the allow-list (only possible
	// if nginx were misconfigured): language "other".   -> DK 17, other 3
	add(d[5], "/en", "DK", 17, "12:00:00")
	add(d[5], "/en", "US", 2, "12:00:00")
	add(d[5], "/xx", "US", 1, "12:00:00")
	// Sun (1): DK at 23:59:30, plus hits just outside the week on both sides.
	add(d[6], "/en", "DK", 1, "23:59:30")
	add(d[0].AddDate(0, 0, -1), "/en", "DK", 3, "23:59:59")
	add(d[6].AddDate(0, 0, 1), "/en", "DK", 3, "00:00:01")
	for i := 0; i < len(hits); i += 50 {
		mustAPI(t, "POST", "how-the-internet-works.localhost", "/api/v0/count", token,
			map[string]any{"no_sessions": true, "hits": hits[i:min(i+50, len(hits))]})
	}
	done()

	day := func(i int) string { return d[i].Format(time.DateOnly) }
	pl := []int{20, 0, 5, 19, 21, 20, 1}
	lang := map[int]map[string]int{
		0: {"en": 15, "da": 5, "other": 0}, 4: {"en": 17, "da": 4, "other": 0}, 5: {"en": 19, "da": 0, "other": 1},
	}
	country := map[int]map[string]int{
		0: {"DE": 10, "DK": 5, "other": 5}, 4: {"DE": 9, "FR": 8, "other": 4}, 5: {"DK": 17, "other": 3},
	}
	want := dataformat.Tables{
		PageLoads: dataformat.PageLoads{Granularity: "day"},
		Language:  dataformat.Breakdown{Granularity: "day", MinDayTotal: 20, Rows: []dataformat.DayCounts{}},
		Country:   dataformat.Countries{Granularity: "day", MinDayTotal: 20, MinCount: 5, Rows: []dataformat.DayCounts{}},
	}
	for i := range 7 {
		want.PageLoads.Rows = append(want.PageLoads.Rows, dataformat.DayCount{Day: day(i), Count: pl[i]})
		if lang[i] != nil {
			want.Language.Rows = append(want.Language.Rows, dataformat.DayCounts{Day: day(i), Counts: lang[i]})
			want.Country.Rows = append(want.Country.Rows, dataformat.DayCounts{Day: day(i), Counts: country[i]})
		}
	}
	return week, d, want
}

func exportEnv(token string) []string {
	return append(os.Environ(), "GOATCOUNTER_TOKEN="+token, "GOATCOUNTER_URL=http://127.0.0.1:8081")
}

func testExport(t *testing.T) {
	week, days, want := seed(t)
	waitFor(t, "the seeded hits to be stored", func() bool {
		total := mustAPI(t, "GET", "how-the-internet-works.localhost", "/api/v0/stats/total?start="+days[0].Format(time.DateOnly)+
			"T00:00:00Z&end="+days[6].Format(time.DateOnly)+"T23:59:59Z", exportToken, nil)
		return num(total["total"]) >= 86
	})

	out := "/tmp/export-out"
	os.RemoveAll(out)
	res, err := runE(exportEnv(exportToken), osBin, "export", "-out", out, "-project", "how-the-internet-works", "-week", week)
	t.Log(indent(res))
	check(t, err == nil, "export -out %s succeeded (%v)", week, err)
	data, err := os.ReadFile(filepath.Join(out, "data/how-the-internet-works/weekly", week+".json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dataformat.ParseWeek(data, hiwProject(t), week)
	if !check(t, err == nil, "the exported file passes the site's strict checks (%v)", err) {
		t.FailNow()
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"page_loads", doc.Tables.PageLoads, want.PageLoads},
		{"language", doc.Tables.Language, want.Language},
		{"country", doc.Tables.Country, want.Country},
	} {
		g, _ := json.Marshal(c.got)
		w, _ := json.Marshal(c.want)
		check(t, string(g) == string(w), "%s table as expected\n      got      %s\n      expected %s", c.name, g, w)
	}
	check(t, reflect.DeepEqual(doc.Source.Collect, []string{"country"}) && doc.Source.DataRetentionDays == 31,
		"source records collect=[country], retention 31: %+v", doc.Source)
	check(t, !regexp.MustCompile(`"(SE|NO|US)"`).Match(data), "countries under 5 on a day are not named (SE 4, NO 4, US 2)")
	settings, err := os.ReadFile(filepath.Join(out, "data/how-the-internet-works/goatcounter-settings.json"))
	check(t, err == nil && strings.Contains(string(settings), `"public": "private"`) &&
		strings.Contains(string(settings), `"collect": [`+"\n      \"country\"\n    ]"), "settings snapshot:\n%s", settings)
	check(t, !strings.Contains(string(settings), "ignore_ips") && !strings.Contains(string(settings), "secret"),
		"settings snapshot has no ignore_ips or secret")

	today := isoweek.DayOf(time.Now())
	for _, c := range []struct{ week, why string }{
		{isoweek.Of(today), "not over yet"}, {isoweek.Of(today.AddDate(0, 0, -38)), "beyond retention"}, {"2026-W99", "no such week"},
	} {
		res, err := runE(exportEnv(exportToken), osBin, "export", "-out", "/tmp/export-bad", "-week", c.week)
		_, statErr := os.Stat("/tmp/export-bad/data")
		check(t, err != nil && os.IsNotExist(statErr), "week %s (%s) refused: %s", c.week, c.why, strings.TrimSpace(res))
	}
	res, err = runE(exportEnv(strings.Repeat("0", 48)), osBin, "export", "-out", "/tmp/export-bad", "-week", week)
	check(t, err != nil, "wrong token refused: %s", strings.TrimSpace(res))

	t.Log("-- export as the systemd unit runs it: commit and push to a git remote, never rewrite")
	origin, clone := "/srv/origin.git", "/var/lib/open-stats/repo"
	run(t, "rm", "-rf", origin, clone, "/tmp/seed-clone", "/tmp/edit-clone")
	run(t, "git", "config", "--global", "--replace-all", "safe.directory", "*") // root's test clones
	run(t, "git", "init", "--quiet", "--bare", "-b", "main", origin)
	run(t, "git", "clone", "--quiet", origin, "/tmp/seed-clone")
	os.WriteFile("/tmp/seed-clone/README.md", []byte("test\n"), 0o644)
	run(t, "git", "-C", "/tmp/seed-clone", "add", "README.md")
	run(t, "git", "-C", "/tmp/seed-clone", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--quiet", "-m", "init")
	run(t, "git", "-C", "/tmp/seed-clone", "push", "--quiet", "origin", "HEAD:main")
	run(t, "chown", "-R", "open-stats:open-stats", origin)
	run(t, "runuser", "-u", "open-stats", "--", "git", "clone", "--quiet", origin, clone)
	run(t, "runuser", "-u", "open-stats", "--", "git", "-C", clone, "config", "user.name", "open-stats export")
	run(t, "runuser", "-u", "open-stats", "--", "git", "-C", clone, "config", "user.email", "export@example.invalid")

	unit, _ := os.ReadFile(filepath.Join(optDir, "collector/systemd/open-stats-export.service"))
	m := regexp.MustCompile(`(?m)^ExecStart=(.*)$`).FindStringSubmatch(strings.ReplaceAll(string(unit), "\\\n", " "))
	if m == nil {
		t.Fatal("no ExecStart")
	}
	cmd := strings.Fields(m[1])
	check(t, reflect.DeepEqual(cmd, []string{osBin, "export", "--repo", clone}), "unit runs %v", cmd)

	asUnit := func() (string, error) {
		env := []string{"PATH=/usr/bin:/bin", "HOME=/var/lib/open-stats"}
		data, _ := os.ReadFile(envFile)
		for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			env = append(env, ln)
		}
		argv := append([]string{"runuser", "-u", "open-stats", "--", "env", "-i"}, env...)
		return runE(nil, append(argv, cmd...)...)
	}
	originLog := func() []string {
		return strings.Split(strings.TrimSpace(run(t, "git", "--git-dir", origin, "log", "--format=%s", "main")), "\n")
	}
	originFile := func(p string) string {
		out, _ := runE(nil, "git", "--git-dir", origin, "show", "main:"+p)
		return out
	}

	// Someone switches on referrer collection: the export publishes nothing
	// until the settings are back.
	adm, done := tempToken(t, "test drift", 8|32)
	site := "/api/v0/sites/" + strconv.Itoa(num(gcQuery(t, "select site_id from sites where cname = 'how-the-internet-works.localhost'")[0]["site_id"]))
	mustAPI(t, "PATCH", "how-the-internet-works.localhost", site, adm, map[string]any{"settings": map[string]any{"collect": 16 | 2}})
	res, err = asUnit()
	var ee *exec.ExitError
	check(t, errors.As(err, &ee) && ee.ExitCode() == 3 && strings.Contains(res, "nothing was published"),
		"with drifted settings the export exits 3 and publishes nothing: %s", strings.TrimSpace(res))
	check(t, reflect.DeepEqual(originLog(), []string{"init"}), "nothing pushed (log: %v)", originLog())
	check(t, strings.TrimSpace(run(t, "git", "-C", clone, "status", "--porcelain")) == "" &&
		strings.TrimSpace(run(t, "git", "-C", clone, "log", "--format=%s")) == "init", "nothing written or committed in the clone")
	done()
	res, err = runE(nil, osBin, "configure", "-projects", "/tmp/projects", "how-the-internet-works")
	check(t, err == nil, "configure puts the settings back: %s", strings.TrimSpace(res))

	res, err = asUnit()
	t.Log(indent(res))
	check(t, err == nil, "first run succeeded (%v)", err)
	check(t, originLog()[0] == "data: "+week, "pushed commit 'data: %s' (log: %v)", week, originLog())
	pushed, err := dataformat.ParseWeek([]byte(originFile("data/how-the-internet-works/weekly/"+week+".json")), hiwProject(t), week)
	check(t, err == nil && reflect.DeepEqual(pushed.Tables, doc.Tables), "pushed file has the same tables as with -out (%v)", err)
	check(t, originFile("data/how-the-internet-works/goatcounter-settings.json") == string(settings), "settings snapshot pushed")
	check(t, !strings.Contains(run(t, "git", "--git-dir", origin, "ls-tree", "-r", "--name-only", "main"), "demo"),
		"projects not built into the binary are not exported")

	before := originLog()
	res, err = asUnit()
	check(t, err == nil && reflect.DeepEqual(originLog(), before) && strings.Contains(res, "already published"),
		"second run: nothing rewritten, no new commit (%s)", strings.TrimSpace(res))

	// Someone edits the file on GitHub, and the local clone has junk in it:
	// the export neither overwrites the published file nor pushes the junk.
	run(t, "git", "clone", "--quiet", origin, "/tmp/edit-clone")
	f := filepath.Join("/tmp/edit-clone/data/how-the-internet-works/weekly", week+".json")
	b, _ := os.ReadFile(f)
	edited := regexp.MustCompile(`"generated_at": "[^"]*"`).ReplaceAllString(string(b), `"generated_at": "2000-01-01T00:00:00Z"`)
	os.WriteFile(f, []byte(edited), 0o644)
	run(t, "git", "-C", "/tmp/edit-clone", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--quiet", "-am", "manual edit")
	run(t, "git", "-C", "/tmp/edit-clone", "push", "--quiet", "origin", "HEAD:main")
	run(t, "runuser", "-u", "open-stats", "--", "sh", "-c", "echo junk > "+clone+"/junk.txt")
	before = originLog()
	_, err = asUnit()
	check(t, err == nil && reflect.DeepEqual(originLog(), before) &&
		originFile("data/how-the-internet-works/weekly/"+week+".json") == edited, "a later run leaves the published file alone")
	check(t, !strings.Contains(run(t, "git", "--git-dir", origin, "ls-tree", "-r", "--name-only", "main"), "junk"),
		"local junk in the clone is never pushed")
	run(t, "rm", "-rf", "/tmp/edit-clone", "/tmp/seed-clone")
}

var (
	tagRE  = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9]*)((?:\s+[a-zA-Z][a-zA-Z0-9:-]*(?:="[^"]*")?)*)\s*(/?)>`)
	attrRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9:-]*)(?:="([^"]*)")?`)
	voidEl = map[string]bool{"meta": true, "link": true, "br": true, "hr": true, "img": true, "input": true,
		"wbr": true, "col": true, "source": true}
)

// wellFormed checks that tags balance and attributes are harmless.
func wellFormed(page string) []string {
	var errs, stack []string
	rest := strings.TrimPrefix(page, "<!doctype html>\n")
	if rest == page {
		errs = append(errs, "no doctype")
	}
	if strings.Count(rest, "<") != len(tagRE.FindAllString(rest, -1)) {
		errs = append(errs, "a '<' that isn't a plain tag")
	}
	for _, m := range tagRE.FindAllStringSubmatch(rest, -1) {
		closing, tag, attrs, self := m[1] == "/", strings.ToLower(m[2]), m[3], m[4] == "/"
		for _, a := range attrRE.FindAllStringSubmatch(attrs, -1) {
			k, v := strings.ToLower(a[1]), a[2]
			if strings.HasPrefix(k, "on") || k == "src" || k == "srcset" || k == "action" || k == "formaction" || k == "style" {
				errs = append(errs, "attribute "+k)
			}
			if regexp.MustCompile(`(?i)javascript:|data:`).MatchString(v) ||
				(k != "href" && k != "xmlns" && strings.Contains(v, "://")) {
				errs = append(errs, fmt.Sprintf("suspicious %s=%q", k, v))
			}
		}
		switch {
		case voidEl[tag] || self:
		case closing:
			if len(stack) == 0 || stack[len(stack)-1] != tag {
				errs = append(errs, fmt.Sprintf("unbalanced </%s> (open: %v)", tag, stack))
			} else {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, tag)
		}
	}
	if len(stack) > 0 {
		errs = append(errs, fmt.Sprintf("unclosed %v", stack))
	}
	return errs
}

func testSite(t *testing.T) {
	data := "/tmp/origin-data"
	run(t, "rm", "-rf", data, "/tmp/site")
	run(t, "git", "clone", "--quiet", "/srv/origin.git", data)
	res, err := runE(nil, osBin, "site", "-data", filepath.Join(data, "data"), "-out", "/tmp/site")
	t.Log(indent(res))
	if !check(t, err == nil, "build succeeded (%v)", err) {
		return
	}
	var files []string
	filepath.WalkDir("/tmp/site", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, strings.TrimPrefix(p, "/tmp/site/"))
		}
		return nil
	})
	check(t, reflect.DeepEqual(files, []string{"index.html"}), "only index.html is produced: %v", files)
	b, _ := os.ReadFile("/tmp/site/index.html")
	page := string(b)
	errs := wellFormed(page)
	check(t, len(errs) == 0, "well-formed HTML with harmless attributes: %v", errs)
	check(t, !strings.Contains(strings.ToLower(page), "<script"), "no <script>")
	check(t, !regexp.MustCompile(`(?i)<(iframe|object|embed|img|link|form|input|base)\b`).MatchString(page),
		"no iframes, images, links to resources or forms")
	check(t, !regexp.MustCompile(`(?i)(src|srcset|action|formaction)\s*=`).MatchString(page), "nothing loaded from anywhere (no src/action)")
	check(t, !regexp.MustCompile(`(?i)\son\w+\s*=`).MatchString(page), "no inline event handlers")
	check(t, strings.Contains(page, "Content-Security-Policy") && strings.Contains(page, "default-src &#39;none&#39;"), "strict CSP meta tag")
	week := isoweek.LastComplete(isoweek.DayOf(time.Now()))
	check(t, strings.Contains(page, week), "week %s is on the page", week)
	check(t, strings.Contains(page, "page loads: 21<") && strings.Contains(page, "page loads: 19<"), "daily page loads are on the page")
	check(t, strings.Contains(page, ">19 page loads: not broken down<") && strings.Contains(page, ">5 page loads: not broken down<"),
		"days under 20 page loads are not broken down")
	check(t, !regexp.MustCompile(`>(SE|NO|US)<`).MatchString(page), "countries under 5 on a day are not named")
	check(t, strings.Contains(page, "<summary>Daily numbers</summary>") &&
		regexp.MustCompile(`<td>21</td><td>\d+</td>`).MatchString(page) &&
		regexp.MustCompile(`<td>19</td><td colspan="\d+" class="quiet">not broken down</td>`).MatchString(page),
		"the charts' numbers are in the daily table, quiet days as a total only")
}

// otherSiteIP is the client IP of requests meant for the stand-in for the
// server's other sites (test/vps/other-site.conf).
const otherSiteIP = "192.0.2.77"

func testLogs(t *testing.T) {
	// By now every kind of request has been sent: allowed and refused,
	// malformed, rate-limited, with the upstream slow and down. Add a
	// connection that is dropped halfway through a request, then a request
	// to another site, which must be logged (nginx's error log is at level
	// info in the container), to show the log works.
	conn, err := tls.Dial("tcp", "127.0.0.1:443", &tls.Config{ServerName: "stats.irq.dk", InsecureSkipVerify: true})
	if err == nil {
		fmt.Fprintf(conn, "GET /how-the-internet-works/co")
		conn.Close()
	}
	send(t, req{sni: "other.example", ip: otherSiteIP, target: "/control", headers: [][2]string{{"Host", "other.example"}}})
	time.Sleep(500 * time.Millisecond)

	entries, _ := os.ReadDir("/var/log/nginx")
	for _, e := range entries {
		p := filepath.Join("/var/log/nginx", e.Name())
		b, _ := os.ReadFile(p)
		if e.Name() != "error.log" {
			check(t, len(b) == 0, "%s: empty (%d bytes)", p, len(b))
			continue
		}
		var controlSeen bool
		var bad []string
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "server: other.example") && strings.Contains(ln, "client: "+otherSiteIP+",") {
				controlSeen = true
				continue
			}
			// The request without a Host header from testForwarding (realip hasn't
			// run yet when nginx rejects it, so it has the peer's address).
			if strings.Contains(ln, "server: other.example") && strings.Contains(ln, `without "Host" header`) {
				continue
			}
			if regexp.MustCompile(`client|request|server:|upstream|limiting|\d+\.\d+\.\d+\.\d+|::1`).MatchString(ln) {
				bad = append(bad, ln)
			}
		}
		check(t, controlSeen, "%s works: the other site's 404 is logged with its client IP", p)
		check(t, len(bad) == 0, "%s: nothing about stats.irq.dk's requests, no IP addresses (%v)", p, bad)
	}
	check(t, len(entries) > 0, "nginx's log directory exists")
}

func testUnits(t *testing.T) {
	units, _ := filepath.Glob(filepath.Join(optDir, "collector/systemd/*"))
	out, _ := runE(nil, append([]string{"systemd-analyze", "verify", "--man=no"}, units...)...)
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "open-stats") || strings.Contains(ln, "goatcounter") {
			lines = append(lines, ln)
		}
	}
	check(t, len(lines) == 0, "systemd-analyze verify finds nothing to complain about: %v", lines)
}
