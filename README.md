# usage-metrics

A tiny, first-party, self-hosted usage-metrics service for household apps —
aggregate, anonymous event counters, plus the sentences the household's
assistants could not answer, kept 30 days and never who said them.

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
- **Unanswered sentences** (owner decision 2026-10-16, #13): `POST /miss` —
  the wording of what the Bote or the voice control could not answer, its
  source and the day. No speaker, no sender, no device: the caller strips
  who asked before it sends. Kept **30 days**, enforced here, readable with
  the token only (`GET /misses`).

## Non-goals

No Prometheus/Grafana/timeseries stack, no dashboards-as-a-product, no
per-user analytics, no impression/glance tracking, no third-party pipeline,
no free text other than the unanswered sentences below.

## Architecture

Client apps do **not** call this service directly. Per the Solaris-Hub BFF
model (mdopp/solarisbay ADR 0010), an app talks only to its own backend (e.g.
the Solaris Engine); that backend forwards aggregate increments here. First
consumer: Solaris forwards widget `src=widget.*` hits (mdopp/solarisbay#1026).

Deployed as a ServiceBay-managed Quadlet service — see `templates/usage-metrics/`:
isolated netns with an explicit `hostPort` (ADR 0007), the counter database on a
`{{DATA_DIR}}/usage-metrics` bind mount, and the token injected from a
`type: "secret"` template variable.

## API

Every data endpoint (`/ingest`, `/summary`, `/miss`, `/misses`) requires the
service token:

```
Authorization: Bearer <USAGE_METRICS_TOKEN>
```

Anything else — no header, the wrong token, another scheme — is a `401` with a
`WWW-Authenticate: Bearer` challenge. There is deliberately **no SSO** here: the
callers are other services' backends, not residents with a browser session, so
per ServiceBay ADR 0009 the credential is a scoped service token. The service
refuses to start when `USAGE_METRICS_TOKEN` is unset rather than serve those
routes open. `/healthz` is the one open route (see below).

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

### `POST /miss`

An unanswered sentence: what the Bote or the voice control could not help
with, so the household's next features can follow what was actually asked.
`Content-Type: application/json`, body exactly:

```json
{"source": "bote", "text": "Wann kommt der Müll?", "day": "2026-10-16"}
```

- `source` — who could not answer, not who asked: `[a-z0-9][a-z0-9._-]*`
  (64 chars max), e.g. `bote`, `stimme`.
- `text` — the sentence, trimmed: 1 to 280 characters, one line, no control
  characters, valid UTF-8. Callers collapse newlines and tabs first. **No
  speaker in it**: the caller sends the sentence, never the name or number
  of whoever said it.
- `day` — `YYYY-MM-DD`, inside the last 30 days, today included. An older day
  would be deleted at once, and a day ahead of today is a clock error; both
  are a `400` and stored nowhere.

There is no other field. Anything else — a `user`, a `sender`, a `count` —
is a `400`, the same strictness as `/ingest`.

The same sentence from the same source on the same day counts up instead of
adding a row. The answer carries that day's count:

```json
{"source": "bote", "day": "2026-10-16", "count": 3}
```

A source holds at most **200 different sentences per day**; past that a new
sentence is a `429` with a JSON `error` and is not stored, while one already
there still counts up. A mis-pointed caller cannot turn this into a text
dump. A non-POST gets `405`, a non-JSON content type `415`.

### `GET /misses`

The unanswered sentences of the last N days, per source and sentence, most
frequent first (then the most recent, then alphabetically), at most 100.

`?days=N` — 1 to 30, default 30, counted like `/summary` (UTC, today the
first day). Anything else is a `400`; a non-GET gets `405`.

```json
{
  "days": 30,
  "from": "2026-09-17",
  "total": 12,
  "misses": [
    {"source": "bote", "text": "Wann kommt der Müll?", "count": 9, "lastDay": "2026-10-16"},
    {"source": "stimme", "text": "Mach das Licht im Keller an", "count": 3, "lastDay": "2026-10-12"}
  ]
}
```

`total` counts every miss in the window, also those past the 100 listed.
`misses` is always an array, never `null`. A row older than its 30 days is
never listed, also between two sweeps.

### The read interface is a contract

`GET /summary?days=` and `GET /misses?days=`, with the bearer token, are the
stable read interface. Haus's Box page reads them, and so does the Operator's
weekly usage suggestions. Their shapes are a contract: fields are only ever
added, never renamed or removed.

### `GET /healthz`

`200 ok` — the ServiceBay install gate. `503` with a reason when the retention
sweep is failing or has stopped running (see Retention).

Unauthenticated on purpose: ServiceBay polls it from the host and its probe
cannot attach a header, and the endpoint discloses only whether the sweep is
still running — no counters, no app names.

## Configuration

| Env | Default | Meaning |
| --- | --- | --- |
| `USAGE_METRICS_DB_PATH` | `/data/usage-metrics.db` | SQLite file. Must live on the mounted volume (`{{DATA_DIR}}/usage-metrics/`), never the container fs, which is wiped on every pod recreate. |
| `USAGE_METRICS_RETENTION_DAYS` | `90` | How many calendar days of counters to keep (the unanswered sentences keep 30, whatever this says). Must be an integer >= 1; a malformed value stops the service from starting rather than falling back to the default. |
| `USAGE_METRICS_TOKEN` | — (required) | Service token for `/ingest` + `/summary`. Generated per install by the ServiceBay wizard (`type: "secret"`), never baked into the image. Unset means the service does not start. |

The service listens on `:8080`.

## Retention

Counters live for **90 days by default**, configurable with
`USAGE_METRICS_RETENTION_DAYS`. Unanswered sentences live for **30 days**,
fixed (owner decision 2026-10-16): the setting does not widen them.

The window is a count of calendar days in **UTC, with today as the first day**:
at `N = 90`, the oldest day kept is today minus 89, a row dated exactly on that
day survives, and everything before it is deleted. Days ahead of today (a caller
with a skewed clock) are left alone.

Old counters and sentences are **deleted, not rolled up** — a rolled-up total is still a record
of activity from a day we promised to forget. There is no soft-delete flag and no
archive table: after a sweep the rows are gone from the database.

The same sweep takes both, each against its own window. It runs once at boot,
before the service accepts a write, and then every 24 hours. It is a pure function of (today, window), so running it twice deletes
nothing the second time. If a sweep fails, or the loop stops ticking, `/healthz`
turns `503` — a service that keeps ingesting while it has stopped forgetting is
not healthy, and ServiceBay's health check is what makes that visible.

## Status

Early scaffold — ingest, storage, retention, the summary readout and the
ServiceBay template are in. The image is not published to a registry yet, so no
real install has been done; see the repo's issues for the rest of the build-out
plan.

## Origin

mdopp/servicebay#2366
