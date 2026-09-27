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
agent/internal/config      environment variables, API token and encryption key resolution
agent/internal/store       SQLite: applications, deployments, replicas, events, tokens, job runs, metric samples
agent/internal/docker      Docker Engine API wrapper (never shells out)
agent/internal/health      the HTTP and TCP health probes
agent/internal/proxy       Caddy: config rendering, admin API client
agent/internal/notify      webhook notifications: queue, formats, signing
agent/internal/deploy      deployment engine: state machine, supervisor, operations, views
agent/internal/api         REST API: routing, auth, error envelope
pkg/spec                   deploy.yaml parser + validator   (shared with the CLI)
pkg/api                    API wire types                   (shared with the CLI)
pkg/cron                   five-field cron schedules        (shared with the CLI)
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
              ├─→ notify
              ├─→ proxy
              └─→ store
```

The engine reaches the outside world through three interfaces:
`deploy.Runtime` (Docker) and `deploy.Proxy` (Caddy), defined where they are
consumed, and `notify.Notifier` (the webhook). They exist for one reason — to
test the engine, failure paths especially, without a Docker daemon, a Caddy or
a chat service — and they are the only interfaces in the codebase.

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

### Recreate

`deploy.strategy: recreate` is for the application that cannot run twice:
anything with a volume, or that holds a lock or a port it cannot share. The
same `rollout` runs it with three differences: after `loadPrevious` the old
version is taken out of routing and *stopped* — not removed — one replica at a
time (`stopPrevious`); all new replicas then start as one batch, since nothing
runs that they could disturb; and a failure is undone by `rollBackRecreate`:
the new containers are removed first (they must be gone before the old version
touches the volumes again), then the stopped containers are started again,
nameless, and verified against the previous spec. If the switch had already
removed them, new containers of the old version are created instead; the
volumes are still there. The application is down from the stop to the moment
the new version is ready, and the events say so.

`pkg/spec` refuses volumes without `recreate`, and more than one replica with
volumes: two processes on one volume is how data gets lost, and Shipwick would
rather not offer the option.

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

A check has one of three kinds, decided by which `health` key is set, and the
engine dispatches on it in one place (`deploy/probe.go`) for both the
deployment and the supervisor:

| Kind | What one probe is | Why |
|---|---|---|
| `path` | `GET` as above | HTTP applications, the common case |
| `tcp` | A connection to `<container ip>:<tcp>`, closed once accepted; nothing is sent | Databases and queues that would log a protocol error on a stray HTTP request. `port` is not required: a check's port and the proxied port need not be the same |
| `command` | The argv run inside the replica through the Engine API's exec (`Runtime.Exec`), no TTY, no shell; exit 0 is healthy | A tool that knows the application (`pg_isready`) can tell "listening" from "ready". Only the last 4 KB of output are read and only its last line is reported, so a chatty check cannot grow an event |

A command that hangs is abandoned at `timeout` — the connection to the daemon
is closed, which ends the read — but the process itself keeps running inside
the container, since Docker has no way to kill an exec. Checks are meant to
be tools that finish.

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

## Jobs

The pre-deploy hook, scheduled jobs and `shipwick run` are one thing at three
moments: a container from the application's image, with its environment,
limits and networks, that runs one command and is removed. One code path
(`agent/internal/deploy/jobs.go`) creates, starts, waits for, reads out and
removes it; the three differ in who starts it and who waits.

| Decision | Why |
|---|---|
| **No volumes.** A job container never mounts the application's volumes. | A replica may be writing them; two writers on one volume is how data gets lost, which is why the recreate strategy exists. A job that needs the data goes through the application like everyone else. |
| **The hook runs before the old version is touched**, under `recreate` too. | So a failed migration fails a deployment that has not started anything: nothing to roll back, the serving version untouched. The price is that the hook runs next to the old code and must be compatible with it — which a rolling update demands of migrations anyway. |
| **Jobs hold no application lock.** The lock is taken only to record the run and start its container; the wait happens outside it. | A job may run for an hour. Deploying, stopping and deleting must not wait for it — and a deployment that finishes leaves a running job of the old version alone. The hook is the exception: it runs inside the deployment, which holds the lock anyway. |
| **The container starts under the lock**, though, not in the background. | Otherwise a delete or a deployment sweeping the application's containers could run between the run's record and its container, and the container would outlive its application. |
| **One run per job at a time**; `shipwick run` commands are independent. | A job that takes longer than its interval must not pile up; a firing while the last run is going is skipped and logged, not queued. |
| **Cron parsing in-house** (`pkg/cron`, five fields, UTC). | The syntax is small and settled; a dependency would bring time zones and macros nobody asked for. UTC because a server's local zone is nobody's, and a job should not move when the server does. |
| **The scheduler is the supervisor's ticker.** Once a minute it compares each running application's jobs with the minutes since it last looked at that application, and starts what fell due — once, however many minutes went by. | A busy application (being deployed) keeps its window and is looked at next tick, so a firing during a deployment is run after it, not lost. The first look after a start considers only the current minute: what fell due while the agent was down is not caught up. |
| **Job containers carry `com.shipwick.managed` and `com.shipwick.app`**, plus `com.shipwick.job` and `com.shipwick.run` instead of a replica index. | They are the agent's to clean up, so `ListContainers` returns them; everything that manages replicas skips containers with a job label, and `delete` removes them with the rest. |
| **On restart**, runs still `running` become `interrupted` and their containers are removed with the other leftovers; a job restarted by hand or by its schedule runs again as new. | The goroutine that waited on the container died with the old process; re-attaching would mean a second bookkeeping for one case. Shutdown does the same, deliberately: a run is marked `interrupted`, its container stopped and removed. |
| **A run keeps the tail of its output** (200 lines, 64 KB) and nothing else of the container. | The container is gone; the output is what someone reading "exit 1" needs. A failed hook's last 20 lines also go into the deployment's events, next to the error. Successful jobs record no event: they run often. |

## Metrics

`deploy/metrics.go`. CPU usage is a rate, so it takes two readings. Docker will
take both itself, a second apart — which makes every request cost a second.
Instead the agent remembers the last reading of each container and computes
the rate against it: a dashboard polling every 5s is answered in milliseconds
(measured: 1.01s for a first request, 5ms after). Readings too close together
(< 500ms) or too far apart (> 1m) are not compared; that request falls back to
Docker's blocking two-sample call.

Memory is reported as the working set (usage minus reclaimable page cache),
like `docker stats`; limits come from the deployment's spec rather than from
Docker, which reports the host's memory for an unlimited container.

**History** (`deploy/sampler.go`). A second loop, started with the supervisor
and driven the same way (`sample(ctx, now)`, time as a parameter), records one
row per running replica every 30 seconds: the cheap single reading, with the
rate computed against the previous one through the same cache the live endpoint
uses — so a dashboard that is polling and the sampler feed each other. The
first reading of a container only primes the rate; a replica appears in the
history a minute after it starts. Rows are written in one transaction per tick
and pruned once an hour to a retention of seven days.

The rows are raw and aggregation happens on read, in SQL: `GROUP BY` the
sample time divided by the step, average CPU, peak memory. Storing raw rows
costs little — a week of 30-second samples of three replicas is 60,000 rows,
a few megabytes — and buys a step chosen per query: the same table answers the
last hour in 30-second buckets, the last day in 5-minute and the week in
1-hour ones, each a few hundred points, without ever deciding at write time
what resolution somebody will want. Buckets with no sample are simply absent,
so a gap in the chart is a gap in the record, not a row of zeros.

## Notifications

`agent/internal/notify`. The engine knows one interface, `Notifier`, with one
method that must return at once; the implementation is a webhook, configured
through the agent's environment rather than deploy.yaml, because where the
operator wants to be told is a property of the server, not of an application.

What gets a notification is short on purpose: the outcome of every deployment
(`deployment.succeeded`, `deployment.failed`, `deployment.rolled_back` — the
last one both for an automatic rollback and a `shipwick rollback` that
succeeded), an application whose replicas have all stopped serving
(`application.down`) and its return (`application.recovered`), and a job that
failed (`job.failed`). A single replica restarting is not on the list: it is
in the event feed, and a message about every restart of a crash loop would
teach people to mute the channel. For the same reason *down* is "not one
ready replica" while *recovered* is stricter — every replica ready and none
with a restart still held against it, which the supervisor forgives after a
minute of running — so a crash-looping replica, which runs for a moment
between crashes, produces one outage and one recovery, not one of each per
attempt.

The message is a sentence for a human, derived from what happened and saying
what to do next ("my-api is still running 1.4.1; the failed deployment did not
affect it", "Shipwick is restoring 1.4.1; check with: shipwick status my-api").
Slack and Discord get only that sentence, in the one field each renders;
everything else gets a JSON object with the sentence and the facts beside it.

Delivery is a queue and a single sender goroutine: `Notify` enqueues and
returns, the sender posts in order, retries after 1s, 5s and 25s (not on a
4xx — a rejected request will be rejected again) with 10 seconds per attempt,
and `Close` drains for at most five seconds. A full queue drops the event and
logs the drop. Blocking the engine on a chat service was never an option, and
a deployment's own context is usually dead by the time its outcome is known,
so delivery has a life of its own. The URL is a credential — Slack's and
Discord's carry the token in the path — so it is validated without being
echoed, only its host is ever logged, and `url.Error`, which repeats the full
URL, is unwrapped before it reaches a log line. Plain `http` is refused unless
the host is loopback or a private address.

## Routing

Caddy terminates TLS and proxies to replicas; `agent/internal/proxy` tells it
what to route where. Shipwick does not touch certificates, ACME or HTTP/3 —
listening on `:443` with host matchers is all Caddy needs to do those itself.

**Routing is two things.** Who serves an application is decided by *names on
the services network*; what Caddy is told is only which name stands behind
which domain. The split exists because loading a Caddy config is not free:
Caddy replaces its servers, and a connection being established at that instant
— a TLS handshake in progress, a first request not yet read — is reset. That
reproduces with Caddy alone, from 2.7 to 2.11, and neither `grace_period` nor
`shutdown_delay` changes it. Measured with a new connection per request from
100 ms away, one reload per replica replaced cost 5 of 233 requests through two
minutes of redeploys. So replicas must come and go without Caddy hearing of it.

**Names.** Every installation has two bridge networks. A replica is born on
`shipwick` (the agent's health probes, user-run databases, and every replica)
and on `shipwick-services` (Caddy and every replica), the second one without
a name. When it is ready it is given its application's names there — `<app>`
and `<app>_<port>` as Docker network aliases — and it loses them when it is not:
running but failing its health check, or being restarted. A stopped container
drops out of Docker's DNS by itself. Docker sets an endpoint's aliases only when
the container joins the network, so changing them means leaving
`shipwick-services` and joining it again; that cuts what the replica has open
over *that* network, and nothing else — its database connections live on
`shipwick`. It is done to newcomers, which have nothing open, and never to a
replica on its way out: that one is stopped with its names on, and stopping is
what takes it out of DNS. (Taking it off the network first was tried; it cuts
the requests it is serving.)

```text
routing = for every replica that should serve: give it its names
        + for every running replica that should not: take them
        + tell Caddy: domain → <app>_<port>, for every name a running replica carries
