# Tests

```sh
test/run.sh          # everything; about two minutes
KEEP=1 test/run.sh   # same, but leave the container running to poke at
```

Needs Go (the version in [`../go.mod`](../go.mod)) and Docker with
`docker compose` (OrbStack and Docker Desktop both work).

## What it does

1. **Unit tests** on this machine: `gofmt`, `go vet` and `go test ./...`.
   These cover the publishing rules (quiet days under 20, the threshold of 5, `other`), strict
   validation of data files, ISO weeks, the export (against a fake
   GoatCounter and a local git repository), the page (escaping, no scripts,
   the CSP) and that nginx's allow-list matches `projects/*.yml`.
2. Cross-compiles `open-stats` and the end-to-end tests for Linux.
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
| `rate-limits` | The per-IP and global limits drop requests, still with 204. |
| `configure` | `open-stats configure` creates the sites (`how-the-internet-works` and a second test project), applies and verifies the settings, writes the env file with mode 600, and is safe to run again. |
| `counting` | The real GoatCounter counts allowed requests under the right path and looks up the country from a public client IP; nothing else is counted; GoatCounter stores no individual pageviews and no browser details; with GoatCounter stopped, still an empty 204. |
| `goatcounter-internals` | Talking to GoatCounter directly: a count that fails while being processed is logged as JSON without the client IP (and, as a control, the IP is in the log without `-json`); requests from cloud and hosting ranges sent through nginx are counted, not recorded as bots (and, as a control, the `bots` table does catch a request sent around nginx with a User-Agent that isbot checks IPs for). |
| `export` | A seeded week is exported to a local bare git repository exactly as worked out by hand (including days of 5 and 19 page loads published as totals only, days of 20 and 21 broken down, and countries under 5 folded into `other`); weeks out of range and a wrong token are refused; through the systemd unit's command, it publishes nothing and exits 3 while GoatCounter's settings differ from `projects/*.yml` (until `open-stats configure` puts them back), then commits and pushes, a second run changes nothing, a manual edit on GitHub is never overwritten and local junk is never pushed. |
| `site` | The page built from the export shows quiet days as totals only and is well-formed HTML with no `<script>`, no event handlers, no external URLs and the strict CSP. |
| `logs` | nginx wrote no access log and nothing about `stats.irq.dk`'s requests in its error log (at level `info`), not even after rate-limit rejections, malformed requests, dropped connections and upstream errors, while the other site's requests are logged there with their IP. |
| `units` | The systemd units pass `systemd-analyze verify`. |

The container differs from the server in these ways, all for testing:

- [`vps/test-realip.conf`](vps/test-realip.conf) lets the tests pick their
  client IP with an `X-Test-Client-IP` header (for the country lookup and the
  per-IP rate limit). It is never installed on the server.
- [`vps/other-site.conf`](vps/other-site.conf) stands in for the server's
  other sites: it is the default server for port 443, so `stats.irq.dk` is
  chosen by name as on the server, and it logs normally.
- nginx's error log is at level `info`, so the `logs` check would catch even
  rate-limit rejections.
- There is no systemd; [`vps/gcctl`](vps/gcctl) starts and stops GoatCounter
  with the same command line as the unit (for the control run, without
  `-json` and with its own log file).
