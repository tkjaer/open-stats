# open-stats

Open, privacy-respecting visit counts for a few small sites: how they are
collected, exactly what is kept, and the published numbers.

The first (and so far only) project is
[How the Internet Works](https://tkjaer.github.io/how-the-internet-works/)
(`how-the-internet-works`); its [privacy notice](https://github.com/tkjaer/how-the-internet-works/blob/main/docs/privacy.md)
describes the counting from a visitor's point of view. This repository holds
everything on the other side: the server configuration, the export program
and the page that shows the published data.

- **Published numbers:** [tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data)
  (CC0; format in [`docs/data-format.md`](docs/data-format.md)), shown as a
  static page built from [`site/`](site/) (published at `https://tkjaer.github.io/open-stats/`
  once this repository is public).
- **Server setup:** [`collector/`](collector/), with a step-by-step
  [setup guide](collector/README.md). Everything that runs on the server
  besides nginx and GoatCounter is one small, static Go program, `open-stats`
  ([`cmd/open-stats`](cmd/open-stats/)), which anyone can rebuild
  byte-for-byte from this repository.
- **Per-project settings:** [`projects/`](projects/), one file per site.
- **Tests:** [`test/`](test/) runs the real nginx configuration and GoatCounter
  in a container and checks what they forward, drop, log and store, the rate
  limits, the export and the page; [`test/README.md`](test/README.md) lists
  exactly what. They can't check the real server (see [Limits](#limits-what-this-can-and-cant-promise)).

## What a visitor's browser sends

Once per page load, one request, with no cookie and no referrer:

```
GET https://stats.irq.dk/how-the-internet-works/count?lang=en
```

The only data in it is the language the app opened in (`en` or `da`). Like any
request it also carries the visitor's IP address and the browser's standard
headers. The answer is always an empty `204 No Content`, whatever happens, so
the app learns nothing from it and is never slowed down by it.

## What the server does with it

`stats.irq.dk` is nginx on a small VPS (hosted by Hetzner Online GmbH in
Germany) in front of a self-hosted
[GoatCounter](https://www.goatcounter.com) that only listens on `127.0.0.1`.
[nginx](collector/nginx/stats.irq.dk.conf):

- writes **no access log and no error log** for `stats.irq.dk`, so not even
  rate-limit rejections or errors, which nginx would otherwise log with the
  client's IP address;
- **drops** (still with an empty 204) every request that isn't exactly
  `GET /<project>/count?lang=<allowed value>`: other values, extra parameters,
  other paths, other methods;
- **drops** requests with **Global Privacy Control** (`Sec-GPC: 1`) or
  **Do Not Track** (`DNT: 1`), obvious bots and scripts (by User-Agent), and
  browser prefetches (by their prefetch headers). It uses these headers only to
  decide, and passes none of them on;
- **caps** what's left at 20 requests a second for all projects together
  (after a burst of up to 200). There is no limit per IP address, so a class
  or an office behind one address is counted in full, and nginx keeps no IP
  addresses for this;
- passes the rest on to GoatCounter as `/count?p=/<value>`, with the visitor's
  IP address and **nothing else from the browser**: no User-Agent, no
  `Accept-Language`, no cookies, no referrer. It doesn't wait for GoatCounter:
  the browser has its answer before GoatCounter is even asked.

GoatCounter has one site per project, set up by
[`open-stats configure`](collector/configure/configure.go) to collect **country only**:
individual pageviews, sessions, referrer, User-Agent, screen size, region and
browser language are all off. For each count it looks up the country from the
IP address in memory (with a database built into GoatCounter, no outside
service), then adds one to two counters: page loads per language (its path,
`/en` or `/da`) per hour, and page loads per country and language per day.
The IP address, the browser details and the exact time are not stored, and no
row per visit is kept, but a counter at 1 does describe one visit (one page
load in that language in that hour, or from that country in that language on
that day). The counters are **deleted after 31 days** (GoatCounter's minimum).
Its dashboard and API are never exposed to the internet; they are only
reachable over an SSH tunnel.

Each export also checks GoatCounter's settings against
[`projects/*.yml`](projects/) and publishes them, in
`data/<project>/goatcounter-settings.json`. If they differ, the export
publishes nothing at all until they are put back. So the history shows the
settings observed at each export; a change that was made and undone between
two exports wouldn't show up.

## What is published

Once a week (Monday, 03:00 UTC), [`open-stats export`](collector/export/export.go) reads the
last complete week from GoatCounter and commits one file per project to
`data/<project>/weekly/` in a separate repository,
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data), as
**three separate tables**:

1. **page loads per day**;
2. **language per day** (`en`, `da`, and `other`, which should always be 0);
3. **countries per day**. A country with **fewer than 5 page loads on a day**
   is counted under "other" for that day, so a single visit from a small
   country can't be picked out. "other" also includes page loads whose
   country is unknown.

**Quiet days:** on a day with **fewer than 20 page loads** in total, only
that day's total is published, with no language or country numbers. On small
days the separate tables could otherwise be put together to single out a
visit (5 page loads, all from Denmark, 1 of them in Danish).

Language and country are never published as one table (no "Danish-language
page loads from Germany"). Putting the two tables together can still say
something about a combination (if all of a day's page loads came from one
country, so did its languages), but only on days with at least 20 page loads
and for countries with at least 5 that day. **No weekly country numbers** are
published, in the data or on the page, that the hidden days could be worked
out from. Days are in UTC. Each file is written once and never rewritten by
the export; **published files and their git history are kept indefinitely**.
The format is described in [`docs/data-format.md`](docs/data-format.md).

## Limits: what this can and can't promise

- **You can't verify the server.** This repository shows the configuration and
  scripts the server is meant to run, and the tests show what they do. Nobody
  outside can check that the VPS actually runs exactly this. Deployment is
  manual: the server never pulls code from here by itself.
- **The numbers are approximate.** Reloads and new tabs count as page loads.
  Visitors with GPC, DNT, a content blocker or "Count my visit" switched off
  aren't counted. Above 20 page loads a second for all projects together
  (after a burst of 200), the excess isn't counted. Countries come from a
  GeoIP database and are sometimes wrong (VPNs, mobile networks).
- **Counts can be faked.** Anyone can send requests that look like page loads,
  and there is no limit per IP address: one script could inflate the numbers,
  by up to the global cap of 20 a second (about 1.7 million a day), and while
  it does, real visits over the cap aren't counted.
- **Where an IP address could still appear:** nginx uses the visitor's IP
  address only while it handles the request. GoatCounter gets the address in
  a header, looks up the country at once, and keeps the address in memory with
  the count until it adds the count to its totals (within about 10 seconds).
  If adding it fails, GoatCounter logs the count to the system journal; it runs
  with a log format that leaves the IP address out (its default format would
  include it). Its bot detection never classifies these requests by IP
  address, so it keeps no per-request bot records. The tests check both.
  GoatCounter also keeps each address, with nginx's fixed User-Agent, as a key
  in its rate limiter, **in memory only, until 12 to 18 hours after that
  address's last count**: a sweep every 6 hours removes keys unused for 12
  hours. With the key it keeps only when the address was first seen, the
  second of its last count and how many counts that second had left. The key
  is never on disk or in a log, a restart clears it, and it isn't linked to
  the page or language
  ([details and source](collector/README.md#goatcounters-rate-limiter)).
  The VPS hosts other sites too, and two kinds of connection are handled by its
  default HTTPS site, under that site's logging, because nginx can't yet tell
  they are for `stats.irq.dk`: a TLS connection that fails before it names
  `stats.irq.dk`, and a request that doesn't say `Host: stats.irq.dk`.
  Browsers, and so the apps, never send the second kind. Like any server, the
  VPS's network stack and hosting provider see connections.

## Adding a project

1. Add `projects/<name>.yml` (copy [`projects/how-the-internet-works.yml`](projects/how-the-internet-works.yml)).
2. Add one line to the allow-list map in
   [`collector/nginx/stats.irq.dk.conf`](collector/nginx/stats.irq.dk.conf)
   (the tests check that the two agree).
3. Build and install the new `open-stats` binary and nginx configuration on
   the server, reload nginx and run `open-stats configure <name>` (see the
   [setup guide](collector/README.md#adding-a-project)).

The export and the page pick up every project in `projects/` automatically.

## Development

One Go module, standard library plus one dependency,
[`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml) (pure Go, no further
dependencies; it reads `projects/*.yml`, which keep their comments).

```
cmd/open-stats/        the binary: export, configure, site, version
collector/             server side: nginx, systemd, GoatCounter pin, setup guide
  export/ configure/ goatcounter/   the Go code behind those subcommands
internal/              ISO weeks, project files, the data format and its rules
projects/              one .yml per project, built into the binary
site/                  the static page renderer
data/                  published numbers (written by the export only)
test/                  the end-to-end test setup
```

- `go test ./...`: unit tests (publishing rules, data validation, the page,
  that nginx's allow-list matches `projects/`).
- [`test/run.sh`](test/README.md): the unit tests, then the real nginx
  configuration and GoatCounter in a container.
- [`collector/build.sh`](collector/build.sh): the server binaries, with
  checksums.

GitHub Actions (all actions pinned by commit, read-only permissions except
where Pages needs more):
[`test.yml`](.github/workflows/test.yml) runs `shellcheck`, `test/run.sh` and
checks that the build is reproducible;
[`pages.yml`](.github/workflows/pages.yml) builds the page daily and on code
changes, from this repository's `main` and the data in
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data). The
server can only push to the data repository, so it can't change the code that
runs on the page's origin (see the [setup guide](collector/README.md#7-the-weekly-export)
for what that relies on). Publishing needs GitHub Pages, which for a private
repository needs a paid plan, so the deploy job only runs once the repository
is public. To turn it on then:

1. Make `open-stats-data` public too, or, while it is private, add a
   fine-grained token with *Contents: read* on `open-stats-data` only as the
   `OPEN_STATS_DATA_TOKEN` secret (delete it once the data repository is
   public).
2. *Settings → Pages → Build and deployment → Source: GitHub Actions*.
3. *Settings → Environments → github-pages*: deployment branches `main`
   only.
4. Run the *Page* workflow once by hand.

## Who runs this

Thomas Kjær Aabo (htiw@thomaskjaer.com) runs the server and is responsible
for the data it handles (the controller, in GDPR terms). The VPS is hosted by
Hetzner Online GmbH in Germany.

## Licences

- Code and configuration: [AGPL-3.0-or-later](LICENSE).
- Data in [tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data):
  [CC0 1.0](https://github.com/tkjaer/open-stats-data/blob/main/LICENSE), no rights
  reserved.