```

- **Ready** means running and not failing its health check (`unknown` counts;
  `starting` does not — a restarted replica waits for its first passing check).
- **Caddy resolves the name for every request** (`dynamic_upstreams`, the `a`
  source, refreshed every second, IPv4 only — an AAAA query would be forwarded
  to the outside resolvers). Two versions on different ports are two sources of
  one route while the rollout lasts. A name is put in the config only while a
  running replica carries it: Docker's DNS forwards a name nobody carries to the
  outside resolvers, and every request would wait seconds for that to fail. With
  no such name the route is a static `503`, and a server-level error route turns
  the `502` Caddy would produce between a replica's death and the next sync into
  the same `503`.
- **A rollout waits 1.5 s** between naming a newcomer and stopping its
  predecessor, so that Caddy's next lookup has found the newcomer. Whoever named
  it — the rollout or the supervisor's tick, which syncs routing too.
- **Who syncs:** a rollout (at every swap), stop/start/delete, and the
  supervisor at the end of *every* tick. All of it under one mutex: naming is
  two calls to Docker, and two callers renaming the same replica collide in the
  middle. The proxy part is cheap because the rendered config is fingerprinted
  and an unchanged fingerprint skips the load; the config changes when a domain
  or a port does, and a rollout, a crash or a restart is not that.
- The fingerprint covers the *rendered config*, not just the routes, so an
  agent upgrade that renders routes differently reloads Caddy once. It is
  embedded as the `@id` of the final catch-all route; every 10s the agent asks
  Caddy for that id, and reloads if it is gone — i.e. if Caddy came back from a
  restart with an older config.
- **The names are service discovery.** `orders` reaches `payments` at
  `http://payments:8080` and gets a healthy replica of the current version,
  because a replica is on `shipwick-services` from its first second (it may
  need a peer to pass its own health check) and findable only once ready.
  Application names that would shadow Shipwick's own containers on that network
  (`agent`, `caddy`, `dashboard`, `localhost`) are refused by `pkg/spec`.

