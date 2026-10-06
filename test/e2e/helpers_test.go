//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	optDir    = "/opt/open-stats"
	envFile   = "/etc/open-stats/env"
	gcBin     = "/usr/local/bin/goatcounter"
	osBin     = "/usr/local/bin/open-stats"
	gcDB      = "sqlite+/var/lib/goatcounter/db.sqlite3"
	browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"
	forwardUA = "Mozilla/5.0 (compatible; open-stats-nginx/1)"
)

// check reports one result like a checklist, and fails the test if !ok.
func check(t *testing.T, ok bool, format string, args ...any) bool {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if ok {
		t.Log("ok   " + msg)
	} else {
		t.Error("FAIL " + msg)
	}
	return ok
}

var ipCounter int

// freshIP is a new benchmarking-range IP per request, so per-IP limits
// don't interfere between checks.
func freshIP() string {
	ipCounter++
	return fmt.Sprintf("198.18.%d.%d", ipCounter/250, ipCounter%250+1)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runE(nil, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

func runE(env []string, args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("%v: %w\n%s%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String() + stderr.String(), nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}

// ---- a raw HTTPS client ------------------------------------------------------

type response struct {
	status  int
	headers map[string]string
	body    []byte
	elapsed time.Duration
}

func (r response) String() string {
	var keys []string
	for k := range r.headers {
		keys = append(keys, k)
	}
	return fmt.Sprintf("status %d, %d body bytes, %d ms, headers %v", r.status, len(r.body), r.elapsed.Milliseconds(), keys)
}

// silent204 is the only answer nginx may give: empty, no cookie, at once.
func (r response) silent204(within time.Duration) bool {
	_, cookie := r.headers["set-cookie"]
	cl, hasCL := r.headers["content-length"]
	return r.status == 204 && len(r.body) == 0 && (!hasCL || cl == "0") && !cookie &&
		r.headers["server"] == "nginx" && r.headers["cache-control"] == "no-store" && r.elapsed < within
}

type req struct {
	target, method, addr, ip string
	headers                  [][2]string // added or (with "" value and drop) replaced
	drop                     []string
	body                     []byte
	raw                      []byte
	sni                      string // TLS server name; default stats.irq.dk
}

// send sends one request and returns as soon as the response headers (and
// body, if any) are in, without waiting for the server to close the
// connection: the 204 comes before the mirrored upstream request finishes.
func send(t *testing.T, r req) response {
	t.Helper()
	if r.target == "" {
		r.target = "/how-the-internet-works/count?lang=en"
	}
	if r.method == "" {
		r.method = "GET"
	}
	if r.addr == "" {
		r.addr = "127.0.0.1"
	}
	h := [][2]string{
		{"Host", "stats.irq.dk"}, {"User-Agent", browserUA}, {"Accept", "*/*"},
		{"Accept-Language", "da,en;q=0.8"}, {"Origin", "https://tkjaer.github.io"},
		{"Sec-Fetch-Mode", "no-cors"}, {"Sec-Fetch-Site", "cross-site"}, {"Connection", "close"},
	}
	for _, kv := range r.headers {
		replaced := false
		for i := range h {
			if strings.EqualFold(h[i][0], kv[0]) {
				h[i][1], replaced = kv[1], true
			}
		}
		if !replaced {
			h = append(h, kv)
		}
	}
	var hb strings.Builder
	fmt.Fprintf(&hb, "%s %s HTTP/1.1\r\n", r.method, r.target)
	for _, kv := range h {
		dropped := false
		for _, d := range r.drop {
			dropped = dropped || strings.EqualFold(d, kv[0])
		}
		if !dropped {
			fmt.Fprintf(&hb, "%s: %s\r\n", kv[0], kv[1])
		}
	}
	if r.ip != "" {
		fmt.Fprintf(&hb, "X-Test-Client-IP: %s\r\n", r.ip)
	}
	if len(r.body) > 0 {
		fmt.Fprintf(&hb, "Content-Length: %d\r\n", len(r.body))
	}
	data := append([]byte(hb.String()+"\r\n"), r.body...)
	if r.raw != nil {
		data = r.raw
	}

	start := time.Now()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(r.addr, "443"),
		&tls.Config{ServerName: cmp.Or(r.sni, "stats.irq.dk"), InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := response{headers: map[string]string{}}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err == nil {
		if f := strings.Fields(status); len(f) > 1 {
			resp.status, _ = strconv.Atoi(f[1])
		}
		for {
			line, err := br.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if err != nil || line == "" {
				break
			}
			k, v, _ := strings.Cut(line, ":")
			resp.headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		if n, _ := strconv.Atoi(resp.headers["content-length"]); n > 0 {
			resp.body = make([]byte, n)
			io.ReadFull(br, resp.body)
		}
	}
	resp.elapsed = time.Since(start)
	return resp
}

// ---- a recording stand-in for GoatCounter -----------------------------------

type captured struct {
	method, uri, body string
	headers           map[string]string // lower-case names, including host
}

type capture struct {
	srv  *http.Server
	mu   sync.Mutex
	reqs []captured
}

// startCapture listens on GoatCounter's port and records what nginx
// forwards; with a delay, it answers slowly.
func startCapture(t *testing.T, delay time.Duration) *capture {
	t.Helper()
	c := &capture{}
	c.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h := map[string]string{"host": r.Host}
		for k, v := range r.Header {
			h[strings.ToLower(k)] = strings.Join(v, ", ")
		}
		c.mu.Lock()
		c.reqs = append(c.reqs, captured{r.Method, r.RequestURI, string(body), h})
		c.mu.Unlock()
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	})}
	var ln net.Listener
	var err error
	for range 50 {
		if ln, err = net.Listen("tcp", "127.0.0.1:8081"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	go c.srv.Serve(ln)
	return c
}

func (c *capture) stop() { c.srv.Close() }

// take returns the requests forwarded since the last call.
func (c *capture) take(settle time.Duration) []captured {
	time.Sleep(settle)
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.reqs
	c.reqs = nil
	return out
}

// ---- GoatCounter -------------------------------------------------------------

func gcctl(t *testing.T, action string) { run(t, "gcctl", action) }

func gcQuery(t *testing.T, sql string) []map[string]any {
	t.Helper()
	out := run(t, "runuser", "-u", "goatcounter", "--", gcBin, "db", "query", "-db", gcDB, "-format", "json", sql)
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("query %q: %v: %s", sql, err, out)
	}
	return rows
}

func gcExec(t *testing.T, sql string) {
	t.Helper()
	run(t, "runuser", "-u", "goatcounter", "--", gcBin, "db", "query", "-db", gcDB, "-format", "exec", sql)
}

func num(v any) int {
	switch v := v.(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return -1
}

func envToken(t *testing.T) string {
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(ln, "GOATCOUNTER_TOKEN="); ok {
			return v
		}
	}
	return ""
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

func api(method, host, path, token string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, "http://127.0.0.1:8081"+path, rd)
	r.Host = host
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	time.Sleep(300 * time.Millisecond) // GoatCounter's API limit
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{resp.StatusCode, string(data)}
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return out, dec.Decode(&out)
}

func mustAPI(t *testing.T, method, host, path, token string, body any) map[string]any {
	t.Helper()
	out, err := api(method, host, path, token, body)
	if err != nil {
		t.Fatalf("%s %s %s: %v", method, host, path, err)
	}
	return out
}

func waitFor(t *testing.T, what string, fn func() bool) bool {
	t.Helper()
	for start := time.Now(); time.Since(start) < 40*time.Second; time.Sleep(time.Second) {
		if fn() {
			return true
		}
	}
	t.Logf("(gave up waiting for %s)", what)
	return fn()
}

// tempToken adds a GoatCounter API token for how-the-internet-works's site
// with the given permissions, and returns it with a function to delete it.
func tempToken(t *testing.T, name string, perms int) (string, func()) {
	t.Helper()
	root := num(gcQuery(t, "select site_id from sites where cname = 'how-the-internet-works.localhost'")[0]["site_id"])
	user := num(gcQuery(t, fmt.Sprintf("select user_id from users where site_id = %d", root))[0]["user_id"])
	token := randomHex(24)
	gcExec(t, fmt.Sprintf("insert into api_tokens (site_id, user_id, name, token, permissions, created_at, sites) values "+
		"(%d, %d, '%s', '%s', '%d', strftime('%%Y-%%m-%%d %%H:%%M:%%S', 'now'), '[%d]')", root, user, name, token, perms, root))
	return token, func() { gcExec(t, "delete from api_tokens where token = '"+token+"'") }
}
