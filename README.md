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
- **Storage**: SQLite, counters keyed `(app, event, day)`. No content, no uid,
  no request bodies.
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

## Status

Early scaffold — see the repo's issues for the initial build-out plan.

## Origin

mdopp/servicebay#2366
