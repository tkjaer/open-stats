# open-stats

Privacy-respecting visit counts for a few small sites, collected in the open.
This repository holds the server configuration, the weekly export and the
static page; the numbers are in
[open-stats-data](https://github.com/tkjaer/open-stats-data) and shown at
<https://tkjaer.github.io/open-stats/>. The first project is
[How the Internet Works](https://tkjaer.github.io/how-the-internet-works/)
([privacy notice](https://github.com/tkjaer/how-the-internet-works/blob/main/docs/privacy.md)).

## What the browser sends

`GET https://stats.irq.dk/how-the-internet-works/count?lang=en`, once per
page load, with no cookie or referrer. The only data is the app's language,
as a two-letter lowercase code; like any request, it also carries the IP
address and standard browser headers. The answer is always an empty `204`.

## What the server keeps

[nginx](collector/nginx/stats.irq.dk.conf) writes **no access or error log**
for `stats.irq.dk`. It **drops** anything but
`GET /<project>/count?lang=<two letters>`, requests with **GPC** or **DNT**,
obvious bots (by User-Agent) and browser prefetches. It **caps** the rest at
10 a second for all projects together (burst 200; about 860,000 a day), with
no limit per IP address. It passes on only the path (`/en`) and the IP
address to a self-hosted [GoatCounter](https://www.goatcounter.com).

GoatCounter collects **country only**. It looks up the country from the IP
address in memory and adds one to two counters: page loads per language per
hour, and per country and language per day. It also keeps copies of the same
counts in a few other tables, with the screen-size, browser-language and
referrer fields left empty
([details](collector/README.md#what-goatcounter-stores)). No IP address,
browser details or row per visit is stored, but a counter at 1 describes one
visit. The counters and their copies are **deleted after 31 days**. The
dashboard is only reachable over an SSH tunnel.

## What is published

Every Monday, last week's numbers, as three separate tables by day (UTC):

- **page loads**;
- **language**: a column per language listed in [`projects/`](projects/),
  and `other` for every other code;
- **country**: countries with **fewer than 5** page loads that day, and
  unknown ones, go into `other`.

On **quiet days, with fewer than 20 page loads**, only the total is
published. There are no language-by-country and no weekly country numbers.
Files are never rewritten and are kept for good. Nothing is published while
GoatCounter's settings differ from `projects/*.yml`.

## Limits: what this can and can't promise

- **You can't verify the server.** The tests show what this configuration
  does; nobody outside can check that the server runs it.
- **The numbers are approximate.** Reloads count; visitors with GPC, DNT, a
  blocker or "Count my visit" off don't; nor does the excess over the cap, or
  what's sent during GoatCounter's hourly restart (under half a second: about
  4 at the full cap, usually none). GeoIP countries are sometimes wrong.
- **Counts can be faked.** One script could inflate them by up to the cap of
  10 a second, crowding out real visits while it does.
- **Tables can be combined.** On days with 20 or more page loads, language and
  country together can still say something about countries with 5 or more (if
  all came from one country, so did their languages).
- **Where an IP address could still appear:**
  - in GoatCounter's memory with its count, until the count is stored (within
    about 10 seconds); a count that fails is logged without it;
  - as a key in GoatCounter's rate limiter, **in memory only, for at most one
    hour (plus the moment a restart takes)**, as GoatCounter restarts hourly.
    The key is never on disk or in a log and isn't linked to the page or
    language ([details](collector/README.md#goatcounters-rate-limiter));
  - in the logs of the server's default site (the server hosts other sites),
    for a TLS handshake that fails before naming `stats.irq.dk` or a request
    without `Host: stats.irq.dk`, which browsers always send
    ([details](collector/README.md#logging));
  - like for any server, the network and hosting provider see connections.

## More

- [Server setup](collector/README.md), including
  [adding a project](collector/README.md#adding-a-project)
- [Data format](docs/data-format.md)
- [Development and tests](test/README.md)
- [Security](SECURITY.md)
- Run by Thomas Kjær Aabo (htiw@thomaskjaer.com), the controller in GDPR
  terms; hosted by Hetzner Online GmbH in Germany.
- Code: [AGPL-3.0-or-later](LICENSE). Data:
  [CC0 1.0](https://github.com/tkjaer/open-stats-data/blob/main/LICENSE).
