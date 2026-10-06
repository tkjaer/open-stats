# Published data, format version 2

Written by `open-stats export` to
[tkjaer/open-stats-data](https://github.com/tkjaer/open-stats-data) under
[CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/):

```
data/<project>/weekly/YYYY-Www.json      one file per ISO week (UTC)
data/<project>/goatcounter-settings.json GoatCounter's settings at the last export
```

A missing week was not exported (the server was down, say); it doesn't mean
zero page loads.

## Weekly file

Shortened to two of the seven days:

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

- `period`: the ISO week, Monday to Sunday in UTC; `id` matches the file name.
- `source`: the GoatCounter version, what it collected and its retention.
- `page_loads`: one row for each of the seven days, in order, including 0.
- `language`: one key per language in the project's `publish.languages`,
  plus `other` for every other code (never read separately from
  GoatCounter). Weeks published before a language was added have no key for
  it: treat a missing key as "not broken out", not 0.
- `country`: ISO 3166-1 alpha-2 codes from GoatCounter's GeoIP database;
  `other` holds countries under `min_count` and unknown countries.

## Rules

- `language` and `country` have rows only for days with at least
  `min_day_total` (20) page loads. A quiet day appears only in `page_loads`:
  otherwise 5 page loads, all from Denmark and 1 in Danish, would single out
  a visit.
- Every named country has at least `min_count` (5) page loads that day.
  `other` is always present, even if small or 0.
- Each `language` and `country` row adds up to that day's page loads.
- Nothing is finer than a day, and nothing is per path, visitor or visit.
  Language and country are separate tables, and there are no weekly numbers
  per country (they would reveal the days a country was in `other`).
- Counts are integers from 0 to 1,000,000,000; key order means nothing.
- Files are written once and never rewritten by the export; a correction by
  hand would show in the git history. They are kept for good.
- The page build rejects any file that breaks these rules, has an unknown or
  missing key or a wrong type, or declares a `min_count` or `min_day_total`
  below its project's in [`projects/`](../projects/) (stricter is fine). See
  [`internal/dataformat`](../internal/dataformat/dataformat.go).
- `granularity` is always `day` and `period.kind` always `iso-week` for now;
  readers should refuse values they don't know. Version 2 added
  `min_day_total`; no version 1 files were published.

## Settings file

The GoatCounter site's settings at the last export, without secrets.
Rewritten only when they change, so the history shows each change seen at an
export. Nothing is published while they differ from the project's file.
`collect` is GoatCounter's setting by name; `public: "private"` means the
dashboard needs a login.

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
