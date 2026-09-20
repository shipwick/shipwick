# Architecture

Shipwick is one long-running process per server — the **agent** — plus clients
that talk to it over HTTP: the `shipwick` CLI and the dashboard.

```text
              shipwick CLI / dashboard
                          │  HTTP + bearer token
                          ▼
                    Shipwick Agent
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
   Docker Engine API    Caddy           SQLite
          │
          ▼
      Containers
```

There is no control plane, no cluster, no external database. State lives in a
single SQLite file; the container runtime is the Docker daemon already on the
machine.

## Repository layout

```text
agent/cmd/shipwick-agent    entrypoint: wiring, signals, graceful shutdown
agent/internal/config      environment variables, API token resolution
agent/internal/store       SQLite: applications, deployments, replicas, events
agent/internal/docker      Docker Engine API wrapper (never shells out)
agent/internal/health      the HTTP health probe
agent/internal/proxy       Caddy: config rendering, admin API client
agent/internal/deploy      deployment engine: state machine, supervisor, operations, views
agent/internal/api         REST API: routing, auth, error envelope
pkg/spec                   deploy.yaml parser + validator   (shared with the CLI)
pkg/api                    API wire types                   (shared with the CLI)
cli/cmd/shipwick          CLI entrypoint
cli/internal/commands      cobra command tree, error rendering
cli/internal/client        agent API client
cli/internal/cliconfig     agent URL / token resolution
cli/internal/ui            terminal output
dashboard/                 Nuxt dashboard: browser → its own server (session, proxy) → agent
```

`agent/internal` is enforced by the Go toolchain: the CLI cannot import agent
internals, only the shared contracts in `pkg/`.

The dependency direction inside the agent is strictly one-way:

```text
api → deploy ─┬─→ docker
              ├─→ health
              ├─→ proxy
              └─→ store
```

The engine reaches the outside world through exactly two interfaces, both
defined where they are consumed: `deploy.Runtime` (Docker) and `deploy.Proxy`
(Caddy). They exist for one reason — to test the engine, failure paths
especially, without a Docker daemon or a Caddy — and they are the only
interfaces in the codebase.

## Deployment lifecycle

Every deployment is an immutable record that moves through a state machine
(`agent/internal/deploy/state.go`):

```text
PENDING → BUILDING → STARTING → HEALTH_CHECKING → HEALTHY → ACTIVE → SUPERSEDED
    │         │          │              │             │
    └─────────┴──────────┴──────────────┴─────────────┴──→ FAILED
                                                              │
                                        ROLLED_BACK ← RESTORING ← ROLLBACK
```