**During a rollout, the rollout dictates routing.** It registers a *route
override* — the exact list of replicas that serve right now, a mix of two
deployments — and updates it at every swap: newcomers in, sync (they get their
names), the settle wait, *then* stop the predecessors. The override exists for
the supervisor's sake: its tick computes routing from the database, which until
the commit still names the old deployment, and would hand the names straight
back to replicas the rollout has just retired. At the commit the override is
dropped; the database now says the same thing. On failure it is dropped too,
and routing follows the database back to the previous deployment. Under
`recreate` the override says "nobody" from the moment the old version is
stopped until the new one is ready.

A proxy or a Docker that cannot be updated fails the deployment at that swap,
before the predecessor is touched.

`stop` follows the same principle: the route goes to the static `503` and the
names are dropped with the containers, SIGTERM second, so no request is cut off
mid-flight. `start` brings replicas back nameless; they earn the names when
they are ready.

**What a crash costs.** An unplanned change is not lossless, and cannot quite
be: requests in flight on the dying replica are gone, and until the supervisor's
next tick moves the route to `503`, a request whose lookup still lists the dead
address is retried elsewhere (`try_duration` 5s, `dial_timeout` 500ms, passive
health checks) or, if it was the last replica, answered `503`. Measured on the
real stack (12 clients, 50 ms added latency, a new connection per request) a
planned change costs nothing: 15,774 of 15,774 requests through 18 rolling
redeploys, 0 Caddy loads.

