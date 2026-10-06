# Server setup

How `stats.irq.dk` is set up, step by step. Everything here is done by hand;
the server never pulls anything from this repository by itself.

What runs on the server:

| Part | What it does | Files |
|---|---|---|
| nginx | Answers every request with an empty 204, forwards allowed counts to GoatCounter | [`nginx/stats.irq.dk.conf`](nginx/stats.irq.dk.conf) |
| GoatCounter | Keeps page loads per path (language) per hour and per country and path per day, for 31 days; listens on `127.0.0.1:8081` only; restarts hourly | [`systemd/goatcounter.service`](systemd/goatcounter.service), [`systemd/goatcounter-restart.*`](systemd/), [`goatcounter.version`](goatcounter.version) |
| `open-stats export` | Weekly: publishes last week's numbers to [open-stats-data](https://github.com/tkjaer/open-stats-data) | [`systemd/open-stats-export.*`](systemd/) |
| `open-stats configure` | Once per project: creates the GoatCounter site, applies its settings, sets up the export token | — |

`open-stats` is one static Go binary built from this repository; the projects
(`projects/*.yml`) are built into it.

The steps assume a Debian-like server with systemd, nginx and git, run as
root. Placeholders are in `<angle brackets>`.

## 1. DNS and certificate

- `stats.irq.dk` must resolve to the server: a CNAME, or A and AAAA records,
  pointing at `<your server>`.
- Get a certificate for `stats.irq.dk` the same way as for the other sites and
  put (or link) it at `/etc/ssl/stats.irq.dk/fullchain.pem` and
  `/etc/ssl/stats.irq.dk/privkey.pem`, or change those two paths in the nginx
  file. The nginx file has **no port-80 server** for `stats.irq.dk`, so it
  doesn't get in the way of an existing ACME setup; if certificates are issued
  with HTTP-01 challenges, the existing port-80 handling has to cover
  `stats.irq.dk` too.

## 2. Users and directories

```sh
adduser --system --group --home /var/lib/goatcounter --no-create-home goatcounter
adduser --system --group --home /var/lib/open-stats --no-create-home open-stats
install -d -o goatcounter -g goatcounter -m 0700 /var/lib/goatcounter
install -d -o open-stats -g open-stats -m 0700 /var/lib/open-stats
install -d -o root -g root -m 0755 /etc/open-stats
```

## 3. Get this repository and the binaries

On a machine with Go (the version in [`../go.mod`](../go.mod) or newer) and a
checkout of the commit to deploy:

```sh
collector/build.sh      # dist/open-stats-linux-{amd64,arm64} and dist/SHA256SUMS
```

The build is reproducible: the same commit and Go version give the same
checksums, so the binary on the server can be compared with a rebuild.
`open-stats version` prints the full commit ID it was built from, with
`-dirty` if the checkout had uncommitted or untracked files. Copy
`dist/` and the repository to the server, for example:

```sh
git clone https://github.com/tkjaer/open-stats.git /tmp/open-stats   # or scp a checkout
scp -r dist <server>:/tmp/open-stats/
```

On the server:

```sh
cd /tmp/open-stats
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
(cd dist && sha256sum -c --ignore-missing SHA256SUMS)
install -m 0755 dist/open-stats-linux-$arch /usr/local/bin/open-stats
open-stats version
```

## 4. GoatCounter

```sh
bash collector/install-goatcounter.sh /usr/local/bin/goatcounter
```

This downloads the release pinned in [`goatcounter.version`](goatcounter.version)
and refuses to install it unless both sha256 sums match.

```sh
install -m 0644 collector/systemd/goatcounter.service \
    collector/systemd/goatcounter-restart.service \
    collector/systemd/goatcounter-restart.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now goatcounter goatcounter-restart.timer
curl -s http://127.0.0.1:8081/status    # {"version":"v2.7.0", ...}
```

