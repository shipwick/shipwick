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
- `/api/agent/**` is a reverse proxy to `${SHIPWICK_AGENT_URL}/api/v1/**`
  (`/api/servers/<name>/agent/**` when several servers are configured, see
  below). It reads the token from the cookie, adds `Authorization: Bearer …`
  and `X-Forwarded-For` with the address the request came from. Status
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
  pages/            index (Overview), applications/ (the list; [name].vue is the frame of an
                    application's page and [name]/ its tabs), deployments/, logs/, servers.vue and
                    servers/ (the server's page and its tabs), volumes/, certificates/,
                    settings/ (secrets, registries, access), login
  components/       hand-rolled UI: UiButton, UiDialog (native <dialog>), UiTabs, StatusBadge, FindingList, LiveAnnouncer,
                    Sparkline and MetricsHistoryChart (SVG), LogViewer, DeploymentProgressPanel,
                    VolumesPanel, JobsSection, dialogs (deploy, rollback, delete, restore,
                    run, run command), …
  composables/      useAgent (fetch wrapper), usePolling, useLogStream, useLiveMetrics,
                    useMetricsHistory, useDeploymentProgress, useSession (the servers, the
                    sign-ins, the one this tab is on), useTheme, useNow, useServerInfo
                    (+ useAccess: the token's role and its applications), useApplication
                    (what the application page shares with its tabs), useAnnounce (what a
                    screen reader is told), useFocusTrap, useRovingFocus, useFocusWhenShown
  middleware/       auth.global (which server an address is about, and whether it is signed
                    in), moved.global (addresses that moved)
  plugins/          server-links.client (with several servers, every link names its server),
                    focus.client (where the focus goes when its control is removed)
  utils/            pure logic, unit-tested: format, ndjson, status, deploymentProgress,
                    deployments, spec, metricsHistory, roles, jobs, agentError, redirect,
                    secrets, volumes, navigation (groups, moved addresses), tabs, diagnosis
                    (what is wrong with an application), overview (the verdict), marks
                    (alerts and certificates in a list), access (limited and expiring
                    tokens, rules, the audit trail), servers (several servers),
                    deployDocument (a pasted deploy.yaml), network, focus (roving focus, the
                    focus trap), announce (what is news for a screen reader)
  types/api.ts      wire types, mirroring pkg/api/types.go and pkg/spec/spec.go
  assets/css/       design tokens (light/dark), Tailwind v4 theme
server/
  api/session.*.ts  login / logout / session check, per server
  api/agent/[...path].ts, api/servers/[server]/agent/[...path].ts
                    the streaming reverse proxy (utils/proxy.ts)
  api/auth.get.ts   whether the agent offers a sign-in through a provider
  routes/auth/      login and callback: the dashboard's half of an OpenID Connect sign-in
  utils/agent.ts    agent client (node:http), cookie options, CSRF check, error envelope
  utils/agents.ts   SHIPWICK_AGENTS; forwarded.ts: the browser's address; signin.ts: PKCE,
                    the sign-in cookie, what a failed sign-in says
mock/agent.mjs      dependency-free mock of the agent API, for development (yaml.mjs reads
                    the deploy.yaml it is sent)
tests/              vitest unit tests
```

No UI kit, chart library, icon font, analytics or CDN. Fonts (Geist and Geist
Mono) are bundled from npm, and a Content-Security-Policy of `default-src
'self'` is sent in production, so the dashboard works on a server without
internet access and makes no request to any third party.

## How it is laid out

- **Navigation** in three groups (`utils/navigation.ts`): what runs (Overview,
  Applications, Deployments, Logs), the server (Status, Volumes, Certificates)
  and settings (Secrets, Registries, Access). The server's name sits above the
  navigation; a choice between several servers belongs there. Addresses that
  moved (`/tokens`, `/secrets`, `/registries`) are redirected by
  `middleware/moved.global.ts`, query included.
- **An application's page** is a frame (`pages/applications/[name].vue`: name,
  status, the actions, the tabs) around one page per tab: Overview, Metrics,
  Logs, Deployments, Jobs, Backups, Configuration. The frame polls the
  application, its deployments and its events and follows a deployment in
  flight; the tabs read them through `useApplication()`. A tab an application
  cannot have (Logs and Jobs for a static one, Backups without volumes) is not
  listed, and its address explains why when opened anyway.
- **The overview tab opens with what is wrong** (`utils/diagnosis.ts`): the
  agent's alerts about the application, then findings derived from its state —
  a replica that keeps crashing, no healthy replica, a deployment that failed
  while the previous version still runs, a certificate that is not in order,
  a proxy that is off — each with the link to where the answer usually is. A
  finding an alert already states is not repeated.
- **The server's page** has the same shape: Status, Backups, Export and
  standby, Encryption key. **Access** too: API tokens, Sign-in, Audit trail.
- **A new application** (`/deploy`, from the header of the applications list)
  is a pasted deploy.yaml: validated by the agent, then deployed.
  `/deploy?application=<name>` is the same page opened to change one.
- **The overview** starts with one sentence (`utils/overview.ts`): how many
  applications need attention, or that everything that should run is healthy.
  On a server without applications it shows the three commands that deploy the
  first one.

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
  role, kind, applications, expires_at}`); the sidebar shows it, and controls
  the role does not cover are disabled with the reason (`read` cannot deploy,
  redeploy, roll back, stop or start; only `admin` can delete, manage tokens,
  download or restore a backup). A `deploy` token limited to applications is
  asked per application (`canDeploy` in `utils/access.ts`: admin, or deploy
  with no list or with the application on it — also for a name that does not
  exist yet): the page of an application it does not cover says so in words,
  and `403 TOKEN_LIMITED` is shown with the agent's sentence. `expires_at`
  within fourteen days is a warning in the sidebar; `401 TOKEN_EXPIRED`,
  `SESSION_EXPIRED` and `SESSION_ENDED` return to the sign-in page with the
  reason instead of "rejected". The Access page (tokens) appears for admins, and an application's page says in words when the token can change nothing. A `403 FORBIDDEN` is rendered
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
- **Secrets** (`/settings/secrets`, every role) lists `GET /secrets`: names and dates,
  never a value. An admin stores one with `PUT /secrets/:name` from a password
  field — the form says when the name is already stored and the value will be
  replaced — and the value is cleared from the page as soon as the request is
  sent, whatever the answer. Removing one says that deployments already made
  keep their value and that the next deploy referring to it is refused.
- **Volumes** (`/volumes`, every role) lists `GET /volumes`: name, application,
  size (`-1` reads `unknown`), `in use` or `application deleted`. Remove is
  offered for `orphan` rows only, to admins, confirming with the size;
  `409 VOLUME_IN_USE` is shown with the agent's message.
- **Traffic** on the Metrics tab of the application page: `GET …/traffic?since=1h|24h|7d` every
  30 seconds, drawn as requests per step with the 5xx among them and the p95
  of the durations, above the window's totals. The series is sparse and a
  missing step is a zero; only the latency has gaps. `409 TRAFFIC_UNAVAILABLE`
  is said once in place of the panel's content, and on `ENDPOINT_NOT_FOUND`
  the panel is not there. Recent requests are `GET …/requests?tail=200` every
  three seconds, only while their table is open. A static application has
  traffic like any other.
- **Certificates.** `certificates` of the application detail puts a badge on
  every hostname that is not `ok`, with the agent's message; issuer and expiry
  are the hostname's tooltip. The **Certificates** page (`/certificates`)
  lists `GET /certificates`, marks the last 30 days and what has expired, and
  lets an admin store one (`PUT`, two PEM fields; the key is cleared from the
  page when the request is sent) or remove one.
- **Registries** (`/settings/registries`, every role) lists `GET /registries`; an admin
  logs in (`PUT`, a password field that is cleared when the request is sent)
  and out. `REGISTRY_LOGIN_FAILED` is explained from `details.refused`.
- **Backups.** An application with volumes has a Backups tab: the listing,
  a summary line in the words of `shipwick status`, *Back up now* and *Verify*
  (deploy), and a dialog per backup with the verification's output, a download
  per volume, *Restore* (only while the application is stopped, confirmed by
  typing its name) and *Remove* (admin). A backup is polled until
  `completed_at`, a verification or a restore until `activity` is empty.
- **The server page** shows the disk, the active alerts, where backups go and
  how the agent's own state is backed up (with *Back up state now*), exports
  and the standby (below), and *Rotate encryption key*: with the key in the
  agent's environment the new one is shown once, with the line to put into
  `/opt/shipwick/.env`. Alerts are also a mark on the Status entry of the
  navigation, a list on the overview and on the page of the application they
  are about.
- **Export and standby.** *Export to backups* (`POST /exports`) and the list of
  exports; the export file itself is downloaded with `shipwick export`, which
  asks for its passphrase. A server that holds applications imported stopped,
  or fetches exports on a schedule, shows a Standby panel: what waits, the
  scheduled fetch, *Import newest now* (`POST /standby/pull`, followed with
  `GET /import`) and *Promote*, confirmed by typing `promote`, which answers
  with each application's state and the DNS records to change. A running
  import is a banner; the last one is listed with every application's outcome.
- **Paths.** An application's address is `domain` + `path` wherever it is
  written or linked; a wildcard domain is text, not a link. The `proxy` block
  is shown read-only, accounts by username and path.
- **A container that is stopping** (`stopping: true`) reads *Stopping*, is
  listed after the replicas, shows neither health nor restarts and is not
  counted as a replica. An agent before 0.6 does not send the field; for it
  the same is inferred from a container that carries the previous
  deployment's id after the deployment completed.
- **Marks in lists.** `certificate_problem`, `alert_count` and
  `alert_severity` of an application become a mark next to its status in the
  applications list and under *Needs attention*, and count in the overview's
  verdict (`utils/marks.ts`, `needsAttention`).
- **A new application.** The name is read from the pasted document (its
  top-level `name`, `utils/deployDocument.ts`) because it is part of the
  address: `POST /applications/:name/validate` first, with the document as
  the body, then `POST …/deploy`. Every entry of `details.fields` is listed
  with its `expected`. A document with `build` or `static` is not sent.
- **Access.** Tokens are created with `applications` (deploy only) and
  `expires_at` only when chosen, since an agent before 0.6 refuses a field it
  does not know. The audit trail is `GET /audit` with `application`, `actor`,
  `since`, `limit=50` and `before=<last id>` for *Load older*, offered while a
  page comes back full; a detail `deployment N` links to the deployment.
  Rules are `/access/rules`, people signed in `/access/sessions`.
- **Signing in through a provider.** The login page asks `GET /api/auth`,
  which relays the agent's `GET /auth` and answers `{configured, issuer,
  problem, failure}`. `GET /auth/login` keeps `state`, `nonce` and the PKCE
  verifier in the cookie `shipwick_signin` (httpOnly, SameSite=Lax, path
  `/auth`, ten minutes) and redirects to the provider; `GET /auth/callback`
  compares `state`, posts the code to the agent's `/auth/exchange` and stores
  the session it answers in the session cookie, for as long as the session
  lasts. Why a sign-in did not complete travels in a cookie the login page
  reads once, never in the address. Signing out ends the session on the agent
  (`DELETE /auth/session`) for a person, and only clears the cookie for a token.
- **Promotion.** `POST /standby/promote?wait=false` answers `202` with the
  record; `GET /standby/promotion` is polled every 1.5 seconds until
  `completed_at`, and a poll that fails with 502, 503 or 504 keeps the last
  record on screen and is repeated. A promotion running when the page opens
  is shown. `404 ENDPOINT_NOT_FOUND` from that endpoint means an agent
  before 0.6: the request is then held until the end, as before.
- **Adopting backups** is `POST /server/backups/adopt` (admin), with
  `{application}` from an application's Backups tab; `adopted` and `skipped`
  are listed, and `409 FOREIGN_BUCKET` is shown with the agent's message.
- **Network.** `network` of `GET /server` is a row on the Status tab only
  when something is set, with a warning when the agent has a proxy and the
  Docker daemon has none.
- **`429`** carries `Retry-After`, and polling waits that long (a minute
  without the header) before it asks again.
- **A deployment refused for a missing secret** (`INVALID_CONFIG` whose
  `expected` is `shipwick secret set NAME`) links to the Secrets page with the
  name filled in; one whose error is the agent's `pull access denied …` links
  to the Registries page.
- **The server-side proxy** gives the agent 60 seconds to answer, except
  where it waits itself: stopping and deleting an application (replicas get
  their `deploy.stop_timeout`), promoting a standby, and archives.
- **`429 RATE_LIMITED`** (too many failed authentications from the dashboard
  server's address) reads "Too many failed attempts from this address; try
  again in a minute", on the login page and wherever an error is shown, and is
  never presented as a rejected token.
- **`ENDPOINT_NOT_FOUND`** (the agent has no such operation) is told apart from
  `NOT_FOUND` (no such application or deployment) by its code.
- **Log tail** follows the agent: per replica when following, merged total
  otherwise; the control says so.

## Keyboard, screen readers and touch

Every page and dialog works with the keyboard alone, is described to a screen
reader, and can be hit with a finger. A new component keeps it that way by
following these rules; the helpers named are the ones the existing components
use.

- **Controls are controls.** A `<button>`, a link or `UiButton`, never a
  click handler on a `div`. A row that opens something on a click carries a
  link or a button in its first cell that opens the same thing.
- **The focus ring** is the global `:focus-visible` outline in the accent
  color. Do not remove it from anything Tab stops at. Inside a box that clips
  (`overflow-hidden`, a row that scrolls sideways) draw it inside the control:
  `focus-visible:-outline-offset-2`.
- **Focus is never lost.** A dialog is a `UiDialog`: it starts at the field
  marked `autofocus` or at its title, keeps Tab inside (`useFocusTrap`, for
  any other surface that must keep it too), closes on Escape and gives the
  focus back to the control that opened it. A popover that is not a dialog
  closes on Escape and when the focus leaves it (the server switcher in
  `AppNav`). A `UiButton` that is `pending` keeps the focus, and one that is
  `disabled` with a `title` stays a Tab stop so the reason can be read; do not
  set the native `disabled` on a button while it has the focus. A result that
  replaces the form which produced it takes the focus (`useFocusWhenShown`,
  on an element with `tabindex="-1"`); when it goes away again, focus the
  control that takes its place. When the control that has the focus is
  removed — a link that was followed, a row its own button deleted —
  `plugins/focus.client.ts` gives the focus to `<main>`. After a navigation
  the page's title is announced, so every route sets one (`useHead`; a tab
  with `tabTitle`).
- **One of several is a radio group**: `role="radiogroup"` around
  `role="radio"` buttons, one Tab stop (`rovingTabStop`), the arrows move and
  choose (`useRovingFocus`) — `ThemeToggle`, `RangeSwitch`. Several of
  several are buttons with `aria-pressed` (`ApplicationsPicker`). Sections
  with an address are links (`UiTabs`), not an ARIA tab list.
- **Everything has a name.** An icon-only button has an `aria-label`
  (`UiIcon` itself is hidden from a screen reader). An action repeated in
  every row names its row and begins with the visible text:
  `aria-label="Revoke ci"`. A link that opens a new tab says so in a
  `sr-only` span. A field has a `<label for>`; its help and its error are one
  paragraph tied to it with `aria-describedby`, and `aria-invalid` is set
  while it is wrong. A table has an `aria-label`. A chart is `role="img"`
  with a sentence, and a `Show as table` below it (`pointFigures`). A status
  is a word next to its color (`StatusBadge`), never the color alone.
- **What changes without a navigation is said once.** Call
  `useAnnounce().announce(text)`; the two live regions are in `app.vue`
  (`LiveAnnouncer`). Never put `aria-live` or `role="status"` on a container
  that holds a clock (`TimeAgo`, an elapsed time) or that polling re-renders:
  it is read out again at every change. Pick the sentence that is news with
  a pure function in `utils/announce.ts`, next to the ones for a deployment,
  a promotion and the backups, and test it. `role="alert"` is for a failed
  action (`InlineError`, `ErrorState`); `role="status"` on a sentence that
  appears once (`Stored`, `Added`) is fine. A log is `role="log"` with
  `aria-live="off"`.
- **A finger needs 44 by 44 CSS px.** `UiButton` and `.input` have it; any
  other control takes the class `target`. It changes nothing with a mouse and
  grows the control on a touch screen (`pointer: coarse`). Links and buttons
  that are text get an area of that size around them from `main.css` without
  growing. With a mouse a control is at least 24 by 24; a link that is text
  has the height of its line and room around it.
- **Contrast.** Text is `fg`, `fg-muted` or `fg-subtle`: each has 4.5:1 on
  `bg`, `bg-subtle`, `bg-hover` and `bg-inset` in both themes. On `bg-active`
  (a selected row) use `fg` or `fg-muted`. `fg-faint` is for decoration — a
  separator, a chevron, a rule in a chart — never for text. The edge of a
  field one types into is `--control` (3:1); `line` and `line-strong` are for
  dividers and for buttons, which their label identifies. The series colors
  have 3:1 on `bg`. A new pair of tokens is checked before it is used.
- **Tables fold by the width of the page's column**, not of the window:
  `data-table stack` with `data-label` on the cells, inside an
  `overflow-x-auto` box, and `cards:` / `rows:` (defined in `main.css`) for
  what a cell shows in only one of the two forms — not `max-sm:` / `sm:`,
  which do not know about the sidebar.
- **Motion.** CSS animations and transitions stop under
  `prefers-reduced-motion` through one rule in `main.css`; scrolling from
  JavaScript has to ask for itself.

## Configuration

| Variable | Default | |
|---|---|---|
| `SHIPWICK_AGENT_URL` | `http://127.0.0.1:9000` | Base URL of the agent, as seen **from the dashboard server**. Read at runtime. (`NUXT_AGENT_URL` works too.) |
| `SHIPWICK_AGENTS` | — | Several servers: `name=URL` pairs separated by commas or spaces, `production=http://agent:9000,staging=https://agent.staging.example.com`. Names are lowercase letters, digits and dashes; at most 20. Set, it replaces `SHIPWICK_AGENT_URL`. |
| `SHIPWICK_COOKIE_SECURE` | auto | `true`/`false` to force the cookie's `Secure` flag. Auto: on when the request is https, directly or via `X-Forwarded-Proto`. |
| `HOST`, `PORT` | `0.0.0.0`, `3000` | Listen address of the production server (Nitro). |

There is no token variable on purpose: the dashboard does not know the token
until someone signs in with it, and then only keeps it in that browser's cookie.

### Several servers

With `SHIPWICK_AGENTS` the dashboard shows each listed server, one at a time:

- **One sign-in per server.** Each has its own cookie
  (`shipwick_session_<name>`; a single server keeps `shipwick_session`).
  `POST /api/session {token, server}` signs in to one, `DELETE
  /api/session?server=<name>` out of one, and `GET /api/session` lists the
  names with whether this browser is signed in to each. URLs never reach the
  browser, except in the error that says an agent cannot be reached.
- **The server is in the address**, as `?server=<name>`, and in the path of
  every request to an agent, `/api/servers/<name>/agent/**`. Two tabs on two
  servers therefore never act on each other's server, and a link that is
  shared opens on the server it was copied from. `/api/agent/**` answers
  `400 SERVER_REQUIRED` when several servers are configured, and an unknown
  name is `404 UNKNOWN_SERVER`: no address is ever built from the request.
- **In the app** links are written without a server; `auth.global` adds the
  page's server to a navigation that lacks one, and `server-links.client`
  adds it to every link's `href`, so "open in new tab" stays on the server.
  An address that names another server than the page holds is loaded afresh.
  An address without a server, typed by hand, opens on the server used last
  (remembered in `localStorage`) or on the list.
- **`/servers`** opened afresh is the list of servers, with what each one's
  agent says of itself; `/servers?server=<name>` is that server's page. The
  box under the logo switches. A link for a server the dashboard does not
  have ends on the list, which says so.
- With one server none of this shows: no parameter, the same cookie, the same
  paths.

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

Tokens `ci` (deploy) and `viewer` (read) exist; a token created on the Access
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
| `…:restart` | the agent goes away for six seconds while replica 1 is checked (connections are dropped, as when it restarts), then records `Resumed after the agent restarted` and finishes the deployment |
| `…:sigterm` | the replica replaced last ignores SIGTERM: it stays listed for its whole `stop_timeout` after the deployment completed and is killed, with the `warn` event |

After every successful deployment the replica replaced last stays listed for a
few seconds, with the previous deployment's id, as it does while the real
agent gives it its `stop_timeout`.

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
| `MOCK_NO_TRAFFIC=1` | Traffic and requests answer `409 TRAFFIC_UNAVAILABLE` (`MOCK_NO_PROXY=1` does too) |
| `MOCK_ALERTS=none\|warning\|critical` | The alerts of `GET /server`: none, two warnings (the default), or those plus a critical `disk` and a critical `unhealthy` alert with the disk at 97% |
| `MOCK_KEY_ENV=1` | The encryption key is "in the environment": `POST /server/rotate-key` answers the new key once, then `409 KEY_ROTATION_PENDING` |
| `MOCK_NO_PASSPHRASE=1` | Backups are not encrypted and the agent's state is not backed up: `POST /server/backups` and `POST /exports` answer `409 BACKUPS_NOT_ENCRYPTED` |
| `MOCK_NO_BUCKET=1` | Backups stay on the server's disk (`destination: local`) |
| `MOCK_VERIFY_FAILS=1` | A backup verification ends with `verify_error` and the container's output |
| `MOCK_DNS_CHALLENGE=1` | `server.proxy.dns_challenge` is true and wildcard hostnames deploy without a supplied certificate |
| `MOCK_STANDBY=1` | The server is a standby: `postgres`, `shop` and `docs` were imported stopped (kind `standby`) and wait for `POST /standby/promote`; `POST /standby/pull` imports over a few seconds, to be followed with `GET /import` |
| `MOCK_EMPTY=1` | A server nobody has deployed to: no applications, deployments or exports |
| `MOCK_OLD_AGENT=1` | Answers like an agent before 0.5: the endpoints it added are `404 ENDPOINT_NOT_FOUND` and the fields it added are absent |
| `MOCK_AGENT=0.5` | Answers like an agent before 0.6: no audit trail, no limits or expiry on tokens (`POST /tokens` refuses the fields as unknown), no promotion record (the request is held), no sign-in, no `init` |
| `MOCK_LIMIT=my-api,web` | With `MOCK_ROLE=deploy`: `MOCK_TOKEN` is limited to these applications; changing another is `403 TOKEN_LIMITED` |
| `MOCK_EXPIRES=9d` | `MOCK_TOKEN` expires that long after the mock started (`s`, `m`, `h`, `d`), then answers `401 TOKEN_EXPIRED`; `expired` for one that has already. Not for the root token (`MOCK_ROLE` unset) |
| `MOCK_NETWORK=1` | `network` on `GET /server` says a proxy the Docker daemon lacks, authorities of its own, the system's resolver and an ACME directory |
| `MOCK_ADOPT=foreign` | `POST /server/backups/adopt` answers `409 FOREIGN_BUCKET` |
| `MOCK_PROMOTING=1` | With `MOCK_STANDBY=1`: a promotion is running when the mock starts, twenty seconds an application |
| `MOCK_NO_SIGN_IN=1` | No sign-in provider: `GET /auth` says `configured: false` |
| `MOCK_DASHBOARD_URL` | Where the dashboard is, for the sign-in's `redirect_uri` (default `http://localhost:3000`) |
| `MOCK_IDP_USER=ada@example.com` | The stand-in provider signs this account in without showing its page |
| `MOCK_HOSTNAME` | The server's hostname (default `shipwick-fsn1-01`); to tell two mocks apart behind `SHIPWICK_AGENTS` |
| `MOCK_AGENT=0.6` | Answers like an agent before 0.7: what 0.7 added is `404 ENDPOINT_NOT_FOUND`, its fields are absent and its filters are ignored. |
| `MOCK_UPDATE=available\|current\|unknown\|off` | `update` on `GET /server`: a newer release (the default), none, GitHub never answered, or the check turned off. |
| `MOCK_LOG_ARCHIVE=off` | Nothing is kept of ended containers, as with `SHIPWICK_LOG_RETENTION_SIZE=0`. |
| `MOCK_NAME_CLAIM=preferred_username` | People are named by that claim instead of their address; rules of kind `name`. |
| `MOCK_EXPORT_MB=64`, `MOCK_EXPORT=trailer\|breaks` | The size of the file `POST /export` streams, and an export that stops half-way: with the agent's error trailer, or with the connection cut. |
| `MOCK_IMPORT_MS=1500` | How long an import takes per application. |
| `MOCK_PORT`, `MOCK_HOST`, `MOCK_TOKEN` | `9100`, `127.0.0.1`, `mock-token-0123456789abcdef` |

What 0.5 added is served with the agent's shapes and refusals: `path`, `proxy`
(passwords masked), `backups`, `deploy.stop_timeout` and `static.fallback` in
specs, with hostname conflicts by hostname and path; `POST …/validate`;
`certificates` on every application detail, one hostname in each state;
traffic (`GET …/traffic?since=`, sparse and the same on every refresh, and
`GET …/requests?tail=`); registries (a registry named `refused.*` refuses the
login, one under `.invalid` cannot be asked); supplied certificates (the PEM
is looked at, not parsed); the ten backup endpoints (a backup is `running` on
the first poll and done a few seconds later; a verification and a restore are
followed through `activity`); exports, the import and the standby. `my-api`
has a `proxy` block and `stop_timeout: 30s`, `docs` serves `/docs` of the
domain `web` serves the rest of, `postgres` has a `backups` block and a week
of backups, `landing` has a fallback page.

What 0.6 added is served the same way. `POST …/validate` and `…/deploy`
read the document as YAML or JSON (`mock/yaml.mjs` reads block and flow
collections, quoted and plain scalars and comments — what a deploy.yaml is
written in — and names the line of what it cannot read), accept `init` and
`backups.before_timeout`, and refuse an unknown key by its name. Tokens take
`applications` and `expires_at` with the agent's refusals; `web-ci` is
limited to two applications and expires in nine days, `contractor` has
expired. Every request that changes something is written to the audit trail
with its outcome, the address and `X-Forwarded-For`, on top of two weeks of
entries the mock starts with (`GET /audit` with the agent's filters and
paging). `POST /server/backups/adopt` adopts two backups of `postgres` and
one of the state and skips an unfinished one, once. A promotion advances by
itself, three seconds an application, and `shop` ends `started`, not ready.
`web` has a hostname waiting for DNS and an alert, `shop` a certificate that
expires: the marks in the lists.

Signing in: `GET /auth`, `POST /auth/exchange` and a stand-in for the
provider at `/mock-idp/authorize`, a page of four accounts —
`ada@example.com` (admin by a rule for her address), `grace@example.com`
(deploy on `my-api` and `web` through the group `developers`),
`sam@example.com` (read through the domain) and `mallory@elsewhere.org` (no
rule: `403 ACCESS_NOT_GRANTED`). The code is bound to the PKCE challenge and
the nonce, a session (`sws_…`) lasts ten hours and is checked against the
rules on every request (`401 SESSION_ENDED` when its rule is removed or
changed, or an admin signs the person out). `/access/rules` and
`/access/sessions` answer with the agent's shapes and messages. Run the mock
with `MOCK_DASHBOARD_URL` set to where the dashboard is when that is not
`http://localhost:3000`.

To try several servers, run two mocks and point the dashboard at both:

```bash
MOCK_PORT=9100 npm run mock
MOCK_PORT=9101 MOCK_EMPTY=1 MOCK_HOSTNAME=shipwick-hel1-02 npm run mock
SHIPWICK_AGENTS=production=http://127.0.0.1:9100,staging=http://127.0.0.1:9101 npm run dev
```

### Scripts

| | |
|---|---|
| `npm run dev` | Nuxt dev server with HMR on :3000 |
| `npm run mock` | Mock agent on :9100 |
| `npm test` | Unit tests (vitest): formatters, NDJSON splitter, status mapping, deployment-progress reducer (incl. the rollback path), rollback-candidate selection and deployment origins, role gating and 403 wording, spec display (argv quoting, health kinds, hostnames, published ports, logging), history bucket → chart mapping (gaps, limits, ticks), run status and outcome wording, argv editor → array, next-run formatting, error-field passthrough, redirect guard, navigation groups and moved addresses, the tabs of an application, what is wrong with an application and the overview's verdict, marks in lists, limited and expiring tokens, the token form, why a session ended, the audit trail's wording, `SHIPWICK_AGENTS` and which server an address is about, the browser's address for the audit trail, the sign-in's PKCE values, cookie and failure texts, a promotion's progress, the network row, a pasted deploy.yaml, the mock's YAML reader, roving focus and the focus trap, what is announced of a deployment, a promotion and the backups, and the titles of tabs |
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
  (`SameSite=Strict`, `Secure` on https, 7 days; one per server when several
  are configured; a person's session until the agent says it ends). The app only knows *whether*
  a session exists, and, from `GET /server`, the token's name and role.
  Nothing is kept in `localStorage` except the theme and, with several servers, the name of the one used last. A token created on the
  Access page is shown once, in the page, and never stored.
- **CSRF.** Every state-changing request (`POST`/`PUT`/`DELETE`, including
  sign-in and sign-out) must carry `X-Shipwick-Request: 1`, which the app's
  fetch wrapper (and the restore upload) always sends. A cross-origin page cannot add a custom header without
  a CORS preflight, and the server never grants one. `Sec-Fetch-Site`, where
  the browser sends it, must be `same-origin`. `SameSite=Strict` is the second
  layer. With `curl`, add `-H 'X-Shipwick-Request: 1'`.
- **The proxy is not an open relay.** The target host comes only from
  `SHIPWICK_AGENT_URL` or `SHIPWICK_AGENTS`; a request chooses among the
  configured names and never supplies an address. Only `GET`, `HEAD`, `POST`, `PUT`, `DELETE` are
  accepted; the path must stay under `/api/v1/` and each segment must match
  `[A-Za-z0-9._~-]` (no `..`, no encoded slashes). Only `Accept`,
  `Content-Type` and, for an upload, `Content-Length` are forwarded to the
  agent: the browser's cookies never are. `X-Forwarded-For` is set by the
  proxy itself, for the agent's audit trail: the last entry of the header a
  reverse proxy in front of the dashboard sent, when the connection comes
  from a private or loopback address (Caddy, in a Shipwick setup), and
  otherwise the connection's own address — a header a browser sends directly
  is ignored. A dashboard reached directly on a private network cannot tell
  the two apart; put it behind the proxy. `POST` bodies over 128 KB are
  refused; a `PUT` body (a volume archive, a secret's value) is streamed
  through and bounded by the agent's own limits.
- **The token is never logged**, by the proxy or the session routes; upstream
  errors are reported by their error code, never by serializing the request.
- **A 401 from the agent ends the session**: the proxy clears the cookie and
  the app returns to the login page. The route middleware is a convenience;
  the boundary is the server, which answers 401 without a valid cookie
  whatever the client does.
- **Post-login redirects** only accept same-site paths.
- **Signing in through a provider** keeps the client secret on the agent: the
  dashboard's server only starts the sign-in and passes the code on. `state`
  is compared in constant time, the PKCE verifier and the nonce never leave
  the server before the exchange, the sign-in cookie is cleared by the
  callback whatever happens, and the browser is only ever redirected to the
  `http(s)` authorization endpoint the agent names.
- **Headers** (production): CSP `default-src 'self'` (with inline script/style
  allowed, which Nuxt's bootstrap and the no-flash theme script need),
  `frame-ancestors 'none'`, `X-Frame-Options: DENY`, `nosniff`,
  `Referrer-Policy: no-referrer`.
- **Roles are enforced by the agent**, not here. Hiding or disabling a control
  for a `read` or `deploy` token is a courtesy; a request the role does not
  cover is answered `403` by the agent whatever the page does.
- Sign-in attempts are limited by the agent, not here: after 20 failed
  authentications within a minute from the dashboard server's address, wrong
  tokens are answered `429 RATE_LIMITED`, which the login page shows as such.
  Accounts are the agent's named tokens with roles; the dashboard has none of
  its own.
