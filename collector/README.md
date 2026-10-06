# Server setup

How to set up `stats.irq.dk` by hand (the server never pulls from here):
nginx, GoatCounter on `127.0.0.1:8081`, and `open-stats`, one static binary
for the weekly `export` and the per-project `configure`. Run as root on a
Debian-like server with systemd, nginx and git; `<placeholders>` are yours.

## 1. DNS and certificate

- `stats.irq.dk` must resolve to the server (a CNAME, or A and AAAA records,
  pointing at `<your server>`).
- Put its certificate at `/etc/ssl/stats.irq.dk/{fullchain,privkey}.pem`, or
  change those paths in the nginx file. The file has no port-80 server: if
  you use HTTP-01 challenges, the existing port-80 setup must cover
  `stats.irq.dk`.

## 2. Users and directories

```sh
adduser --system --group --home /var/lib/goatcounter --no-create-home goatcounter
adduser --system --group --home /var/lib/open-stats --no-create-home open-stats
install -d -o goatcounter -g goatcounter -m 0700 /var/lib/goatcounter
install -d -o open-stats -g open-stats -m 0700 /var/lib/open-stats
install -d -o root -g root -m 0755 /etc/open-stats
```

## 3. Binaries

On a machine with Go (the version in [`../go.mod`](../go.mod) or newer):

```sh
collector/build.sh      # dist/open-stats-linux-{amd64,arm64} and dist/SHA256SUMS
```

The build is reproducible: the same commit and Go version give the same
checksums. `open-stats version` prints the commit (with `-dirty` for
uncommitted changes). Copy the repository and `dist/` to the server, then:

```sh
cd /tmp/open-stats
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
(cd dist && sha256sum -c --ignore-missing SHA256SUMS)
install -m 0755 dist/open-stats-linux-$arch /usr/local/bin/open-stats
open-stats version
```

## 4. GoatCounter

```sh
bash collector/install-goatcounter.sh /usr/local/bin/goatcounter   # checks the pinned sha256 sums
install -m 0644 collector/systemd/goatcounter.service \
    collector/systemd/goatcounter-restart.service \
    collector/systemd/goatcounter-restart.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now goatcounter goatcounter-restart.timer
curl -s http://127.0.0.1:8081/status    # {"version":"v2.7.0", ...}
```

