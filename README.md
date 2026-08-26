# usage-metrics

A tiny, first-party, self-hosted usage-metrics service for household apps —
aggregate, anonymous event counters, nothing else.

## Why

We need to know which app surfaces actually get used (first driver: native
home-screen widgets, solaris-android#69) — without building per-user analytics
or shipping data to a third party. This is a small, shared service so any
household app can report into the same place from day one, under one privacy
and retention policy.

## What

- **Ingest**: `POST /ingest` — aggregate increments only, e.g.
  `{"app": "solaris", "event": "widget.tasks.compose", "day": "2026-07-23", "count": 1}`.
  LAN/token-internal only — never internet-exposed, never called directly by a
  client app (see Architecture below).
- **Storage**: SQLite in WAL mode, counters keyed `(app, event, day)`, upserted
  with the increment. No content, no uid, no request bodies.
- **Retention**: counters older than the window (default 90 days, configurable)
  are deleted, not archived or rolled up. Fully deletable.
- **Readout**: `GET /summary` — counts per `app` × `event` over the last N days,
  as JSON. Enough to answer "is this used", not a BI tool.
- **Multi-app from day one**: the `app` dimension namespaces callers. A new
  caller just starts POSTing with its own `app` id.

## Non-goals

No Prometheus/Grafana/timeseries stack, no dashboards-as-a-product, no
per-user analytics, no impression/glance tracking, no third-party pipeline.

## Architecture

Client apps do **not** call this service directly. Per the Solaris-Hub BFF
model (mdopp/solarisbay ADR 0010), an app talks only to its own backend (e.g.
the Solaris Engine); that backend forwards aggregate increments here. First
consumer: Solaris forwards widget `src=widget.*` hits (mdopp/solarisbay#1026).

Deployed as a ServiceBay-managed Quadlet service — see `templates/usage-metrics/`.

## API

### `POST /ingest`

`Content-Type: application/json`, body exactly:

```json
{"app": "solaris", "event": "widget.tasks.compose", "day": "2026-07-23", "count": 1}
```

- `app`, `event` — `[a-z0-9][a-z0-9._-]*` (64 / 128 chars max). Lower-case only,
  so case variants cannot fragment one counter across several rows.
- `day` — a calendar date, exactly `YYYY-MM-DD`.
- `count` — a positive integer.

Anything else is rejected with a `400` and a JSON `{"error": ...}` explaining
why: an unknown field, a free-form payload, a missing field, a malformed date.
The validation is deliberately strict — a request that changes nothing must not
answer as if it did. A non-POST gets `405`, a non-JSON content type `415`.

On success the response carries the stored total, which is what makes a write
verifiable by the caller:

```json
{"app": "solaris", "event": "widget.tasks.compose", "day": "2026-07-23", "total": 4}
```

### `GET /summary`

Counts per `app` × `event` over the last N days.

`?days=N` — how many calendar days to cover, counting today as the first.
Defaults to the retention window, so asked nothing it shows everything the box
still holds. Must be an integer between 1 and 3650; anything else is a `400`.
A non-GET gets `405`.

```json
{
  "days": 5,
  "from": "2026-07-19",
  "total": 33,
  "apps": [
    {"app": "photos", "total": 16, "events": [
      {"event": "album.view", "count": 12},
      {"event": "upload.done", "count": 4}
    ]},
    {"app": "solaris", "total": 17, "events": [
      {"event": "widget.open", "count": 10},
      {"event": "widget.tasks.compose", "count": 7}
    ]}
  ],
  "knownApps": ["photos", "recipes", "solaris"]
}
```

`from` is the oldest day covered — **the same window retention uses**
(`windowStart` in `window.go` is the one definition both call), so the readout
covers exactly the days that still exist.

`apps` lists only what was counted inside the window; an app with nothing this
window is simply absent. `knownApps` lists every app the database still holds a
counter for, whatever its day, which is what keeps an empty readout unambiguous:

- `apps: []`, `knownApps: []` — nothing has ever reported.
- `apps: []`, `knownApps: ["solaris"]` — solaris has reported before, just not
  inside this window.

Both are always JSON arrays, never `null`.

It is a JSON endpoint and not an HTML page on purpose: the reader today is the
operator, with `curl` or their own dashboard. A resident-facing page would have
to be native ServiceBay UI (`service-ui-design-standard` — design tokens,
mobile-first, user-language state text), which is a much bigger surface than the
readout is currently worth. If a resident ever needs to read this, that page is
the change to make then.

### `GET /healthz`

`200 ok` — the ServiceBay install gate. `503` with a reason when the retention
sweep is failing or has stopped running (see Retention).

## Configuration

| Env | Default | Meaning |
| --- | --- | --- |
| `USAGE_METRICS_DB_PATH` | `/data/usage-metrics.db` | SQLite file. Must live on the mounted volume (`{{DATA_DIR}}/usage-metrics/`), never the container fs, which is wiped on every pod recreate. |
| `USAGE_METRICS_RETENTION_DAYS` | `90` | How many calendar days of counters to keep. Must be an integer >= 1; a malformed value stops the service from starting rather than falling back to the default. |

The service listens on `:8080`.

## Retention

Counters live for **90 days by default**, configurable with
`USAGE_METRICS_RETENTION_DAYS`.

The window is a count of calendar days in **UTC, with today as the first day**:
at `N = 90`, the oldest day kept is today minus 89, a row dated exactly on that
day survives, and everything before it is deleted. Days ahead of today (a caller
with a skewed clock) are left alone.

Old counters are **deleted, not rolled up** — a rolled-up total is still a record
of activity from a day we promised to forget. There is no soft-delete flag and no
archive table: after a sweep the rows are gone from the database.

The sweep runs once at boot, before the service accepts a write, and then every
24 hours. It is a pure function of (today, window), so running it twice deletes
nothing the second time. If a sweep fails, or the loop stops ticking, `/healthz`
turns `503` — a service that keeps ingesting while it has stopped forgetting is
not healthy, and ServiceBay's health check is what makes that visible.

## Status

Early scaffold — ingest, storage, retention and the summary readout are in; see
the repo's issues for the rest of the build-out plan.

## Origin

mdopp/servicebay#2366