**Upgrading to this from an agent that routed by container name.** The
replicas it finds on startup carry no names. `Recover` reads each container's
names from Docker, and the first sync gives the nameless ones theirs — before
Caddy is told to look for them; the Caddy config then changes once. Caddy
itself is recreated by that upgrade, because it joins the second network.

**Aliases and redirects.** An application's `aliases` are more hostnames in
the host matcher of its one route: the same handler, the same names behind it,
nothing else to keep in step. Its `redirects` are a second route with no
backend at all — a static `308` whose `Location` is `https://<domain>` plus the
request's path and query — so they answer while the application is stopped or
has no healthy replica, and a rollout never touches them. Both kinds of
hostname sit in host matchers on `:443` like the domain does, which is all
Caddy needs to obtain certificates for them — `https://www.example.com` has to
be answered before it can be redirected. `308` rather than `301` keeps the
method, so a `POST` to the old hostname stays a `POST`. During a rollout the
route override carries the whole set of hostnames, not only the domain, or the
aliases would disappear for its duration — and reappear with a reload.

**Security.** The config is built as Go data and marshalled, never templated:
input cannot change its structure (there is a test that tries). Domains,
aliases and redirects are validated hostnames, and a hostname belongs to one
application in one role: a deployment that claims one another application has
anywhere in its active configuration is refused, and the agent's own hostname
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
| Networks | Two bridge networks. `shipwick`: every replica, the agent (health probes) and whatever the user runs beside Shipwick. `shipwick-services`: every replica and Caddy; a replica carries its application's names here while it is ready (see Routing). No host ports are published, with the one exception under Ports — no port conflicts, replicas just work, and the proxy is the way in. |
| Volumes | `volumes` in deploy.yaml become named Docker volumes `shipwick_<app>_<volume>`, created with labels, mounted at the given path. They belong to the application: every deployment mounts the same ones, and nothing removes them — not a rollback, not `delete`. Never a host path. **Backup and restore** go through the replica's container and Docker's archive endpoints (`CopyFromContainer`, `CopyToContainer`), which read and write a container's filesystem whether or not it runs: no helper container, no image to pull, no shell. A backup holds the lock while it streams and is rewritten on the fly so that its entries are relative to the mount point. A restore is the one thing that removes a volume: with the application stopped, the container goes, then the volume, then `createReplicas` makes both again — empty — and the archive is extracted into the mount point before any process could write there. It is the only user of the create-only half of `ensureReplicas`. |
| Ports | None, unless deploy.yaml has `publish`; then exactly the listed container ports are bound on the server (`PortBindings`), on the address given or on every address, for services the proxy cannot serve because they are not HTTP. Only for recreate applications with one replica: a server port has one holder, so the old version is stopped before the new one binds it. The engine refuses, before anything is recorded, a port the agent or the proxy listens on and a port another application's active configuration publishes — Docker would refuse the bind too, but only at start, after the old version is gone. Published ports bypass the host firewall on most distributions (Docker inserts its own iptables rules), which is why the README says to bind to a private address. |
| Images | After a successful deployment, and after `delete`, the images that only retired deployments of the application name are untagged. Kept, across all applications: the active deployment's image and its most recent superseded one (the rollback target). Never forced: an image any container uses stays, so does anything no deployment ever named. |
| Restart policy | Docker's is set to `no`. Restarts belong to Shipwick's supervisor, which adds backoff, health awareness and crash-loop detection; two restart mechanisms would fight. |
| Limits | `resources.cpu` → `NanoCPUs`; `resources.memory` → `Memory`, with `MemorySwap` equal to it so the limit is a hard cap. |
| Hardening | Never privileged; `no-new-privileges`; no host mounts (named volumes only); nothing from `deploy.yaml` ever runs on the server itself. |
| Process | `entrypoint`, `command` and `user` go to Docker as `Entrypoint`, `Cmd` and `User`: argv as written, nothing split, joined or passed through a shell. Unset means the image's own. They change what runs inside the container, which the image always decided anyway; the container's boundaries are the same. |
| Logs | `json-file` driver capped at 3 × 10 MB per container, so a chatty app cannot fill the disk. `logging` selects another driver, whose options reach the daemon as written; the caps stay for `json-file` and `local` unless the application sets its own, and a collector address must be `scheme://host:port` — a socket, or a certificate file, is a path on the server, and no application gets to name one. `shipwick logs` reads what the daemon keeps locally, which for a remote driver is its dual-logging cache. |
| Job containers | `shipwick_<app>_job_<job>_<run id>`, e.g. `shipwick_my-api_job_nightly-report_42`; labels `com.shipwick.job` and `.run` instead of `.replica`. Same image, env, limits, networks and hardening as a replica; no volumes, no published ports, the image's entrypoint unless `entrypoint` overrides it, the job's command as the command. Removed when the run ends. See Jobs. |

