# Agent HTTP API

Base URL: `http://<agent>:9000/api/v1` · JSON everywhere · wire types in [`pkg/api`](../pkg/api/types.go).

## Authentication

Every endpoint except `GET /health` requires

```http
Authorization: Bearer <SHIPWICK_AGENT_TOKEN>
```

The agent holds only the SHA-256 of the token and compares in constant time.

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
| 400 | `INVALID_REQUEST` | Malformed path or query parameter, body/URL name mismatch |
| 400 | `INVALID_CONFIG` | deploy.yaml failed validation; `details.fields` lists **every** problem. Also returned, with field `domain`, when the domain is already served by another application or by the agent |
| 401 | `UNAUTHORIZED` | Missing or wrong token |
| 404 | `NOT_FOUND` | Unknown application or deployment |
| 404 | `ENDPOINT_NOT_FOUND` | This agent has no such operation — an older agent, or a typo in the path |
| 409 | `DEPLOYMENT_IN_PROGRESS` | Another operation holds this application |
| 409 | `NOT_DEPLOYED` | The application has no active deployment to act on |
| 409 | `NO_ROLLBACK_TARGET` | No earlier successful deployment; or the requested one is active, never succeeded, or belongs to another application |
| 413 | `INVALID_REQUEST` | Body larger than 64 KB |
| 503 | `RUNTIME_UNAVAILABLE` | Agent is shutting down |
| 500 | `INTERNAL_ERROR` | Anything else; `message` carries the cause |

## Endpoints

| Method | Path | |
|---|---|---|
| `GET` | `/health` | Liveness. No token. `{status, version}` |
| `GET` | `/server` | Host facts: OS, Docker version, CPUs, memory, counts, and `proxy: {enabled, reachable, error, routes}` |
| `GET` | `/applications` | Summaries of all applications |
| `GET` | `/applications/:name` | Detail: status, active spec (env masked), containers |
| `POST` | `/applications/:name/deploy` | Start a deployment → `202` |
| `POST` | `/applications/:name/redeploy` | Deploy the active configuration again; optional body `{"image": "…"}` → `202` |
| `POST` | `/applications/:name/rollback` | Deploy the configuration of an earlier successful deployment; optional body `{"deployment_id": 12}`, default: the most recent one → `202` |
| `POST` | `/applications/:name/stop` | Stop all replicas; the app stays stopped |
| `POST` | `/applications/:name/start` | Start a stopped app |
| `DELETE` | `/applications/:name` | Remove containers **and history** → `204` |
| `GET` | `/applications/:name/logs?tail=100` | Last lines across replicas, merged by time (max 5000) |
| `GET` | `/applications/:name/logs?follow=true&tail=100` | Live stream, see [Following logs](#following-logs) |
| `GET` | `/applications/:name/events?limit=50` | The application's own feed, newest first (max 500): crashes, restarts, health changes, stops and starts |
| `GET` | `/applications/:name/metrics` | Point-in-time CPU and memory of the application and each replica, see [Metrics](#metrics) |
| `GET` | `/deployments?application=&limit=50` | History, newest first (max 500) |
| `GET` | `/deployments/:id` | One deployment with spec (env masked) and events |

### Deploying

The body **is** the deploy.yaml document. JSON is accepted as well.

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
`source_deployment_id`: the deployment whose configuration it re-used.

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
equals `running`. **During a rollout** the numbers describe the replicas that
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

`type` is `supervisor` or `app`; `level` is `info`, `warn` or `error`. Only the
newest 500 per application are kept.

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
- It is a **point-in-time sample**; build history by polling. The agent keeps
  the previous sample of each container, so polling every few seconds is
  answered instantly. A first request (or one after a minute's pause) takes
  about a second: CPU usage is a rate, and Docker needs two readings for it.

### Log tail semantics

Without `follow`, `tail=N` is the merged total: the last N lines across all
replicas. With `follow=true` it applies **per replica**: each stream starts with
its own last N lines, so two replicas and `tail=100` begin with up to 200.
