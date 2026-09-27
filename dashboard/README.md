# Shipwick dashboard

The web console for a [Shipwick](../README.md) agent: applications and their
replicas, deployments, live CPU/memory and its seven-day history, events and
logs, scheduled jobs and their runs, volume backups, the volumes on the
server, secrets, API tokens, plus the everyday actions (deploy another image,
roll back, stop/start, delete, run a job or a one-off command, restore a
volume, store or remove a secret, remove a deleted application's volume).

It is a client of the agent's HTTP API ([docs/api.md](../docs/api.md)) and
nothing more: it has no database and keeps no state of its own. Anything it can
do, `shipwick` and `curl` can do too.

## Architecture

```text
 browser ── same origin ──▶ dashboard server (Nitro) ── Bearer token ──▶ Shipwick agent
            cookie session     /api/session                               /api/v1/**
            no token in JS     /api/agent/**  → streams, passes through
```

The browser never talks to the agent and never holds the token:

- `POST /api/session {token}` verifies the token against the agent
  (`GET /api/v1/server`) and stores it in an **httpOnly, SameSite=Strict**
  cookie (`Secure` when the request came over https). `DELETE` clears it,
  `GET` answers `{authenticated}`.
- `/api/agent/**` is a reverse proxy to `${SHIPWICK_AGENT_URL}/api/v1/**`. It
  reads the token from the cookie and adds `Authorization: Bearer …`. Status
  codes and the JSON envelope (`{data}` / `{error:{code,message,details}}`)
  pass through untouched; response bodies are piped, never buffered, so
  `logs?follow=true` (NDJSON) arrives line by line and a volume archive
  streams to the browser's download (the agent's `Content-Disposition` names
  the file). A `PUT` body (a volume archive, or a secret's value) is streamed
  to the agent as it arrives; the agent's limits apply, and the proxy waits for
  the answer only once the last byte has been sent. The dashboard uploads
  neither static folders nor images: `shipwick deploy` does that.

Why a proxy rather than calling the agent from the browser: the agent token is
root-equivalent on the server (see the main README, *Security*). Keeping it out
of JavaScript means an XSS bug or a malicious browser extension cannot read it;
the agent can stay bound to loopback or a private network with only the
dashboard exposed; and the agent needs no CORS support at all.

The app itself is a client-rendered Nuxt 4 application (`ssr: false`): every
view is live data behind a login, so there is nothing worth rendering on the
server. Nitro serves the static app, the session routes and the proxy.

```text
app/
  pages/            index (Overview), applications/, deployments/, servers/, logs/, volumes/, secrets/,
                    tokens/ (admin), login
  components/       hand-rolled UI: UiButton, UiDialog (native <dialog>), StatusBadge,
                    Sparkline and MetricsHistoryChart (SVG), LogViewer, DeploymentProgressPanel,
                    VolumesPanel, JobsSection, dialogs (deploy, rollback, delete, restore,
                    run, run command), …
  composables/      useAgent (fetch wrapper), usePolling, useLogStream, useLiveMetrics,
                    useMetricsHistory, useDeploymentProgress, useSession, useTheme, useNow,
                    useServerInfo (+ useAccess: the token's role)
  utils/            pure logic, unit-tested: format, ndjson, status, deploymentProgress,
                    deployments, spec, metricsHistory, roles, jobs, agentError, redirect,
                    secrets, volumes
  types/api.ts      wire types, mirroring pkg/api/types.go and pkg/spec/spec.go
  assets/css/       design tokens (light/dark), Tailwind v4 theme
server/
  api/session.*.ts  login / logout / session check
  api/agent/[...path].ts   the streaming reverse proxy
  utils/agent.ts    agent client (node:http), cookie options, CSRF check, error envelope
mock/agent.mjs      dependency-free mock of the agent API, for development
tests/              vitest unit tests
```

No UI kit, chart library, icon font, analytics or CDN. Fonts (Geist and Geist
Mono) are bundled from npm, and a Content-Security-Policy of `default-src
'self'` is sent in production, so the dashboard works on a server without
internet access and makes no request to any third party.

## How the UI reads the API

- **A deployment is over when `completed_at` is set**, never before. `FAILED`
  in particular is not an end state: a rolling deployment that fails after some
  replicas were replaced continues `ROLLBACK → RESTORING → ROLLED_BACK`. The
  progress panel follows all of it; `error` (the original cause) is shown from
  the moment it appears.
- **Three outcomes.** `ACTIVE` is the only success (green). `FAILED`: the
  previous version was never touched (red). `ROLLED_BACK`: it failed part-way
  and the previous version was restored (amber, "Failed — rolled back to
  1.4.1"): handled, but never presented as success.
- **Origins.** `kind` and `source_deployment_id` become "rollback to #3 1.4.0"
  / "redeploy of #6" in every history table and on the deployment page, linked
  to the source.
- **Rollback targets** are exactly the application's `SUPERSEDED` deployments,
  the agent's own rule (`utils/deployments.ts`); `NO_ROLLBACK_TARGET` refreshes
  the list.
- **Following a deployment** started elsewhere uses
  `Application.in_flight_deployment_id`.
- **CPU** is percent of one core; the ceiling next to it and in the sparkline
  is the sample's own `cpu_limit_percent` (0 = unlimited, no ceiling drawn).
- **Metrics** are point-in-time samples; the sparklines' history is built in
  the browser while the page is open. `NOT_DEPLOYED` (no active deployment) is
  shown as "Nothing is deployed", not as an error.
- **History** (`metrics/history?since=1h|24h|7d`) is drawn as the agent
  returns it: one line per replica, the agent's `step`, a dashed line at the
  per-replica limit (`limits.cpu × 100` for CPU), and a gap wherever a bucket
  has no sample; nothing is zero-filled or interpolated. Refreshed every 30
  seconds, the sampling interval. Replica n keeps series color n, as in the
  log viewer.
- **Roles.** `GET /server` says which token the session is (`token: {name,
  role}`); the sidebar shows it, and controls the role does not cover are
  disabled with the reason (`read` cannot deploy, redeploy, roll back, stop or
  start; only `admin` can delete, manage tokens, download or restore a
  backup). The Tokens page appears for admins. A `403 FORBIDDEN` is rendered
  from its `details` ("This token has the read role; deploying needs deploy or
  admin"). An agent from before roles omits `token` and is treated as one
  admin token.
- **Volumes.** A backup is a plain link to the archive endpoint through the
  proxy (`download` attribute), so the browser saves it under the agent's
  filename. A restore is offered only while the application is stopped,
  checks the file starts with a tar header before uploading, uploads with
  progress (XMLHttpRequest, for `upload.onprogress`) and, on `204`, shows the
  agent's own event and offers Start; `APPLICATION_RUNNING` reads "Stop the
  application first".
- **Jobs.** The Jobs section of an application lists `GET …/jobs`: schedule
  (five cron fields, marked UTC), last run with its outcome and exit code,
  next run relative to now (`null` while stopped reads "Not while stopped").
  "Run now" posts `…/jobs/:job/run`; a `409 JOB_ALREADY_RUNNING` is explained
  in place. The run history is `GET …/runs?job=` (hook, scheduled and one-off
  runs alike, filterable); a row opens the run's dialog, which loads
  `GET …/runs/:id` and polls it every second until `finished_at` is set, then
  shows exit code and the output in monospace with its newlines. "Run
  command" is an argv editor, one field per argument, because the API takes
  an array and never a shell string; the run is followed the same way. Both
  actions need the `deploy` role. `type: "job"` events arrive with level
  `warn` and render as warnings; the pre-deploy hook's steps and its `log`
  event render like any other step and crash output.
- **Configuration** is rendered by kind: a health check as `GET /health`,
  `TCP :5432` or `command pg_isready -U postgres`; hostnames as the domain,
  its aliases and `www.example.com → example.com` redirects; published ports
  as `5432/tcp → server port 15432 on 10.0.0.5`; `entrypoint`/`command` joined
  with spaces (arguments with spaces quoted, display only); `logging` as
  `gelf (2 options)` with the options collapsed. When the logging driver is
  not `json-file` or `local`, the log viewer says the logs are shipped and
  that it shows Docker's local copy.
- **Static applications** (`Application.static`) are folders the proxy serves
  itself: the list shows `static` in place of the replica count and the
  version is the upload's digest, twelve hex characters. Their page has no
  replicas table, history, jobs or logs — the agent would answer
  `409 STATIC_APPLICATION`, so nothing asks — and says instead what is served
  (`42 files, 3.1 MB, served by the proxy`, from `Deployment.static`). Stop,
  start, rollback, redeploy and delete work; the deploy dialog offers no image
  field (there is none), nor for an application with `spec.build`, whose image
  is built and sent by `shipwick deploy` and shown as `built by shipwick
  deploy from . (Dockerfile)`. A health check's `start_period` reads `after a
  2m start period`.
- **Secrets** (`/secrets`, every role) lists `GET /secrets`: names and dates,
  never a value. An admin stores one with `PUT /secrets/:name` from a password
  field — the form says when the name is already stored and the value will be
  replaced — and the value is cleared from the page as soon as the request is
  sent, whatever the answer. Removing one says that deployments already made
  keep their value and that the next deploy referring to it is refused.
- **Volumes** (`/volumes`, every role) lists `GET /volumes`: name, application,
  size (`-1` reads `unknown`), `in use` or `application deleted`. Remove is
  offered for `orphan` rows only, to admins, confirming with the size;
  `409 VOLUME_IN_USE` is shown with the agent's message.
- **`429 RATE_LIMITED`** (too many failed authentications from the dashboard
  server's address) reads "Too many failed attempts from this address; try
  again in a minute", on the login page and wherever an error is shown, and is
  never presented as a rejected token.
- **`ENDPOINT_NOT_FOUND`** (the agent has no such operation) is told apart from
  `NOT_FOUND` (no such application or deployment) by its code.
- **Log tail** follows the agent: per replica when following, merged total
  otherwise; the control says so.

## Configuration

| Variable | Default | |
|---|---|---|
| `SHIPWICK_AGENT_URL` | `http://127.0.0.1:9000` | Base URL of the agent, as seen **from the dashboard server**. Read at runtime. (`NUXT_AGENT_URL` works too.) |
| `SHIPWICK_COOKIE_SECURE` | auto | `true`/`false` to force the cookie's `Secure` flag. Auto: on when the request is https, directly or via `X-Forwarded-Proto`. |
| `HOST`, `PORT` | `0.0.0.0`, `3000` | Listen address of the production server (Nitro). |

There is no token variable on purpose: the dashboard does not know the token
until someone signs in with it, and then only keeps it in that browser's cookie.

## Development

Requirements: Node 22+ (24 recommended).

```bash
cd dashboard
npm install
cp .env.example .env          # SHIPWICK_AGENT_URL=http://127.0.0.1:9100

npm run mock                  # terminal 1: mock agent on :9100
npm run dev                   # terminal 2: dashboard on http://localhost:3000
```

Sign in with the mock token: `mock-token-0123456789abcdef`.

To develop against a real agent instead, set
`SHIPWICK_AGENT_URL=http://127.0.0.1:9000` (the repository's `docker compose up`)
and sign in with `dev-token-do-not-use-in-production`.

### The mock agent

`mock/agent.mjs` implements the API of [docs/api.md](../docs/api.md) in memory,
so the UI can be developed without Docker. It answers with the agent's error
codes, rejects unknown JSON fields as the agent does, and uses the agent's own
step wording. `POST …/deploy` takes the deploy.yaml as JSON only (the agent
accepts JSON too; the mock has no YAML parser), validates hostnames, ports
and the health block, and refuses a hostname or server port another
application holds with the agent's `INVALID_CONFIG` shape (`aliases[1]`,
`publish[0].host`). It also serves tokens, roles, volumes and their archives,
the metrics history, and jobs (`GET …/jobs` with the next firing computed from
the cron expression in UTC, `GET …/runs`, `GET …/runs/:id` with output, both
`POST`s answering `202` with a run that finishes by itself a few seconds
later, `409 JOB_ALREADY_RUNNING` while one is going), secrets (`GET /secrets`,
`PUT`/`DELETE /secrets/:name`; a deploy whose env refers to an unknown
`${NAME}` is refused with the agent's `INVALID_CONFIG` fields), the server's
volumes (`GET /volumes`; `DELETE /volumes/:name` answers `204` for an orphan,
`409 VOLUME_IN_USE` otherwise; deleting an application turns its volumes into
orphans), static uploads (`PUT …/static` reads a real tar archive and answers
its digest and file count; `POST …/deploy?static=<digest>` deploys a `static`
spec, `400` without the digest, `404` for an unknown one), image archives
(`POST …/images` → `201`), and `429 RATE_LIMITED` with `Retry-After` after 20
failed authentications within a minute from one address. Its fixtures cover
every application status:

| Application | Status | Notable |
|---|---|---|
| `my-api` | `HEALTHY` | 2 replicas, domain, health check, limits (CPU 1 core each: ceiling 200%); a `pre_deploy` migration hook and two jobs (`nightly-report` at 03:00 UTC, `cleanup-sessions` every 15 minutes) with a run history in every status (one still running when the mock starts); history with a `rollback`, a `redeploy` with another image, and a `FAILED` attempt whose pre-deploy hook exited 1, with its output as a `log` event; the three oldest deployments carry no `by` |
| `web` | `DEGRADED` | 3 replicas; domain `example.com` with an alias and two redirects; replica 3 keeps getting OOM-killed and restarted (flaps every 40s, and has gaps in its history); a `ROLLED_BACK` deployment in its history |
| `worker` | `CRASH_LOOP` | replica 2 unhealthy, restart counter grows, registry with a port in the image name; `entrypoint`, `command` (one argument with a space), `user`, `logging: gelf` |
| `postgres` | `HEALTHY` | 1 replica, `recreate`, a `data` volume (backup and restore work: stop it first), `health: {tcp: 5432}`, `5432/tcp` published on `10.0.0.5:15432` |
| `docs` | `STOPPED` | start it to see logs and metrics |
| `landing` | `HEALTHY` | static: a `dist` folder served by the proxy (`static: true`, zero replicas, no image, version = digest); two static deployments in its history; logs, metrics, jobs and run answer `409 STATIC_APPLICATION` |
| `shop` | `HEALTHY` | `build: .` — image `shipwick.local/shop:<stamp>` sent by the CLI; redeploy with an image from elsewhere is refused |
| `billing` | `DEPLOYING` | first deployment stuck in `HEALTH_CHECKING`; `health.start_period: 2m0s`; fails after 15 minutes |
| `legacy-cron` | `FAILED` | never deployed successfully |

Tokens `ci` (deploy) and `viewer` (read) exist; a token created on the Tokens
page signs in with its role (the value is kept in memory for that). Secrets
`POSTGRES_PASSWORD` (rotated once) and `STRIPE_KEY` are stored; the volumes
are `postgres`'s `data` and an orphan `shipwick_pgtest_data` of a deleted
application. The
archive download is a small real tar; the restore accepts any tar body and
records the agent's event. The history is generated per bucket, deterministic
across refreshes, with one gap shared by every series (an outage of the
agent).

`redeploy` and `rollback` create a **rolling** deployment, like the agent's:
one replica at a time ("Started 1 container", "Replica 1 passed health checks",
"Replica 1/2 is serving 1.5.0; its 1.4.2 predecessor is retired", …, "Routed
https://… to 2 replicas", "Deployment successful"), about six seconds for two
replicas, then `completed_at`. While it runs, the application lists containers
of both versions. Magic image tags exercise the unhappy paths:

| Tag | Outcome |
|---|---|
| `…:fail` | replica 1 crashes → `FAILED` with a `log` event; nothing of the old version was touched |
| `…:rollback` | replica 1 is replaced, replica 2 crashes → `FAILED` → `ROLLBACK` → `RESTORING` → `ROLLED_BACK`, `error` keeps the cause. Needs 2+ replicas (`my-api`, `web`); with one replica it behaves like `:fail` |
| `…:local` | the pull fails but a local copy exists: a `warn` step |
| `…:hookfail` | the pre-deploy command exits 1 → `FAILED` with its output as a `log` event and a failed hook run; no replica was touched (needs an application with `pre_deploy`: `my-api`) |

A run started from the dashboard succeeds unless its command mentions `fail`
(exit 1, plus a `job` event) or `timeout` (timed out). The mock's scheduler
fires `cleanup-sessions` on the quarter hour while the application runs.

When the last old container is retired the followed log streams end, as with
the real agent, so the log viewer's reconnect can be seen.

| Variable | |
|---|---|
| `MOCK_ROLE=read\|deploy\|admin` | The role of `MOCK_TOKEN` (default `admin`, signed in as `root`); endpoints above it answer `403 FORBIDDEN` with `{role, required}`, exactly as the agent's role table. Use it to see the dashboard as a read-only or deploy-only token |
| `MOCK_WEBHOOK=1` | `server.notifications.webhook` is true |
| `MOCK_NO_PROXY=1` | `server.proxy.enabled` is false, and deploying an application with a domain records the "No reverse proxy is configured, so … is not being served" `warn` step |
| `MOCK_PORT`, `MOCK_HOST`, `MOCK_TOKEN` | `9100`, `127.0.0.1`, `mock-token-0123456789abcdef` |

### Scripts

| | |
|---|---|
| `npm run dev` | Nuxt dev server with HMR on :3000 |
| `npm run mock` | Mock agent on :9100 |
| `npm test` | Unit tests (vitest): formatters, NDJSON splitter, status mapping, deployment-progress reducer (incl. the rollback path), rollback-candidate selection and deployment origins, role gating and 403 wording, spec display (argv quoting, health kinds, hostnames, published ports, logging), history bucket → chart mapping (gaps, limits, ticks), run status and outcome wording, argv editor → array, next-run formatting, error-field passthrough, redirect guard |
| `npm run typecheck` | `vue-tsc` over app, server and config, strict + `noUncheckedIndexedAccess` |
| `npm run build` | Production build into `.output/` |
| `npm start` | `node .output/server/index.mjs` |

## Production

```bash
npm ci && npm run build
SHIPWICK_AGENT_URL=http://127.0.0.1:9000 node .output/server/index.mjs
```

or the image (multi-stage, `node:24-alpine`, runs as the unprivileged `node`
user, contains only `.output/`):

```bash
docker build -t ghcr.io/shipwick/dashboard dashboard/
docker run -d --name shipwick-dashboard --network shipwick \
  -p 127.0.0.1:3000:3000 \
  -e SHIPWICK_AGENT_URL=http://shipwick-agent:9000 \
  ghcr.io/shipwick/dashboard
```

Put it behind TLS (Caddy, in a Shipwick setup). Signing in sends the agent token
to the dashboard server; over plain HTTP on anything but loopback that is a
root credential in clear text. Behind a reverse proxy, make sure it forwards
`X-Forwarded-Proto` (Caddy does) so the cookie gets its `Secure` flag, and that
it does not buffer responses, or followed logs will arrive in bursts.

## Security notes

- **The token never reaches JavaScript.** It lives in an `httpOnly` cookie
  (`SameSite=Strict`, `Secure` on https, 7 days). The app only knows *whether*
  a session exists, and, from `GET /server`, the token's name and role.
  Nothing is kept in `localStorage` except the theme. A token created on the
  Tokens page is shown once, in the page, and never stored.
- **CSRF.** Every state-changing request (`POST`/`PUT`/`DELETE`, including
  sign-in and sign-out) must carry `X-Shipwick-Request: 1`, which the app's
  fetch wrapper (and the restore upload) always sends. A cross-origin page cannot add a custom header without
  a CORS preflight, and the server never grants one. `Sec-Fetch-Site`, where
  the browser sends it, must be `same-origin`. `SameSite=Strict` is the second
  layer. With `curl`, add `-H 'X-Shipwick-Request: 1'`.
- **The proxy is not an open relay.** The target host comes only from
  `SHIPWICK_AGENT_URL`. Only `GET`, `HEAD`, `POST`, `PUT`, `DELETE` are
  accepted; the path must stay under `/api/v1/` and each segment must match
  `[A-Za-z0-9._~-]` (no `..`, no encoded slashes). Only `Accept`,
  `Content-Type` and, for an upload, `Content-Length` are forwarded to the
  agent: the browser's cookies never are. `POST` bodies over 128 KB are
  refused; a `PUT` body (a volume archive, a secret's value) is streamed
  through and bounded by the agent's own limits.
- **The token is never logged**, by the proxy or the session routes; upstream
  errors are reported by their error code, never by serializing the request.
- **A 401 from the agent ends the session**: the proxy clears the cookie and
  the app returns to the login page. The route middleware is a convenience;
  the boundary is the server, which answers 401 without a valid cookie
  whatever the client does.
- **Post-login redirects** only accept same-site paths.
- **Headers** (production): CSP `default-src 'self'` (with inline script/style
  allowed, which Nuxt's bootstrap and the no-flash theme script need),
  `frame-ancestors 'none'`, `X-Frame-Options: DENY`, `nosniff`,
  `Referrer-Policy: no-referrer`.
- **Roles are enforced by the agent**, not here. Hiding or disabling a control
  for a `read` or `deploy` token is a courtesy; a request the role does not
  cover is answered `403` by the agent whatever the page does.
- Not included: login rate limiting (put it in the reverse proxy; the token is
  at least 16 random characters). Accounts are the agent's named tokens with
  roles; the dashboard has none of its own.
