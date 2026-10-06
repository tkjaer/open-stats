# Development and tests

One Go module; its only dependency is
[`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml).
[`cmd/open-stats`](../cmd/open-stats/) is the binary (`export`, `configure`,
`site`, `version`), [`collector/`](../collector/) the server side,
[`internal/`](../internal/) the shared rules, [`projects/`](../projects/) one
file per site, [`site/`](../site/) the page.

```sh
go test ./...        # unit tests only
test/run.sh          # everything; about seven minutes
KEEP=1 test/run.sh   # same, but leave the container running
test/load/run.sh     # load measurement on one core; not part of run.sh
```

`run.sh` needs Go and Docker with `docker compose`. In CI,
[`test.yml`](../.github/workflows/test.yml) runs it with `shellcheck` and a
reproducible-build check; [`pages.yml`](../.github/workflows/pages.yml)
builds the page daily from `main` and open-stats-data (actions pinned by
commit, read-only permissions except for Pages).

What `run.sh` checks:

- **Unit tests** (`go test -race`, `gofmt`, `go vet`): the publishing rules,
  strict validation of data files, the export against a fake GoatCounter, the
  page (escaping, no scripts, the CSP, daily tables matching the charts) and
  that nginx's allow-list matches `projects/`.
- **End-to-end** ([`e2e/`](e2e/)), in a Debian 12 container with the real
  nginx file and the pinned GoatCounter:
  - nginx forwards only allowed two-letter `lang` values, with the client IP
    and a fixed User-Agent and nothing else; drops other values, projects,
    paths, GPC, DNT, bots and prefetches; always answers an empty 204, also
    with GoatCounter slow or down; caps at 10 a second with no per-IP limit;
  - GoatCounter counts under the right path and country, stores no pageviews
    or browser details, fills the copy tables with empty fields, counts more
    than 4 a second from one address, logs a failed count without its IP,
    and keeps no bot records;
  - `configure` and the export (a seeded week exactly as worked out by hand,
    settings drift, a hung remote, local junk never pushed);
  - nothing about `stats.irq.dk`'s requests reaches nginx's logs, even at
    level `info`.
- **The page** in the Nu HTML Checker and in Chromium with
  [axe-core](https://github.com/dequelabs/axe-core) ([`a11y/`](a11y/)), in
  light and dark mode: no errors, no violations, nothing fetched.
- **systemd** in a second container: the real units, sandboxing and memory
  limits; the hourly restart clears GoatCounter's rate limiter, loses no
  accepted count, leaves a stopped GoatCounter stopped and isn't delayed by a
  hung export.

The test container differs from the server only for testing: an
`X-Test-Client-IP` header picks the client IP
([`vps/test-realip.conf`](vps/test-realip.conf)), a stand-in for the other
sites is the default server ([`vps/other-site.conf`](vps/other-site.conf)),
and [`vps/gcctl`](vps/gcctl) runs GoatCounter without systemd.
