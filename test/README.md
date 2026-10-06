# Tests

```sh
test/run.sh          # everything; about four minutes
KEEP=1 test/run.sh   # same, but leave the container running to poke at
```

Needs Go (the version in [`../go.mod`](../go.mod)) and Docker with
`docker compose` (OrbStack and Docker Desktop both work).

## What it does

1. **Unit tests** on this machine: `gofmt`, `go vet` and `go test -race ./...`.
   These cover the publishing rules (quiet days under 20, the threshold of 5, `other`), strict
   validation of data files, ISO weeks, the export (against a fake
   GoatCounter and a local git repository), the page (escaping, no scripts,
   the CSP) and that nginx's allow-list matches `projects/*.yml`.
2. Cross-compiles `open-stats` for Linux, and builds the end-to-end tests
   with the race detector in a pinned Go container (it needs cgo).
3. Starts one container ([`docker-compose.yml`](docker-compose.yml),
   [`vps/Dockerfile`](vps/Dockerfile)) standing in for the server: Debian 12,
   its nginx with [`../collector/nginx/stats.irq.dk.conf`](../collector/nginx/stats.irq.dk.conf)
   unchanged (with a self-signed certificate at the placeholder paths), and
   GoatCounter installed with
   [`../collector/install-goatcounter.sh`](../collector/install-goatcounter.sh),
   so the pinned sha256 sums are checked too.
4. Runs the **end-to-end checks** ([`e2e/`](e2e/)) inside it, as root, over
   TLS on `127.0.0.1` and `::1`:

| Step | Checks |
|---|---|
| `binary` | `open-stats` is a static binary; GoatCounter is built with the isbot version its bot detection was checked against. |
| `forwarding` | With a recording stand-in on GoatCounter's port: allowed counts are forwarded as `/count?p=/<lang>` with the right `Host`, the client IP in `X-Real-IP`, a fixed User-Agent and no other header. Other values, projects, paths, methods, extra parameters, encodings, `Sec-GPC: 1` and `DNT: 1` are never forwarded. Every response is an empty 204, at once, including malformed requests and a slow or missing upstream. Requests without `Host: stats.irq.dk` go to the default site and are not forwarded. |
| `rate-limits` | 60 quick requests from one IP address are all forwarded (no per-IP limit); the global cap (10 a second, burst 200) drops the rest of 300 quick requests from different addresses, still with 204. |
| `configure` | `open-stats configure` creates the sites (`how-the-internet-works` and a second test project), applies and verifies the settings, writes the env file with mode 600, and is safe to run again. |
| `counting` | The real GoatCounter counts allowed requests under the right path and looks up the country from a public client IP; nothing else is counted; GoatCounter stores no individual pageviews and no browser details; with GoatCounter stopped, still an empty 204. |
| `goatcounter-internals` | 12 counts from one IP address within a second are all counted with the unit's `-ratelimit` flag (and, as a control, fewer without it). Talking to GoatCounter directly: a count that fails while being processed is logged as JSON without the client IP (and, as a control, the IP is in the log without `-json`); requests from cloud and hosting ranges sent through nginx are counted, not recorded as bots (and, as a control, the `bots` table does catch a request sent around nginx with a User-Agent that isbot checks IPs for). |
| `export` | A seeded week is exported to a local bare git repository exactly as worked out by hand (including days of 5 and 19 page loads published as totals only, days of 20 and 21 broken down, and countries under 5 folded into `other`); weeks out of range and a wrong token are refused; through the systemd unit's command, it publishes nothing and exits 3 while GoatCounter's settings differ from `projects/*.yml` (until `open-stats configure` puts them back), then commits and pushes, a second run changes nothing, a manual edit on GitHub is never overwritten and local junk is never pushed. |
| `site` | The page built from the export shows quiet days as totals only and is well-formed HTML with no `<script>`, no event handlers, no external URLs and the strict CSP. |
| `logs` | nginx wrote no access log and nothing about `stats.irq.dk`'s requests in its error log (at level `info`), not even after rate-limit rejections, malformed requests, dropped connections and upstream errors, while the other site's requests are logged there with their IP. |
| `units` | The systemd units pass `systemd-analyze verify`. |

5. Starts a second container from the same image with **systemd** as PID 1
   (the `systemd` profile in [`docker-compose.yml`](docker-compose.yml),
   privileged), installs the units from
   [`../collector/systemd`](../collector/systemd) as the setup guide does, and
   runs `TestSystemd` ([`e2e/systemd_test.go`](e2e/systemd_test.go)) in it:

| Step | Checks |
|---|---|
| `unit` | GoatCounter runs under its real unit, sandboxing included, as its own user, with `GOMEMLIMIT=100MiB`, `MemoryHigh` and `MemoryMax`. |
| `restart-only-if-running` | `goatcounter-restart.service` restarts a running GoatCounter and leaves a stopped one stopped. |
| `restart-clears-rate-limiter` | With a test-only limit of 2 counts an hour per IP address, a third count is refused; after the timer (set to every 20 seconds for the test) restarts GoatCounter, the same address is counted again, so its key is gone. |
| `counts-across-restarts` | 20 counts a second for about a minute while the timer restarts GoatCounter at least twice: every count GoatCounter accepted is stored, and at most a second's worth per restart is refused (it measures how long; about 0.4 s). |
| `restart-schedule` | The real timer fires every hour at half past (UTC), next within the hour, with 1 s accuracy. |

The first container differs from the server in these ways, all for testing:

- [`vps/test-realip.conf`](vps/test-realip.conf) lets the tests pick their
  client IP with an `X-Test-Client-IP` header (for the country lookup and to
  send from many addresses). It is never installed on the server.
- [`vps/other-site.conf`](vps/other-site.conf) stands in for the server's
  other sites: it is the default server for port 443, so `stats.irq.dk` is
  chosen by name as on the server, and it logs normally.
- nginx's error log is at level `info`, so the `logs` check would catch even
  rate-limit rejections.
- It has no systemd (the second container covers the units):
  [`vps/gcctl`](vps/gcctl) starts and stops GoatCounter with the same command
  line and environment as the unit (for the control runs, without `-json` and
  with its own log file, or without `-ratelimit`).

## Load measurement

[`load/run.sh`](load/run.sh) (not part of `run.sh`, about 4 minutes) measures
what counting costs on a one-core server. It starts the same container with
one CPU core, one nginx worker and GoatCounter in a cgroup with its unit's
memory limits, and a second container that sends counts the way browsers do,
each on a new TLS connection from a random public IP address: 10 a second for
150 seconds, then 200 at once, then 30 a second for 30 seconds (over nginx's
cap). It reports nginx's and GoatCounter's CPU and memory, checks that every
answer is an empty 204, and checks that GoatCounter counted exactly what nginx
let through. For that count only, it logs nginx's rate-limit rejections. The
results are in the setup guide's
[Rate limit](../collector/README.md#rate-limit) section.