GoatCounter then listens on `127.0.0.1:8081` only, over plain HTTP, with
logs going to the journal. Its unit keeps it sandboxed: its own user, nothing
writable but `/var/lib/goatcounter`, no outbound network, low CPU and I/O
priority, at most 150 MB of memory (with `GOMEMLIMIT=100MiB`, so Go collects
garbage harder before the cgroup throttles it). It runs with `-json` (see
[Logging](#logging)) and `-ratelimit=count:1000/1`, and the timer restarts
it every hour at half past (see
[GoatCounter's rate limiter](#goatcounters-rate-limiter)).

## 5. Create the project's site

```sh
open-stats configure -admin-email <you@example.org> how-the-internet-works
```

The first time, this creates the GoatCounter site `how-the-internet-works.localhost` and the
dashboard login (GoatCounter asks for the password; or set
`OPEN_STATS_ADMIN_PASSWORD`). It then applies the settings from
[`../projects/how-the-internet-works.yml`](../projects/how-the-internet-works.yml) through GoatCounter's API, reads
them back to check them, and writes an API token for the export to
`/etc/open-stats/env` (mode 600). The token can only read statistics and site
settings. Running it again is safe and fixes any settings that have drifted.

The settings it applies:

| GoatCounter setting | Value | Why |
|---|---|---|
| Collect | country only (`16`) | No individual pageviews, sessions, referrer, User-Agent, screen size, region or browser language. |
| Data retention | 31 days | GoatCounter's minimum; the export runs weekly, well within it. |
| Dashboard | private, no public counter, no embedding | The numbers are published here instead. |

The dashboard user's time zone must stay **UTC** (it's the default): GoatCounter
groups days by it, and the export refuses to run otherwise.

## 6. nginx

```sh
install -m 0644 collector/nginx/stats.irq.dk.conf /etc/nginx/sites-available/
ln -s /etc/nginx/sites-available/stats.irq.dk.conf /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx
curl -si 'https://stats.irq.dk/how-the-internet-works/count?lang=en'    # HTTP/2 204, nothing else
```

All `map`s and zones in the file start with `openstats`, so they don't clash
with the other sites.

### Rate limit

nginx forwards at most **10 counts a second for all projects together** (about
860,000 a day), after a burst of up to 200 (`limit_req`, zone
`openstats_global`). The zone is keyed by the server name, so it holds no IP
addresses. Requests over the cap still get their 204 and aren't counted.
There is deliberately **no limit per IP address**: a class of 30 opening the
page together from behind one address should count as 30. So the cap protects
the server, not the numbers: one script can still fake up to 10 counts a
second. The cap is also what bounds GoatCounter's memory between its hourly
restarts (see [GoatCounter's rate limiter](#goatcounters-rate-limiter)).

What this costs, measured with [`test/load/run.sh`](../test/load/run.sh): the
test container on one CPU core, one nginx worker, GoatCounter in a cgroup with
the unit's memory limits, and every count a new TLS connection from a random
public address.

| Load | nginx CPU: average, busiest second | GoatCounter CPU: average, busiest second | GoatCounter memory (RSS) |
|---|---|---|---|
| idle | 0.1% | 0.1% | 59 MB |
| 10 a second for 150 s | 2.3%, 5.0% | 1.1%, 2.4% | at most 59 MB |
| then 200 at once | 8.2% in the busiest second | 1.8% in the busiest second | at most 50 MB |
| 30 a second for 30 s (3 times the cap) | 5.9%, 7.4% | 1.6%, 3.1% | at most 51 MB |

Every request got an empty 204 (the burst within 92 ms), nginx let 2,165 of
2,600 through, and GoatCounter counted exactly 2,165. The percentages are of
one core of the test machine (Apple silicon), which is probably faster than a
VPS core; expect up to about twice as much there. Most of nginx's share is the
TLS handshake, which every request costs whether it's over the cap or not.

### GoatCounter's rate limiter

GoatCounter has its own limit on `/count`, by default 4 a second per client IP
address and User-Agent
([handlers/backend.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/backend.go#L95-L109),
[handlers/mw.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/mw.go#L404-L415),
[handlers/handlers.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/handlers.go#L37-L45)).
nginx sends one fixed User-Agent, so that would count only 4 a second from
one address, fewer than nginx lets through. The unit raises it with
**`-ratelimit=count:1000/1`**, far above anything nginx forwards, so it never
applies. The tests check that 12 counts from one address within a second are
all counted (and, as a control, that fewer are without the flag).

The limiter can't be switched off, and it keys on the visitor's address:
GoatCounter replaces the connection's address with `X-Real-IP` before the
limiter runs
([`mware.RealIP()`](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/backend.go#L52)).
Its store, [go-limiter](https://github.com/sethvargo/go-limiter) v1.1.0's
`memorystore` with its default settings, keeps one entry per address **in
memory only**: the key (the address and nginx's User-Agent), when the address
was first seen, the one-second window of its last count and how many counts
that window had left. Nothing about the page or language. It is never written
to disk or logged. Left alone, a sweep every 6 hours would remove entries
unused for 12 hours
([memorystore/store.go](https://github.com/sethvargo/go-limiter/blob/v1.1.0/memorystore/store.go#L85-L92)),
so an address would stay for 12 to 18 hours after its last count, and there
is no setting to shorten that. Each entry also costs about 400 bytes: at the
nginx cap, 12 hours of new addresses (about 430,000) would push GoatCounter
past its memory limit.

So **GoatCounter restarts every hour**, which empties the store:
[`goatcounter-restart.timer`](systemd/goatcounter-restart.timer) runs
[`goatcounter-restart.service`](systemd/goatcounter-restart.service) at half
past every hour (UTC), which runs `systemctl try-restart goatcounter`. An
address is therefore kept **at most one hour, plus the moment a restart
takes**, and at the cap the store holds at most about 36,000 entries (about
15 MB). `try-restart` only restarts GoatCounter if it is running, so a stopped
or failing GoatCounter isn't started every hour. The restart deliberately
doesn't wait for the export, so a slow or hung export can never delay it
(see [step 7](#7-the-weekly-export)); half past keeps it away from the export
at 03:00 on Mondays anyway. `GOMEMLIMIT=100MiB` in the unit is a safety net
under `MemoryHigh` in case something else grows.

A restart loses (almost) nothing that was accepted. On `SIGTERM`, GoatCounter
stops accepting connections and waits for the requests it is handling
([zhttp serve.go](https://github.com/arp242/zhttp/blob/9a43cabb6d05/serve.go#L126-L145)),
then stores every buffered count before it exits
([cmd/goatcounter/serve.go](https://github.com/arp242/goatcounter/blob/v2.7.0/cmd/goatcounter/serve.go#L361-L378),
[memstore.go](https://github.com/arp242/goatcounter/blob/v2.7.0/memstore.go#L200-L209)).
The one exception: if its regular store (every 10 seconds,
[cron/cron.go](https://github.com/arp242/goatcounter/blob/v2.7.0/cron/cron.go#L76-L105))
is running at that very moment, the shutdown store is refused
([pkg/bgrun/bgrun.go](https://github.com/arp242/goatcounter/blob/v2.7.0/pkg/bgrun/bgrun.go#L155-L159)),
and counts accepted during those few milliseconds are lost. What is lost is
the time GoatCounter isn't listening: a restart takes about 0.25 s, and counts
sent during about 0.4 s get their 204 from nginx but aren't counted. That's
about 4 counts at the full cap, and usually none. Measured under systemd: at
5 counts a second over 3 restarts, and at 100 a second, every accepted count
was stored; the tests check this across two timer restarts at 20 a second.

### Logging

The `stats.irq.dk` server has `access_log off` **and**
`error_log /dev/null crit`. The second is needed because nginx's error log
records the client's IP address on rate-limit rejections, malformed requests,
closed connections and upstream errors. Keep both when editing the file.

nginx picks the `server` from the TLS name (SNI) and then from the `Host`
header. Two things happen before it knows a connection is for `stats.irq.dk`,
so they are handled and logged by the **default server for port 443** (one of
the other sites), with its log settings:

- a TLS handshake that fails before the client names `stats.irq.dk`; nginx
  logs most of these at level `info`, which the usual error log level
  (`error`) leaves out;
- a request with no `Host` header, or another site's, even over a connection
  made to `stats.irq.dk`. nginx treats it as a request for the default site.
  Browsers always send `Host: stats.irq.dk`, so the app's requests never take
  this path; hand-made requests might.

To keep these out of the logs too, leave nginx's main `error_log` at its
default level (`error`) or higher, and check what the default HTTPS site logs.
The tests run with another site as the default server and the error log at
`info`, and check that nothing from `stats.irq.dk`'s requests appears.

GoatCounter logs to the journal (`journalctl -u goatcounter`). Its only peer
is nginx on `127.0.0.1`; the visitor's address reaches it in the `X-Real-IP`
header and is kept with each count until the count is processed (every 10
seconds), and as a key in its rate limiter (see above). Its HTTP error messages name the method, URL, site and User-Agent
(nginx's fixed one), never the address. But when processing a count fails
(a site lookup or database error, or a panic), GoatCounter logs the whole
count, and in its default text format that **includes the IP address**. The
unit therefore runs it with **`-json`**: in JSON, GoatCounter leaves out the
address, User-Agent, location and time of a count. Keep `-json` when editing
the unit. Its request log (`-debug req`, off in the unit) skips `/count`
anyway. The tests force such a processing error and check that GoatCounter's
log has the error but not the address, and that none of the other client
addresses they used appear in its log or database.

### Bot detection

GoatCounter doesn't count a request it takes for a bot. Instead it keeps a
record of it (page, time, bot type, User-Agent) in its `bots` table for 30
days. Its detection ([isbot](https://github.com/arp242/isbot) v1.0.0 in
GoatCounter v2.7.0) looks at prefetch headers, the User-Agent and a `b`
parameter from GoatCounter's own script. For a few User-Agents it also looks
at the IP address (cloud and hosting providers). None of this applies here:

- nginx drops prefetch requests and bot-like User-Agents itself, and forwards
  no browser header and no `b` parameter;
- the fixed User-Agent nginx sends is "no match" for isbot, and that skips
  the IP check (isbot v1.0.0 checks IP ranges only for three specific
  User-Agents).

So a visitor on a cloud or VPN address is counted like anyone else, and the
`bots` table stays empty. The tests check both. They also check the isbot
version in the GoatCounter binary: after an upgrade that changes it, they fail
until this has been checked again (`isbotChecked` in `test/e2e`).

## 7. The weekly export

The export publishes to a separate repository,
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data), never to
this one. The page at `tkjaer.github.io/open-stats` shares an origin with the
apps, and it is built from this repository's code. So whoever controls the
server (its `open-stats` user or root) can publish numbers, but never code that
runs on that origin. That only holds while these stay true:

- **The deploy key is only on `open-stats-data`.** This repository has no
  deploy key with write access.
- **`open-stats-data` has GitHub Actions and Pages switched off**
  (*Settings → Actions → General → Disable actions*; no Pages site). With
  either on, a pushed workflow or HTML file could run or be served on
  `tkjaer.github.io`. Check both after any change to the repository.
- **This repository's `github-pages` environment only deploys from `main`**
  (*Settings → Environments → github-pages → Deployment branches and tags →
  Selected branches: `main`*). The workflow checks this too.
- **The page build treats every data file as untrusted.** It reads only
  `data/<project>/weekly/*.json` and `goatcounter-settings.json`, checks them
  strictly (including that a file's thresholds are no lower than the
  project's in `projects/*.yml`, so a file can't publish smaller numbers by
  declaring smaller thresholds) and escapes everything.

Create a deploy key that can only write to `open-stats-data`:

```sh
ssh-keygen -t ed25519 -N '' -C 'open-stats export' -f /etc/open-stats/deploy_key
chown open-stats:open-stats /etc/open-stats/deploy_key
chmod 600 /etc/open-stats/deploy_key
cat /etc/open-stats/deploy_key.pub
```

Add the public key on GitHub under **`open-stats-data`'s** *Settings → Deploy
keys*, with *Allow write access*. A deploy key works for that one repository
only.

Pin GitHub's SSH host keys (check them against
<https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints>):

```sh
ssh-keyscan -t ed25519,ecdsa,rsa github.com > /etc/open-stats/known_hosts
ssh-keygen -lf /etc/open-stats/known_hosts
```

Clone the data repository for the export to write into:

```sh
export GIT_SSH_COMMAND='ssh -i /etc/open-stats/deploy_key -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/etc/open-stats/known_hosts -o ConnectTimeout=30 -o ServerAliveInterval=15 -o ServerAliveCountMax=4'
runuser -u open-stats -- env GIT_SSH_COMMAND="$GIT_SSH_COMMAND" \
    git clone git@github.com:tkjaer/open-stats-data.git /var/lib/open-stats/repo
runuser -u open-stats -- git -C /var/lib/open-stats/repo config user.name 'open-stats export'
runuser -u open-stats -- git -C /var/lib/open-stats/repo config user.email '<you@example.org>'
```

`/etc/open-stats/env` was written by `configure`; see
[`env.example`](env.example) for what it may contain. It must stay owned by
root with mode 600 (systemd reads it before dropping privileges).

Install and test the units:

```sh
install -m 0644 collector/systemd/open-stats-export.service collector/systemd/open-stats-export.timer /etc/systemd/system/
systemctl daemon-reload
systemctl start open-stats-export.service    # exports last week now
journalctl -u open-stats-export.service -n 20
systemctl enable --now open-stats-export.timer
systemctl list-timers open-stats-export.timer
```

The export runs every Monday at 03:00 UTC (with up to 10 minutes of random
delay, and at the next boot if the server was off). It exports the ISO week
that ended on Sunday:

- it resets the clone to what's on GitHub first, so nothing left over locally
  is ever pushed;
- it writes `data/<project>/weekly/<week>.json` only if that file doesn't exist
  yet, and `data/<project>/goatcounter-settings.json` if the settings changed;
- it commits and pushes (three attempts);
- every git command is killed after one minute (`--git-timeout`), and ssh
  gives up on an unreachable or silent GitHub (`ConnectTimeout`,
  `ServerAlive*`), so a run ends within a few minutes; `TimeoutStartSec=15min`
  in the unit is the outer bound. A killed push changes nothing on GitHub (the
  branch is updated atomically), and the next run starts from what's there;
- if GoatCounter is restarting (its hourly restart refuses connections for
  under a second), it retries for up to 20 seconds;
- it checks every project's GoatCounter settings against `projects/*.yml`
  before writing anything. If any differ, it writes, commits and pushes
  nothing and exits with status 3; put them back with `open-stats configure`
  (step 5) and start the export again. It exits with 1 on other errors. Both
  show up as a failed unit.

To export a missed week by hand (only weeks that started at most 30 days ago;
older data may already be gone from GoatCounter):

```sh
systemctl start open-stats-export.service    # last week, or:
runuser -u open-stats -- env $(cat /etc/open-stats/env) GIT_SSH_COMMAND="$GIT_SSH_COMMAND" \
    open-stats export --repo /var/lib/open-stats/repo --week 2026-W40
```

## The dashboard

GoatCounter's dashboard and API are never exposed. To look at them, tunnel to
the server:

```sh
ssh -L 8081:127.0.0.1:8081 <server>
```

and open <http://how-the-internet-works.localhost:8081/> (one site per project:
`<project>.localhost`). Chrome and Firefox resolve `*.localhost` to your own
machine by themselves; for Safari, add `127.0.0.1 how-the-internet-works.localhost` to
`/etc/hosts`.

## Adding a project

1. In this repository: add `projects/<name>.yml` and one line to the
   allow-list `map` at the top of `nginx/stats.irq.dk.conf`. `go test ./...`
   checks that they agree.
2. Build and install the new binary (step 3) and nginx file (step 6), then
   `nginx -t && systemctl reload nginx`.
3. `open-stats configure <name>`: creates `<name>.localhost` linked to the
   first site (same login), and extends the export token to cover it.

The next export includes the new project.

## Upgrading

- **open-stats:** build, check and install the binary as in step 3. Nothing
  else to restart; the timer runs the new binary next time.
- **GoatCounter:** change [`goatcounter.version`](goatcounter.version) (version
  and the four sha256 sums from the release), run the tests, then on the server
  `bash collector/install-goatcounter.sh && systemctl restart goatcounter`.
  The unit runs database migrations automatically. Check the release notes for
  changes to how it counts, what it stores or what it logs first, and see
  [Bot detection](#bot-detection): the tests fail until it has been checked
  again for a new isbot version.

## Removing everything

```sh
systemctl disable --now open-stats-export.timer goatcounter-restart.timer goatcounter
rm /etc/nginx/sites-enabled/stats.irq.dk.conf /etc/nginx/sites-available/stats.irq.dk.conf
systemctl reload nginx
rm -rf /var/lib/goatcounter /var/lib/open-stats /etc/open-stats
rm /etc/systemd/system/goatcounter.service /etc/systemd/system/goatcounter-restart.* \
    /etc/systemd/system/open-stats-export.*
rm /usr/local/bin/goatcounter /usr/local/bin/open-stats
```

and delete the deploy key from `open-stats-data` on GitHub.
