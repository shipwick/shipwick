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
agent/internal/certs       checks on certificates the operator supplies
agent/internal/notify      webhook notifications: queue, formats, signing
agent/internal/backup      where backups are kept: the directory, the S3 client, encryption on the way
agent/internal/disk        how full the disk under the data directory is
agent/internal/oidc        signing in: the provider's endpoints and keys, the code exchange, ID token verification
agent/internal/deploy      deployment engine: state machine, supervisor, operations, views
agent/internal/api         REST API: routing, auth, error envelope
pkg/spec                   deploy.yaml parser + validator   (shared with the CLI)
pkg/api                    API wire types                   (shared with the CLI)
pkg/cron                   five-field cron schedules        (shared with the CLI)
pkg/backupfile             the encrypted backup format      (shared with the CLI)
pkg/cloudflare             Cloudflare's address ranges      (shared with the CLI)
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
api → deploy ─┬─→ backup
              ├─→ docker
              ├─→ certs
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
| `BUILDING` | Image is pulled. The agent does not build images; this state covers obtaining one. If the pull fails but the image exists locally, the local copy is used and a warning event is recorded. An image under `shipwick.local` (see [Images without a registry](#images-without-a-registry)) is never pulled: it is on the server already or the deployment fails. |
| `STARTING` | Network ensured; the first new replica is created, recorded and started (on a first deployment: all of them). |
| `HEALTH_CHECKING` | With a `health` block: every replica must answer its check once within `start_period + interval × retries`, probed every second (see [Health and supervision](#health-and-supervision)). Without one: replicas must stay running for a stabilization window (3s). Either way, a replica that exits fails the deployment at once, and its last log lines are saved as an event — the container is about to be deleted, and with it the only clue. |
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
  commit. A rollback therefore changes no deployment pointers, and an agent
  that dies mid-rollout finds everything it needs to go on (see
  [Crash safety](#crash-safety)).
- The common failure — a bad image — shows at the *first* replica, before
  anything was retired. That path is still "delete the new containers":
  `FAILED`, no rollback.
- During a rollout the application's routing is dictated by the rollout (see
  the *route override* under [Routing](#routing)), since no single deployment
  record describes "new 1, old 2, old 3".
- A replaced replica is retired in the background (below). Whatever relies on
  "retired" meaning gone waits for it: the rollback, before it creates
  containers of the same names.
- If the agent is shutting down, a failing rollout does not start a restore it
  could not finish; the next start completes the old version.

### Retiring a replica, and what a deployment waits for

Replacing the single replica of a ten-line Node application
(`CMD ["node", "server.js"]`) took 13.5 and 14.0 seconds, measured against
Docker 29 with the development stack, where its first deployment took 2.0.
The events show where the time went: 1.0s until the new replica passed its
health check, then 12.0s until its predecessor was retired — the 1.5s the
proxy is given to find the newcomer, and 10.3s of `docker stop`. Node, running
as PID 1, has no handler for `SIGTERM`, and the kernel delivers to PID 1 only
the signals it handles: the process never saw the signal and was killed when
the grace period ran out (exit code 137). Any program started as PID 1 without
a handler behaves this way, and the deployment sat through it once per
replica.

Two fixes were candidates, and they answer different questions.

*Run every replica under an init* (`HostConfig.Init`: Docker puts `tini` in as
PID 1, which forwards signals and reaps zombies). Measured: the same container
then stops in 0.3s, exit code 143. Not done, for two reasons. It changes
what the user's image is — an image built on s6-overlay refuses to start
(`s6-overlay-suexec: fatal: can only run as pid 1`, tried with
`lscr.io/linuxserver/baseimage-alpine:3.20`), and one that already runs
`tini` gets a second one above it and a warning in its log on every start.
And it makes a slow deployment fast by ending the old process at once: an
application that has no `SIGTERM` handler has no graceful shutdown either, so
what it was serving is cut either way — the init only moves the cut ten
seconds forward. That is the application's to decide, not a default to
impose on every image — so it is a key of deploy.yaml, off unless set:
`init: true` is `HostConfig.Init` on every container made from the
application's image, replicas, the pre-deploy hook, jobs, one-off commands
and the container a backup is verified in alike, because the image is what
has or lacks an init, not the role a container plays. Without the key the
field is left unset rather than false, so a daemon configured with
`"init": true` keeps deciding for itself. Measured through the agent on
Docker 29, the ten-line Node server again: `shipwick stop` returned after
0.3s with `init: true` (exit code 143) and after 10.4s without (137).

*Do not wait for it.* By the time a replica is retired, its successor has
been verified and has taken its place; the deployment has done what it was
for. So `swap` hands the old replica to `retireInBackground`
(`deploy/drain.go`): `SIGTERM`, the grace period, removal, in a goroutine of
its own, outside the application's lock — and the rollout goes on. The same
deployment now takes 2.5–3.0s, and the time no longer depends on how the old
process takes the signal. What keeps this within the rules:

- **The lock.** A draining container belongs to no operation. The supervisor
  and a later deployment's sweep, which remove whatever does not belong to the
  active deployment, skip it (`draining`); everything that needs it gone waits
  for it (`awaitDrains`): the next batch of the same rollout, a later
  deployment before it starts a replica, a rollback, `delete`. `completed_at`
  still means that the next operation is accepted.
- **N+1.** A rollout waits for the replica it just replaced before it starts
  the next one, so an application with several replicas still pays one grace
  period per replica but the last, and never runs two containers more than it
  asks for. A deployment that starts while its predecessor's last replica is
  still draining waits likewise, and says how long (`Waited 5s for a replaced
  container to stop`).
- **Recreate** does not drain: the old version is stopped before the new one
  starts, and removing a stopped container takes no time.
- **Shutdown.** Drains run under the engine's context and are counted as
  operations. An agent that stops does not sit out a grace period: Docker
  finishes a stop it was asked for whether or not the caller is still there
  (checked: a stop request abandoned after 2s still killed the container at
  its 8s), and the container is removed as a leftover at the next start.

One thing is unchanged and worth knowing: a replaced replica keeps its names
on the services network until its process exits (taking them away would cut
the requests it is finishing, see [Routing](#routing)). A process that stops
listening on `SIGTERM` leaves the rotation at once; one that ignores the
signal keeps receiving its share of requests until it is killed, and the
requests it holds at that moment are lost. The agent cannot tell the two
apart beforehand, so it says so afterwards: a replaced container that ended
with exit code 137 gets a warning in the application's events, with what can
be done about it — `init: true`, a handler, a longer `deploy.stop_timeout` —
and without the first when the configuration the container ran with already
has it. `stop` says the same about a replica it had to kill.

While it drains, the container is still one of the application's as far as
Docker's labels go, and `GET /applications/:name` lists it. It carries
`stopping: true` there, read from the same map that makes the supervisor
skip it, so that a client need not infer it from a deployment id that is not
the active one: `shipwick status` shows it below the replicas, and it was
never counted among them — `replicas` counts the active deployment's
containers, or during a rollout the ones the rollout has serving.

`deploy.stop_timeout` (1s–10m) is the grace period of an application, for the
one that holds WebSockets or long uploads; the agent's default is 10s. It is
read from the configuration of the deployment the container belongs to — the
version that is stopping, not the one replacing it — everywhere a replica is
stopped gracefully: a rollout, `stop`, `delete`, the supervisor restarting an
unhealthy replica, leftovers. Job containers keep the default.

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

### Images without a registry

An application with `build:` in its deploy.yaml has no image in any registry.
The CLI builds it (`docker build`, as an argv, never through a shell) on the
developer's machine for the server's architecture, tags it
`shipwick.local/<app>:<UTC stamp>-<4 hex>`, saves it and streams the archive
to `POST /applications/:name/images`; the agent loads it through the Engine
API (`ImageLoad`) and the CLI then deploys a deploy.yaml whose `image` is that
reference. From the engine's point of view nothing is new: a deployment with
an image, which happens to be present already.

The agent never builds, and this is a decision rather than an omission. A
build runs whatever the Dockerfile says, with the network, CPU and memory of
the machine it runs on; the server's job is to serve, and the developer's
machine has the source, the cache and Docker already. Loading an archive is
the one thing the server does, and it is the same primitive `docker load`
uses.

`shipwick.local` is a reserved host that does not resolve. That is the point:
an image under it can never be pulled, so the engine does not try — not in
`pullImage`, not when a replica is recreated and its image turns out to be
gone. Instead the deployment fails with a sentence that says the image was
built on a developer's machine and asks for `shipwick deploy` from the project
again. The same host tells the agent which uploads to accept: an archive for
`my-api` must carry exactly one image tagged `shipwick.local/my-api:<tag>`, so
no upload can plant an image in another application's name; anything else is
refused and untagged again. deploy.yaml validation enforces the same rule from
the other side — next to `build`, only a `shipwick.local` image — and a
redeploy with an image from elsewhere is refused.

Local images live in Docker's store like pulled ones, so image pruning treats
them alike: the active image and the rollback target stay, older ones go. A
rollback to a pruned local image fails cleanly with the sentence above, where
a registry image would have been pulled again.

Only the first deployment sends the whole image. An image built for the
next one shares its base layers, and usually all but the last, with one the
server has, and those are left out of the archive. The mechanism rests on
three things Docker 29 was observed to do, with the classic image store and
with the containerd one alike:

- `docker save` writes an OCI layout: every layer is a file
  `blobs/sha256/<digest>`, and `manifest.json`, which follows them, lists
  those files base layer first. The classic store writes layers uncompressed,
  named by their diff IDs; the containerd store writes the compressed blobs it
  holds, named by their own digests.
- Both stores keep a layer under the chain of diff IDs beneath it — a layer of
  the classic store, a snapshot of the containerd one — and `docker load` does
  not open the file of a layer it has. An archive without those files loads,
  and the image runs, whatever store or compression the layers first arrived
  in.
- An archive that leaves out a layer the daemon lacks does not load. The
  classic store answers with an error. The containerd store records and tags
  the image and reports "Error unpacking image" as a progress line; the agent
  reads that line as the failure it is and untags the image.

So a layer is identified by its diff ID, which both stores report as an
image's `RootFS`, and is "on the server" when some image there starts with
the same diff IDs up to and including it. `POST
/applications/:name/images/missing` compares the diff IDs of the image about
to be sent with the `RootFS` of every image the daemon lists and answers the
rest. The CLI reads the saved archive twice from the local daemon — once to
its end for `manifest.json`, which says which file holds which layer, then
again as a tar stream copied to the upload without the files of the layers the
server has. Nothing is held in memory or on disk but one tar header at a time;
the first pass costs a local read (about a second for 58 MB), and is skipped
when the server has none of the layers.

The agent trusts none of it. The reduced archive goes through the same
`ImageLoad` and the same checks as a whole one, and Docker itself decides
whether the layers it was not given are there: an archive that names content
the daemon lacks fails in the daemon (`409 IMAGE_INCOMPLETE`) and leaves
nothing behind. The question reveals only whether the server has layers whose
digests the caller already knows, and takes the role that uploads take.

The optimization never decides a deployment. An agent without the endpoint, a
local Docker that does not report layers, an archive in another layout (more
than one image, layers outside `blobs/sha256`), a count of layer files that
differs from the count of diff IDs, a refused reduced upload — an image
pruned between the question and the upload, say — each ends with the whole
archive being sent, as before, and the output says only what was sent.

On the containerd store an image loaded this way holds the snapshots of all
its layers and the blobs of only those that were sent. It runs, and a later
image builds on its snapshots, but `docker save` or `docker push` of it on the
server would be incomplete. An export is the one thing that asks the daemon
to write an image out, and it asks before it commits to the answer: see
[Moving a server, and standing by](#moving-a-server-and-standing-by).

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
is finished with it — leftovers are swept, images pruned, a failed rollout's
containers removed. During that window the application is still locked.
`completed_at` is stamped only after cleanup **and** after the lock is
released, so it is the reliable signal. (The replicas a rollout replaced may
still be on their way out by then; nothing waits for them that does not have
to, see above.)

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

1. Deployments found mid-flight are **resumed** (below). Each takes its
   application's lock right there, before the supervisor or a request could.
2. Containers that belong to a **known** application but neither to its active
   deployment nor to one that resumes are removed, in the background: starting
   does not wait for anybody's grace period.
3. Containers of applications the database does **not** know are never touched.
   If the database is lost, the agent must not tear down what is running.

Running applications do not depend on the agent being up: restarting or
upgrading the agent does not restart any container.

**Deployments outlive the agent** (`deploy/resume.go`). An upgrade of Shipwick
restarts the agent, and a deployment running at that moment used to fail for
no reason of its own. Now an agent that shuts down leaves it exactly as it is
— record in flight, `completed_at` unset, containers in place — and the agent
that starts next goes on with it; a crash is the same thing without the
warning. Clients polling the deployment see the agent gone for a moment and
then the event `Resumed after the agent restarted`.

Nothing is written down for this beyond what a deployment records anyway.
Where it was is its status, and what it had done is on the server:

- its containers carry its labels; those the store has a row for are adopted
  instead of created again, and started if they were only created (one that
  was created and never recorded — the agent stopped between the two — is
  removed: nothing knows it, and its name is needed);
- a replica that carries its application's names on the services network was
  given them after it was verified, so it was serving and serves on, and the
  replica it replaced goes if it is still there;
- a replica of the previous version whose container is gone was retired.

From that the rollout rebuilds what it held in memory — who serves, what is
left to replace, whether anything was retired — and takes over the
application's routing before `Recover` returns, so that not one proxy
configuration is computed from the database alone, which until the commit
names a deployment whose replicas are partly gone. Then `execute` runs as it
always does, with two differences: a status that has been reached is not
entered again (`reach`; every transition is still one step of `CanTransition`
and a compare-and-swap), and replicas come from `bring`, which adopts what
exists and leaves the rest to `ensureReplicas`. Everything else is simply
done again, because it can be: pulling an image, waiting for a replica to
answer, retiring a replica that still exists.

| Interrupted while… | On resume |
|---|---|
| pulling (`BUILDING`) | The pull is repeated. An image that is gone for good — a `shipwick.local` one — fails the deployment as it would have |
| the pre-deploy command ran | **Fails**: "the pre-deploy command was interrupted when the agent restarted, and is not run a second time". Whether a migration that was cut off can run again is for its author to say. A command that had finished is not repeated either, and the deployment goes on |
| starting (`STARTING`) | Created containers are adopted and started, missing ones created |
| health checking | The replica is adopted and checked again; its health budget starts over |
| between two replicas | Replicas already serving keep serving; the rollout continues with the next |
| `HEALTHY`, before the commit | Committed |
| rolling back (`ROLLBACK`, `RESTORING`) | The rollback is finished: missing replicas of the previous version are created, all of them verified, what is left of the failed version removed |
| after it settled (`ACTIVE`, `FAILED`) | Nothing to resume; leftovers are removed and reconciliation completes whatever version is active |

What cannot be resumed fails as every interrupted deployment used to:
`FAILED`, "agent restarted during deployment…", its containers removed as
leftovers. That is a record the engine cannot have left — two deployments of
one application in flight, a rollback towards a version that is not the
active one — and a state that cannot be read back from Docker. A resumed
deployment gets a fresh deployment timeout; the restart is not counted
against it.

One guess remains. A replica of the new version without a `port` carries no
names, so nothing says whether it had been verified: it is verified again.
The cost is a second stabilization window, not a wrong answer.

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

- **Deploying** — `start_period + interval × retries` is the replica's
  *startup budget*. `start_period` exists so that a slow starter buys time at
  startup only; raising `retries` would also delay noticing a running
  replica's failure.
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
| **A timeout ends the command, not only the wait.** At its limit the container is stopped — `SIGTERM`, then `SIGKILL` after the agent's stop timeout — and removed, and the run is `timed_out`. | A container is something the daemon can stop, which is the reason hooks and jobs are containers and not commands started inside a replica. Seen on a real daemon for a job and for a hook: nothing of either was left. |
| **A run keeps the tail of its output** (200 lines, 64 KB) in its record; the log archive keeps more of it (below). | The container is gone; the output is what someone reading "exit 1" needs. A failed hook's last 20 lines also go into the deployment's events, next to the error. Successful jobs record no event: they run often. |

## The log archive

What a container printed lives in Docker's log of that container and is gone
when the container is: a replica that a rollout replaced, the replicas of a
deployment that failed, a job that finished. A replica that crashes and is
started again keeps its container, and its log, only until the driver rotates
it away. `agent/internal/deploy/logarchive.go` copies the end of every run
of a container out before that happens; `logsearch.go` reads it back.

| Decision | Why |
|---|---|
| **The unit is a run of a container**: from a start to the stop after it. Every copy begins where the last copy of the same container ended — the later of that run's end and of its last line, read back from the entry — and an in-place copy ends at the container's `FinishedAt`. | It is what makes "without storing a line twice" hold without bookkeeping of its own: a crash loop leaves one entry per attempt, a stop followed by a start and a rollout leaves two, and the removal of a container whose runs are all archived leaves none. The mark is in the database, so it survives the agent. |
| **One place where output enters** (`archiveLogs`), called where a run ends: by the supervisor when it finds a replica down or restarts an unhealthy one, by `Stop`, and by `removeContainer` and `collectJob` before they remove a container. | Removal has one path for replicas and one for jobs; hooking those two catches the rollout, the failed deployment, the rollback, the leftover and the interrupted job without naming them. The reason is worked out from where the container's deployment stands, not passed down through six callers. |
| **A container that stays is copied in the background; one that goes, by whoever removes it.** | The copy has to precede the removal, so it cannot be deferred there — but a rollout retires replicas in the background already (drain.go), so no deployment waits for it and no lock is held. Where the lock is held — a failed deployment being cleaned up — the cost is one bounded read: measured at 5 to 18 ms for a replica, 50 ms for a job's 10,000 lines. The supervisor never reads under the lock. |
| **Bounded three ways**: the last 2,000 lines and 1 MB of a replica's run, 10,000 lines and 4 MB of a job's; a line is cut at 16 KB. The daemon is asked for the tail, and lines pass through a ring. | Never more memory than the bound, whatever the container wrote. The crash is at the end of the log; the hour of requests before it is not what the archive is for. A job's output is its result, and gets more room. The run's own record keeps its 200-line tail, so `GET …/runs/:id` answers as before. |
| **A run that printed nothing is an entry only when it died**: exit code other than 0, or killed for memory, by itself. | "It exited 139 and said nothing" is the answer to "why did it die"; an empty entry for every quiet replica a rollout replaced is noise. A code the agent's own stop produced (143, 137) is not a death. |
| **Files, not rows.** One gzip file per entry under `<data>/logs/<application id>/<entry id>.log.gz`; the database has a row per entry (`log_archives`) saying what it is. | The database is one file behind one connection. Output in it would be copied by every backup of the agent's state — seven a week — would not give its space back when removed without a `VACUUM`, and a search reading 300 MB of it would hold the connection every deployment and request needs. A full disk fails one write of one file, logged and dropped, instead of a transaction of the engine's. The path is made of two numbers the database assigned: nothing a request or an application named is in it. |
| **The file is written before its row, and renamed after**, under a lock the housekeeping shares. | An entry is never listed before it can be read; a crash in between leaves a temporary file, which the next start removes. Housekeeping also removes files without a row (an application deleted during a copy) and rows without a file (a database restored from a backup, which has no files). |
| **gzip, plain lines**: `<time> <o\|e> <message>`. | 4.7 times smaller on the measured output, in the standard library, and readable on the server with `zcat` when the agent is not. |
| **Search reads; there is no index.** The database rules out entries by application, deployment, replica, run and time; the rest is decompressed and compared, case-folded, a line at a time. A request stops at 128 MiB or at its `limit` and returns a cursor. | Measured on a developer's machine with this driver's standard build, which does include FTS5: a trigram index (what a substring search needs) answered in 1 to 140 ms, but took 130 MiB for 29 MiB of lines — 4.5 times the text, 25 times the gzip — and 79 µs of the one connection for every line added (200,000 lines in 15.8 s). Reading: 2.5 million lines in 257 entries, 301 MB, in 1.3 s on the development stack, over three requests of at most 0.6 s. A single server's week of last-lines is of that size; the index would cost more disk than the logs. |
| **A search also reads the replicas that exist**, from where their last copy ended, their last 50,000 lines. | The question is "where did this happen", not "where in the archive". Starting after the last copy is what keeps a line from being found twice. |
| **Retention by age and by size**, enforced at every copy and hourly: 14 days, 1 GiB. Over the size, the application that holds the most loses its oldest entries. | A size alone lets one chatty job evict what a quiet application printed when it crashed; oldest-first across applications does exactly that. Taking from the largest holder keeps the archive useful for the application that is not the problem. |
| **While the disk is at its alert's threshold the archive does not grow**: a new entry removes at least its own size first. | The agent must not be what fills the last of a disk it is warning about. The newest output is still the most useful, so it replaces the oldest rather than being dropped. |
| **Deleted with the application; in no backup and no export.** | It is output, not state: nothing needs it back. Volumes and backups survive a delete because the application may return for its data. |
| **Read with the role that reads logs, and written nowhere else.** No line reaches an event, a notification, the audit trail or the agent's own log; the request log has the path without the query. Not encrypted at rest. | It is exactly as sensitive as the live log, which Docker keeps in clear next to it; sealing one and not the other would be decoration. |
| **`ReadLogs` is an optional interface** of the runtime, not a method of `Runtime`. | A runtime without it archives nothing and everything else works; the fake implements it, with a log per container and a stop time, so the engine's tests cover every path without a daemon. |

What it cannot do: a container removed by something else while the agent was
down is gone with its output; a run whose copy the agent could not finish
before it was stopped itself is copied at the next start, and if its replica
is running again by then the entry is `restarted`, without an exit code; a
logging driver that
keeps nothing the daemon can read leaves nothing to copy, and the copy fails
quietly.

## Backups

`agent/internal/deploy/backups.go` decides what is backed up and when;
`agent/internal/backup` knows where it goes. A backup is one tar archive per
volume, the archive `GET …/volumes/:volume/archive` streams, written to a
directory on the server and, when one is configured, to a bucket. The agent's
own state — a copy of the database and the encryption key — is a backup like
any other, owned by `_agent` instead of an application.

| Decision | Why |
|---|---|
| **A scheduled backup is a job that needs the volume**, so it is not a job container: the archive is read through the replica, and `before` runs inside it. | A job container never gets the volumes. What a backup needs first is whatever the application itself must do to make its files safe to copy, and that happens in its own process. |
| **A backup holds the application's lock from start to end.** A scheduled one takes it the way the supervisor does, a manual one as a user operation. | The archive is read through a container that a deployment would replace half-way. The difference in kind is the difference in answers: nobody asked for the backup at 03:00, so a `deploy` that meets it waits; a person who started one by hand and then deploys is told. The price is that the supervisor does not restart that application's replicas while an archive is written. |
| **The backup scheduler has its own ticker and waits for the supervisor**, for up to one tick. | The supervisor ticks at the same pace and holds every application for a moment each time; a scheduler that gave up on "busy" would give up every time. An application held by a user operation is still left for the next tick, with its window kept, so a backup due during a deployment is taken after it. |
| **`stop` does not touch the desired state.** The replicas are stopped under the lock and started again in a deferred call. | The application is meant to run. If the agent dies between the two, the next one finds an application that should be running and is not, and the supervisor starts it — which a recorded "stopped" would prevent. |
| **A failed backup keeps nothing**, in either place. | Two volumes out of three, or an archive that never reached the bucket, would be listed and counted as a backup. Retention counts successes only, so failures never push out the last backup that worked. |
| **The directory first, then the bucket, from the file.** | A single S3 `PUT` needs the length and the hash of what it sends before the first byte, and the archive is a stream of unknown length. Written to the server first — which it is anyway — it has both, hashed as it was written. |
| **An archive larger than one part, 64 MiB, is a multipart upload**: created, sent part by part from the file, completed. Each part is read twice, once for its hash and once for the request, and sent up to three times. Parts grow past 64 MiB only when 10,000 of them would not hold the file. | One `PUT` ends at 5 GB. The file is on the server already, so a part needs no buffer: memory does not grow with the archive, and the second read comes from the page cache. The hash is not optional, because the signature covers the payload. Parts go one after the other because the disk and the line are the limit, and because an upload with one request in flight is one that can be aborted cleanly. What remains is the service's limit for an object, 5 TiB on S3. Smaller archives stay a single `PUT`, signed with the hash taken while the file was written. |
| **An upload that does not complete is aborted by whoever started it**, with a context of its own; completing is checked in the body, where S3 reports a failure after answering `200`. | Parts of an unfinished upload are in no listing and on every invoice. The upload's context is often what ended it, so the abort cannot use it. |
| **An agent that died in the middle leaves a note**: `<file>.upload` next to the file being sent, holding the upload's id, written before the first part and removed when the upload is over. The next agent aborts what the notes name once the bucket is known to be its own and before its own first upload, then asks the bucket for unfinished uploads under its prefix and aborts those whose keys have the shape `<owner>/<id>/<file>`. | Asking the bucket alone is not enough: S3 lists unfinished uploads by prefix, MinIO only under an exact key, which was found against a real one. A note names the upload on every service. The listing is for the server whose disk went with the agent, where it is answered. The shape is checked because a bucket shared without a prefix has other people's uploads in it. It happens with the first use of the bucket rather than at start, because nothing is removed from a bucket before it is known to be this installation's; an agent that died over a backup finds that backup `running` at its start and uses the bucket to clean up after it. |
| **Inside the replica, `backups.before_timeout` bounds the wait, not the process.** | The Docker Engine API starts a command in a container and cannot end it. The agent stops reading at the limit, fails the backup and frees the application's lock; a command that hangs is the container's until it exits or the container is replaced. Saying "stopped" would be saying something that is not checked. The ways around it were looked at and left: the process id the daemon reports is the host's, and signalling it takes the host's process namespace, which the agent will not ask for; a command started with a terminal ends when the terminal hangs up, but a terminal changes what commands do — `psql` under one waited in a pager. |
| **`backups.before_in: container` runs the command in a container of its own, which is ended at the limit**: the replica's image, environment, user and limits, the application's volumes at their paths, the replica's network namespace, an init process in front, no entrypoint. It is stopped and removed like a job, also when the agent shuts down under it. | A container is what the daemon can stop. Sharing the network namespace makes `localhost` the replica, so a dump over TCP and `redis-cli` work as they did; the volumes are where a dump is written. It mounts volumes a replica is writing, which a job never may: here the command is the application's own way of making those files fit to copy, and it runs under the application's lock. The entrypoint is left out because the command stands for one that would have been started inside the replica, where no entrypoint runs. The init process is there because a command that is its container's first process never sees `SIGTERM` unless it handles it. |
| **The replica stays the default.** | The container does not have the rest of the replica's filesystem, nor its processes. A socket under the replica's `/var/run` or `/tmp` is not there, and that is where `pg_dump` and `mysqldump` look unless they are told a host: tried against a real PostgreSQL, the documented `pg_dump -U postgres …` failed in the container and worked with `-h localhost`. Sharing the process namespace as well does not reach the socket (`/proc/1/root` is refused). A configuration that works today must go on working, so the application asks for the container, and its documentation says what to change. |
| **Adoption writes records from files** (`POST /server/backups/adopt`): the run id from the path, the volumes from the names, the time from the newest file, the size from the stored size, with the trigger `adopted`. An encrypted file's plaintext size is computed, not read: the format adds a 33-byte header and 16 bytes per chunk. The ids the database knows are read before the destinations are listed, and the insert leaves an existing id alone. | A database restored from yesterday has forgotten today's backups, whose files sit under ids it will not hand out again. There is no manifest to read, and none is needed for what the API promises: `Content-Length` of a download is the plaintext size, which the size of the file determines. Reading the ids first means a backup pruned meanwhile is not adopted as a ghost, and one started meanwhile has a record the insert respects. What files cannot say is whether an application's backup was finished; the state's two files and an export's one are known, and a run that lacks one is reported instead of adopted. Owner and file names come from a bucket's keys, which anybody with the credentials can choose: they are checked before they can become a path. |
| **No SDK for S3.** Put, get, list, delete and the multipart upload, path-style, signed with Signature Version 4 using the standard library alone, tested against the vectors AWS publishes, against a fake that verifies every signature and implements multipart, and against a real server — once with a file over 5 GB. | No dependency where the standard library will do, and a handful of signed requests are within its reach. Path-style addressing is what every S3-compatible service accepts; virtual-hosted buckets would need DNS the endpoint may not have. |
| **Encryption is the agent's, not the bucket's**, and applies to the directory as well. | Server-side encryption protects against the provider's disks, not against whoever holds the bucket's credentials. Encrypting before the first write means one rule: without the passphrase, nothing the agent wrote can be read. |
| **The key is never written unencrypted.** Without a passphrase there is no backup of the agent's state at all. | `encryption.key` next to `shipwick.db` in a directory of backups is every secret in clear, one `cp` away. No backup is the smaller harm, as long as it is said — `GET /server` and `shipwick doctor` say it. |
| **The database is copied with `VACUUM INTO`, before the backup is recorded.** | A copy of a file in use, and of the write-ahead log next to it, may not open. And a copy that held its own backup as `running` would, once restored, take it for one an agent died over and remove its files: the files it was just restored from. |
| **A bucket is marked with the installation that writes to it** (`_agent/installation`, a random name kept in the database), and another installation is refused. Before its first backup an agent also moves its run ids past the highest one any destination holds. | Run ids name directories and keys, and a database knows only the ids it handed out. A reinstalled server counts from 1 again; a restored database has forgotten what came after it. Either would write its run 7 over the run 7 in the bucket — and would do it in the week somebody needs that one. For the same reason nothing is removed from a bucket before it is known to be this installation's. |
| **Backups are recorded by application name, not by reference.** | Deleting an application keeps its volumes on purpose; its backups are the other half of that. |
| **Verification is a job-labelled container on scratch volumes**, `shipwick_<app>_<volume>_verify_<run>`, held to the health check by the code path a deployment uses. | Everything that manages replicas already skips job containers, so it gets no route, no service name, and no supervisor; `Recover` removes one an agent died over, and a sweep at start removes its volumes. The application's lock is held only to decide what to verify with: the container runs next to the application, not instead of it. |

**The encrypted format** (`pkg/backupfile`), in full. A file is a 33-byte
header followed by chunks:

```text
header
   0   8   the ASCII bytes "SWBACKUP"
   8   1   format version: 1
   9   4   PBKDF2 iteration count, big-endian (600000 in files written today)
  13  16   salt, random per file
  29   4   chunk size: plaintext bytes per chunk, big-endian (65536)

key    = PBKDF2-HMAC-SHA256(passphrase, salt, iterations), 32 bytes
chunk  = AES-256-GCM(key, nonce, plaintext, additional data = the 33 header bytes)
         stored as ciphertext followed by the 16-byte tag
nonce  = 12 bytes: the chunk's index as 8 bytes big-endian, counting from 0;
         3 zero bytes; then 1 for the last chunk of the file, 0 for the others
```

Every chunk but the last holds exactly `chunk size` bytes of plaintext, so it
is `chunk size + 16` bytes on disk and needs no length prefix. The last chunk
holds what remains — anything from one byte to a full chunk, and nothing at
all only when the whole plaintext is empty — and is always present. To
decrypt: read the header, derive the key, then read `chunk size + 16` bytes at
a time; a chunk is the last one when the file ends with it. A reader must
refuse the file unless the chunk it took for the last one decrypts with the
last-chunk nonce.

What that buys: the salt is random, so the key is unique to the file and a
counter is a safe nonce. The index in the nonce means chunks cannot be
reordered, dropped or repeated. The last-chunk marker means a file cut at a
chunk boundary fails like one cut anywhere else: the chunk that now ends the
file was sealed as "not the last". The header is authenticated with every
chunk, so the chunk size and the iteration count cannot be altered either. A
wrong passphrase and a damaged first chunk look the same, and the message says
so. The reader bounds the chunk size and the iteration count a header may ask
for, since both are spent before anything has been authenticated.

## Moving a server, and standing by

`deploy/export.go`, `deploy/import.go`, `deploy/standby.go`. A backup of the
agent's state restores a server as itself. An export is for the other case —
another installation takes over what this one runs — and so it carries
configuration and data and leaves identity behind: no history, no tokens, no
key.

| Decision | Why |
|---|---|
| **Values travel in clear inside one encryption, not sealed.** What the database seals is opened for the export and sealed again by the store of the server that imports it. | The two servers have different keys, and should keep them: shipping `encryption.key` would make the new server's database readable with a key that also sits in every old backup. `pkg/backupfile` with a passphrase is the one thing protecting the file, and it is the format backups already use. |
| **The engine writes clear text; only callers that encrypt may call it.** The API wraps the connection in `backupfile.Writer` with the request's passphrase; the scheduled export goes through `backup.Storage`, which refuses to exist without the agent's. | One writer for both, and no path on which an export is on a disk or a wire unencrypted. The passphrase of a request is used and forgotten. |
| **A stream, written once and read once.** The file is a tar archive in the order an import needs it; nothing seeks. | A volume is gigabytes. An export is piped from Docker through the encryption to the connection, and an import from the connection through the decryption into Docker; neither side holds an archive in memory or needs disk for it. |
| **Members of unknown length are cut into parts.** | A tar entry states its size before its content, and a volume read through Docker's archive endpoint has no size until it ends. Parts of 4 MiB, numbered, concatenated on the way in: one buffer of that size, and `tar x` followed by `cat` still gets at the data by hand. |
| **Each application is exported under its lock**, with its `app.json` written from the deployment the lock protects. | The configuration and the archives must describe the same moment. A manifest written up front could name a version that a deployment replaced before its volume was read. |
| **An import is deployments.** Each application goes through `Engine.start` with the kind `import`; what is special happens in the function that resolves what to deploy, under the application's lock and before the record exists. | One way to start a deployment: the imported application rolls out, is health-checked, routed, recorded and rolled back like any other, and nothing in the rollout knows where the spec came from. |
| **Volumes are filled before the deployment, through a container that is never started.** It carries the job label, mounts the volumes and exists for the duration of `CopyToContainer`. | The first process of a database must find its data, not initialise an empty directory that is then replaced under it. A job-labelled container is invisible to everything that manages replicas and swept by `Recover` if the agent dies. Any image would do for a container that never runs; the application's own is used because it is the one image known to be there. |
| **One application at a time, each waited for.** | The order of the file is the only dependency information an export has. Deploying in parallel would throw it away. |
| **The order is "no domain first, then by age".** | An export has no `after` graph; that lives in a `shipwick.yaml` on somebody's machine. What the agent does know is that an application without a hostname is reached by other applications, not by visitors. It is a heuristic, said as such: a wrong guess is a failed health check and a `redeploy`, not lost data. |
| **Nothing existing is replaced without `overwrite`**, and a volume left by a deleted application counts as existing. | `delete` keeps volumes on purpose. An import that extracted over one would produce a mix of two states that nobody chose. |
| **A configuration out of an export is validated as the document that says it.** `spec.Validate` writes the parsed value back into the form a deploy.yaml is decoded into and runs the one validator there is; the import deploys what comes out. | An export carries parsed configurations, and the rules are written for documents. A second set of rules for values would drift from the first with every key added — the first version of the import checked names, images and hostnames and took health paths, the `proxy` block, `publish` and logging options as the file had them. Going back through the document form costs a function that mirrors the parser field by field; a test fails when a field is added to one and not the other. The file is authenticated by its passphrase, so this is defence against a damaged or hand-made export rather than against a stranger. |
| **An image the daemon cannot write out does not fail the export.** The image is opened, and its first byte waited for, before `app.json` says whether it follows. | See [Images without a registry](#images-without-a-registry): on the containerd store an image may lack blobs for layers it shares. The import then skips that application and says how to bring it over by hand. With Docker 29, images sent in reduced archives were written out whole in the cases tried; the refusal is there for the case that is not. |

**The file**, inside the encryption described under [Backups](#backups):

```text
export.json                                  format, time, version; secrets, registry
                                             credentials and certificates; application names in order
applications/<name>/app.json                 name, version, the spec in clear, its references,
                                             stopped?, what follows
applications/<name>/image.tar.0000 …         `docker save` of a shipwick.local image
applications/<name>/static.tar.0000 …        the folder, as PUT …/static takes it
applications/<name>/volumes/<volume>.tar.0000 …   as GET …/volumes/:volume/archive answers it
```

**Deployed and stopped.** A standby must hold applications without running
them: an application that starts on the standby sends its mail and runs its
jobs twice. `executeDormant` is the rollout for that: the image is obtained,
the containers are created and recorded by `createReplicas` — the creating
half of `ensureReplicas`, as a volume restore uses it — and the deployment
is committed with the application recorded as stopped. Nothing is started,
probed or routed, and `pre_deploy` does not run. The record passes the same
statuses on its way to `ACTIVE`, because the transition table has one way
there, and an event says what they did not involve. That a
deployment is dormant is written into its record (`deployments.dormant`)
before it begins, under the lock that created it: an import that the agent
is restarted under leaves its deployment in flight, the agent that starts
next resumes it, and it must resume it as what it was — for a `standby`
deployment, whose kind says so, and for the `import` of an application that
was stopped where it came from, whose kind does not. A resumed deployment
takes over its application's routing before it runs (`prepare`); the dormant
rollout gives it back when it commits, like every other.

`start` is all a promotion does to an application: the stopped `standby`
deployments are started in the order they were made, each held to its health
check or stabilization window before the next. A promoted application is one
that runs; nothing about it is recorded.

**A promotion is a record that is followed.** The request that asks for one
gets the record as it begins (`202`), the work runs in the background under
the engine's context, and `GET /standby/promotion` is read until
`completed_at` is set — a deployment's contract, for the same reason: the
work takes as long as the applications' startup budgets together, and a
connection that long is the weakest part of it. The record is one row of
`transfer_state`, rewritten at every change. It is not a table of
promotions: a server is promoted once or twice in its life, and what is
needed is the one that runs or ran last.

An agent that is restarted under a promotion resumes it. The alternative —
report it as interrupted and let a person run it again — was weighed and
refused: a promotion is run on the worst day, the restart may be the
server's own (it was just handed load it never carried), and a server that
was told to take over and then holds half of its applications stopped is a
state nobody chose. Resuming needs no more than the record: applications
with an outcome keep it; the one the promotion was at is started unless it
is recorded as running, and waited for again; the rest follow. Starting a
container that a moment before the restart was started already is a no-op
to Docker. Who asked for the promotion is kept with the record, so that the
applications started after the restart name the same person in their
events.

A promotion and an import exclude each other, in both directions: an import
replaces stopped applications, which are exactly what a promotion is about
to start. The scheduled pull skips its turn without recording a failure.
A client that cannot follow a record — the CLI before 0.6 — is answered as
before: without `wait=false` the request is held until the promotion ends.
The default is the old behaviour because the old client cannot say what it
is; the new one can.

That is also what makes the scheduled import safe to leave configured. An
import that leaves applications stopped replaces only applications that are
stopped, and keeps the server's secrets once it finds one of the export's
applications running. After a promotion the next export of the old server —
written before it died, fetched after — is refused application by
application, with a reason that says the server has been promoted.

**Where a standby gets its exports.** The scheduled export is a backup run
owned by `_export`, written through `backup.Storage` like the agent's state:
one encrypted file, in the directory and the bucket, kept three deep. The
standby is given the same bucket and passphrase and reads it through a
`Storage` of its own whose directory nothing writes to; its own backups are
taken off the bucket for as long as it is a standby, since a bucket is marked
with the one installation that writes to it. It asks for the highest run that
holds the file — a file appears under its name only when it is complete — and
skips one it has already imported. Which one that was, and the record of the
last import, are rows of `transfer_state` too, written when they change and
read by `Recover`: an agent that restarts does not restore every volume once
more for an export it already holds, and `GET /import` still answers. An
import itself is not resumed. It reads a stream that ended with the agent;
the record found `running` at startup is settled as failed, and the next
scheduled pull, which sees no successful import of that export, takes it
again.

What this does not do is in the handbook, in the same words: nothing watches
the first server, nothing decides, and nothing prevents both servers from
running at once.

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
(`application.down`) and its return (`application.recovered`), a job that
failed (`job.failed`), a scheduled backup that failed (`backup.failed`; one
taken by hand tells the person who asked), and a certificate the proxy has not
renewed (`certificate.expiring`, see Certificate status); conditions that last are
[alerts](#alerts), below. A single replica restarting is not on the list: it is
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

## Alerts

`deploy/alerts.go`. A notification is an occurrence; an alert is a condition
with a beginning and an end. That difference is the whole design: the engine
keeps the set of alerts that are active, and a condition is compared against
it, so that it is told once when it becomes true and once when it stops, and
never in between. Whether a condition holds is decided by four pure functions
over samples and times — `memoryAlerting`, `diskSeverity`, `restartAlerting`,
`unhealthySeverity` — which is why their tests need neither a clock nor an
engine.

- **Memory** and **disk** are read after every round of sampling
  (`checkAlerts`, every 30 seconds). Memory is decided from the rows the
  sampler has just stored, not from a second reading: three consecutive samples
  at or above the threshold, where consecutive means "within the last two and
  a half intervals", so three readings with an outage between them do not
  count. The disk is one `statfs` of the data directory (`agent/internal/disk`;
  off Linux it reports unknown and there is no disk alert), counted the way
  `df` counts: used over used plus what an unprivileged process may still
  take, because the blocks reserved for root do not help an application that
  runs as somebody else.
- **Restarts** and **unhealthy** are what the supervisor sees, so they are
  evaluated in its tick (`noteAlerts`, called per application under its lock).
  The supervisor's restarts are remembered per container for ten minutes.
  *Healthy* is the definition notifications already use for *recovered* —
  every replica ready and none with a restart held against it — so that a
  crash-looping replica, up for a moment between crashes, neither ends the
  unhealthy period nor restarts its five minutes.

Each level is left below where it is entered: memory clears ten points under
its threshold, the disk five, and a critical disk steps back to a warning only
under 90%. Without that distance a value that hovers at the threshold would be
an alert per sample. Stepping down a level changes the state and tells nobody;
only raising, turning critical and clearing are told.

Two rules keep alerts from repeating what a notification already said. The
restart alert is held — neither raised nor cleared — while the application is
down or the replica crash-looping: the outage has been reported, and three
restarts are how every outage begins. And when an application that was
reported down recovers, its `unhealthy` alert is cleared in the event feed
only, because `application.recovered` is sent in the same tick.

Alerts are not stored. Like the supervisor's restart counts they live in the
agent's memory, and an agent that restarts raises again what still holds;
persisting them would buy one message less per agent upgrade for a table and
the question of what an alert means that was raised by a process that is gone.
An application that is stopped or deleted takes its alerts with it, silently.

The same pass records, per application, how the supervisor found it — replicas
running and healthy, whether one is crash-looping. `GET /metrics`
(`deploy/prometheus.go`, rendered in `api/routes_prometheus.go`) is answered
from that, from one read transaction on the database and from the sampler's
last rows; it never calls Docker. A scraper comes every few seconds for ever,
and an endpoint that listed containers each time would put that load on the
daemon and fail whenever the daemon is slow — which is when the numbers matter.
The text format is written by hand: it is a `# HELP` line, a `# TYPE` line and
`name{labels} value`, with three characters to escape, and a client library
would bring a registry and a dependency tree to produce it. Deployments are
counted by outcome rather than by status, because a status moves (`ACTIVE`
becomes `SUPERSEDED`) and a counter must not go down.

## Routing

Caddy terminates TLS and proxies to replicas; `agent/internal/proxy` tells it
what to route where. Shipwick does not touch certificates, ACME or HTTP/3 —
listening on `:443` with host matchers is all Caddy needs to do those itself.
Compression is Caddy's too: every application route carries an `encode`
handler in front of `reverse_proxy` (zstd and gzip, zstd preferred, from
1024 bytes, Caddy's default set of compressible content types). Routes marked
`Streaming` — the agent's API and the dashboard, which relay followed logs —
carry it inside a subroute that a request with a `follow` parameter goes
around. The encoder was suspected of holding back the first lines of a
followed log; measured against Caddy 2.11, it does not. It decides on the
first write whether to compress at all — a first line is shorter than 1024
bytes and `application/x-ndjson` is not a type it compresses, so the stream
passes through as it is — and when it is forced to compress a stream, it
flushes line by line. What it does hold back is the response header, until
the first byte of the body: behind the encoder, the followed log of an
application that prints nothing delivered no header at all, where without
it the header arrives in 60 ms, and a client cannot tell an open stream
from a request that hangs. Everything else on those routes is compressed:
500 log lines as JSON went from 73,942 bytes to 5,197.

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
backend at all — a static `308` whose `Location` is `https://<domain>`, then
the application's `path` if it has one, plus the request's path and query —
so they answer while the application is stopped or
has no healthy replica, and a rollout never touches them. Both kinds of
hostname sit in host matchers on `:443` like the domain does, which is all
Caddy needs to obtain certificates for them — `https://www.example.com` has to
be answered before it can be redirected. `308` rather than `301` keeps the
method, so a `POST` to the old hostname stays a `POST`. During a rollout the
route override carries the whole set of hostnames, not only the domain, or the
aliases would disappear for its duration — and reappear with a reload.

**Paths.** A route may carry a `Path`, and its matcher is then the hostnames
*and* `["/api", "/api/*"]` — two patterns, because `/api*` would take
`/apix` as well. Caddy takes the first route that matches, so the config
orders routes by the length of their path, longest first and the ones
without a path last; that single global order is right for every hostname at
once, whatever hostnames the routes share, and leaves the order of pathless
routes — every installation before paths existed — as it was. Applications
that share a hostname stay separate routes with separate backends: each has
its own name on the services network, its own rollout, its own 503 when it
is down. Ownership is checked per hostname *and* path (`checkDomain`): the
same path twice is refused, and so are two applications both without one.
Caddy matches paths without regard to case, so that is how they are compared.
A redirected hostname has no paths to share and is taken whole, like the
agent's own; what it redirects to is the part of the domain its application
serves, `https://example.com/api/users` for a request for `/users` and an
application at `/api`, since the rest of the domain may be another
application's or nobody's. Whether the prefix reaches the application is the
application's choice (`proxy.strip_prefix`, a `rewrite` handler in front of
`reverse_proxy`); the default is to pass the request as it came, because a
proxy that strips cannot fix the links and redirects the application then
writes without the prefix.

**What a route does besides routing.** The `proxy` block of a deploy.yaml —
response headers, basic-auth accounts, path redirects — is data in the spec,
copied onto the `Route` and rendered as handlers in front of the proxy:

```text
headers (deferred)  →  subroute: redirects, accounts  →  encode  →  rewrite  →  reverse_proxy | file_server
```

There is no Caddyfile snippet and no pass-through: every option is a field
with a validator, and the renderer is the only thing that writes handler
JSON. Three details carry weight:

- *Text is never a placeholder.* Caddy expands `{…}` in header values,
  `Location` headers and rewrite targets, and its placeholders reach the
  proxy's environment and files — `{env.X}`, `{file./data/…}` — where the
  certificates' keys are. Every user-supplied string that lands in such a
  field goes through `literal`, which escapes the opening brace; a header
  value of `{env.HOME}` is sent as those ten characters. (Tested against a
  real Caddy: unescaped, the same value came back as `/root`.)
- *Accounts are grouped by path, longest first, in one route group.* Of a
  group Caddy runs only the first route that matches, so `/admin` asks for
  the accounts of `/admin` and not for those of the whole application as
  well — a browser sends one pair of credentials, and two `authentication`
  handlers in a row could never both be satisfied by different users. The
  group routes are not `terminal`: a terminal route inside a subroute ends
  the request with an empty response instead of handing it on.
- *Headers are `deferred`.* Set before the upstream answers, they would be
  joined by the application's own header of the same name; deferred, they
  are applied when the response is written and replace it. They sit first in
  the chain, so redirects carry them. The `401` does not: Caddy's
  authentication handler answers through the error path, which the chain's
  header handler never sees.

**Passwords** follow the rule for `env` values end to end: `${NAME}` is
resolved by the CLI or left for the agent (`resolveSecrets`), the resolved
value is sealed in the stored spec (`sealSpec`, under the name
`proxy.basic_auth[i].password`), `Redacted` masks it, and nothing logs it.
Caddy gets a bcrypt hash (cost 10). bcrypt salts randomly, so hashing on every
sync would produce a different config, and a reload, every second; the engine
therefore keeps each hash for as long as a routed deployment has the account
(`accounts.go`), keyed by a digest of username and password so that the next
version of an application, with the same password, renders the same bytes
and a rollout stays reload-free. The cache is memory only: after a restart of
the agent the hashes are new, and Caddy is reloaded once if any application
has accounts. Persisting them would avoid that reload at the price of a
second secret-bearing column; it was not worth it. Caddy's `hash_cache` is
on, or every request to a protected path would cost a bcrypt comparison.

**During a rollout** the route override carries the new version's path and
proxy block along with its hostnames. They take effect with the first batch
that puts a new replica behind the name, as hostnames always have: for the
rest of a rolling deployment old replicas may receive requests under the new
version's prefix handling. A change of `path` or `strip_prefix` that the old
version cannot serve is a case for `deploy.strategy: recreate`.

**Static applications** have no replica to name. Their route carries a
`StaticRoot` instead of backends — the folder's directory in Caddy's own
container — and renders as a `file_server` with `index.html` as the index; a
path that names no file is a `404`, unless the spec names a fallback page.
Then the route is two file servers with a rewrite between them: the first
passes on what it does not find (`pass_thru`), the rewrite turns the request
into one for the fallback page, the second serves it — status 200, which is
what a single-page application's router needs, and no matcher that would have
to stat the file a second time. Under a `path` the prefix is always stripped
(the files are looked up in the folder), and the path itself redirects to its
trailing-slash form, as a directory does on any web server. The rollout takes over routing only at the
switch, with an override naming the new directory, and drops it at the commit;
until then the database names the previous deployment's directory, and a failed
rollout leaves routing where it was. Every new folder is a new root, so a static
deployment reloads Caddy once — the reload a rollout of containers is designed
to avoid, accepted here because the alternative, a stable path swapped
underneath Caddy, would serve one version's HTML with another's assets for the
duration of a copy. An application that changes from containers to a folder
keeps its files serving until the first replica is ready, and the other way
round; `retireOthers` removes the containers a folder replaced. The folders a
container version replaced are swept by the same rule as a static
deployment's (`retireStaticDirs`): the one the default rollback returns to
stays, so the first container version keeps its predecessor's folder and the
second removes it, with the application's directory. A container deployment
looks into the proxy only when that directory exists, and says nothing when
the proxy is not a container.

**DNS gates the proxy** (`agent/internal/deploy/dns.go`). A hostname is put
into Caddy's config only once it resolves — and, when the server's addresses
are known, resolves to one of them. Caddy asks for a certificate the moment a
hostname appears in a host matcher, and Let's Encrypt allows five failed
authorizations per hostname per hour; a domain deployed before its record
exists would burn them in minutes and stay without a certificate for the rest
of the hour. So the sync resolves every planned hostname *before* taking the
routing mutex (a lookup may take its whole 3 s timeout, and nothing that
renames replicas should wait on a resolver), keeps the verdict in a cache —
trusted for 30 s when the hostname points here, 10 s when it does not, so a
fixed record is noticed within ten seconds — and builds the routes from the
cache alone. A domain that is not ready drops its whole route, redirects and
aliases included: the redirects' target is the domain. An alias or redirect
that is not ready is left out on its own. The verdict says what to do, not
only what is wrong: with the server's addresses known, a hostname that does
not resolve is told the A (and AAAA) record to add, one that points elsewhere
the record to change, and one whose addresses are all in Cloudflare's
published ranges (`pkg/cloudflare`, dated, shared with the CLI's `doctor` so
that both say the same thing) is told to turn the proxy off for the record,
or to give the agent a Cloudflare token — the record is right, and the orange
cloud is what breaks the certificate. The deployment records what is held
back and why as warning steps, in place of the "Routed" line; the log says so
once per change of the held-back set; and when a hostname starts pointing
here, the next sync adds it and records an application event. The agent's and
the dashboard's own hostnames are never gated: they come from the operator's
configuration, not from a `deploy.yaml`, and holding them back could lock the
operator out. The server learns its addresses at startup by resolving those
two hostnames; with neither set, "resolves at all" is the only check.
`checkDomain` is unaffected: conflicts are still refused at deploy time.

Lookups go to public resolvers (1.1.1.1, 8.8.8.8, 9.9.9.9), in turn: the
first that knows the hostname decides, "no such host" is believed only when
every reachable one says so, and the system resolver is the fallback when
none can be reached. The server's own resolver would answer from its negative
cache for the zone's negative TTL after a record was created, and the gate
would hold the hostname back for half an hour on Cloudflare — the wait it
exists to prevent.

Each public resolver gets 700 ms of the three seconds a lookup may take
(`resolvers.go`). A firewall that drops outgoing DNS does not refuse, it stays
silent: asked without a bound of its own, the first resolver used up the
whole lookup, the fallback was never reached, and no hostname was ever routed
on such a server. When none of them answered, they are left alone for five
minutes and the system resolver is asked directly, so that the supervisor
does not spend two seconds per hostname on every round.
`SHIPWICK_DNS_RESOLVERS` replaces the list, or with `system` skips it: see
*Leaving the server*.

**Certificates through DNS** (`agent/internal/proxy/tls.go`). Behind
Cloudflare's proxy no authority can reach the server, so neither of Caddy's
default challenges works. With `SHIPWICK_CLOUDFLARE_API_TOKEN` the rendered
config gains one automation policy without subjects — it applies to every
hostname — whose ACME issuer uses the DNS challenge with the Cloudflare
provider, and Cloudflare's ranges become the server's `trusted_proxies`, so
that the `X-Forwarded-For` Cloudflare sends is passed on instead of replaced.
One policy for everything rather than one per proxied hostname: whether a
record is proxied changes with a click in someone else's dashboard, and a
certificate that depended on the agent noticing would fail at the next
renewal. The price is that every hostname must be in a zone the token can
edit. The gate then accepts a hostname that resolves to Cloudflare's ranges:
it cannot look further, and the certificate no longer depends on what is
behind them. Without the token the config has no `tls` app at all and Caddy's
defaults apply, as before.

The token is a credential the agent only passes on. It is in the config, so
the fingerprint — a truncated SHA-256 of the config — changes with it without
carrying it; Caddy's Cloudflare module quotes the token in the error for one
it considers malformed, so `config.Load` refuses that form before Caddy can
(and placeholders, which the module would expand), and an error from loading
a config is scrubbed of the token before it is logged or reaches
`GET /server`. Caddy's autosaved config holds it; that file is on a volume
only Caddy mounts.

The DNS provider is not in the official Caddy image, so the proxy is an image
of Shipwick's own: `Dockerfile.caddy`, an `xcaddy` build of a pinned Caddy
with `caddy-dns/cloudflare` at a pinned version on the official image of the
same version, published as `ghcr.io/shipwick/caddy` with each release and
pinned in the release's compose file like the agent and the dashboard. Every
installation runs it, with or without a token: two proxy images would be two
paths to test, and turning Cloudflare on would mean replacing the proxy
instead of setting a variable.

**Wildcards.** `*.example.com` is a hostname like any other to ownership —
`checkDomain` compares strings, so it and `api.example.com` can belong to
different applications — and to Caddy's host matcher. Two things are special.
Order: routes are matched first to last, and a wildcard would answer for the
exact names that sort after it, so `render` emits every route's exact
hostnames first and all wildcard matchers after them. And the certificate: an
authority issues a wildcard only through the DNS challenge, so `Engine.start`
refuses one unless the challenge is configured or a supplied certificate
covers it (`checkWildcards`, a config error on the field), and the gate, which
cannot look a wildcard up, decides it the same way.

**Supplied certificates** (`agent/internal/deploy/certificates.go`,
`agent/internal/certs`). A certificate of the operator's own is an object of
the server, stored under a hostname, not a key of `deploy.yaml`: one wildcard
certificate serves the hostnames of many applications, and a key does not
belong in a file that lives in a repository. `certs.Check` decides before
anything is stored — the chain parses, the key belongs to its first
certificate (the same `tls.X509KeyPair` Caddy will run), that certificate
covers the hostname and is within its validity — and each refusal is a
sentence that never quotes the input. In the config the pairs are
`tls.certificates.load_pem`, and every route hostname one of them covers is
listed in the server's `automatic_https.skip_certificates`: Caddy would skip
them anyway on finding a matching certificate loaded, but the agent's intent
should not rest on that. Those hostnames skip the DNS gate — it protects an
authority's rate limit, and no authority is asked — and such a verdict is
never cached as an answer from DNS, so the sync that follows the removal of a
certificate looks its hostnames up and judges them as ordinary ones again. The engine keeps the
stored certificates in memory between changes; it is the only writer, and the
supervisor syncs routing every second.

**Security.** The config is built as Go data and marshalled, never templated:
input cannot change its structure (there is a test that tries). Domains,
aliases and redirects are validated hostnames, paths are validated to a
narrow alphabet, and a hostname and path belong to one application: a
deployment that claims what another application has in its active
configuration is refused, and the agent's own hostname cannot be claimed
under any path. The admin API is reached over a unix socket in a volume only
the agent and Caddy share; on a TCP port of the application network, any
application container could take over all routing.

### What the proxy sees

`deploy/traffic.go`, `deploy/accesslog.go`. Status codes, durations and
request counts exist in one place, the proxy, and there were three ways to get
them out. Caddy's Prometheus endpoint has them per server, not per hostname,
unless per-host metrics are switched on, and then somebody has to scrape and
store them — a second time-series store next to SQLite. A log file on a shared
volume needs rotation, a tailer that survives it, and a volume. The third is
what the agent already does for replicas: Caddy writes its access log to
standard output, Docker keeps it (capped at 3 × 10 MB like every container's
log), and the agent follows it through the Engine API. No new volume, no new
port, nothing to rotate, and a proxy that restarts or is replaced is just a
stream that ended: the agent finds the container again by its Compose labels
and resumes at the time of the last line it saw, dropping the lines Docker
repeats from that instant.

The proxy configuration puts the access log on a logger of its own
(`http.log.access.shipwick` → `stdout`) and excludes it from the default log,
so standard output carries nothing else and Caddy's own messages stay on
standard error. A `filter` encoder removes every field that is not needed —
request and response headers, TLS details, the remote port — and cuts the
URI at the question mark, so a token in a query string never reaches the
log, let alone the agent. What is left of a request is its time, hostname,
method, path, status, duration, size and client address. The agent's and the
dashboard's own hostnames are skipped by Caddy (`skip_hosts`): a dashboard
that polls would otherwise fill the log with itself.

Each line is attributed by hostname — domain, alias or redirect of an active
deployment — and, where applications share a hostname by `path`, by the
longest path the request is under; the table of owners is read from the
database at most every five seconds, and only while requests arrive. A
request counts into its application's current minute: requests, the four
status classes, bytes, and a histogram of durations with fourteen fixed bounds
from 1 ms to 30 s. Once a minute the minutes that have ended are written to
`traffic_samples`, one row per application and minute with traffic, and
pruned after seven days like the metric samples; a quiet application writes
nothing. Reads add the stored rows and the minutes still in memory, bucket by
bucket — 1 minute, 5 minutes or an hour, by window — and compute the
percentiles from the summed histogram, which is why the histogram is stored
and not the percentiles: percentiles of minutes cannot be combined into the
percentile of an hour. The bounds are fixed for the same reason. On shutdown
the current minutes are written too; a minute that straddles a restart is two
rows, and rows of one minute add up.

The last 200 requests of each application are kept in a ring in memory, for
`shipwick traffic --requests` and for naming the slowest and the failing
paths. They are not stored: a week of requests is a different order of
magnitude than a week of counts, and the access log itself is still in
Docker's log files for whoever needs every line.

The cost was measured on the development stack, 30,000 requests at 880 a
second through Caddy to a one-replica application: the agent used 0.98 s of
CPU over the 35 seconds (against 0.16 s per 30 s idle), about 27 µs per
request, while Caddy used 14.7 s for the same requests; the agent's memory
stayed at 12 MB.

### Certificate status

`deploy/certstatus.go`. Caddy's admin API does not say which certificates
it holds or how old they are, and its storage is its own business. So the
agent asks the way a client would: a TLS handshake to the proxy
(`SHIPWICK_PROXY_TLS_ADDR`, `caddy:443` in the compose setup) with the
hostname as the server name, reading the leaf certificate's issuer and
`NotAfter`. The chain is deliberately not verified — the agent reports what a
visitor will be handed, including a certificate from Caddy's local authority
on a development machine — and nothing is sent over the connection. A
handshake that fails, or a certificate for another name, is `obtaining`; a
proxy that cannot be reached is `unknown`, and what was known before is kept.
`waiting_for_dns` never comes from a handshake: the DNS gate knows which
hostnames it is holding back, and those are not in the proxy to be asked
about.

A hostname is looked at once a minute, every ten seconds while it has no
certificate (somebody is waiting for it), and on the first tick after it is
routed. The state is in memory. That decides what is an event: a certificate
*first seen in order* is not news — after an agent restart that is every
certificate — but one that appears on a hostname the agent has seen without
is. `expiring` is 14 days before the end, or the last quarter of the lifetime
for certificates shorter than 56 days, since the certificates Caddy's own
authority issues for `*.localhost` live twelve hours and would be expiring
from birth. Either way it is past the point where Caddy renews (a third of
the lifetime), so it means renewal is failing, and it is one of the few
things worth a notification: `certificate.expiring` when the state is
entered and once more at 3 days. An agent restart forgets that it has
warned, and warns again.

The list of applications carries a summary of both this and the alerts
(`deploy/attention.go`), so that a list can mark an application without a
request per row: `certificate_problem` is the hostname whose certificate is
furthest from in order, with the status and message its own page shows, and
`alert_count` / `alert_severity` are the application's active alerts. Both
are read from the watch's and the alert book's memory when the list is
built; nothing is probed for them. The order of badness is
`waiting_for_dns` (the hostname is not served at all, and waits for the
operator), `expiring` (it works until somebody fails to act), `obtaining`
(usually over in seconds). `unknown` is not a problem: nothing is known
against the certificate, and an agent without a proxy would otherwise mark
every application it runs.

### Graceful shutdown

On `SIGINT`/`SIGTERM`: end log streams → stop accepting HTTP requests → stop the
supervisor and interrupt in-flight deployments (each stays as it is, and is
resumed at the next start: see [Crash safety](#crash-safety)) → wait up to 30s →
close Docker client and database. A second signal kills immediately.

## Containers

| Aspect | Decision |
|---|---|
| Name | `shipwick_<app>_<deployment sequence>_<replica>`, e.g. `shipwick_my-api_7_1`. For humans. App names cannot contain `_`, so it parses unambiguously. |
| Identity | Labels `com.shipwick.managed`, `.app`, `.deployment`, `.replica`. The agent finds its containers by label, never by name. |
| Networks | Two bridge networks. `shipwick`: every replica, the agent (health probes) and whatever the user runs beside Shipwick. `shipwick-services`: every replica and Caddy; a replica carries its application's names here while it is ready (see Routing). No host ports are published, with the one exception under Ports — no port conflicts, replicas just work, and the proxy is the way in. |
| Volumes | `volumes` in deploy.yaml become named Docker volumes `shipwick_<app>_<volume>`, created with labels, mounted at the given path. They belong to the application: every deployment mounts the same ones, and nothing removes them — not a rollback, not `delete`. Never a host path. **Backup and restore** go through the replica's container and Docker's archive endpoints (`CopyFromContainer`, `CopyToContainer`), which read and write a container's filesystem whether or not it runs: no helper container, no image to pull, no shell. A backup holds the lock while it streams and is rewritten on the fly so that its entries are relative to the mount point. A restore is the one thing that removes a volume: with the application stopped, the container goes, then the volume, then `createReplicas` makes both again — empty — and the archive is extracted into the mount point before any process could write there. It is the only user of the create-only half of `ensureReplicas`. |
| Ports | None, unless deploy.yaml has `publish`; then exactly the listed container ports are bound on the server (`PortBindings`), on the address given or on every address, for services the proxy cannot serve because they are not HTTP. Only for recreate applications with one replica: a server port has one holder, so the old version is stopped before the new one binds it. The engine refuses, before anything is recorded, a port the agent or the proxy listens on and a port another application's active configuration publishes — Docker would refuse the bind too, but only at start, after the old version is gone. Published ports bypass the host firewall on most distributions (Docker inserts its own iptables rules), which is why the README says to bind to a private address. |
| Images | After a successful deployment, and after `delete`, the images that only retired deployments of the application name are untagged. Kept, across all applications: the active deployment's image and its most recent superseded one (the rollback target). Never forced: an image any container uses stays, so does anything no deployment ever named. Images the CLI built and sent (`shipwick.local/…`) are loaded with `ImageLoad`, live in the same store and are pruned the same way; they are never pulled, since their host does not exist. An archive may leave out the layers the server has (see [Images without a registry](#images-without-a-registry)); one that leaves out more is refused by the daemon and the image it tagged is removed. One of them that no deployment names — sent for a deploy the agent then refused — is removed by the application's next sweep once it is ten minutes old (the CLI deploys within seconds of sending), and by `delete` whatever its age. A deployment that ends `FAILED` or `ROLLED_BACK` removes the image it named right then, under the same rules — kept if it is anybody's active image or rollback target, never forced, never an error: a first deployment that keeps failing would otherwise leave one image behind per attempt, with no successful deployment to sweep them. |
| Static folders | `static` in deploy.yaml is served by Caddy itself, from `/srv/shipwick/<app>/<digest>` inside Caddy's own container — the `caddy-static` volume of the compose setup. The CLI uploads the folder as a tar archive (`PUT …/static`); the agent inspects it while it hashes it — files and directories only, nothing outside the folder, no symbolic links, which the file server would follow into the container that holds the certificates — keeps it in `<data>/uploads/<app>/` under its digest, one per application, and answers with the digest; the deployment names it (`?static=`). The rollout runs in the proxy's container through the Engine API's exec, the way command health checks run in a replica: `mkdir -p`, `CopyToContainer` into `<digest>.part`, `test -f …/index.html`, `mv` into place — so a crash half-way never leaves a directory that looks complete — then a route whose root is the directory. Directories are named by content, not by deployment: a rollback, or a redeploy of the same folder, routes to a directory that is already there and needs no upload. After a deployment the directories that neither the active deployment nor its most recent predecessor serve are removed (`ls -1`, `rm -rf`), like images; `delete` removes the application's directory and its upload. The proxy is found by its Compose labels: the `caddy` service of the project the agent's own container belongs to, which the agent reads off itself once (it inspects the container its hostname names, as it does to join the network) — `shipwick` in the files Shipwick ships, anything under `docker compose -p`. An agent that is not a container, or not one Compose started, looks in the project `shipwick`; one whose proxy is not such a container fails the deployment with a sentence. The access log is read from the same container. The supervisor skips static applications: there is nothing to keep alive. Logs, metrics, jobs and one-off commands answer `STATIC_APPLICATION`. |
| Restart policy | Docker's is set to `no`. Restarts belong to Shipwick's supervisor, which adds backoff, health awareness and crash-loop detection; two restart mechanisms would fight. |
| Stopping | `SIGTERM`, then `SIGKILL` after the application's `deploy.stop_timeout` (10s unless set). An init process is put in front of the image's own only where deploy.yaml says `init: true`: see [Retiring a replica](#retiring-a-replica-and-what-a-deployment-waits-for). |
| Limits | `resources.cpu` → `NanoCPUs`; `resources.memory` → `Memory`, with `MemorySwap` equal to it so the limit is a hard cap. |
| Hardening | Never privileged; `no-new-privileges`; no host mounts (named volumes only); nothing from `deploy.yaml` ever runs on the server itself. |
| Process | `entrypoint`, `command` and `user` go to Docker as `Entrypoint`, `Cmd` and `User`: argv as written, nothing split, joined or passed through a shell. Unset means the image's own. They change what runs inside the container, which the image always decided anyway; the container's boundaries are the same. |
| Logs | `json-file` driver capped at 3 × 10 MB per container, so a chatty app cannot fill the disk. `logging` selects another driver, whose options reach the daemon as written; the caps stay for `json-file` and `local` unless the application sets its own, and a collector address must be `scheme://host:port` — a socket, or a certificate file, is a path on the server, and no application gets to name one. `shipwick logs` reads what the daemon keeps locally, which for a remote driver is its dual-logging cache, and so does the log archive when a container ends. |
| Job containers | `shipwick_<app>_job_<job>_<run id>`, e.g. `shipwick_my-api_job_nightly-report_42`; labels `com.shipwick.job` and `.run` instead of `.replica`. Same image, env, limits, networks and hardening as a replica; no volumes, no published ports, the image's entrypoint unless `entrypoint` overrides it, the job's command as the command. Removed when the run ends. See Jobs. |

## Storage

SQLite via `modernc.org/sqlite` (pure Go → static binary, trivial
cross-compilation, no CGO). WAL mode, foreign keys on, and a **single
connection**: the agent's write volume is tiny, and serializing access rules
out `SQLITE_BUSY` and lock-upgrade deadlocks by construction.

Tables: `applications`, `deployments`, `deployment_replicas`, `events`,
`tokens`, `audit_log` (one row per request that changed something, kept for
a year), `access_rules` and `sessions` (who may sign in as what, and who is
signed in: the SHA-256 of each session, never its value), `metric_samples`, `traffic_samples` (one row per application and
minute with requests: counts and a latency histogram), `job_runs` (one row
per run of a hook, job or one-off command, with the tail of its output; the
last 50 per job are kept), `secrets` (name, sealed value, timestamps),
`registries` (registry, username, sealed password), `certificates`
(hostname, chain, sealed key), `deployment_references` (for a deployment
whose document referred to stored secrets: those values as the document
wrote them, sealed; see [Configuration as the API](#configuration-as-the-api)),
`backup_runs` (one row per backup, with what
was done with it since), `backup_installation` (one row: the name this
installation marks its bucket with) and `log_archives` (one row per ended
run of a container whose output was kept; the lines are files, see
[The log archive](#the-log-archive)).
Migrations are an append-only list tracked in `PRAGMA user_version`.
Timestamps are fixed-width UTC text, so they sort lexicographically.
A static deployment records `static_digest`, `static_files` and `static_bytes`
next to its spec: the folder it serves, which names its directory in the
proxy. The archive itself waits in `<data>/uploads/<app>/<digest>.tar` between
the upload and the deployment, one per application, with a `.json` note of
what it holds; it is not state — the proxy's volume is — and the data
directory is not what a backup of Shipwick needs to include for it.

The spec of every deployment is stored with it (as JSON), which is what makes
rollback "deploy the spec of an older record again" rather than a separate
code path.

**Environment values are encrypted in that JSON**, and the basic-auth
passwords of the `proxy` block with them, sealed under their position
(`proxy.basic_auth[0].password`) where an env value is sealed under its
variable's name; nothing else is. The
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
  must be backed up with the database. The agent says so when it creates
  it, and takes that backup itself when it may: see [Backups](#backups). A key that does not match the database is caught at start, not at the
  first deployment: opening the store proves the key against every stored
  value.
- The store does the sealing and opening at its boundary (`sealSpec` on write,
  `openSpec` on read), so the engine sees clear values, as it must to create
  containers, and every code path that persists a spec is covered by the same
  two functions. A store on disk refuses to open without a key; only the
  in-memory database of the tests may go without.
- The `secrets` table — the values behind `shipwick secret set` — uses the
  same seal, with the secret's name as the additional data, so the same key
  covers both, and a row moved to another name fails to open the same way.
  Values leave the store by name only, to the engine (`GetSecrets`); the
  listing query never selects the column.
- The `registries` table — the credentials behind `shipwick registry login` —
  seals the password the same way, bound to `registry:<name>`. The prefix
  keeps a registry's password from opening as a secret or a variable that
  happens to carry a registry's name.

**Registry credentials are the agent's, not the daemon's.** A private image
used to need `docker login` on the server and a mount of the resulting file
into the agent's container. The agent now keeps the credential itself and
sends it with the pull, which is what the Engine API expects anyway: the
daemon holds no registry credentials, every pull carries its own.

- One function pulls: `Engine.pull` (`deploy/registry.go`). Rollouts, the
  re-pull of a pruned image when replicas are recreated, and jobs all go
  through it, so a credential is found the same way everywhere: by the
  registry of the image reference, normalised as Docker normalises it
  (`docker.io` for every name of Docker Hub, lower case, the port kept).
- Precedence: the stored credential, else whatever the Docker configuration
  file offers, which is what `Runtime.PullImage` does when it is handed no
  credential. Existing installations with a mounted `config.json` keep
  working untouched.
- A credential is checked before it is stored, with the Engine API's login
  call. That call verifies and keeps nothing, so it costs no state, and it
  moves the discovery of a mistyped token from the next deployment to the
  moment of typing.
- The daemon does not classify refusals consistently: depending on the
  registry and the image store, a pull refused for authentication arrives as
  401, 403, 404 or 500. `docker.pullDenied` therefore also reads the text,
  and the engine turns a refusal into one sentence that names
  `shipwick registry login <registry>`.
- Credential helpers are not supported, and will not be: a helper is a
  program on the host that the Docker CLI executes. The agent's container
  does not have it, and the agent executes nothing. The daemon is no way
  around that. It pulls with the credential the request carries: asked
  through the Engine API without one, Docker 29 refused an image of a
  registry that the CLI on the same machine was logged in to through a
  `credsStore`, and pulled it when the request carried the credential. And
  a `config.json` with a `credsStore` holds the registries' names without
  their passwords, so there is nothing in it to read. What helpers are used
  for in practice — Amazon ECR and Google Artifact Registry — is a token
  that expires, and that is a matter of who renews it: `shipwick registry
  login` reads a password from standard input, so the cloud's own CLI can
  be piped into it wherever that CLI is signed in. Running a helper for the
  operator, on their machine or in a job of the agent's, would add a
  program and its cloud credentials to what Shipwick has to keep safe, for
  one line of shell.

**The key can be rotated while the agent runs** (`store/rotation.go`).

- One list, `sealedColumns`, names every sealed column outside deployment
  specs: table, key column, sealed column, and how the name a value is bound
  to is built from the row's key. Rotation walks that list; a test scans the
  schema and fails for a `BLOB` column that is neither on the list nor
  declared unsealed, so a sealed column cannot be added and forgotten. Specs
  are not on the list: rotation opens each with `openSpec` under the old key
  and seals it with `sealSpec` under the new one, so whatever those two
  functions seal is covered without rotation knowing the fields. Only the
  fields that opening changed are replaced in the stored JSON; the rest of a
  deployment record stays byte for byte what it was.
- The database and the key are two files, and no step changes both. The order
  is: the new key is written beside the key file as `encryption.key.new` and
  synced; the transaction commits, with `synchronous=FULL` for this one
  commit, because under the usual `NORMAL` a commit survives a crash of the
  agent but not necessarily a power cut, and this commit decides which key
  the data needs; the new key is renamed over the key file. A crash before
  the commit leaves the old key in place and a pending key that opens
  nothing; a crash after it leaves a pending key that opens everything. On
  start the store tries which of the two opens the data and discards or
  promotes the pending key accordingly. Nothing is deleted while neither
  fits.
- The store's cipher is a small key ring: it seals with the current key and
  opens with the current one or, failing that, the one before the last
  rotation, which it keeps in memory only. That covers a reader that fetched
  a row just before the rotation committed. Writers are the harder half: a
  value sealed under the old key and written after the rotation went over
  the table would be lost at the next start. Whoever seals and writes
  therefore holds a read lock across both, and the rotation holds the write
  lock.
- With the key in `SHIPWICK_ENCRYPTION_KEY` the last step is not the agent's
  to take. Refusing to rotate in that mode was considered; so was returning
  the key and keeping it in memory only, which loses every secret if the
  response is lost and the agent restarts. Instead the pending file stays:
  the response carries the key once, the file is the copy that survives a
  lost response, a start with the old key finds a database it cannot open
  and a pending key that can and refuses with that explanation, and a start
  with the new key removes the file. The cost is stated in the handbook:
  until that restart the data directory holds the key next to the database,
  which is what setting the variable was meant to avoid.
- The `certificates` table keeps the chain in clear — it is public, and
  readable in the file — and the key under the same seal, with
  `certificate:<hostname>` as the additional data. Keys leave the store for
  the engine, which hands them to the proxy inside its configuration; no API
  response is built from them.

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

Failed authentications are counted per client address (`agent/internal/api/
ratelimit.go`): 20 within a minute, and the address's further wrong tokens
are answered `429 RATE_LIMITED` for the next minute. Only failures count, and
a valid token is never refused. The address is the connection's, never
`X-Forwarded-For`, which a caller could set to be counted under someone
else's; the price is that behind Caddy every client shares the proxy's
address — which is why a valid token must pass even while the address is
limited, or one guesser would lock the dashboard and every CLI out, and why
the limit is one that honest use cannot reach. It is in memory, pruned once
a minute, and is not meant to stop a determined attacker: a 256-bit token
does that. It gives the guess a clear answer and spares the hash.

Who did what is recorded where it is cheap and durable: the authenticated
token's name travels in the request context (`deploy.WithActor`), each
deployment stores it (`by` in the API), and stop and start events name it
unless it is root.

### Who may do what

Authentication ends with an identity — a kind, a name, a role, a list of
applications, an expiry — and everything after it works on that identity,
not on a token: `authorize` in `agent/internal/api/access.go` is the one
place that decides, and the audit trail's actor is the identity's kind and
name. A second kind of caller is another way to arrive at an identity; it
needs neither a second check nor a second trail.

**A limit is a list on the token, not a second set of roles.** The
alternative was a role per application — `deploy` on one, `read` on another,
`admin` on a third. That is a matrix to explain, to render and to get wrong,
and its `admin` cell has no good meaning: almost everything `admin` does —
secrets, registries, certificates, tokens, the key, exports — belongs to no
application. So the role stays what it was, and a `deploy` token may carry
the applications it deploys. What it loses is exactly what can be said in
one sentence: it changes those applications and reads everything.

Reading is not limited, on purpose. A limit that hid the other applications
would have to hide them everywhere they show — the list, the deployments,
the server's counts, the proxy's routes, Prometheus — and would promise a
separation that one Docker daemon does not give. The limit is there so that
a CI token leaked from one repository cannot redeploy another's application;
it is not tenancy.

**No handler can forget it.** A handler never sees the question. Every
authenticated endpoint is registered through the route table, which notes its
role and whether its path is under `/applications/{name}`; `authorize` runs
before the handler and compares `{name}` with the list. The name in the path
is also the name a first deployment creates, so creating is covered by the
same comparison — which is why a limit may name an application that does not
exist yet. An endpoint that takes `deploy` and is not under
`/applications/{name}` is refused to a limited token outright: nothing says
which applications it would touch, and refusing is the answer that cannot be
wrong. A test walks the route table and fails for an application route the
prefix does not catch, and sends every `deploy` route a request for an
application the token does not have.

A route that takes a document and no name (`documentRoutes`: `POST
/applications`, `POST /validate`) is a third kind, not an exception to the
second. Before `authorize` runs, the name is read out of the body
(`spec.DocumentName`: the top-level `name`, held to the pattern of a name,
nothing else of the document judged) and becomes the request's `{name}`; the
body is put back for the handler. From there the route is an application
route: the same comparison, the same refusal, the same audit entry. A
document whose name cannot be read is about no application, and a limited
caller is refused as for an endpoint about none. The handler then insists
that the document it parses has the name that was checked, so the check and
the deployment cannot be about two different applications even if the two
ways of reading a name ever disagreed. The same test sends each such route
the document of another application, of the token's own, and documents
without a name.

**Expiry is checked after the token is known**, never instead: the answer
`TOKEN_EXPIRED` goes only to a caller who presented the token itself, so it
gives nothing to someone guessing, and it is neither counted by the rate
limit nor subject to it — a CI job that keeps trying with last quarter's
token must not lock the dashboard out, and must keep being told why it
fails. An expired token stays in the table: deleting it would turn the clear
answer into "invalid token".

**A token's applications and its end can be changed; its role and its value
cannot** (`PUT /tokens/:name`, `handlers_token_update.go`). 0.6 allowed
neither, on the principle that what a value someone holds may do should not
change under them. Two things spoke against keeping it whole. Widening a
limit by one application meant a new value in every place the old one was
stored, which is the work that makes people create tokens without a limit.
And an end that cannot be moved has the same effect on ends: the pipeline
that went red on its date gets a token that never expires. So both are
editable, by `admin`, each change recorded with what was there before
(`applications a -> a b`, `expires … -> …`) under the name of whoever made
it. The role stays fixed because it is the one change that turns a token
into a different kind of thing: `read` to `admin` by an edit is how a token
handed out for looking ends up deleting. An end moves into the future only,
also for a token that has expired — `TOKEN_EXPIRED` is answered from the
row, so the next request with the old value simply works — and what that
gives up is said in the handbook: the value that had until its date to leak
has longer. The update reads the row, validates against the role it finds
(`ValidateTokenApplications`, as creation does) and writes in one
transaction; the store's `UpdateToken` writes the list and the end and
nothing else, whatever its callback returns. Nothing is cached about a
token between requests except when its use was last written down, so the
change holds from the next request without anything to invalidate.

### The audit trail

One table, `audit_log`, written by the same wrapper that authorizes: a
request to an endpoint listed in `auditedRoutes` (`agent/internal/api/
audit.go`) gets a response writer that notes the status, the error code and
the `Location` of what was started, and the entry is written when the
handler returns — also when it panics, and also when the request was refused
before it reached the handler, since an attempt is what one looks for after a
token leaked. The list is keyed by route pattern, next to a second list of
the endpoints that are not `GET`s and change nothing, each with its reason;
a test fails for a route that is in neither.

What an entry holds comes from the path — the application, and the segment
that names the job, volume, secret, registry, hostname or backup — and from
the answer. A handler adds to it only where the path does not say what was
acted on: the name and the permissions of a token being created, the options
of an import. The body is never read for it, so a secret cannot get in by
accident; a test sends every audited endpoint a body full of a marker and
looks for it in the table. The one thing a body gives the trail is the name
of the application a document is about, on the routes that have no name in
their address, and only when it has the form of a name.

The outcome is the HTTP answer's, which for a deployment means "accepted".
How it went on is in the deployment's own record, and the entry names it;
copying the final status over would make two records of one fact, one of
them written later by something that is not a request.

The address is the connection's, with the proxy's `X-Forwarded-For` next to
it rather than in its place. Caddy replaces the header with the address it
saw, so through the proxy it is the client; on a connection that bypasses the
proxy it is whatever the caller sent. Keeping both lets the reader tell.

Retention is a year and 100,000 entries, enforced in the transaction that
writes an entry: two indexed deletes that almost always delete nothing. A
separate pruning loop would be one more thing running for a table that
changes when a person or a pipeline does something.

**Searching.** `GET /audit` filters by application, actor, kind of actor,
time, action and outcome. An action filter is an exact action or the start
of a family up to its dot (`token.`), several of either; that is all the
structure the action names have, and it spares a list of every action in
every client. The filters become one `WHERE` clause (`AuditFilter.conditions`)
with every value bound. There is no index by action: the table is bounded
at 100,000 rows, and a filter that an index would serve is still ordered by
id.

**Whether there is more** is answered by reading one entry beyond the page
and saying so next to `data` (`"more"`). A full page says nothing about the
next one, and a client that guessed from it offered an empty page whenever
the count came out even. The field sits beside `data` rather than in it so
that a client from before reads the list as it always did; a client that
finds no `more` is talking to an older agent, which is also how the CLI
learns that its newer filters were ignored and refuses to show an
unfiltered trail as if it were the answer.

**Export** (`GET /audit/export`, `audit_search.go`) streams everything that
matches as CSV or as one JSON object per line. The store has one connection
to SQLite, so a cursor held open while a slow reader takes the response
would stall every other request; the trail is read in pages of 500 by id
instead (`EachAuditEntry`), each page a query that is over before its rows
are written out. Memory is one page whatever the trail's size, and what is
written during an export is newer than where it started and not part of
it. A failure after the first byte is said in the trailer the server export
uses, and the entry is recorded as failed. The export is audited like the
downloads of volumes and backups — a `GET` that hands out what only admin
may have — with the format, the filters and the count.

**CSV cells are made safe for spreadsheets at the export, not in the
trail.** The trail records what was asked, including the name in a path
that was refused, and a name can begin with `=`. A spreadsheet runs such a
cell as a formula. `api.SpreadsheetSafe` puts an apostrophe before a cell
that begins with `=`, `+`, `-`, `@`, a tab or a carriage return; it is
applied to every text column of the CSV and to nothing else, because the
JSON export and the API are read by programs, for which the apostrophe
would be a corruption.

### People: signing in with an OpenID Connect provider

A person in a browser is not given a token. The agent can be configured with
an OpenID Connect provider (`agent/internal/config/oidc.go`,
`agent/internal/oidc`), and what a sign-in there produces is the identity
described above with the kind `user` and the e-mail address as its name.
`authorize`, `serve` and the audit trail are not told the difference; the
only place that knows about sessions is `identify`, which looks a presented
credential up among the sessions when it is not a token
(`agent/internal/api/signin.go`).

**No users of its own.** The agent stores no password and no account. It
stores rules — a role for an address, a group or a domain — and the sessions
of people who are signed in. Second factors, password rules and offboarding
stay with the provider, which is where a company already manages them.

**Configured in the agent's environment, in one place.** The issuer, the
client id and secret, the scopes and the name of the groups claim are
variables next to the others; the dashboard is told what it needs by the
agent. Two places to configure would be two places to get out of step, and
the secret belongs with the component that uses it. The endpoints and the
signing keys come from the issuer's discovery document, fetched when first
needed — the agent starts whether or not the provider is up — and again
after an hour, which is also the longest a key the provider withdrew is
still believed. A key id the cache does not know fetches the keys at once,
at most once a minute, so that a rotation is followed without a restart and
a stream of forged tokens cannot turn the agent into a client hammering the
provider. A discovery document that names another issuer than the
configured one is not used: its keys would sign for someone else. The issuer
must be `https`, or `http` towards localhost or a private address, the rule
the webhook follows; every endpoint the document names is held to it too.
The requests go out through an HTTP client of the package's own with a
ten-second limit that follows no redirect, so that the client secret is
sent to the token endpoint the document names and nowhere that endpoint
points.

**The authorization-code flow with PKCE, split between two servers.** The
dashboard's server talks to the browser and the agent talks to the provider.
The dashboard's server generates `state`, `nonce` and the PKCE verifier,
keeps them in an `httpOnly` cookie, and redirects; on the callback it checks
`state` and hands the code, the verifier and the nonce to
`POST /auth/exchange`. The agent holds the client secret, redeems the code,
verifies the ID token and decides. Each of the three values closes one hole:

- `state` stays in the dashboard and is compared there, because what it
  protects is the dashboard's callback: without it, a page elsewhere could
  complete a sign-in in someone's browser with a code of its own choosing.
- The verifier makes a code worthless to whoever reads it off the callback
  URL: the provider redeems the code only with the verifier whose hash went
  out with the authorization request, and the verifier never leaves the
  dashboard's cookie until the exchange. This is what lets the exchange
  endpoint take no token: what it is handed is the credential.
- The nonce binds the ID token to this sign-in, and the agent accepts each
  nonce once (a map of hashes kept for fifteen minutes, longer than an ID
  token is accepted for). A code is redeemed once by the provider; the nonce
  makes that true even of a provider that would redeem one twice. The agent
  marks a nonce used only after the ID token verified, so the map grows with
  sign-ins the provider vouched for and cannot be filled by guessing.

The agent could have generated the nonce and kept the pending sign-in
itself. That would have put state created by unauthenticated requests into
the agent, to be bounded and expired; kept in the browser's cookie it costs
the agent nothing until a provider has vouched for someone.

The redirect URI is `https://` + `SHIPWICK_DASHBOARD_DOMAIN` +
`/auth/callback`, taken from the configuration. The exchange refuses a
request that names another, though the provider would refuse it as well:
the agent never asks a provider to send anyone to a place a request chose.
(`SHIPWICK_OIDC_REDIRECT_URL` replaces it for a dashboard on the developer's
machine and accepts only a localhost URL.)

**ID tokens are verified with the standard library.** A JWT library would be
a dependency for little code: split, decode, check one signature. RS256 is
accepted, which the providers the handbook names sign with by default, and
ES256; the algorithm is read before any key is
looked at, so `none` and the symmetric algorithms, where a public key would
be taken for a secret, never reach a verification. A token must name the
configured issuer and this client as its audience (and as its authorized
party wherever it has one or several audiences), be within `exp` and `nbf`
with a minute's allowance for clocks, be at most ten minutes old by `iat` —
it comes straight from the token endpoint; an older one was kept from
somewhere — and carry the nonce. RSA keys under 2048 bits are not loaded.
`email_verified: false` is refused; a provider that omits the claim
(Microsoft Entra) is trusted with its addresses, because trusting the
provider is the premise.

**The address is the identity.** Rules are about e-mail addresses because
that is what an admin knows about a colleague; the `sub` claim would be
stabler and unusable in a command. The address is lowercased and has to
look like a company address — it ends up in the audit trail, in logs and in
a deployment's `by`.

**The most specific rule decides.** Address, then groups, then domain; the
first kind with a match is the answer and the rest is not consulted
(`api.ResolveAccess`). An order rather than "the highest role of everything
that matches", because the common exception runs downwards: everyone in a
group deploys, except the contractor who may only read. Within groups the
highest role counts and limits to applications add up, since a person in two
teams is expected to do what either does.

**A session is a row, and is asked about on every request.** The credential
is 32 random bytes behind `sws_`, stored as its SHA-256 like a token and
presented like one, so the dashboard's cookie, the proxy and the rate limit
needed nothing new. A signed token of the agent's own would have saved the
lookup and could not have been taken back; a row can. The row holds what the
provider said (address, groups) and what the rules gave (role,
applications). Each request resolves the rules again for that address and
those groups — one more small query — and a session whose answer differs
from what it was given is ended there and then, with the reason. Comparing
the outcome rather than remembering which rule matched means that adding an
unrelated rule ends nobody's session, and that any change which does alter
what a person may do — the rule revoked, its role changed, a more specific
rule added — takes effect with the next request instead of at expiry. An
ended session keeps its row until it would have expired, so that its holder
is told why rather than "invalid token"; rows are removed a day after that,
in the transaction that creates the next session.

What a session cannot see is the provider: groups are as of the sign-in.
That bounds the lifetime. Ten hours, fixed from the sign-in and not extended
by use: longer than a working day, so the dashboard does not send anyone
back to the provider in the middle of one, and shorter than the night
between two, so a membership removed at the provider is gone here by the
next morning at the latest. `DELETE /access/sessions/:email` is for when
that is not soon enough.

**Refusals that say what to do.** No matching rule is `ACCESS_NOT_GRANTED`
with a sentence that names the address and the command an admin would run,
because the person reading it cannot fix it and has to forward it. Ended and
expired sessions have codes of their own for the reason expired tokens do:
only the holder gets them, so they give nothing away, and they are not
counted as guesses. A session nobody issued is a wrong token. Failed
sign-ins are counted by the same limiter, and a limited address is answered
before the provider is asked — otherwise the exchange endpoint, which takes
no token, would let anyone make the agent present its client credentials to
the provider as often as they liked.

**Sign-ins are in the audit trail**, written by the handler since there is
no authenticated request for the wrapper to record: the person the provider
vouched for is the actor, admitted or not. A sign-in that failed before
that has no actor and goes to the log.

**The name is a claim of the operator's choice** (`SHIPWICK_OIDC_NAME_CLAIM`,
default `email`). Everything downstream of the sign-in knows a person by one
string — rules, sessions, the audit trail, `by` — so the claim is chosen in
one place and nothing else changes. What changes is what the string may be.
An address was validated as one; a `sub` or a user name is whatever the
provider issues, so `api.ValidatePersonName` bounds it to 254 characters of
letters, digits and `. _ % + ' @ | : = # ~ -`: enough for Entra's base64url
identifiers and Auth0's `provider|id`, and nothing that breaks a log line
or a table. A value outside that is refused and not repeated. Only the
`email` claim is brought to lowercase; another claim is kept as issued,
because subject identifiers are case-sensitive by specification and
lowering them could make two people one. `email_verified` is consulted for
the `email` claim alone.

Rules follow from that. `email` and `domain` rules match a name that reads
as an address, compared in lowercase — which keeps every 0.6 rule working
and makes them useful with `upn`. A fourth kind, `name`, matches the string
exactly and decides first. It exists because the alternative, letting an
`email` rule hold a non-address, would have made one kind mean two
comparisons depending on configuration the rule cannot see. Rules can be
written before a provider is configured, so the agent accepts every kind
whatever the claim is, and the CLI says when a rule cannot match under the
claim in use.

A session records the claim its name was read from (migration 21), and
`identifySession` ends a session whose claim is no longer the agent's: its
name is not the name the rules are now written for. Sessions from 0.6 are
`email` by the column's default and carry on.

**Entra's multi-tenant issuers are accepted as far as they can be checked**
(`oidc/tenants.go`). The discovery document at `…/organizations/v2.0` names
`https://login.microsoftonline.com/{tenantid}/v2.0` as its issuer, which
the rule "the document must name the configured issuer" refuses, rightly:
a document that names someone else describes someone else's keys. The
exception is as narrow as it can be made. The configured issuer's path must
be `/organizations/v2.0` or `/common/v2.0`, and the document's issuer must
be that same origin with `{tenantid}` in the first segment — computed from
the configuration and compared whole, not "any issuer containing a
placeholder". A token's `iss` must then equal the template with the token's
own `tid` substituted, and `tid` must be a GUID, so the substitution cannot
smuggle a path. Where a key in the key set names the issuer it signs for,
as Entra's do, the token must be from that issuer; that keeps the key of
the personal-accounts tenant, which `common` publishes, from signing for a
company. The keys still come from the configured origin over TLS, which is
what made the single-issuer case trustworthy too.

None of that says which tenants should get in, and every Microsoft
customer is one. `SHIPWICK_OIDC_TENANTS` is therefore required with such an
issuer, at start, and checked after the nonce so that only the sign-in it
belongs to learns that a tenant is refused. `*` is accepted only with the
name claim `sub`: addresses and user names are chosen by each tenant's
administrators, and with every tenant admitted anyone could name an account
after the subject of a rule. For the same reason the groups claim is
dropped when every tenant is admitted. With a list, the tenants on it are
trusted as a single tenant is. The tenant is stored with the session, and
a session whose tenant has left the list ends with its next request, like
one whose rule was removed. Rules per tenant were considered and left out:
a list in the environment answers "who may sign in at all", the rules
answer "as what", and a rule kind that mixed both would need the tenant in
every other rule to be safe.

Rules, sessions and the audit trail are the server's own and are not part of
an export, like tokens.

## Configuration as the API

`POST /applications/:name/deploy` takes the `deploy.yaml` document itself as
the body. JSON works too, since YAML subsumes it. One parser and one validator
(`pkg/spec`) serve both the CLI and the agent, so error messages are identical
on both sides — and the agent validates again regardless, because client-side
validation is a convenience, not a trust boundary.

A `shipwick.yaml` — several applications in one file — is the CLI's concept
alone. `spec.ParseMany` splits it into one `deploy.yaml` document per entry,
validates each with the same `Parse`, and checks the `after` graph: known
names, no cycles. The CLI then sends the documents as separate deployments, in
dependency order, up to four at a time. The agent sees nothing new: each
application keeps its own record, lock and rollback, and a failure in one
cannot touch another. The parser lives in `pkg/spec` rather than in the CLI so
that the agent could accept the file itself one day without a second
implementation of its rules.

`${NAME}` placeholders are the one part of the document two sides fill in. The
CLI substitutes what its environment and `--env-file` know; whatever is left
in `env` values travels as written and is substituted by the engine from the
`secrets` table, at the start of `Deploy`, before the record exists. The
record then holds the resolved values: a deployment is a fact about what ran,
so a secret changed later reaches the next `deploy` and no redeploy or
rollback — those re-use stored specs and resolve nothing. Only `env` values
are resolved on the server, because only they are secrets; a `${TAG}` in
`image` is the client's, and the CLI still refuses to send one it cannot fill.
The two sides share one pattern (`spec.Placeholder`), and the CLI leaves
`$${NAME}` in `env` values untouched for the agent to unescape, since a
`${NAME}` it produced would look like a placeholder to the agent. A name
neither side has is refused as `INVALID_CONFIG` with the variable as the
field, so the CLI renders it like any other validation error.

`POST /applications/:name/validate` is the same question without the
deployment. What `deploy` checks before it records anything is one function
on each side of the handler — `readConfig` (the body, `pkg/spec`, the name in
the URL) and, in the engine, `resolveSecrets` followed by `admit` (the image
of a `build`, hostnames, published ports) — and `Deploy` and `Validate` both
call them, so the two cannot drift apart; a test sends the same refused
documents to both endpoints and compares the answers byte for byte.
`Validate` takes no lock: it only reads, a deployment that is running must
not make it fail, and its answer is advice — `deploy` checks again under the
lock, which is the answer that counts. It exists to be asked before the CLI
builds an image and uploads it, so it is lenient exactly there: `build`
without `image` passes, and a static application names no upload.

**The document, given back.** `GET /applications/:name/config` writes the
active deployment as a deploy.yaml (`spec.Document`), so that a configuration
can be edited where it is shown and a lost file can be had again. The record
cannot simply be turned around, because it holds values where the document
had references. Three decisions follow from that:

| Decision | Why |
|---|---|
| **The references are kept next to the record, not in it**: a table of their own, `deployment_references`, one sealed row per deployment that had any — the text of each secret value as the document wrote it, placeholders in place. A redeploy and a rollback copy the row of the deployment they re-use; an export carries it in `app.json`, and an import checks it like the spec. | The record stays what it is, the values the containers were started with, and nothing that reads a spec learns a new field: `spec.App` is what the API returns, what an export carries and what `spec.Validate` rewrites, and a field of it that is not a key of deploy.yaml would have to be explained to each. A row of its own is written in the transaction that writes the record, goes when the record goes, and is one more entry in the list a key rotation walks. It is sealed because the text around a reference is whatever the document said there. |
| **A value that arrived as a literal is not returned, and says so.** The document has `"********"` in its place, a comment on the line, and the answer lists the fields. | The agent sees no difference between a value typed into the file and one the CLI filled in from the environment: both arrive as text. It cannot tell a password from a log level, so it hands out neither; guessing would be right until the day it mattered. A document with fewer masks is one click away for the user — store the value as a secret and refer to it. |
| **The mask is refused wherever a document arrives** (`resolveSecrets`, which `Deploy`, `DeployStatic` and `Validate` share), by exact comparison and before any secret is filled in. | A document that can be fetched will be sent back, and a mask deployed as a value takes an application down with a password of eight asterisks. Refusing it in the one function every document passes covers the document of this endpoint, a spec copied out of `GET /applications/:name`, and whatever else is pasted. The price is that `********` cannot be an env value; a stored secret may still hold it, since the comparison is with what the document wrote. |

It takes the `deploy` role, not `read`. The text around a reference —
`postgres://app:…@db:5432/app` — is more than `GET /applications/:name` shows,
and a value in which the CLI filled in one placeholder and left another to the
server arrives with the first as a literal in that text. Whoever may deploy an
application can read its whole environment with `shipwick run`, so the
document tells them nothing new; a reader it would.

The document comes in two spellings because two programs read it. The agent
fills in `${NAME}` in secret values only, the CLI everywhere, and unescapes
`$${NAME}` everywhere else. A literal `${NAME}` in a `command` is therefore
written as it is for `POST /applications` and as `$${NAME}` for a file
(`?escape=true`, which `shipwick config` asks for). The agent does the
escaping rather than the CLI because it wrote the document and knows which
scalars are which.

`POST /applications` and `POST /validate` take a document without a name in
the address, for a client that has a document and no parser for it. They are
the handlers of `deploy` and `validate` behind a different way of finding the
name: see [Who may do what](#who-may-do-what).

## Installing from the laptop

`shipwick server install` is the installer run over SSH, not a second
installer. The CLI runs the system `ssh` as a program with arguments
(`os/exec`, never a shell) with `BatchMode=yes`, so that it fails rather than
prompts, and `StrictHostKeyChecking=accept-new`, so that the key of a server
nobody has connected to yet is taken on first contact while a key that
changed is still refused; every remote command is a fixed string of ours. The two hostnames
and the version are the only values from the command line that reach the
server; they pass the same `spec.ValidateDomain` the agent applies to
`deploy.yaml`, and go in as environment assignments of the installer, which
reads them as documented. The token is taken from the installer's summary and
never read from `/opt/shipwick/.env`: the command has exactly the access of
someone running the installer by hand. `shipwick doctor` resolves hostnames
through the same public resolvers as the agent (see Routing); the CLI carries
its own copy, since `agent/internal` cannot be imported.

## The CLI on the server

The installer signs the server's own `shipwick` in when the API has a
hostname, and only then: the agent publishes no port, so the hostname is the
one address the CLI can use there, the same one a laptop uses. A loopback
port published for the server's sake would be a second, unencrypted way in to
keep closed.

The context is written by the CLI, not by the installer: `shipwick login
--token-stdin --no-check`, the token on standard input. The file's format and
its permissions stay in one place (`cliconfig`), and the shell script holds
no YAML. `--no-check` exists for this caller. `login` otherwise asks the
agent whether the token is right, and on a new server that request would
fail for reasons that have nothing to do with the token: the hostname's DNS
record is often created after the installation (`shipwick server install`
prints the records to create at its end), and the certificate follows the
record. The installer has the token from the `.env` the agent was started
with, so there is nothing for the check to find out.

Root's contexts are root's. The installer reads `shipwick context ls` and
writes only a context whose URL is this server's, or `default` when nothing
is saved; saving the same token again is how "replace it if it is stale" is
done without reading the file. It restores the current context afterwards,
since `login` makes the one it saves current.

Whether a command runs on the server is decided by the installation's `.env`
(`/opt/shipwick/.env`, or under `SHIPWICK_INSTALL_DIR`): it exists, or the
directory holding it may not be looked into, which only root's `0700`
directory answers. Only then, and only for an agent address on the loopback
interface, the "cannot reach the agent" message drops the SSH tunnel and
names the hostname setting instead.

`GET /server` carries `dashboard_url` so that the CLI can send a user to the
dashboard without knowing how the server was installed. The agent builds it
from `SHIPWICK_DASHBOARD_DOMAIN`, the same value its proxy route is built
from, and it is always `https`: the route exists only behind Caddy.

## Leaving the server

What an installation reaches outside itself, and through what, on a network
where the way out is a proxy, an authority of the company's own, or nothing.

**Images are the daemon's.** The agent asks Docker to pull; the request to
the registry is made by the daemon, with the daemon's proxy and the daemon's
trust store. No setting of the agent can change that, so the agent does not
pretend to: a pull that fails before the registry answers
(`docker/pull_network.go` recognises a dial, DNS, proxy or certificate error
in the daemon's message) ends in a sentence about `/etc/docker/daemon.json`
or `/etc/docker/certs.d`, chosen after asking the daemon whether it has a
proxy at all. `GET /server` reports both sides (`network.proxy`,
`network.docker_proxy`), and `shipwick doctor` points at the mismatch.

**One place decides how the agent goes out** (`pkg/outbound`). The webhook
and the bucket take their transport from it; nothing else in the agent leaves
the server over HTTP. The proxy is the environment's, read by `net/http` as
everywhere, with two additions. A name without a dot is never proxied: it is
a container on the installation's own networks, which only Docker's resolver
knows, and requiring `NO_PROXY` to list every application would fail the day
one is added. And a proxy's refusal of `CONNECT` is turned into an error that
names the proxy, the destination and the status; `net/http` reports it as the
bare status text, which in a log line reads as the destination's answer.
Health checks, the Caddy admin client and the Docker client build transports
of their own with no proxy, as before.

The proxy's URL may hold a password. It is validated at startup with an
error that names the variable and the rule; `net/http`'s own error quotes the
value, and is replaced. The log and `GET /server` carry the host and port.

**Caddy gets the same variables**, for the certificate authority and
Cloudflare's API. Its reverse proxy would honour them too: with `HTTP_PROXY`
set, Caddy sent requests for replicas to the proxy, which knows no
container. The rendered configuration therefore
sets `network_proxy: {"from": "none"}` on the transport to replicas.

**Authorities are added, not replaced.** Go already reads `SSL_CERT_FILE`,
which replaces the system's authorities: right for a container that should
trust one authority, wrong for an agent that posts to Slack and to a bucket
inside the company. `SHIPWICK_CA_FILE` adds to the system pool. It is checked
at startup because every way it can be wrong — above all the directory Docker
creates in place of a file that is missing on the host — otherwise shows up
as a failed handshake blamed on the other side. Caddy needs no variable: it
reads every certificate in `/etc/ssl/certs`, so the mount is the setting.

**An ACME directory is one field** (`TLS.ACMEDirectory`): the `ca` of the
ACME issuer, alone or next to the Cloudflare challenge. Naming an issuer
replaces Caddy's defaults, so a server with no way out no longer asks Let's
Encrypt and ZeroSSL for anything.

**A bundle is the release, plus the images.** `shipwick server bundle` is a
command of the CLI rather than a script because the machine with the
connection is a laptop, often Windows. It verifies the release's files as
`upgrade` does and calls `docker pull --platform` and `docker save`, as an
argv. The images are pulled under the release's tags and held against the
digests the release published, as described below; the archive's checksum,
written into the bundle, guards the copy. The installer is
the same script in both modes: with a bundle, `fetch_release_asset` copies
instead of downloading and verifies against the bundle's `checksums.txt`, and
`docker load` stands where `docker compose pull` was. The release publishes
`install.sh` for this, so that a bundle holds the installer of its own
release and not that of `main`.

**The images of a bundle are proven by the release, not by the registry.**
The release publishes `image-digests.txt`: for each of the three images the
digest of the manifest list its tag points at and, for `linux/amd64` and
`linux/arm64`, the digest of the platform's manifest and of its
configuration. The file is written from the registry after the push
(`scripts/image-digests.sh`, `docker buildx imagetools inspect`), not from
the build's output: it says what a pull is given. It is added to
`checksums.txt` in the job that publishes, the one file of a release that
cannot exist before its images do, so the chain is one: `checksums.txt` →
`image-digests.txt` → manifest or configuration → layers, each link a
SHA-256.

Three digests for an image, because what can be compared depends on who holds
it. `docker save` writes what the daemon has. With the containerd image
store that is what the registry served — the platform's manifest, the
configuration, the compressed layers — and the published manifest digest
settles it. With the classic store the layers are unpacked and the manifest
is one Docker writes on the spot, so no digest of the registry's survives
except the configuration's; but the configuration lists every layer by the
digest of its unpacked content, which is what the archive then holds. The
CLI reads the archive itself rather than asking the daemon (`docker image
inspect`): it hashes every file, requires the published configuration, then
either the published manifest with all its layers or every layer the
configuration lists, and requires that `manifest.json` and `index.json` —
the two tables a `docker load` goes by, depending on the store — name that
image and no other by the release's tag. Pulling by digest would prove the
pull and not the bytes that end up in the bundle.

On the server there is no Go, only `sh` and Docker. The installer loads the
archive and compares each image's ID with the same file: the classic store's
ID is the configuration's digest, the containerd store's the digest of the
manifest it loaded by. That manifest is the registry's unless the bundle was
made with the classic store; then the installer reads it out of the archive
by its digest and checks which configuration it names. An image that fails
is untagged again before the installer stops. All four combinations of
stores were tried with the images of 0.6.0. The check runs after `docker
load` because nothing short of loading says what the daemon will make of an
archive, and before the compose file is replaced because that is the point
of no return.

What this does not do: a bundle brings its own `checksums.txt`, so on the
server the checks catch damage and mix-ups, not a bundle rebuilt with intent.
Both ends print that file's SHA-256 to compare by eye. `--no-pull` proves
nothing, by definition, and leaves `image-digests.txt` out of the bundle,
which is how the installer knows to say so.

## Releases and packages

**The agent asks about new releases; the dashboard does not.** The notice
belongs in the dashboard and in `shipwick server status`, and neither should
need a way out of its own: the dashboard's server may be a container behind
the same firewall, the CLI a laptop on another network, and the agent is the
one place that already has the operator's proxy and certificate authorities
(`pkg/outbound`). So the agent asks, and `GET /server` carries the answer.

It asks the website's `/releases/latest`, whose redirect names the release,
rather than the API: no limit of requests per address, which servers behind
one address would share, and no body to parse. The request is a `HEAD` with
the `User-Agent` `shipwick-agent` and nothing else. A version in it would
tell GitHub which servers run what; nobody on this side reads GitHub's logs,
so it would buy nothing. Once a day, counted from the last attempt whether it
was answered or not, and the attempt is kept in a file in the data directory
so that an agent that restarts in a loop asks once. Failure is a debug line:
a server without a way to GitHub is a normal server. There is no migration
for this: one small JSON file that may be deleted at any time is not worth a
table.

**A package of the agent, and what it cannot be.** The agent is one static
binary and packages as one: binary, unit, settings file, data directory. The
rest of an installation does not. Caddy must stand on the two Docker networks
to find replicas by name (`Backends` are names Docker's DNS resolves), its
container is where static applications are copied to and its output is where
requests are counted, and the agent finds it by its compose labels; a Caddy
of the host can do none of that, and making it possible would mean a second
way to route. The dashboard needs Node. So the package ships a compose file
for those two and says so, instead of pretending to be a whole installation
without containers.

Three things follow from the agent being outside while the proxy is inside.
The admin socket is shared through a directory of the host mounted at the
same path in the container, because the agent repeats the admin address in
every configuration it loads and Caddy binds what it is told. The API must
listen where a container can reach it — the Docker bridge's address — when
it is to be served at a hostname or used by the dashboard; loopback stays the
default, and the settings file says which line to change. And the agent
reads certificates at `127.0.0.1:443`, the proxy's published port, not at
`caddy:443`. None of this needed a change to the agent.

It runs as root. A user of its own would need the Docker socket, which is
root by another name, and the unit would then promise an isolation that is
not there. The unit removes what root does not need instead
(`NoNewPrivileges`, `ProtectSystem=full`, `ProtectHome=read-only`,
`PrivateTmp`). The package starts and enables nothing on installation — the
agent needs Docker and settings, and starting what was just installed would
start an agent that fails or, worse, one nobody configured — and restarts an
agent that runs on upgrade. The settings file is not a file of the package
but written once by its script, with a generated token and mode `0600`: a
package manager must never have an opinion about a file that holds a
credential.

The packages are built from the compiled binary by `dpkg-deb` and
`rpmbuild` in containers (`scripts/build-packages.sh`), with no packaging
tool as a dependency of the repository and the two maintainer scripts shared
between both formats. The files travel in and out as a tar stream, so the
build needs no mount and runs the same on a developer's Windows machine.
