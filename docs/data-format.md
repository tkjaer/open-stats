# Published data, format version 2

The data lives in its own repository,
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data), under
`data/`. Everything there is written by `open-stats export` on the server
(see [`collector/`](../collector/)) and is released under
[CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/).

```
data/<project>/weekly/YYYY-Www.json      one file per ISO week (UTC)
data/<project>/goatcounter-settings.json GoatCounter's settings at the last export
```

Weekly files are written once, after the week is over, and never rewritten
by the export. If one is ever corrected by hand, the git history shows it.
Published files and their history are kept indefinitely. Weeks with no file
were not exported (the server was down, say); they are not weeks with zero
page loads.

## Weekly file

An example, shortened to two of the seven days:

```json
{
  "format": "https://github.com/tkjaer/open-stats/blob/main/docs/data-format.md",
  "version": 2,
  "project": "how-the-internet-works",
  "period": {
    "kind": "iso-week",
    "id": "2026-W40",
    "first_day": "2026-09-28",
    "last_day": "2026-10-04",
    "timezone": "UTC"
  },
  "generated_at": "2026-10-05T03:00:00Z",
  "source": {
    "collector": "goatcounter",
    "goatcounter_version": "v2.7.0",
    "collect": ["country"],
    "data_retention_days": 31
  },
  "tables": {
    "page_loads": {
      "granularity": "day",
      "rows": [
        { "day": "2026-09-28", "count": 104 },
        { "day": "2026-09-29", "count": 12 }
      ]
    },
    "language": {
      "granularity": "day",
      "min_day_total": 20,
      "rows": [
        { "day": "2026-09-28", "counts": { "da": 34, "en": 70, "other": 0 } }
      ]
    },
    "country": {
      "granularity": "day",
      "min_day_total": 20,
      "min_count": 5,
      "rows": [
        { "day": "2026-09-28", "counts": { "DE": 101, "other": 3 } }
      ]
    }
  }
}
```

| Field | Meaning |
|---|---|
| `format`, `version` | This document, and the format version (2). A change that existing readers can't handle gets a new version. |
| `project` | The project's slug, as in `projects/<slug>.yml` and the directory name. |
| `period` | The ISO week the file covers: Monday to Sunday, in UTC. `id` matches the file name. |
| `generated_at` | When the export ran (UTC). |
| `source` | Where the numbers come from: the GoatCounter version, what it was set to collect, and how long it kept data. |
| `tables` | Three separate tables, by day. |

### The tables

- **`page_loads`**: page loads per day. One row for each of the seven days,
  in order, including days with 0.
- **`language`**: page loads per day by the language the app was opened in.
  The app sends the language as a two-letter lowercase code. The keys are
  the project's languages (`publish.languages` in `projects/<slug>.yml`) plus
  `other`, which holds every other code together: it is the day's page loads
  minus the listed languages, so a code without its own column is never
  named, and its own count is never even read from GoatCounter. Each row adds
  up to that day's page loads. A week published before a language was added
  to the list has no key for it (its page loads are in `other`); readers
  should treat a missing key as "not broken out", not as 0.
- **`country`**: page loads per day by country (ISO 3166-1 alpha-2 codes, as
  looked up by GoatCounter's built-in GeoIP database). A country is only named
  on a day when it has at least `min_count` page loads that day. Everything
  else is in `other`: countries below `min_count` and page loads with an
  unknown country. `other` is always present and shown as it is, even if it is
  small or 0. Each row adds up to that day's page loads.

**Quiet days.** `language` and `country` have a row only for the days with at
least `min_day_total` page loads (the same number in both tables), in order.
A day with fewer page loads in total (a quiet day) appears only in
`page_loads`. On a small day the separate tables could otherwise be combined
to single out a visit: a day with 5 page loads, all 5 from Denmark and 1 of
them in Danish, says that one Danish-language visit came from Denmark.

### What is guaranteed

Language and country are published as separate tables, never as one table of
language by country. Taken together they can still say something about a
combination, so the rules above limit what they can say:

- only days with at least `min_day_total` (20) page loads have any language
  or country numbers;
- on those days, every named country has at least `min_count` (5) page loads;
  smaller ones are only in `other`;
- there is nothing finer than a day, and nothing per path, visitor or visit.

The rules don't make such combinations impossible. If all of a day's page
loads came from one country, that day's language split is that country's too,
even when one language has a single page load. They make sure this can only
happen on a day with at least `min_day_total` page loads in total, and for a
country with at least `min_count` page loads that day.

There are deliberately **no weekly or other multi-day numbers per country**,
here or on the page. With them, the days a country was folded into `other`
could be worked out. Summing a country over the days it is named gives a
number that leaves out the other days, so it is not that country's total.

### Reading the files

- Key order in objects means nothing (the export writes map keys
  alphabetically).
- All counts are integers from 0 to 1,000,000,000.
- The page is built with strict checks: a file with an unknown or missing
  key, a wrong type, a day that doesn't add up, a country under `min_count`, a
  breakdown for a quiet day (or none for a day that isn't one), a
  `min_count` or `min_day_total` below the project's own in
  [`projects/<name>.yml`](../projects/) (stricter is fine) or anything else
  unexpected stops the build. The rules are in
  [`internal/dataformat`](../internal/dataformat/dataformat.go).

### Changes

- **Version 2** added `min_day_total` and dropped the language and country
  rows of quiet days. Version 1 had all seven days in every table; no version
  1 files were ever published.

### Room for later

`granularity` is `"day"` for every table today. The same structure can hold
other periods and granularities later (a `period.kind` other than `iso-week`
or a `granularity` other than `day`) without changing what version 2 means.
Readers should refuse values they don't know.

## Settings file

`goatcounter-settings.json` holds the settings the GoatCounter site had at the
last export, without anything secret (the site's secret and ignored IP
addresses are left out). The export only publishes when they match the
project's file in `projects/`; otherwise it publishes nothing at all. The
file is rewritten when they change, so the git history shows the settings
observed at each export. A change made and undone between two exports can't
be seen.

```json
{
  "format": "https://github.com/tkjaer/open-stats/blob/main/docs/data-format.md",
  "version": 2,
  "project": "how-the-internet-works",
  "goatcounter": {
    "version": "v2.7.0",
    "site": "how-the-internet-works.localhost",
    "collect": ["country"],
    "collect_bitmask": 16,
    "data_retention_days": 31,
    "public": "private",
    "allow_counter": false,
    "allow_embed": ""
  }
}
```

`collect` is GoatCounter's `collect` setting, by name; `public: "private"`
means the dashboard needs a login (and is only reachable over SSH anyway).
The settings file's shape is the same as in version 1.
