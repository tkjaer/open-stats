# Security

open-stats is the visit counter behind `stats.irq.dk`: an nginx configuration,
a self-hosted GoatCounter, a weekly export and a static page. Security and
privacy problems are possible in any of them.

In scope:

- the collector configuration in [`collector/`](collector/): the nginx site,
  the GoatCounter and export systemd units, and the setup guide;
- the export (`open-stats export`) and `open-stats configure`;
- the page build (`open-stats site`, [`site/`](site/)) and the data checks it
  applies;
- the GitHub Actions workflows in [`.github/workflows/`](.github/workflows/).

Problems with the published numbers in
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data) belong
here too.

## Reporting a problem

Please **don't open a public issue**. Report it privately through
[GitHub's private vulnerability reporting](https://github.com/tkjaer/open-stats/security/advisories/new).
I'll reply as soon as I can and credit you in the fix unless you'd rather not be named.

## Supported versions

Only the current `main` branch gets fixes.