The unit sandboxes GoatCounter, caps its memory and runs it with `-json`
([Logging](#logging)) and `-ratelimit`
([rate limiter](#goatcounters-rate-limiter)).

## 5. Create the project's site

```sh
open-stats configure -admin-email <you@example.org> how-the-internet-works
```

This creates the site `how-the-internet-works.localhost` and the dashboard
login (it asks for a password, or reads `OPEN_STATS_ADMIN_PASSWORD`), applies
and checks the settings from [`../projects/`](../projects/), and writes a
read-only API token for the export to `/etc/open-stats/env` (mode 600). It is
safe to run again and fixes drifted settings. It sets **collect: country
only**, **retention: 31 days**, and a private dashboard. Keep the dashboard
user's time zone at **UTC**; the export refuses to run otherwise.

### What GoatCounter stores

Each count (bots are skipped) is added to these tables:

| Table | One row per | Holds |
|---|---|---|
| `hit_counts` | path, hour | the count |
| `location_stats` | path, day, country | the count |
| `hit_stats` | path, day | the count per hour |
| `ref_counts` | path, hour, referrer | the count; the referrer is always empty |
| `size_stats` | path, day, screen width | the count; the width is always 0 |
| `language_stats` | path, day, browser language | the count; the language is always empty |

The last four are copies of `hit_counts`. GoatCounter fills them whatever it
collects, after blanking the fields it doesn't collect
([memstore.go](https://github.com/arp242/goatcounter/blob/v2.7.0/memstore.go#L268-L301),
[cron/tasks.go](https://github.com/arp242/goatcounter/blob/v2.7.0/cron/tasks.go#L138-L148)).
`browser_stats`, `system_stats`, `campaign_stats`, `hits` (individual
pageviews) and `bots` stay empty. Retention covers all of them
([`Site.DeleteOlderThan`](https://github.com/arp242/goatcounter/blob/v2.7.0/site.go#L647-L716)).
The e2e test checks all of this.

## 6. nginx

```sh
nginx -T 2>/dev/null | grep -E '^\s*listen' | grep 443
nginx -T 2>/dev/null | grep -E '^\s*server_name' | grep -F 'stats.irq.dk'   # must find nothing yet
install -m 0644 collector/nginx/stats.irq.dk.conf /etc/nginx/sites-available/
ln -s /etc/nginx/sites-available/stats.irq.dk.conf /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx
curl -si 'https://stats.irq.dk/how-the-internet-works/count?lang=en'    # HTTP/2 204, nothing else
```

### Listen addresses and the other sites

- If the other sites listen on specific addresses (e.g.
  `listen 192.0.2.1:443 ssl;`, `listen [2001:db8::1]:443 ssl;`), use the same
  addresses in this file's two `listen` lines. Otherwise nginx never picks
  this server for those addresses, and requests get another site's answer
  (e.g. a 404).
- No other `server` block may name `stats.irq.dk` (e.g. one left over from
  issuing the certificate).
- Never make this server the default; [Logging](#logging) relies on another
  site being the default for each address.

### Rate limit

nginx forwards at most **10 counts a second for all projects together**
(about 860,000 a day), after a burst of 200. Its key is the server name, not
the IP address, and there is no per-IP limit, so a class behind one address
counts in full. Measured on one core ([`test/load/run.sh`](../test/load/run.sh)),
10 new TLS connections a second cost nginx about 2% and GoatCounter about 1%
(at most 59 MB), and every count let through was counted.

### GoatCounter's rate limiter

GoatCounter limits `/count` per client IP address and User-Agent, by default
to 4 a second
([handlers/backend.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/backend.go#L95-L109),
[handlers/mw.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/mw.go#L404-L415),
[handlers/handlers.go](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/handlers.go#L37-L45)).
nginx sends one fixed User-Agent, so the unit sets `-ratelimit=count:1000/1`
to make sure it never applies. It can't be switched off, though, and it keys
on the visitor's address (from `X-Real-IP`,
[`mware.RealIP()`](https://github.com/arp242/goatcounter/blob/v2.7.0/handlers/backend.go#L52)).
Its store ([go-limiter](https://github.com/sethvargo/go-limiter) v1.1.0
`memorystore`) keeps, **in memory only**, the key (the address and nginx's
User-Agent), when it was first seen, the second of its last count and the
tokens left: nothing about the page or language, and never on disk or in a
log. Left alone, it would keep an address for 12 to 18 hours, with no setting
to shorten that
([memorystore/store.go](https://github.com/sethvargo/go-limiter/blob/v1.1.0/memorystore/store.go#L85-L92)).

So **GoatCounter restarts every hour**, which empties the store: the
[timer](systemd/goatcounter-restart.timer) runs `systemctl try-restart
goatcounter` at half past every hour (UTC). An address is kept **at most one
hour, plus the moment a restart takes**. At the cap, that's about 36,000
entries (15 MB). `try-restart` leaves a stopped GoatCounter stopped, and the
restart doesn't wait for the export.

On `SIGTERM`, GoatCounter finishes its requests and stores every buffered
count
([zhttp](https://github.com/arp242/zhttp/blob/9a43cabb6d05/serve.go#L126-L145),
[serve.go](https://github.com/arp242/goatcounter/blob/v2.7.0/cmd/goatcounter/serve.go#L361-L378),
[memstore.go](https://github.com/arp242/goatcounter/blob/v2.7.0/memstore.go#L200-L209)),
unless its regular 10-second store is running at that moment
([bgrun.go](https://github.com/arp242/goatcounter/blob/v2.7.0/pkg/bgrun/bgrun.go#L155-L159)).
Counts sent in the 0.4 s it isn't listening get their 204 but aren't counted:
about 4 at the full cap, usually none.

### Logging

Keep both `access_log off` **and** `error_log /dev/null crit`: nginx's error
log would otherwise record client addresses.

Before nginx knows a connection is for `stats.irq.dk`, the **default server
for that address and port** (another site) handles it, with its own logs: a
TLS handshake that fails before naming `stats.irq.dk` (mostly logged at
`info`), and a request without `Host: stats.irq.dk` (browsers always send
it). Keep nginx's main `error_log` at `error` or higher, and check what the
default HTTPS site logs.

GoatCounter logs a count that fails to store; in its default format that
includes the IP address, so the unit runs it with **`-json`**, which doesn't.

### Bot detection

GoatCounter's [isbot](https://github.com/arp242/isbot) v1.0.0 records
suspected bots in the `bots` table, checking the IP address only for three
User-Agents. nginx drops bots and prefetches itself and sends a fixed
User-Agent that isbot doesn't match, so `bots` stays empty and cloud or VPN
visitors are counted. The tests fail on a new isbot version until this is
checked again (`isbotChecked` in `test/e2e`).

## 7. The weekly export

The export pushes to [open-stats-data](https://github.com/tkjaer/open-stats-data),
never here. The page, which shares an origin with the apps, is built from this
repository, so the server can publish numbers but never code, as long as:

- the deploy key is only on `open-stats-data`;
- `open-stats-data` has GitHub Actions and Pages switched off;
- this repository's `github-pages` environment only deploys from `main`;
- the page build treats every data file as untrusted
  ([data format](../docs/data-format.md#rules)).

```sh
ssh-keygen -t ed25519 -N '' -C 'open-stats export' -f /etc/open-stats/deploy_key
chown open-stats:open-stats /etc/open-stats/deploy_key
chmod 600 /etc/open-stats/deploy_key
cat /etc/open-stats/deploy_key.pub   # add to open-stats-data's Deploy keys, with write access
ssh-keyscan -t ed25519,ecdsa,rsa github.com > /etc/open-stats/known_hosts
ssh-keygen -lf /etc/open-stats/known_hosts   # compare with GitHub's (link below)

export GIT_SSH_COMMAND='ssh -i /etc/open-stats/deploy_key -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/etc/open-stats/known_hosts -o ConnectTimeout=30 -o ServerAliveInterval=15 -o ServerAliveCountMax=4'
runuser -u open-stats -- env GIT_SSH_COMMAND="$GIT_SSH_COMMAND" \
    git clone git@github.com:tkjaer/open-stats-data.git /var/lib/open-stats/repo
runuser -u open-stats -- git -C /var/lib/open-stats/repo config user.name 'open-stats export'
runuser -u open-stats -- git -C /var/lib/open-stats/repo config user.email '<you@example.org>'

install -m 0644 collector/systemd/open-stats-export.service collector/systemd/open-stats-export.timer /etc/systemd/system/
systemctl daemon-reload
systemctl start open-stats-export.service && journalctl -u open-stats-export.service -n 20
systemctl enable --now open-stats-export.timer
```

Check the host keys against
[GitHub's fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints).
`/etc/open-stats/env` (from `configure`; see [`env.example`](env.example))
must stay root-owned with mode 600.

The export runs Mondays at 03:00 UTC (up to 10 minutes later, or at the next
boot) for the ISO week that ended on Sunday. It resets the clone to GitHub's
state, writes each week's file only if it doesn't exist, commits and pushes.
Each git command is killed after a minute, ssh gives up on a silent GitHub,
and `TimeoutStartSec=15min` bounds the whole run; a killed push changes
nothing. It exits with 3, publishing nothing, if GoatCounter's settings
differ from `projects/*.yml` (fix with `open-stats configure`), and 1 on
other errors.

A missed week can be exported by hand while it is within retention (started
at most 30 days ago):

```sh
runuser -u open-stats -- env $(cat /etc/open-stats/env) GIT_SSH_COMMAND="$GIT_SSH_COMMAND" \
    open-stats export --repo /var/lib/open-stats/repo --week 2026-W40
```

## The dashboard

Run `ssh -L 8081:127.0.0.1:8081 <server>` and open
<http://how-the-internet-works.localhost:8081/> (Safari needs that name in
`/etc/hosts`).

## Adding a project

1. Add `projects/<name>.yml` and its path to the allow-list `map` in
   `nginx/stats.irq.dk.conf` (`go test ./...` checks they agree).
2. Install the new binary (step 3) and nginx file (step 6), and reload nginx.
3. Run `open-stats configure <name>`. The next export includes it.

**A new language** needs no server change: nginx forwards every two-letter
code. To give it its own published column, add it to `publish.languages` and
install the new binary. Earlier weeks keep it in `other` (shown as "–"). Never
remove a published language from the list; the page would reject those weeks.

## Upgrading

- **open-stats:** install the new binary (step 3).
- **GoatCounter:** update [`goatcounter.version`](goatcounter.version) and
  run the tests (they fail on a new isbot version until
  [Bot detection](#bot-detection) is checked again). Read the release notes
  for changes to what it stores or logs. Then
  `bash collector/install-goatcounter.sh && systemctl restart goatcounter`.

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

Then delete the deploy key from `open-stats-data`.
