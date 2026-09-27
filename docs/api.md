# Agent HTTP API

Base URL: `http://<agent>:9000/api/v1` · JSON everywhere · wire types in [`pkg/api`](../pkg/api/types.go).

## Authentication

Every endpoint except `GET /health` requires

```http
Authorization: Bearer <token>
```

A token has a **role**, and every endpoint requires one. A role includes the
ones below it:

| Role | May |
|---|---|
| `read` | See everything: `GET /server`, `/applications…`, `/deployments…`, logs, events, metrics |
| `deploy` | And change what runs: `deploy`, `redeploy`, `rollback`, `stop`, `start` |
| `admin` | And everything else: `DELETE /applications/:name`, the `/tokens` endpoints, setting and removing `/secrets` |

There are two kinds of token. The **root token** is the one the agent is
configured with — `SHIPWICK_AGENT_TOKEN`, or the one generated on first start.
It has the `admin` role and the name `root`, is not stored in the database and
cannot be revoked through the API. Every other token is created with
[`POST /tokens`](#tokens), named, given a role, and shown exactly once.

The agent holds only the SHA-256 of each token and compares in constant time.
A wrong or revoked token is `401 UNAUTHORIZED`. After 20 failed
authentications within a minute from one client address, every request from
it is `429 RATE_LIMITED` for the next minute, with a `Retry-After` header in
seconds, before its token is looked at; successful authentications never
count, and `GET /health` is not limited. A valid token whose role does
not cover the endpoint is `403 FORBIDDEN`, and `details` says which role it has
and which the endpoint wants:

```json
{ "error": { "code": "FORBIDDEN",
             "message": "this token has the read role; deploying needs deploy or admin",
             "details": { "role": "read", "required": "deploy" } } }
```

`GET /server` tells a client who it is: `"token": {"name": "ci", "role": "deploy"}`.

## Envelope

Success:

```json
{ "data": … }
```

Failure — `details` is always an object:

```json
{
  "error": {
    "code": "INVALID_CONFIG",
    "message": "invalid deploy.yaml",
    "details": {
      "fields": [
        { "field": "resources.memory", "message": "invalid value \"abc\"", "expected": "128mb, 512mb, 1gb, ..." }
      ]
    }
  }
}
```

| HTTP | `code` | Meaning |
|---|---|---|
| 400 | `INVALID_REQUEST` | Malformed path or query parameter; body/URL name mismatch; invalid JSON, an unknown field or an invalid `image` in a redeploy or rollback body; such a body larger than 4 KB; a missing or invalid `command` in a run body; an image archive that is not sent as `application/x-tar`, or that does not carry exactly one image tagged `shipwick.local/<name>:<tag>`; a deploy.yaml with `build` but no `image`, or a redeploy of such an application with an image from elsewhere; a static upload that is not `application/x-tar`, holds no files, or holds anything but files and directories; a static `deploy` without `?static=`, or `?static=` on a container application |
| 400 | `INVALID_CONFIG` | deploy.yaml failed validation; `details.fields` lists **every** problem. Also returned when a hostname is already served by another application (as its domain, an alias or a redirect) or by the agent: one entry whose `field` names the offending line — `domain`, `aliases[0]`, `redirects[1]` — and whose `message` names the owner; and with field `publish[i].host` when a server port the configuration publishes is already published by another application, or is one the agent or the proxy listens on. By `redeploy` and `rollback` too, since a stored configuration's hostnames and ports may have been taken since. Also when an `env` value refers to a `${NAME}` that is not among the stored [secrets](#secrets): one entry per reference, `field` `env.<VARIABLE>`, `expected` the command that stores it |
| 401 | `UNAUTHORIZED` | Missing, wrong or revoked token |
| 403 | `FORBIDDEN` | The token's role does not cover this endpoint; `details: {role, required}` |
| 404 | `NOT_FOUND` | Unknown application, deployment or token, including a rollback's `deployment_id` that does not exist; a `deploy` whose `?static=` digest names no upload the agent has |
| 404 | `ENDPOINT_NOT_FOUND` | This agent has no such operation — an older agent, a typo in the path, or a method the path does not support (there is no `405`). Answered without checking the token |
| 409 | `DEPLOYMENT_IN_PROGRESS` | Another operation holds this application |
| 409 | `NOT_DEPLOYED` | The application has no active deployment to act on |
| 409 | `NO_ROLLBACK_TARGET` | No earlier successful deployment; or the requested one is active, never succeeded, or belongs to another application |
| 409 | `TOKEN_EXISTS` | A token with that name exists |
| 409 | `APPLICATION_RUNNING` | A volume restore was asked of an application that is not stopped |
| 409 | `JOB_ALREADY_RUNNING` | A run of this job has not finished yet; a job runs one at a time |
| 409 | `STATIC_APPLICATION` | Logs, metrics, jobs or a one-off command were asked of an application the proxy serves from a folder; it has no containers |
| 409 | `VOLUME_IN_USE` | The volume belongs to an application that still exists; `details: {application}`. Delete the application first, or replace the data with a restore |
| 429 | `RATE_LIMITED` | 20 authentications from this address failed within a minute; `Retry-After` says in how many seconds to try again. Answered without checking the token |
| 413 | `INVALID_REQUEST` | A `deploy` body (the deploy.yaml) larger than 64 KB; a volume archive larger than 10 GB; an image archive larger than 4 GB; a static folder larger than 512 MB |
| 503 | `RUNTIME_UNAVAILABLE` | Agent is shutting down |
| 500 | `INTERNAL_ERROR` | Anything else; `message` carries the cause |

## Endpoints

| Method | Path | Role | |
|---|---|---|---|
| `GET` | `/health` | — | Liveness. No token. `{status, version}` |
| `GET` | `/server` | read | Host facts: OS, Docker version, CPUs, memory, counts, `proxy: {enabled, reachable, error, routes}`, `notifications: {webhook}` and `token: {name, role}` — the caller's own |
| `GET` | `/applications` | read | Summaries of all applications |
| `GET` | `/applications/:name` | read | Detail: status, active spec (env masked), containers |
| `POST` | `/applications/:name/deploy` | deploy | Start a deployment → `202`. A static application adds `?static=<digest>`, see [Static folders](#static-folders) |
| `POST` | `/applications/:name/redeploy` | deploy | Deploy the active configuration again; optional body `{"image": "…"}` → `202` |
| `POST` | `/applications/:name/rollback` | deploy | Deploy the configuration of an earlier successful deployment; optional body `{"deployment_id": 12}`, default: the most recent one → `202` |
| `POST` | `/applications/:name/stop` | deploy | Stop all replicas; the app stays stopped |
| `POST` | `/applications/:name/start` | deploy | Start a stopped app |
| `DELETE` | `/applications/:name` | admin | Remove containers **and history** → `204` |
| `GET` | `/applications/:name/logs?tail=100` | read | Last lines across replicas, merged by time (max 5000) |
| `GET` | `/applications/:name/logs?follow=true&tail=100` | read | Live stream, see [Following logs](#following-logs) |
| `GET` | `/applications/:name/events?limit=50` | read | The application's own feed, newest first (max 500): crashes, restarts, health changes, stops and starts |
| `GET` | `/applications/:name/metrics` | read | Point-in-time CPU and memory of the application and each replica, see [Metrics](#metrics) |
| `GET` | `/applications/:name/metrics/history?since=1h` | read | CPU and memory of each replica over the last `1h`, `24h` or `7d`, see [Metrics history](#metrics-history) |
| `GET` | `/deployments?application=&limit=50` | read | History, newest first (max 500) |
| `GET` | `/deployments/:id` | read | One deployment with spec (env masked) and events |
| `GET` | `/tokens` | admin | The stored tokens, without their values, see [Tokens](#tokens) |
| `POST` | `/tokens` | admin | Create a token; the value is in this response and nowhere else → `201` |
| `DELETE` | `/tokens/:name` | admin | Revoke a token → `204` |
| `GET` | `/secrets` | read | The secrets stored on the server, names and dates only, see [Secrets](#secrets) |
| `PUT` | `/secrets/:name` | admin | Store a secret, creating or replacing it; body `{"value": "…"}` → `204` |
| `DELETE` | `/secrets/:name` | admin | Remove a secret → `204` |
| `GET` | `/applications/:name/volumes` | read | The volumes of the active deployment: `[{name, path}]` |
| `GET` | `/applications/:name/volumes/:volume/archive` | admin | The volume's contents as a tar archive, see [Volume backups](#volume-backups) |
| `PUT` | `/applications/:name/volumes/:volume/archive` | admin | Replace the volume's contents with the tar archive in the body → `204` |
| `POST` | `/applications/:name/images` | deploy | Load an image archive (`docker save` format) for the application, see [Images built by the CLI](#images-built-by-the-cli) → `201` |
| `GET` | `/volumes` | read | Every volume Shipwick created, with its application and size, see [Volumes of deleted applications](#volumes-of-deleted-applications) |
| `DELETE` | `/volumes/:name` | admin | Remove a volume whose application was deleted → `204`; `409 VOLUME_IN_USE` while the application exists |
| `GET` | `/applications/:name/jobs` | read | The scheduled jobs of the active deployment, each with its last and next run, see [Jobs and one-off commands](#jobs-and-one-off-commands) |
| `GET` | `/applications/:name/runs?job=&limit=50` | read | Runs of jobs, hooks and one-off commands, newest first (max 500), without output |
| `GET` | `/applications/:name/runs/:id` | read | One run with its output |
| `POST` | `/applications/:name/jobs/:job/run` | deploy | Start a scheduled job now → `202` |
| `POST` | `/applications/:name/run` | deploy | Run a one-off command; body `{"command": ["…"]}` → `202` |
| `PUT` | `/applications/:name/static` | deploy | Upload the folder of a static application as a tar archive → `{digest, size_bytes, files}`, see [Static folders](#static-folders) |

### Deploying

The body **is** the deploy.yaml document. JSON is accepted as well.

`${NAME}` placeholders in `env` values are filled in by the agent from the
[secrets](#secrets) stored on the server, before the deployment is recorded;
the record holds the values, so a later change of a secret does not reach a
redeploy or rollback of it. `$${NAME}` there is a literal `${NAME}`. A
placeholder anywhere else is a convention of the CLI, which fills it in before
sending, and is stored literally here.

```bash
curl -X POST http://localhost:9000/api/v1/applications/my-api/deploy \
  -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" \
  --data-binary @deploy.yaml
```

`202 Accepted`, with `Location: /api/v1/deployments/7`:

```json
{
  "data": {
    "id": 7, "application": "my-api", "sequence": 3,
    "version": "1.4.2", "image": "ghcr.io/company/my-api:1.4.2",
    "status": "PENDING", "error": "",
    "started_at": "2026-03-01T10:00:00Z", "completed_at": null
  }
}
```

The deployment runs in the background. **Poll `GET /deployments/7` until
`completed_at` is non-null**, then read `status` and `error`: `ACTIVE` is the
only success. `FAILED` means nothing of the previous version was lost;
`ROLLED_BACK` means part of it had already been replaced and was restored —
`error` says why the deployment failed in both cases. Do not stop at the first `ACTIVE`: the engine may still be retiring the
old version, and a new operation would get `409` until `completed_at` appears.

`events` narrate progress; `type: "step"` entries are meant to be shown to users:

```text
state  BUILDING
step   Pulled image ghcr.io/company/my-api:1.4.2
state  STARTING
step   Started 1 container
state  HEALTH_CHECKING
step   Replica 1 passed health checks        (or: "running and stable" without a health block)
step   Replica 1/2 is serving 1.4.2; its 1.4.1 predecessor is retired
step   Replica 2 passed health checks
step   Replica 2/2 is serving 1.4.2; its 1.4.1 predecessor is retired
state  HEALTHY
step   Routed https://api.example.com to 2 replicas   (only with a domain)
state  ACTIVE
step   Deployment successful
```

On failure there is a `state` event `FAILED: <reason>` and, if a replica
crashed, a `log` event holding its last 20 lines of output. A rollback adds
`state` events `ROLLBACK`, `RESTORING`, `ROLLED_BACK` and steps narrating it.

### Redeploy and rollback

Both answer exactly like `deploy` — `202`, a `Deployment`, a `Location` to poll
— because both *are* deployments. They differ only in where the configuration
comes from: the server's own record of an earlier deployment. That is also why
they exist as endpoints at all: the API only ever returns configurations with
env values masked, so no client could re-submit one.

```bash
curl -X POST …/applications/my-api/redeploy -d '{"image": "ghcr.io/company/my-api:1.5.0"}'
curl -X POST …/applications/my-api/rollback                      # previous successful version
curl -X POST …/applications/my-api/rollback -d '{"deployment_id": 12}'
```

The body is optional; unknown fields are rejected (a typo such as `"imgae"`
must not quietly redeploy the old image). Every `Deployment` carries
`kind` — `deploy`, `redeploy` or `rollback` — and, for the latter two,
`source_deployment_id`: the deployment whose configuration it re-used. It also
carries `by`: the name of the token that started it (`"root"` for the agent's
own token); the field is absent on deployments made before tokens had names.

A rollback's `deployment_id` is the deployment's `id`, not its per-application
`sequence`. Omitted or `0`, the target is the most recent `SUPERSEDED`
deployment. Only `SUPERSEDED` deployments of the same application are valid
targets — they are the ones that once served successfully: anything else is
`409 NO_ROLLBACK_TARGET`, an id that does not exist is `404 NOT_FOUND`, and a
negative one is `400 INVALID_REQUEST`.

### Following logs

`GET /applications/:name/logs?follow=true` answers with
`Content-Type: application/x-ndjson`: one [`LogLine`](../pkg/api/types.go)
object per line, **without** the `data` envelope, flushed as lines arrive.

```json
{"replica":1,"container":"shipwick_my-api_7_1","stream":"stdout","time":"2026-03-01T10:00:00.12Z","message":"listening on :8080"}
{"replica":2,"container":"shipwick_my-api_7_2","stream":"stderr","time":"2026-03-01T10:00:00.31Z","message":"cache warm"}
```

- Each replica starts with its last `tail` lines; `tail=0` means "only what is
  logged from now on" (valid only when following).
- Lines of one replica arrive in order; replicas interleave as output arrives.
- Problems found before streaming starts (unknown app, nothing deployed, bad
  parameters) are regular JSON errors with a non-200 status.
- The stream ends when the client disconnects, when the agent shuts down, or
  when every followed container is gone — which is what a new deployment does.
  Reconnect to follow the new containers.

### Volume backups

The two `archive` endpoints are the other exception to the envelope: the body
**is** the tar archive, both ways.

`GET /applications/:name/volumes/:volume/archive` answers with
`Content-Type: application/x-tar` and
`Content-Disposition: attachment; filename="<app>-<volume>-<UTC yyyymmdd-hhmmss>.tar"`,
streamed as it is read from the container. The entries are the volume's
contents relative to its mount point (`base/…`, not `data/base/…`). The
container may be stopped or running; the copy of a database that is being
written to is not guaranteed consistent. The application is locked while the
archive streams, so a deployment asked for meanwhile is `409
DEPLOYMENT_IN_PROGRESS`. Problems found before the first byte (unknown
application or volume, busy) are regular JSON errors; after that, a cut
connection is the only signal left.

`PUT /applications/:name/volumes/:volume/archive` takes the archive as the body
with `Content-Type: application/x-tar`, up to 10 GB, and replaces the volume's
contents with it: the replica container is removed, the volume with it, and
both are created again before the archive is extracted into the mount point —
what the archive does not name is gone. The application must be stopped
(`409 APPLICATION_RUNNING` otherwise) and stays stopped; `POST …/start`
brings it back. A body that does not start with a tar header is `400
INVALID_REQUEST`, and nothing has been touched. Success is `204` with an
application event `Volume data restored from a backup (12.5 MB)`.

```bash
curl -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" -OJ \
  http://localhost:9000/api/v1/applications/postgres/volumes/data/archive
curl -X PUT -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" -H "Content-Type: application/x-tar" \
  --data-binary @postgres-data-20260927-153000.tar \
  http://localhost:9000/api/v1/applications/postgres/volumes/data/archive
```

Listing volumes needs the `read` role; both archive endpoints need `admin`: a
backup carries the application's data, a restore replaces it.

### Images built by the CLI

An application with `build:` in its deploy.yaml has its image built where
`shipwick deploy` runs and sent here; the agent never builds. The CLI does
three things, and any client may do the same:

1. `docker build --platform <the server's> -t shipwick.local/<name>:<tag> …`,
   where `<tag>` is `<UTC yyyymmdd-hhmmss>-<4 hex>` and the platform follows
   `architecture` from `GET /server`.
2. `POST /applications/:name/images` with the output of `docker save` as the
   body, `Content-Type: application/x-tar`, up to 4 GB, chunked or with a
   `Content-Length`. The archive must carry exactly one image, tagged
   `shipwick.local/<name>:<tag>` for this application; anything else is `400
   INVALID_REQUEST` and what was loaded is removed again. Loading takes no
   application lock. `201`:

   ```json
   {"data": {"image": "shipwick.local/my-api:20260927-153000-a1b2", "size_bytes": 267078442}}
   ```

3. `POST /applications/:name/deploy` with `image` set to that reference. A
   `build` document without an `image` is `400 INVALID_REQUEST`.

`shipwick.local` is a host that does not exist: the agent never pulls such an
image, and a deployment whose local image is not on the server — pruned, or a
rollback to a version this server was never sent — fails with `… is not on
this server; it was built on a developer's machine — run shipwick deploy from
the project again`. Local images are pruned like any other. A redeploy of
such an application with an `image` from elsewhere is `400 INVALID_REQUEST`;
without one, it keeps the image it has.

```bash
docker save shipwick.local/my-api:20260927-153000-a1b2 \
  | curl -X POST -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" -H "Content-Type: application/x-tar" \
      --data-binary @- http://localhost:9000/api/v1/applications/my-api/images
```

### Static folders

A static application (`static: dist/` in deploy.yaml) is a folder the proxy
serves itself; the agent creates no container for it. Deploying one is two
requests.

`PUT /applications/:name/static` takes the folder as a tar archive with
`Content-Type: application/x-tar`, up to 512 MB: files and directories only,
paths relative to the folder, nothing outside it — a symbolic link is refused,
since the proxy's file server would follow it. The agent keeps the archive
under its digest, one per application (a new upload replaces the last), and
answers:

```json
{ "data": { "digest": "sha256:3f2a…", "size_bytes": 3250000, "files": 42 } }
```

`POST /applications/:name/deploy?static=sha256:3f2a…` then deploys the
deploy.yaml in the body for that upload, and answers like any deployment. The
`Deployment` has no `image`; its `version` is the digest's first twelve hex
characters, and it carries `static: {digest, size_bytes, files}`. Its events
read `Received 42 files (3.1 MB)`, `Copied 42 files into the proxy`, `Found
index.html`, `Routed https://example.com to the uploaded files`. A folder
without an `index.html` fails the deployment: `FAILED: the folder has no
index.html…`. A digest the agent has no upload for is `404 NOT_FOUND` before
anything is recorded; a static deploy.yaml without `?static=` is `400
INVALID_REQUEST`.

Redeploy and rollback need no upload: the proxy keeps the folder of the
serving version and of the one before it, and both re-route to a kept folder.
A rollback to a version whose folder is gone fails with `the files of <version>
are no longer on the server: deploy the folder again`.

```bash
tar -C dist -cf - . | curl -X PUT -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" \
  -H "Content-Type: application/x-tar" --data-binary @- \
  http://localhost:9000/api/v1/applications/web/static
curl -X POST -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" --data-binary @deploy.yaml \
  "http://localhost:9000/api/v1/applications/web/deploy?static=sha256:3f2a…"
```

In the application views `static` is `true` and `replicas` is all zeros; the
status is `HEALTHY` while the deployment is active and `STOPPED` after `stop`,
which makes the domain answer `503` until `start`. Logs, metrics, jobs,
`run` and metrics history answer `409 STATIC_APPLICATION`; `volumes` is an
empty list.
### Volumes of deleted applications

`DELETE /applications/:name` keeps the application's volumes. `GET /volumes`
lists every volume Shipwick created on the server, by its Docker name, with
the application it was created for and whether that application still exists:

```json
{ "data": [
  { "name": "shipwick_pgtest_data", "application": "pgtest", "volume": "data",
    "size_bytes": 13631488, "orphan": true },
  { "name": "shipwick_postgres_data", "application": "postgres", "volume": "data",
    "size_bytes": 2684354560, "orphan": false }
] }
```

`size_bytes` is what the daemon's disk-usage report says, `-1` when it reports
nothing for the volume. `DELETE /volumes/:name` removes a volume of a deleted
application with everything in it → `204`. While the application exists the
answer is `409 VOLUME_IN_USE` with `details.application`: its data belongs to
it, and a restore is the way to replace it. A name that is not
`shipwick_<application>_<volume>` is `400 INVALID_REQUEST`; a well-formed name
that no volume carries is `404 NOT_FOUND`.

### Application status

| `status` | Meaning |
|---|---|
| `HEALTHY` | All desired replicas of the active deployment are healthy |
| `DEGRADED` | Some are |
| `DOWN` | None are — not running, or running but failing their health check |
| `CRASH_LOOP` | A replica keeps dying or never turns healthy; its restarts are now rate-limited to one every 5 minutes. Outranks `DEGRADED`/`DOWN`: it says restarting is not helping |
| `STOPPED` | Stopped on request (`desired_state: "stopped"`) |
| `DEPLOYING` | First deployment in flight, nothing active yet |
| `FAILED` | No deployment has ever succeeded |

`deploying: true` is set whenever a deployment is in flight — an app can be
`HEALTHY` (old version serving) and `deploying` at once — and
`in_flight_deployment_id` then names the deployment to follow.

`replicas` is `{desired, running, healthy}`. A replica is **healthy** when it
runs and is not failing its health check; without a `health` block, `healthy`
equals `running`. For a static application (`static: true`) all three are
zero: the proxy serves it, and there is nothing to count. **During a rollout** the numbers describe the replicas that
are serving right now — a mix of the old and the new version — and `desired` is
the capacity the rollout maintains (the smaller of the two replica counts), so
an application being upgraded reads `HEALTHY`, not `DEGRADED`. `containers`
lists every container of the application, of both versions, each with its
`deployment_id`.

Each entry of `containers` in the application detail carries what the
supervisor knows about it:

| Field | |
|---|---|
| `health` | `""` no health check configured · `unknown` not probed yet, e.g. right after an agent restart (counts as healthy) · `starting` (re)started, within its startup budget · `healthy` · `unhealthy` |
| `restarts` | Restarts performed by the supervisor over the container's lifetime |
| `crash_loop` | Restarts of this replica are currently rate-limited |

### Events

Deployment events (`GET /deployments/:id`) narrate one deployment and never
change afterwards. Application events (`GET /applications/:name/events`) are
the running commentary of the supervisor and of stop/start:

```json
{ "id": 91, "deployment_id": null, "level": "warn", "type": "supervisor",
  "message": "Replica 2 exited with code 137; restarting in 2s", "created_at": "2026-03-01T10:00:00Z" }
```

`type` is `supervisor`, `app` or `job` (a scheduled job or one-off command
that failed or timed out); `level` is `info`, `warn` or `error`. Only the
newest 500 per application are kept. A stop or start made with a token other
than root names it: `Application stopped by ci`.

### Tokens

```bash
curl -X POST …/tokens -d '{"name": "ci", "role": "deploy"}'
```

`201 Created`:

```json
{
  "data": {
    "id": 3, "name": "ci", "role": "deploy", "created_at": "2026-03-01T10:00:00Z",
    "token": "swk_Xk3nM9…"
  }
}
```

`token` is shown here and never again: the agent stores its SHA-256 and
nothing else. Values are 32 random bytes in unpadded base64url behind the
prefix `swk_`, which is not part of the secret; it exists so that a token is
recognisable where it must not be — a log, a repository.

Names match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`, are unique
(`409 TOKEN_EXISTS`), and `root` is taken. `role` is `read`, `deploy` or
`admin`. Unknown fields are rejected.

`GET /tokens` lists them, oldest first, without the value or the hash:

```json
{ "data": [
  { "id": 3, "name": "ci", "role": "deploy", "created_at": "2026-03-01T10:00:00Z",
    "last_used_at": "2026-03-01T10:42:00Z" }
] }
```

`last_used_at` is `null` until the token is first used, and is then kept to
the minute: it says whether a token is still in use, not what it did last.
The root token is not listed: it is configured on the agent, not stored.

`DELETE /tokens/ci` → `204`; requests with the token are `401` from then on.
`DELETE /tokens/root` is `400`. A token may revoke itself.

### Secrets

A secret is a value for `${NAME}` in the `env` values of a deploy.yaml, kept
on the server so that no client has to hold it.

```bash
curl -X PUT …/secrets/DATABASE_PASSWORD -d '{"value": "hunter2"}'
```

`204 No Content`, whether the secret was created or replaced. Names match
`^[A-Za-z_][A-Za-z0-9_]*$` (at most 64 characters: they are environment
variable names). The value is at most 64 KB, not empty, without NUL bytes; the
body is at most 260 KB; unknown fields are rejected. At most 500 secrets are
stored; the 501st is `400 INVALID_REQUEST`. The value is written encrypted
(AES-256-GCM, the name as additional data, like an `env` value) and is never
returned, logged or repeated in an error.

`GET /secrets` lists them by name, without values:

```json
{ "data": [
  { "name": "DATABASE_PASSWORD", "created_at": "2026-03-01T10:00:00Z",
    "updated_at": "2026-03-01T10:42:00Z" }
] }
```

`DELETE /secrets/DATABASE_PASSWORD` → `204`; `404 NOT_FOUND` for a name that
is not stored. Deployments already made keep the value they were started
with; the next `deploy` whose `env` refers to the name is refused:

```json
{ "error": { "code": "INVALID_CONFIG", "message": "invalid deploy.yaml",
             "details": { "fields": [ {
               "field": "env.DATABASE_URL",
               "message": "refers to ${DATABASE_PASSWORD}, which is not set where shipwick runs and not stored on the server",
               "expected": "shipwick secret set DATABASE_PASSWORD" } ] } } }
```

### Metrics

```json
{
  "data": {
    "application": "my-api", "collected_at": "2026-03-01T10:00:00Z",
    "cpu_percent": 42.1, "cpu_limit_percent": 400,
    "memory_bytes": 432013312, "memory_limit_bytes": 2147483648,
    "replicas": [
      { "replica": 1, "container": "shipwick_my-api_7_1",
        "cpu_percent": 21.0, "cpu_limit_percent": 200,
        "memory_bytes": 216006656, "memory_limit_bytes": 1073741824 }
    ]
  }
}
```

- **CPU is in percent of one core** (as in `docker stats`): a replica keeping two
  cores busy reads 200, and `cpu: 2` is a `cpu_limit_percent` of 200.
- **Memory is the working set**: usage minus the page cache the kernel would
  give back — what the limit is enforced against.
- Limits are `0` when unlimited. Application-level numbers are sums over replicas.
- A replica that is not running reports zeros; it never fails the request.
  An application with no active deployment answers `409 NOT_DEPLOYED`.
- It is a **point-in-time sample**; the history is below. The agent keeps
  the previous sample of each container, so polling every few seconds is
  answered instantly. A first request (or one after a minute's pause) takes
  about a second: CPU usage is a rate, and Docker needs two readings for it.

### Metrics history

`GET /applications/:name/metrics/history?since=1h`

```json
{
  "data": {
    "application": "my-api", "since": "2026-03-01T09:00:00Z", "step": "30s",
    "series": [
      { "replica": 1, "points": [
          { "at": "2026-03-01T09:00:00Z", "cpu_percent": 12.5, "memory_bytes": 216006656 },
          { "at": "2026-03-01T09:00:30Z", "cpu_percent": 14.0, "memory_bytes": 216268800 }
      ] },
      { "replica": 2, "points": [ … ] }
    ],
    "limits": { "cpu": 1, "memory_bytes": 1073741824 }
  }
}
```

- `since` is `1h` (the default), `24h` or `7d`; anything else is
  `400 INVALID_REQUEST`. The agent picks the `step`: `30s`, `5m` and `1h`
  respectively, so a series is at most a few hundred points.
- The agent samples every running replica every 30 seconds and keeps seven
  days. Each point aggregates the samples of one step: `cpu_percent` is their
  average, `memory_bytes` their peak. A step in which a replica has no sample
  (it was not running, the agent was down) has no point: the series are
  sparse, not zero-filled. `at` is the start of the step.
- `limits` are the active deployment's per-replica limits as written in
  deploy.yaml — `cpu` in cores, `memory_bytes` in bytes, `0` when unlimited.
  A limit line for the CPU chart is `cpu × 100`.
- Units are those of [Metrics](#metrics). An application with no active
  deployment answers `409 NOT_DEPLOYED`; one that was just deployed answers
  with empty series until the first minute has passed.

### Jobs and one-off commands

A *run* is one execution of a one-off container from the application's image,
with its environment and limits and without its volumes: the pre-deploy hook
of a deployment (`kind: hook`, job `pre-deploy`), a scheduled job (`kind:
scheduled`) or a command started by hand (`kind: manual`; job `run` for a
command, or the job's name).

```json
{ "id": 42, "application": "my-api", "job": "nightly-report", "kind": "scheduled",
  "command": ["node", "report.js"], "status": "failed", "exit_code": 1, "deployment_id": 7,
  "started_at": "2026-03-01T03:00:00Z", "finished_at": "2026-03-01T03:00:12Z" }
```

`status` is `running`, `succeeded` (exit 0), `failed` (any other exit code, or
`exit_code: null` when the container could not be started), `timed_out`
(stopped when its timeout ran out) or `interrupted` (the agent was restarted
while it ran). `GET /applications/:name/runs/:id` adds `output`: the last 200
lines the container wrote, at most 64 KB, joined with newlines. The listing
leaves it out. The last 50 runs of each job are kept.

`GET /applications/:name/jobs` describes the jobs of the active deployment:

```json
{ "name": "nightly-report", "schedule": "0 3 * * *", "command": ["node", "report.js"], "timeout": "1h0m0s",
  "last_run": { "id": 42, "status": "failed", "exit_code": 1, "…": "a Run, or null" },
  "next_run_at": "2026-03-02T03:00:00Z" }
```

Schedules are read in UTC and `next_run_at` is UTC; it is `null` while the
application is stopped, since a stopped application runs no jobs. Without an
active deployment the endpoint answers `409 NOT_DEPLOYED`.

Both `POST`s answer `202` with the run (`status: "running"`) and a `Location`
to poll until `finished_at` is set. `/jobs/:job/run` refuses with
`409 JOB_ALREADY_RUNNING` while an earlier run of that job is still going — the
schedule skips such firings too; one-off commands are independent of one
another. The `command` of a run body is validated like one in deploy.yaml:
a non-empty array of at most 256 strings, each at most 4 KB, and never a
shell string. A failed or timed-out run adds an event of `type: "job"` to the
application's feed; a successful one records nothing.

### Log tail semantics

Without `follow`, `tail=N` is the merged total: the last N lines across all
replicas. With `follow=true` it applies **per replica**: each stream starts with
its own last N lines, so two replicas and `tail=100` begin with up to 200.