## Storage

SQLite via `modernc.org/sqlite` (pure Go → static binary, trivial
cross-compilation, no CGO). WAL mode, foreign keys on, and a **single
connection**: the agent's write volume is tiny, and serializing access rules
out `SQLITE_BUSY` and lock-upgrade deadlocks by construction.

Tables: `applications`, `deployments`, `deployment_replicas`, `events`,
`tokens`, `metric_samples`, `job_runs` (one row per run of a hook, job or one-off
command, with the tail of its output; the last 50 per job are kept).
Migrations are an append-only list tracked in `PRAGMA user_version`.
Timestamps are fixed-width UTC text, so they sort lexicographically.

The spec of every deployment is stored with it (as JSON), which is what makes
rollback "deploy the spec of an older record again" rather than a separate
code path.

**Environment values are encrypted in that JSON**, and nothing else is. The
database file is the thing that travels: it is backed up, copied off the
server, opened with `sqlite3` by whoever debugs it, and `env` is where the
secrets are. Names, images, domains and the variable *names* stay in clear, so
the file remains debuggable; only the values are ciphertext.

- AES-256-GCM, a fresh 12-byte random nonce per value, the variable name as the
  additional authenticated data. Binding the name means a ciphertext cut from
  `DB_PASSWORD` and pasted into `DEBUG_ECHO` does not decrypt: a tampered
  database cannot make the agent hand a secret to a different variable.
