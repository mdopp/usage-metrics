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
- **Retention**: rollup/drop after ~90 days (configurable). Fully deletable.
- **Readout**: a minimal summary — counts per `app` × `event` over the last N
  days. Enough to answer "is this used", not a BI tool.
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

### `GET /healthz`

`200 ok` — the ServiceBay install gate.

## Configuration

| Env | Default | Meaning |
| --- | --- | --- |
| `USAGE_METRICS_DB_PATH` | `/data/usage-metrics.db` | SQLite file. Must live on the mounted volume (`{{DATA_DIR}}/usage-metrics/`), never the container fs, which is wiped on every pod recreate. |

The service listens on `:8080`.

## Status

Early scaffold — ingest + storage are in; see the repo's issues for the rest of
the build-out plan.

## Origin

mdopp/servicebay#2366
