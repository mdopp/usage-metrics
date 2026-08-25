# usage-metrics — house rules

A tiny, first-party, self-hosted usage-metrics service: aggregate, anonymous
event counters for household apps (Solaris, and future apps), deployed on
ServiceBay. See `README.md` for the full spec and origin (mdopp/servicebay#2366).

It runs **on ServiceBay**. These rules apply to every session, human or agent.

## What this is, and isn't

- **Is:** one small container. `POST /ingest` for aggregate increments keyed
  `(app, event, day)`, SQLite storage, a retention rollup, a minimal summary
  readout. Multi-app from day one via the `app` dimension.
- **Isn't:** a BI tool, a Prometheus/Grafana stack, per-user analytics, or a
  third-party pipeline. No content, no uid, no request bodies are ever stored —
  counters only.
- **Isn't reachable directly by client apps.** Per the Solaris-Hub BFF model
  (solarisbay ADR 0010), apps talk only to their own backend (e.g. Solaris);
  that backend forwards aggregate increments here. `/ingest` is LAN/token
  -internal, never internet-exposed.

## Stack

**Go**, standard library `net/http` + `database/sql` (SQLite driver). Chosen for
the smallest possible image and fastest boot — this service is about as minimal
as a service gets (two HTTP handlers + counters), and Go produces a single
static binary with no runtime dependency layer. Deviate only with a stated
reason.

## Structure (two pieces, per ServiceBay's new-service-architecture assist)

1. **App** — this repo's Go module: `POST /ingest`, the SQLite counter store
   (WAL mode), the retention rollup, a summary readout endpoint, `/healthz`.
   `Dockerfile` builds a lean multi-stage static binary image.
2. **Template** — `templates/usage-metrics/` — the ServiceBay Quadlet template
   that deploys the image: ports, mounts (`{{DATA_DIR}}/usage-metrics/`), health
   check, and whatever token/trust wiring `/ingest` needs (ADR 0009 — scoped
   service tokens, no ambient authority; not Authelia SSO, since callers are
   other services, not a resident).

## Platform standards this repo must respect

Full text via ServiceBay's assist catalog (`get_assist`, flavor `servicebay`,
from any session with access to the ServiceBay MCP) — read in full before
non-trivial work, don't re-derive:

- `new-service-architecture` — language/structure/libraries/tests/storage/
  secrets recommendations (this file already reflects its conclusions).
- `new-service-standards` — the platform ADRs a new service is bound by
  (0001 SSO/LLDAP, 0003 release-please only, 0004 non-destructive installs,
  0007 network isolation, 0009 service tokens) and the enforced gates
  (`check:arch`, lint zero-errors, 70% diff-coverage floor).
- `generic-project-standards` — Conventional Commits, never hand-bump a
  version, secret hygiene, scripts over prose.
- `create-service` — the concrete recipe for building + deploying a service
  behind ServiceBay.
- `service-ui-design-standard` — if a resident-facing summary page is built,
  it must read as native ServiceBay UI (tokens, real streaming progress, no
  bare `<input type=file>`, mobile-first).

## Commits & releases

- **Conventional Commits**: `type(scope): subject` — `feat`/`fix`/`refactor`/
  `chore`/`docs`/`test`. No stray parentheses beyond the conventional
  `(scope)` — release-please parses these.
- **release-please only** — never hand-bump a version or edit `CHANGELOG.md`.

## Privacy (hard requirement, not a preference)

- First-party only — data never leaves the box, no third-party analytics.
- Aggregate + anonymous — counters, no per-user attribution, no content, no
  request bodies persisted.
- Short retention (default ~90 days, configurable), and the data must be
  fully deletable.

## Scope discipline

- Smallest change that solves the task. A bug fix doesn't need surrounding
  cleanup; a one-shot doesn't need a helper.
- Three similar lines beat a premature abstraction. No speculative
  error-handling, fallbacks, or feature flags for cases that can't happen.

## Comments

Default to none. Add one only for a non-obvious *why* (a hidden constraint,
a workaround for a specific bug, behavior that would surprise a reader) —
never to restate what well-named code already says.

## Secrets

Never bake a secret into the image, template, or repo. Express credentials as
`type: "secret"` template variables (the wizard injects the value at deploy)
or read from a mounted file/env at runtime. Placeholders fine, concrete values
never.

<!-- BEGIN SERVICEBAY STANDARDS POINTER (generated — do not edit by hand) -->

## Standards: fetch them, never re-derive them

This repo is built for a ServiceBay box, so **ServiceBay's standards catalog is
the binding source of its architecture decisions** — this file only points at it.

1. **Before the first stack, CI, storage, or auth decision**, call the ServiceBay
   MCP tool `get_service_standards` (flavor `servicebay`) and fetch every id it
   lists under `assistsToRead` via `get_assist(id)`. Read first, design second —
   a stack chosen before reading is a stack chosen against the ADRs by accident.
2. **If the ServiceBay MCP is not connected in this session, stop and say so.**
   An unconnected session cannot see the ADRs, so anything it decides about auth,
   health, storage, or CI is a guess. Connecting it is the first task, not an
   optional extra.
3. **The catalog wins.** Where this file and the catalog disagree, this file is
   the stale one — fix it here, not in your head.
4. **Report gaps back.** A missing, ambiguous, or wrong standard is itself a
   finding: file a `standards-gap` issue on `mdopp/servicebay` and propose the
   assist/docs fix. See `get_assist("report-standards-gaps")`.

This block is generated. Regenerate or verify it from a `mdopp/servicebay`
checkout: `npm run standards:bootstrap -- --write <repo>` / `-- --check <repo>`.

<!-- END SERVICEBAY STANDARDS POINTER -->