- Stored form: `enc1:` + base64(nonce ‖ ciphertext). The prefix versions the
  scheme and marks the value as encrypted, which is what lets the agent
  recognize rows written by releases before encryption and encrypt them in
  place, in one transaction, on the first start after the upgrade.
- The key is 32 bytes, from `SHIPWICK_ENCRYPTION_KEY` or, by default,
  `encryption.key` (mode `0600`) in the data directory, generated with
  `crypto/rand` on first start. It lives next to the database rather than in
  it, so a copy of the database alone is useless; the price is that the file
  must be backed up with the database, and the agent says so when it creates
  it. A key that does not match the database is caught at start, not at the
  first deployment: opening the store proves the key against every stored
  value.
- The store does the sealing and opening at its boundary (`sealSpec` on write,
  `openSpec` on read), so the engine sees clear values, as it must to create
  containers, and every code path that persists a spec is covered by the same
  two functions. A store on disk refuses to open without a key; only the
  in-memory database of the tests may go without.

## Authentication

Every request carries a bearer token; every endpoint is registered with the
role it requires (`read` < `deploy` < `admin`), and one middleware decides.

The token the agent is configured with — `SHIPWICK_AGENT_TOKEN`, or the one
generated on first start — is the **root token**. It is not in the database:
the agent keeps only its hash, in memory and in `agent-token.sha256`, so a
lost or corrupt database can never lock the operator out. It is checked first,
by constant-time comparison of hashes, and is `admin` named `root`. Every other
token lives in the `tokens` table with its name, role and the SHA-256 of its
value. A presented token is hashed once and looked up by that hash — the column
is unique, so this is one indexed query — and the row's hash is then compared
in constant time as well, so the database's own comparison cannot be turned
into a timing oracle.

Token values are 32 random bytes in unpadded base64url behind the prefix
`swk_`. The prefix carries no entropy; it is there so that a token is
recognisable in the places it must never be — a log line, a commit — and so
that a leak scanner can grep for it.

`last_used_at` is written at most once a minute per token. It answers "is
anything still using this token?", for which the minute is plenty; written on
every request, a dashboard polling every few seconds would turn every read
into a write on the single connection.

Who did what is recorded where it is cheap and durable: the authenticated
token's name travels in the request context (`deploy.WithActor`), each
deployment stores it (`by` in the API), and stop and start events name it
unless it is root.

## Configuration as the API

`POST /applications/:name/deploy` takes the `deploy.yaml` document itself as
the body. JSON works too, since YAML subsumes it. One parser and one validator
(`pkg/spec`) serve both the CLI and the agent, so error messages are identical
on both sides — and the agent validates again regardless, because client-side
validation is a convenience, not a trust boundary.
