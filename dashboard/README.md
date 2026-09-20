# Shipwick dashboard

The web console for a [Shipwick](../README.md) agent: applications and their
replicas, deployments, live CPU/memory, events and logs, plus the everyday
actions (deploy another image, roll back, stop/start, delete).

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
  pass through untouched; bodies are piped, never buffered, so
  `logs?follow=true` (NDJSON) arrives line by line.

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
  pages/            index (Overview), applications/, deployments/, servers/, logs/, login
  components/       hand-rolled UI: UiButton, UiDialog (native <dialog>), StatusBadge,
                    Sparkline (SVG), LogViewer, DeploymentProgressPanel, dialogs, …
  composables/      useAgent (fetch wrapper), usePolling, useLogStream, useMetricsHistory,
                    useDeploymentProgress, useSession, useTheme, useNow, useServerInfo
  utils/            pure logic, unit-tested: format, ndjson, status, deploymentProgress, redirect
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
step wording. The one exception is `POST …/deploy`, which it refuses: the
dashboard never submits a deploy.yaml. Its fixtures cover every application
status:

| Application | Status | Notable |
|---|---|---|
| `my-api` | `HEALTHY` | 2 replicas, domain, health check, limits (CPU 1 core each: ceiling 200%); history with a `rollback`, a `redeploy` with another image, and a `FAILED` attempt with saved output |
| `web` | `DEGRADED` | 3 replicas; replica 3 keeps getting OOM-killed and restarted (flaps every 40s); a `ROLLED_BACK` deployment in its history |
| `worker` | `CRASH_LOOP` | replica 2 unhealthy, restart counter grows, registry with a port in the image name |
| `docs` | `STOPPED` | start it to see logs and metrics |
| `billing` | `DEPLOYING` | first deployment stuck in `HEALTH_CHECKING`; fails after 15 minutes |
| `legacy-cron` | `FAILED` | never deployed successfully |

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

When the last old container is retired the followed log streams end, as with
the real agent, so the log viewer's reconnect can be seen.

| Variable | |
|---|---|
| `MOCK_NO_PROXY=1` | `server.proxy.enabled` is false, and deploying an application with a domain records the "No reverse proxy is configured, so … is not being served" `warn` step |
| `MOCK_PORT`, `MOCK_HOST`, `MOCK_TOKEN` | `9100`, `127.0.0.1`, `mock-token-0123456789abcdef` |

### Scripts

| | |
|---|---|
| `npm run dev` | Nuxt dev server with HMR on :3000 |
| `npm run mock` | Mock agent on :9100 |
| `npm test` | Unit tests (vitest): formatters, NDJSON splitter, status mapping, deployment-progress reducer (incl. the rollback path), rollback-candidate selection and deployment origins, redirect guard |
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
  a session exists. Nothing is kept in `localStorage` except the theme.
- **CSRF.** Every state-changing request (`POST`/`DELETE`, including sign-in
  and sign-out) must carry `X-Shipwick-Request: 1`, which the app's fetch
  wrapper always sends. A cross-origin page cannot add a custom header without
  a CORS preflight, and the server never grants one. `Sec-Fetch-Site`, where
  the browser sends it, must be `same-origin`. `SameSite=Strict` is the second
  layer. With `curl`, add `-H 'X-Shipwick-Request: 1'`.
- **The proxy is not an open relay.** The target host comes only from
  `SHIPWICK_AGENT_URL`. Only `GET`, `HEAD`, `POST`, `DELETE` are accepted; the
  path must stay under `/api/v1/` and each segment must match `[A-Za-z0-9._~-]`
  (no `..`, no encoded slashes). Only `Accept` and `Content-Type` are forwarded
  to the agent: the browser's cookies never are. Bodies over 128 KB are refused.
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
- Not included: login rate limiting (put it in the reverse proxy; the token is
  at least 16 random characters) and multi-user accounts (the agent has a
  single token).