| State | What happens |
|---|---|
| `PENDING` | Record created, application lock held. |
| `BUILDING` | Image is pulled. Shipwick does not build images; this state covers obtaining one. If the pull fails but the image exists locally, the local copy is used and a warning event is recorded. |
| `STARTING` | Network ensured; the first new replica is created, recorded and started (on a first deployment: all of them). |
| `HEALTH_CHECKING` | With a `health` block: every replica must answer its check once within `interval × retries`, probed every second (see [Health and supervision](#health-and-supervision)). Without one: replicas must stay running for a stabilization window (3s). Either way, a replica that exits fails the deployment at once, and its last log lines are saved as an event — the container is about to be deleted, and with it the only clue. |
| `HEALTHY` | Every replica has been replaced and serves. |
| `ACTIVE` | **Commit point.** One SQLite transaction promotes the deployment, marks the previous one `SUPERSEDED`, and repoints the application. A final sweep removes any container of the application that does not belong to it. |
| `FAILED` | Nothing of the old version had been retired yet: routing returns to it, the new containers are discarded, done. Otherwise the rollback path below is taken. |
| `ROLLBACK` → `RESTORING` → `ROLLED_BACK` | The replicas of the previous deployment that had already been retired are recreated from its stored spec and verified like any new replica; then traffic returns to them and the new containers are removed. If even that fails: `FAILED`, with both causes recorded — and reconciliation keeps trying, since the previous deployment is still the active one. |

Illegal transitions are rejected by the engine, and every transition is a
compare-and-swap in the database (`UPDATE … WHERE status = <expected>`), so a
stale writer can never clobber a newer state.

### Rolling, and what it costs

`deploy/rollout.go`. A rollout replaces replicas one at a time:

```text
for every replica i:
    start new i → wait until it is ready → route to new i instead of old i → retire old i
then: retire old replicas that have no successor (scaling down) → commit
```

Replicas with nothing to replace — a first deployment, scaling up — start
together as one batch, so that N replicas do not cost N waits.

**Why not start the whole new version next to the old one?** That was the
first design, and it has a lovely property: nothing of the old version is
touched before the commit, so failure handling is just "delete the new
containers". But it needs 2N containers for the duration, and Shipwick is for
small servers — a 1 GB application with two replicas would need 4 GB to
deploy. Rolling peaks at N+1 and never serves with fewer than N.

**The price is a real rollback.** Old replicas are retired *before* the commit,
so a failure half-way leaves the previous version incomplete. It is then
completed again: the retired replicas are recreated from the previous
deployment's stored spec (`ensureReplicas` — the same function rollouts and the
supervisor's reconciliation use; there is one way replicas come to exist),
verified against *its* health check, and only then does traffic leave the new
replicas that were already serving — so capacity does not dip a second time.
What keeps this tractable:

- The database never stops calling the previous deployment *active* until the
  commit. A rollback therefore changes no deployment pointers, and if the agent
  dies mid-rollout, `Recover` plus reconciliation arrive at the same end state
  the rollback would have.
- The common failure — a bad image — shows at the *first* replica, before
  anything was retired. That path is still "delete the new containers":
  `FAILED`, no rollback.
- During a rollout the application's routing is dictated by the rollout (see
  the *route override* under [Routing](#routing)), since no single deployment
  record describes "new 1, old 2, old 3".
- An old replica is retired on an uncancellable context: a half-retired
  replica helps nobody, and rollback relies on "retired" meaning gone.
- If the agent is shutting down, a failing rollout does not start a restore it
  could not finish; the next start completes the old version.

### Rollback and redeploy on request

`Engine.Rollback` and `Engine.Redeploy` contain no deployment logic. Each
resolves a spec — from an earlier `SUPERSEDED` deployment, or from the active
one — and hands it to `Engine.start`, the single entry point every deployment
goes through; `Deploy` does the same with a spec that came over the wire. The
spec is resolved *under the application's lock*, so "the active deployment" is
still the active deployment when the new record is created.

Consequences that fall out rather than being built: a rollback rolls, is
health-checked, is zero-downtime, and a rollback that fails is itself rolled
back. The record keeps its origin (`kind`, `source_deployment_id`), and history
is only ever appended to.

### `completed_at`: when is a deployment *done*?

A deployment's status settles (`ACTIVE`, `FAILED`) slightly before the engine
is finished with it — old containers still need their graceful shutdown. During
that window the application is still locked. `completed_at` is stamped only
after cleanup **and** after the lock is released, so it is the reliable signal:

> Poll `GET /deployments/:id` until `completed_at` is set. From that moment a
> new operation on the application is guaranteed not to be rejected as busy.

### Concurrency

One operation per application at a time: deploy, stop, start, delete **and the
supervisor** contend for the same in-memory lock. Different applications are
fully independent. There is no queue — a queue hides the conflict from the
person who needs to know about it.

The lock knows who holds it, because a user operation that finds it taken
deserves a different answer in each case:

| Held by | A user operation… |
|---|---|
| another user operation | fails at once with `409 DEPLOYMENT_IN_PROGRESS`: a real conflict, theirs to resolve |
| the supervisor | **waits** (up to 30s). The supervisor holds an application for moments, to restart a replica; "operation in progress" would be a baffling answer to a lone `deploy` |

The supervisor itself never waits: a busy application is simply looked at again
on the next tick.

### Crash safety

At startup, before serving requests, the agent reconciles (`Engine.Recover`):

1. Deployments found mid-flight are marked `FAILED` ("agent restarted during
   deployment") — their goroutine died with the old process.
2. Containers that belong to a **known** application but not to its active
   deployment are removed.
3. Containers of applications the database does **not** know are never touched.
   If the database is lost, the agent must not tear down what is running.

Running applications do not depend on the agent being up: restarting or
upgrading the agent does not restart any container.

## Health and supervision

`agent/internal/health` performs one probe: `GET http://<container ip>:<port><path>`,
2xx is healthy. It deliberately goes direct (never through a proxy from the
agent's environment), opens a fresh connection per probe (a kept-alive
connection can outlive a wedged listener and lie), and does not follow
redirects (a redirect is not a 2xx, and could walk the probe off the container
network).

The same probe is used in two places with different patience:

- **Deploying** — `interval × retries` is the replica's *startup budget*.
  Probing every second instead of every `interval` means a connection refused
  by a booting app is "not yet", not a strike, and a fast app is not made to
  wait ten seconds to be told it was ready after one.
- **Running** — the supervisor probes every `interval`; `retries` consecutive
  failures make a replica `unhealthy`. After a supervisor restart the replica
  is `starting` and gets its startup budget again before failures count.

The **supervisor** (`deploy/supervisor.go`) is one loop, ticking every second.
Per application — and only while holding that application's lock, so it can
never interleave with a deployment — it compares the replicas of the active
deployment with what Docker reports:

```text
exited  ──policy allows?──no──▶ leave stopped, say so once
   │yes
   ▼
wait backoff[restarts] ──▶ start ──▶ restarts++ ──restarts ≥ 5──▶ CRASH_LOOP (retry every 5m)

running + unhealthy ──policy ≠ never──▶ same backoff ──▶ restart
running + proven for 60s ──▶ restarts = 0, crash loop cleared
```

Design points:

- **Backoff `1s, 2s, 5s, 10s, 30s`, then every 5 minutes, forever.** "Do not
  restart in a hot loop" and "give up" are different things. Most crash loops
  are caused by a dependency being down; the slow retry makes the application
  come back by itself when the dependency does.
- **Docker's own restart policy is `no`.** Two restart mechanisms would fight,
  and only this one knows about backoff, health and crash loops.
- **"Proven" means more than running.** A replica with a health check is only
  forgiven once it has actually passed it — otherwise an app that runs but
  never answers would be forgiven every minute and never reach `CRASH_LOOP`.
- **State is in memory**, per agent process: after an agent restart every
  replica starts with a clean slate, and its health is `unknown` until probed.
  `unknown` counts as fine: an application must not flap to `DOWN` because its
  *supervisor* was restarted. Only the restart counter is persisted, for display.
- **Probes run outside the tick**, one goroutine each, at most one in flight
  per replica: a probe that takes its full timeout must not delay the restart
  of some other application's replica.
- **The tick takes the time as a parameter**, so tests drive the entire backoff
  schedule — twelve simulated minutes — in milliseconds, deterministically.
- **Reconciliation.** A replica whose container no longer exists (removed by
  hand, pruned) is recreated from the active deployment's stored spec —
  desired 3, present 2 → create 1 — on the same backoff as restarts, and
  containers of any *other* deployment of the application are swept away.
  Both rely on the container list being taken **under the application's
  lock**: a snapshot from before the lock could predate a deployment that has
  since finished, and would make its brand-new replicas look missing. Before
  a replica is declared gone, Docker is asked once more, by ID.

What the supervisor sees and does is recorded as application events
(`GET /applications/:name/events`, shown by `shipwick status`), capped at the
newest 500 per application — a crash loop would otherwise grow the table
forever.

For probes to work the agent must reach container addresses. Running in a
container, it finds itself through the Docker API (hostname = container ID) and
joins the application network on its own if it is not on it already.

## Metrics

`deploy/metrics.go`. CPU usage is a rate, so it takes two readings. Docker will
take both itself, a second apart — which makes every request cost a second.
Instead the agent remembers the last reading of each container and computes
the rate against it: a dashboard polling every 5s is answered in milliseconds
(measured: 1.01s for a first request, 5ms after). Readings too close together
(< 500ms) or too far apart (> 1m) are not compared; that request falls back to
Docker's blocking two-sample call.

There is no background sampler: with nobody watching, nothing is measured, and
nothing is stored — history is the client's business. Memory is reported as the
working set (usage minus reclaimable page cache), like `docker stats`; limits
come from the deployment's spec rather than from Docker, which reports the
host's memory for an unlimited container.

## Routing

Caddy terminates TLS and proxies to replicas; `agent/internal/proxy` tells it
what to route where. Shipwick does not touch certificates, ACME or HTTP/3 —
listening on `:443` with host matchers is all Caddy needs to do those itself.

**The agent owns the whole configuration.** On every change it renders the
complete Caddy JSON from the desired routes and `POST`s it to `/load`, which
Caddy applies gracefully. There is no patching of config paths, hence no
half-applied state and no drift: same routes in, same bytes out.

```text
desired routes = for every application with a domain:
                   the ready replicas of its serving deployment
                 + the agent's own route (SHIPWICK_AGENT_DOMAIN)
```

- **Ready** means running and not failing its health check (`unknown` counts;
  `starting` does not — a restarted replica waits for its first passing check).
- **Upstreams are container names**, not IPs: a restarted container may get a
  new address, and Docker's DNS always knows the current one.
- **Who syncs:** a rollout (at every swap), stop/start/delete, and the
  supervisor at the end of *every* tick. That last one is what takes crashed
  and unhealthy replicas out of rotation. It is affordable because the rendered
  config is fingerprinted and an unchanged fingerprint skips the reload.
- The fingerprint covers the *rendered config*, not just the routes, so an
  agent upgrade that renders routes differently reloads Caddy too. It is
  embedded as the `@id` of the final catch-all route; every 10s the agent asks
  Caddy for that id, and reloads if it is gone — i.e. if Caddy came back from a
  restart with an older config.

**During a rollout, the rollout dictates routing.** It registers a *route
override* — the exact list of replicas that serve right now, a mix of two
deployments — and updates it at every swap: new replica in, its predecessor
out, sync, *then* retire the predecessor. The override exists for the
supervisor's sake: its tick computes routes from the database, which until the
commit still names the old deployment, and would route traffic straight back to
replicas the rollout has just retired. At the commit the override is dropped;
the database now says the same thing. On failure it is dropped too, and routing
follows the database back to the previous deployment.

A proxy that cannot be updated fails the deployment at that swap, before the
predecessor is touched.

`stop` follows the same principle in reverse: routing goes to "no upstreams"
(`503`) first, SIGTERM second, so no request is cut off mid-flight.

**What a crash costs.** Planned changes are lossless (measured under constant
load: 100/100 `200` through a rolling redeploy, 76/76 through a rollout that
failed half-way and was rolled back). An unplanned one is not, and cannot quite be:
requests in flight on the dying replica are gone, and the one request that
discovers the dead upstream may get a `502`. Caddy would normally retry it on
another replica, but it abandons retries in progress when its config is
reloaded — and removing the dead replica *is* a reload. A short `dial_timeout`
(Docker's DNS takes seconds to give up on a name it no longer knows) and
passive health checks keep that window to about half a second and one request.
Delaying the removal to let retries finish was considered and rejected as not
worth the machinery.

**Security.** The config is built as Go data and marshalled, never templated:
input cannot change its structure (there is a test that tries). Domains are
validated hostnames, unique across applications, and the agent's own hostname
cannot be claimed. The admin API is reached over a unix socket in a volume only
the agent and Caddy share; on a TCP port of the application network, any
application container could take over all routing.

### Graceful shutdown

On `SIGINT`/`SIGTERM`: end log streams → stop accepting HTTP requests → stop the
supervisor and cancel in-flight deployments (each is marked `FAILED` and cleaned
up, on a fresh context) → wait up to 30s → close Docker client and database. A
second signal kills immediately.

## Containers

| Aspect | Decision |
|---|---|
| Name | `shipwick_<app>_<deployment sequence>_<replica>`, e.g. `shipwick_my-api_7_1`. For humans. App names cannot contain `_`, so it parses unambiguously. |
| Identity | Labels `com.shipwick.managed`, `.app`, `.deployment`, `.replica`. The agent finds its containers by label, never by name. |
| Network | All containers join one bridge network (`shipwick`), shared with the agent (health probes) and Caddy (upstreams). **No host ports are published** — no port conflicts, replicas just work, and the proxy is the only way in. |
| Restart policy | Docker's is set to `no`. Restarts belong to Shipwick's supervisor, which adds backoff, health awareness and crash-loop detection; two restart mechanisms would fight. |
| Limits | `resources.cpu` → `NanoCPUs`; `resources.memory` → `Memory`, with `MemorySwap` equal to it so the limit is a hard cap. |
| Hardening | Never privileged; `no-new-privileges`; no host mounts; no user-controlled command execution. |
| Logs | `json-file` driver capped at 3 × 10 MB per container, so a chatty app cannot fill the disk. |

## Storage

SQLite via `modernc.org/sqlite` (pure Go → static binary, trivial
cross-compilation, no CGO). WAL mode, foreign keys on, and a **single
connection**: the agent's write volume is tiny, and serializing access rules
out `SQLITE_BUSY` and lock-upgrade deadlocks by construction.

Tables: `applications`, `deployments`, `deployment_replicas`, `events`.
Migrations are an append-only list tracked in `PRAGMA user_version`.
Timestamps are fixed-width UTC text, so they sort lexicographically.

The spec of every deployment is stored with it (as JSON), which is what makes
rollback "deploy the spec of an older record again" rather than a separate
code path.

## Configuration as the API

`POST /applications/:name/deploy` takes the `deploy.yaml` document itself as
the body. JSON works too, since YAML subsumes it. One parser and one validator
(`pkg/spec`) serve both the CLI and the agent, so error messages are identical
on both sides — and the agent validates again regardless, because client-side
validation is a convenience, not a trust boundary.
