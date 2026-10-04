# Agent HTTP API

Base URL: `http://<agent>:9000/api/v1` · JSON everywhere · wire types in [`pkg/api`](../pkg/api/types.go).

## Authentication

Every endpoint except `GET /health`, `GET /auth` and `POST /auth/exchange`
requires

```http
Authorization: Bearer <token>
```

A token has a **role**, and every endpoint requires one. A role includes the
ones below it:

| Role | May |
|---|---|
| `read` | See everything: `GET /server`, `/applications…`, `/deployments…`, logs, the log archive and its search, events, metrics, traffic and requests, jobs and their runs, volumes, the backups of an application, `/registries`, `/certificates`, `/standby` and `/standby/promotion`, `GET /metrics`, the names of `/secrets`; `DELETE /auth/session`, which ends the caller's own session |
| `deploy` | And change what runs: `deploy`, `validate`, `redeploy`, `rollback`, `stop`, `start`, `run`, starting a job, sending an image or a static folder, taking and verifying a backup; `POST /applications` and `POST /validate`, which take a document that names its application; and `GET /applications/:name/config`, the document of what runs |
| `admin` | And everything else: `DELETE /applications/:name`, the `/tokens` endpoints, setting and removing `/secrets`, `/registries` and `/certificates`, rotating the key, restoring, downloading and removing a backup, the backups of the agent's state, adopting backups, `/export`, `/import`, `/exports`, pulling to and promoting a standby, `GET /audit`, `GET /audit/export`, the `/access` endpoints |

A `deploy` token may be **limited to applications**. It then has the `deploy`
row for the applications it names and the `read` row for everything. An
endpoint of the `deploy` row is under `/applications/:name`, and `:name` is
what is checked — also when a `deploy` would create the application — or it
takes a deploy.yaml as its body (`POST /applications`, `POST /validate`), and
the `name` in that document is what is checked, before anything else of the
document is read. For another application the answer is `403 TOKEN_LIMITED`:

```json
{ "error": { "code": "TOKEN_LIMITED",
             "message": "this token is limited to my-api and web; it can read worker and not change it",
             "details": { "applications": ["my-api", "web"], "application": "worker" } } }
```

An endpoint that takes `deploy` and is not about one application is refused
to a limited token in the same way, without `application` in the details;
there is none today. So is a document without a name that can be read — not
YAML, no `name`, not a valid name — with the message `… and the document does
not name an application`; a token that is not limited gets the
`400 INVALID_CONFIG` of the document instead. The role is judged first: what takes `admin` is
`403 FORBIDDEN` for a limited token as for any other `deploy` token. `read`
and `admin` tokens cannot be limited.

There are two kinds of token. The **root token** is the one the agent is
configured with — `SHIPWICK_AGENT_TOKEN`, or the one generated on first start.
It has the `admin` role and the name `root`, is not stored in the database and
cannot be revoked through the API. Every other token is created with
[`POST /tokens`](#tokens), named, given a role, optionally a list of
applications and a time at which it expires, and shown exactly once.

The agent holds only the SHA-256 of each token and compares in constant time.
A wrong or revoked token is `401 UNAUTHORIZED`. After 20 failed
authentications within a minute from one client address, its further wrong
tokens are `429 RATE_LIMITED` for the next minute, with a `Retry-After` header
in seconds. A valid token is never refused — behind the proxy every client
shares one address — successful authentications never count, and `GET /health`
is not limited.

The API answers the proxy, the dashboard and the server itself. A request
that comes from the container of an application is `403 APPLICATION_CALLER`
on every endpoint, `GET /health` included, whatever token it carries; the
token is not looked at, and the request is not counted as a failed
authentication:

```json
{ "error": { "code": "APPLICATION_CALLER",
             "message": "the API answers the proxy, the dashboard and the server itself, not the containers of applications; from a container, call it at its hostname",
             "details": {} } }
```

Which requests those are depends on how the agent is installed (handbook,
§12, "Who can reach the API"). Requests from the server itself, over
loopback, are always answered. On the address the agent has on the control
network, every address that is not of that network is refused. On any other
address it listens on, the addresses of the containers the agent manages are
refused, and with the control arrangement in place every address of the two
application networks as well. Where applications can still reach the API —
the agent listens on a network they are on — `GET /server` carries
`open_to_applications: true`. A
request that reaches the API through the proxy, at the API's hostname, comes
from the proxy and is answered like any other.

A valid token whose role does
not cover the endpoint is `403 FORBIDDEN`, and `details` says which role it has
and which the endpoint wants:

```json
{ "error": { "code": "FORBIDDEN",
             "message": "this token has the read role; deploying needs deploy or admin",
             "details": { "role": "read", "required": "deploy" } } }
```

A token past its `expires_at` is `401 TOKEN_EXPIRED`:

```json
{ "error": { "code": "TOKEN_EXPIRED",
             "message": "token ci expired on 2026-10-02 at 12:00 UTC; an admin creates a new one with: shipwick token create",
             "details": { "name": "ci", "expired_at": "2026-10-02T12:00:00Z" } } }
```

Only the token itself gets this answer, so it tells nothing to anyone who does
not hold it; a wrong token is `401 UNAUTHORIZED` whatever else exists. An
expired token does not count towards the rate limit and is never answered
`429`. The root token does not expire.

`GET /server` tells a client who it is and what it may do:

```json
"token": { "name": "ci", "role": "deploy", "kind": "token",
           "applications": ["my-api"], "expires_at": "2027-01-01T00:00:00Z" }
```

`kind` is `"token"`, or `"user"` for a person who [signed in](#signing-in):
`name` is then the e-mail address, and what follows is said of the session.
`applications` is the list a token is limited to, and empty for one that is not limited;
`expires_at` is `null` for one that does not expire. From these a client
knows what to offer: what takes `deploy` on an application is allowed when
`role` is `admin`, or when `role` is `deploy` and `applications` is empty or
names that application. An agent before 0.6 sends `name` and `role` only.

Next to it, `"sign_in": {"configured": true, "issuer": "https://accounts.example.com", "name_claim": "email"}`
says whether people can sign in at all; an agent before 0.6 leaves it out.
`name_claim` is the ID token claim people are named by, `""` without a
provider; an agent before 0.7 leaves it out, and names people by `email`.
A session that is refused for its role or its limit gets the same codes as a
token, `FORBIDDEN` and `TOKEN_LIMITED`, with "this session" in the message.

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
| 400 | `INVALID_CONFIG` | deploy.yaml failed validation; `details.fields` lists **every** problem. Also returned when a hostname is already served by another application (as its domain, an alias or a redirect) or by the agent: one entry whose `field` names the offending line — `domain`, `aliases[0]`, `redirects[1]` — and whose `message` names the owner; and with field `publish[i].host` when a server port the configuration publishes is already published by another application, or is one the agent or the proxy listens on. By `redeploy` and `rollback` too, since a stored configuration's hostnames and ports may have been taken since. Also when an `env` value refers to a `${NAME}` that is not among the stored [secrets](#secrets): one entry per reference, `field` `env.<VARIABLE>`, `expected` the command that stores it. See [Paths and the proxy block](#paths-and-the-proxy-block) for what `path` and `proxy` add |
| 400 | `REGISTRY_LOGIN_FAILED` | A registry did not accept the credential of a `PUT /registries/:registry`, or could not be asked; `details: {registry, refused}`. Nothing was stored |
| 400 | `INVALID_CONFIG` | Also for a wildcard hostname (`*.example.com` as `domain` or an alias) that no certificate can be had for: the agent has no DNS challenge configured and no supplied [certificate](#certificates) covers it. One entry on the field that names it; `expected` says both ways out |
| 400 | `INVALID_CERTIFICATE` | The certificate or key in a `PUT /certificates/:hostname` cannot serve that hostname; `message` is a sentence that says why, see [Certificates](#certificates) |
| 401 | `UNAUTHORIZED` | Missing, wrong or revoked token |
| 401 | `TOKEN_EXPIRED` | The token is a real one, past its `expires_at`; `details: {name, expired_at}` |
| 401 | `SESSION_EXPIRED` | The session of a person who signed in is past its ten hours; `details: {name, expired_at}` |
| 401 | `SESSION_ENDED` | The session was ended before its time: its rule was removed or changed, or an admin signed the person out; `details: {name, reason}`, see [Signing in](#signing-in) |
| 401 | `SIGN_IN_FAILED` | A sign-in the provider or the ID token did not carry; `details: {reason}` |
| 403 | `FORBIDDEN` | The token's role does not cover this endpoint; `details: {role, required}` |
| 403 | `TOKEN_LIMITED` | The role would do, and the token is limited to other applications; `details: {applications}`, and `application` when the request was about one |
| 403 | `ACCESS_NOT_GRANTED` | A person signed in whom no [access rule](#access-rules) gives a role; `details: {email}` |
| 403 | `APPLICATION_CALLER` | The request came from the container of an application; the API answers the proxy, the dashboard and the server itself. Answered without checking the token, see [Authentication](#authentication) |
| 404 | `NOT_FOUND` | Unknown application, deployment or token, including a rollback's `deployment_id` that does not exist; a `deploy` whose `?static=` digest names no upload the agent has |
| 404 | `ENDPOINT_NOT_FOUND` | This agent has no such operation — an older agent, a typo in the path, or a method the path does not support (there is no `405`). Answered without checking the token |
| 409 | `DEPLOYMENT_IN_PROGRESS` | Another operation holds this application |
| 409 | `NOT_DEPLOYED` | The application has no active deployment to act on |
| 409 | `NO_ROLLBACK_TARGET` | No earlier successful deployment; or the requested one is active, never succeeded, or belongs to another application |
| 409 | `TOKEN_EXISTS` | A token with that name exists |
| 409 | `APPLICATION_RUNNING` | A volume restore was asked of an application that is not stopped |
| 409 | `JOB_ALREADY_RUNNING` | A run of this job has not finished yet; a job runs one at a time |
| 409 | `STATIC_APPLICATION` | Logs, metrics, jobs or a one-off command were asked of an application the proxy serves from a folder; it has no containers |
| 409 | `IMAGE_INCOMPLETE` | An image archive left out layers the server does not have; nothing was loaded. Send the whole archive |
| 409 | `VOLUME_IN_USE` | The volume belongs to an application that still exists; `details: {application}`. Delete the application first, or replace the data with a restore |
| 409 | `KEY_ROTATION_PENDING` | The encryption key was already rotated since the agent started, and the agent's environment still holds the old one; `details: {key_file}` |
| 409 | `TRAFFIC_UNAVAILABLE` | Traffic or requests were asked of an agent that has no access log to read: no proxy, or one that is not the `caddy` container of its compose project |
| 409 | `BACKUP_BUSY` | The backup is still being taken, verified, restored or removed; or a backup of the agent's state is already running |
| 409 | `BACKUP_NOT_USABLE` | A verification, a restore or a download was asked of a backup that failed, so nothing was kept of it; or of one whose files do not decrypt with the agent's passphrase. Also when backups or exports are asked of an agent that has no backup destination |
| 409 | `NO_VOLUMES` | A backup was asked of an application without volumes |
| 409 | `FOREIGN_BUCKET` | Backups were to be adopted from a bucket that, under the agent's prefix, another installation writes to |
| 400 | `INVALID_EXPORT` | The body of an import is not an export, its passphrase does not match, or it breaks off |
| 409 | `IMPORT_IN_PROGRESS` | An import is running; a server takes one at a time |
| 409 | `EXPORT_IN_PROGRESS` | An export to where backups go is being written already |
| 409 | `STANDBY_NOT_CONFIGURED` | An import from the bucket was asked of an agent that has no bucket to read exports from |
| 409 | `PROMOTION_IN_PROGRESS` | A promotion is running: a second one was asked for, or an import. `GET /standby/promotion` follows the one that runs |
| 409 | `SIGN_IN_NOT_CONFIGURED` | A sign-in was asked of an agent without an OpenID Connect provider |
| 409 | `BACKUPS_NOT_ENCRYPTED` | A backup of the agent's state was asked for and `SHIPWICK_BACKUP_PASSPHRASE` is not set; or an encrypted backup was asked for and the agent no longer has a passphrase |
| 429 | `RATE_LIMITED` | A wrong token, after 20 authentications from this address failed within a minute; `Retry-After` says in how many seconds wrong tokens are answered `401` again. A valid token is never refused |
| 413 | `INVALID_REQUEST` | A `deploy` body (the deploy.yaml) larger than 64 KB; a volume archive larger than 10 GB; an image archive larger than 4 GB; a static folder larger than 512 MB |
| 502 | `SIGN_IN_UNAVAILABLE` | The sign-in provider could not be reached or used; `message` says what to change |
| 503 | `RUNTIME_UNAVAILABLE` | Agent is shutting down; or, from 0.8, the Docker daemon does not answer — it is not running, or it took a question — about a container, the list of them, or the daemon itself — and said nothing for 15 seconds. `message` says which, and for Docker where to look; an agent before 0.8 answers `500 INTERNAL_ERROR` for a daemon that is down and does not answer at all for one that is silent |
| 507 | `DISK_FULL` | The server's disk has no room for what the request writes: a record in the agent's database, an uploaded folder, an image. Nothing was changed, and the same request succeeds once there is room. An agent before 0.8 answers `500 INTERNAL_ERROR`, or `400 INVALID_REQUEST` for a folder |
| 500 | `INTERNAL_ERROR` | Anything else; `message` carries the cause |

## Endpoints

| Method | Path | Role | |
|---|---|---|---|
| `GET` | `/health` | — | Liveness. No token. `{status, version}` |
| `GET` | `/server` | read | Host facts: OS, Docker version, CPUs, memory, counts, `swap_bytes` (how much swap the server has; `0` is none, absent where the agent cannot tell), `unlimited_memory` (the names of the running applications without a memory limit; absent when there are none), `proxy: {enabled, reachable, error, routes, dns_challenge, plain_lookups}` (`dns_challenge`: certificates are obtained through a DNS record, so hostnames may be proxied by Cloudflare and may be wildcards; `plain_lookups`: present and `true` when the proxy is not Shipwick's image of this version, and finds replicas without keeping the last answer of a name lookup), `notifications: {webhook}`, `backups: {destination, encrypted, state_last_at, state_error}` (see [Backups the agent takes](#backups-the-agent-takes)), `token: {name, role, kind, applications, expires_at}` — the caller's own, see [Authentication](#authentication) — `sign_in: {configured, issuer, name_claim}`, `dashboard_url`: `https://<SHIPWICK_DASHBOARD_DOMAIN>`, or `""` when the dashboard has no hostname (an agent before 0.5 leaves the field out), `disk` and `alerts`, see [Alerts and disk](#alerts-and-disk), `log_archive`, see [The log archive](#the-log-archive), and `open_to_applications`: present and `true` when the containers of applications can reach the API over the network, so that only the token keeps them out; absent otherwise, and from agents before 0.8, which never say (see [Authentication](#authentication)); `docker: {rootless, unenforced_limits}`: whether the daemon runs as an ordinary user, and which of `memory` and `cpu` it accepts as a limit and does not apply — an empty list where it applies both; absent from an agent older than 0.8 |
| `GET` | `/applications` | read | Summaries of all applications; each with `domain` and, when it serves only a part of it, `path`, and with `certificate_problem`, `alert_count` and `alert_severity`, see [Application status](#application-status) |
| `GET` | `/applications/:name` | read | Detail: status, active spec (env values and basic-auth passwords masked), containers, and the [certificate](#certificate-status) of each hostname |
| `POST` | `/applications/:name/deploy` | deploy | Start a deployment → `202`. A static application adds `?static=<digest>`, see [Static folders](#static-folders); `?plain=` names the env values written in plain sight, see [Deploying](#deploying) |
| `POST` | `/applications/:name/validate` | deploy | Ask whether a deployment of the document in the body would be accepted, without deploying it → `{valid}`, see [Validating before deploying](#validating-before-deploying) |
| `POST` | `/applications` | deploy | Start a deployment of the document in the body, for the application the document names → `202`, see [A document without a name in the address](#a-document-without-a-name-in-the-address) |
| `POST` | `/validate` | deploy | `validate` for such a document → `{valid}` |
| `GET` | `/applications/:name/config?escape=false` | deploy | The deploy.yaml that describes what the application runs, with references to stored secrets and values deployed as plain kept and other secret values masked, see [The document of what runs](#the-document-of-what-runs) |
| `POST` | `/applications/:name/redeploy` | deploy | Deploy the active configuration again; optional body `{"image": "…"}` → `202` |
| `POST` | `/applications/:name/rollback` | deploy | Deploy the configuration of an earlier successful deployment; optional body `{"deployment_id": 12}`, default: the most recent one → `202` |
| `POST` | `/applications/:name/stop` | deploy | Stop all replicas; the app stays stopped |
| `POST` | `/applications/:name/start` | deploy | Start a stopped app |
| `DELETE` | `/applications/:name` | admin | Remove containers **and history** → `204` |
| `GET` | `/applications/:name/logs?tail=100` | read | Last lines across replicas, merged by time (max 5000) |
| `GET` | `/applications/:name/logs?follow=true&tail=100` | read | Live stream, see [Following logs](#following-logs) |
| `GET` | `/applications/:name/logs/archive?kind=&deployment=&replica=&run=&limit=50&before=` | read | What is kept of the output of ended containers and runs, newest first (max 500), without the lines, see [The log archive](#the-log-archive) |
| `GET` | `/applications/:name/logs/archive/:id?tail=` | read | One entry with its lines |
| `GET` | `/applications/:name/logs/search?q=&since=&until=&deployment=&replica=&run=&limit=200&cursor=` | read | Lines that contain `q`, in the archive and in the replicas that exist, a page at a time |
| `GET` | `/applications/:name/events?limit=50` | read | The application's own feed, newest first (max 500): crashes, restarts, health changes, stops and starts |
| `GET` | `/applications/:name/metrics` | read | Point-in-time CPU and memory of the application and each replica, see [Metrics](#metrics) |
| `GET` | `/applications/:name/metrics/history?since=1h` | read | CPU and memory of each replica over the last `1h`, `24h` or `7d`, see [Metrics history](#metrics-history) |
| `GET` | `/deployments?application=&limit=50` | read | History, newest first (max 500) |
| `GET` | `/deployments/:id` | read | One deployment with spec (env values and basic-auth passwords masked) and events |
| `GET` | `/tokens` | admin | The stored tokens, without their values, see [Tokens](#tokens) |
| `POST` | `/tokens` | admin | Create a token; the value is in this response and nowhere else → `201` |
| `DELETE` | `/tokens/:name` | admin | Revoke a token → `204` |
| `PUT` | `/tokens/:name` | admin | Change the applications a token is limited to, its expiry, or both; the role and the value stay → the token |
| `GET` | `/audit?application=&actor=&actor_kind=&action=&outcome=&since=&limit=50&before=` | admin | Who changed what, newest first (max 500), see [Audit trail](#audit-trail) |
| `GET` | `/audit/export?format=csv|json&…` | admin | Everything that matches the same filters, streamed as CSV or as one JSON object per line; itself recorded |
| `GET` | `/auth` | — | Whether people can sign in, and what an authorization request needs. No token, see [Signing in](#signing-in) |
| `POST` | `/auth/exchange` | — | Turn the code the provider sent the browser back with into a session. No token; body `{"code", "code_verifier", "nonce", "redirect_uri"}` |
| `DELETE` | `/auth/session` | read | Forget the session the request is made with → `204` |
| `GET` | `/access/rules` | admin | Who may sign in, and as what, see [Access rules](#access-rules) |
| `POST` | `/access/rules` | admin | Give an address, a group, a domain or a name a role, creating or replacing the rule; body `{"kind", "subject", "role", "applications"?}` → `201` or `200` |
| `DELETE` | `/access/rules/:id` | admin | Remove a rule → `204`; sessions that rested on it end with their next request |
| `GET` | `/access/sessions` | admin | Who is signed in right now |
| `DELETE` | `/access/sessions/:email` | admin | End every session of a person, named as the sessions list them → `{sessions}` |
| `GET` | `/secrets` | read | The secrets stored on the server, names and dates only, see [Secrets](#secrets) |
| `PUT` | `/secrets/:name` | admin | Store a secret, creating or replacing it; body `{"value": "…"}` → `204` |
| `DELETE` | `/secrets/:name` | admin | Remove a secret → `204` |
| `GET` | `/registries` | read | The registries the agent holds a credential for, without passwords, see [Registry credentials](#registry-credentials) |
| `PUT` | `/registries/:registry` | admin | Check a credential against its registry and store it, creating or replacing; body `{"username": "…", "password": "…"}` → `204`; `400 REGISTRY_LOGIN_FAILED` when the registry refuses it |
| `DELETE` | `/registries/:registry` | admin | Remove a registry's credential → `204` |
| `POST` | `/server/rotate-key` | admin | Replace the key stored secrets are encrypted with and re-encrypt them, see [Key rotation](#key-rotation) |
| `GET` | `/certificates` | read | The certificates supplied by the operator: what each says about itself, never the key or the PEM, see [Certificates](#certificates) |
| `PUT` | `/certificates/:hostname` | admin | Store a certificate and its key under a hostname, creating or replacing it; body `{"certificate": "<PEM chain>", "key": "<PEM>"}` → `200` with the stored certificate |
| `DELETE` | `/certificates/:hostname` | admin | Remove a certificate → `204` |
| `GET` | `/applications/:name/volumes` | read | The volumes of the active deployment: `[{name, path}]` |
| `GET` | `/applications/:name/volumes/:volume/archive` | admin | The volume's contents as a tar archive, see [Volume backups](#volume-backups) |
| `PUT` | `/applications/:name/volumes/:volume/archive` | admin | Replace the volume's contents with the tar archive in the body → `204` |
| `POST` | `/applications/:name/images` | deploy | Load an image archive (`docker save` format) for the application, see [Images built by the CLI](#images-built-by-the-cli) → `201` |
| `POST` | `/applications/:name/images/missing` | deploy | Which layers of an image about to be sent the server lacks; body `{"layers": ["sha256:…"]}`, see [Images built by the CLI](#images-built-by-the-cli) |
| `GET` | `/volumes` | read | Every volume Shipwick created, with its application and size, see [Volumes of deleted applications](#volumes-of-deleted-applications) |
| `DELETE` | `/volumes/:name` | admin | Remove a volume whose application was deleted → `204`; `409 VOLUME_IN_USE` while the application exists |
| `GET` | `/applications/:name/jobs` | read | The scheduled jobs of the active deployment, each with its last and next run, see [Jobs and one-off commands](#jobs-and-one-off-commands) |
| `GET` | `/applications/:name/runs?job=&limit=50` | read | Runs of jobs, hooks and one-off commands, newest first (max 500), without output |
| `GET` | `/applications/:name/runs/:id` | read | One run with its output |
| `POST` | `/applications/:name/jobs/:job/run` | deploy | Start a scheduled job now → `202` |
| `POST` | `/applications/:name/run` | deploy | Run a one-off command; body `{"command": ["…"]}` → `202` |
| `PUT` | `/applications/:name/static` | deploy | Upload the folder of a static application as a tar archive → `{digest, size_bytes, files}`, see [Static folders](#static-folders) |
| `GET` | `/applications/:name/traffic?since=1h` | read | Requests, status classes, bytes and latency percentiles over the last `1h`, `24h` or `7d`, see [Traffic](#traffic) |
| `GET` | `/applications/:name/requests?tail=50` | read | The most recent requests as the proxy logged them (max 200), see [Requests](#requests) |
| `GET` | `/applications/:name/backups?limit=50` | read | The backups the agent took of the application, newest first (max 500), see [Backups the agent takes](#backups-the-agent-takes) |
| `GET` | `/applications/:name/backups/:id` | read | One backup, with the output of its last verification |
| `POST` | `/applications/:name/backups` | deploy | Take a backup now → `202` |
| `POST` | `/applications/:name/backups/:id/verify` | deploy | Prove that the backup restores; `:id` may be `latest` → `202` |
| `POST` | `/applications/:name/backups/:id/restore` | admin | Replace the application's volumes with the backup's; the application must be stopped → `202` |
| `GET` | `/applications/:name/backups/:id/volumes/:volume/archive` | admin | One volume of the backup as a tar archive, decrypted |
| `DELETE` | `/applications/:name/backups/:id` | admin | Remove the backup from every destination → `204` |
| `GET` | `/server/backups?limit=50` | admin | The backups of the agent's own state, newest first |
| `GET` | `/server/backups/:id` | admin | One of them |
| `POST` | `/server/backups` | admin | Back up the agent's state now → `202` |
| `POST` | `/server/backups/adopt` | admin | Record the backups the destinations hold and the database does not know; body `{"application"?}` → `{adopted, skipped}` |
| `POST` | `/export` | admin | Everything the server runs as one encrypted file; body `{"passphrase", "applications"?}`, see [Export, import and the standby](#export-import-and-the-standby) |
| `POST` | `/import?stopped=false&overwrite=false` | admin | Take in an export sent as the body and deploy what it holds; answers when it is done |
| `GET` | `/import` | admin | The import that is running, or ran last |
| `GET` | `/exports?limit=50` | admin | The exports written to where backups go, newest first |
| `GET` | `/exports/:id` | admin | One of them |
| `POST` | `/exports` | admin | Write one there now → `202` |
| `GET` | `/standby` | read | The applications that were imported stopped, the DNS records a promotion asks for, how the scheduled import is doing, and the promotion that runs or ran last |
| `POST` | `/standby/pull` | admin | Import the newest export in the bucket now, stopped → `202` |
| `POST` | `/standby/promote?wait=false` | admin | Start the applications that were imported stopped, in order → `202`, poll `/standby/promotion` |
| `GET` | `/standby/promotion` | read | The promotion that is running, or ran last |

`GET /metrics` is the one authenticated endpoint outside `/api/v1`, because
that is where a scraper looks; it needs the `read` role and answers in the
Prometheus text format, not in the envelope: see
[Prometheus metrics](#prometheus-metrics).

### Deploying

The body **is** the deploy.yaml document. JSON is accepted as well.

`${NAME}` placeholders in `env` values, and in the passwords of
`proxy.basic_auth`, are filled in by the agent from the
[secrets](#secrets) stored on the server, before the deployment is recorded;
the record holds the values, so a later change of a secret does not reach a
redeploy or rollback of it. `$${NAME}` there is a literal `${NAME}`. A
placeholder anywhere else is a convention of the CLI, which fills it in before
sending, and is stored literally here.

`?plain=env.LOG_LEVEL,env.REGION` — fields separated by commas, or the
parameter repeated — says which `env` values of the document were written in
it in plain sight, as opposed to filled in by whoever sends it. The agent
keeps the statement with the deployment and does one thing with it: those
values are in [the document of what runs](#the-document-of-what-runs) as they
are, where every other value is masked. It is the sender's word, taken by an
endpoint that takes `deploy`; nothing else the API returns changes, and
without the parameter nothing is plain. A field that is not `env.<NAME>` with
a variable the document sets is `400 INVALID_REQUEST`; one whose value holds
a `${NAME}` for the agent stays the reference it is. An agent before 0.8
ignores the parameter.

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
A deployment that ended while the agent's database could not be written —
its disk is full — keeps the status of its last successful write and no
`completed_at` until the database takes writes again. It is completed then,
and if its status was still one of work, it becomes `FAILED` with the error
it failed with at the time.

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
A deployment that ends `FAILED` or `ROLLED_BACK` removes the image it named
unless something else needs it, and says so in a step.

`completed_at` does not wait for the replicas that were replaced to exit.
They are out of rotation and have been sent `SIGTERM`; each then has the
application's `deploy.stop_timeout` (10s unless set) before it is killed, and
is removed after that. Until then it is listed among the application's
`containers` with `stopping: true`. One that had to be killed is reported in
the application's [events](#events), at level `warn`. A deployment that starts
while one is still stopping waits for it before it starts a replica, and says
how long it waited.

A deployment that is in flight when the agent stops — an upgrade restarts
it — is neither failed nor completed: the agent resumes it when it starts, and
its events then include the step `Resumed after the agent restarted`. To a
client that polls, the agent is unreachable for a moment and the deployment
then goes on; keep polling. One that cannot be resumed ends `FAILED` with an
`error` that begins `agent restarted during deployment`, or that says its
pre-deploy command was interrupted.

### Validating before deploying

`POST /applications/:name/validate` takes the same body as `deploy` and
answers what `deploy` would answer, without deploying: nothing is recorded,
pulled or started, and the application is not locked — while a deployment
runs, `validate` still answers, where `deploy` would answer
`409 DEPLOYMENT_IN_PROGRESS`.

```bash
curl -X POST http://localhost:9000/api/v1/applications/my-api/validate \
  -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" \
  --data-binary @deploy.yaml
```

```json
{ "data": { "valid": true } }
```

A document that would be refused gets the error `deploy` would give, status
and body alike: `400 INVALID_CONFIG` with `details.fields` for a field that
does not validate, a hostname or a published port that something else holds,
or a `${NAME}` in `env` that is not among the stored secrets;
`400 INVALID_REQUEST` when the document names another application than the
URL.

It is meant to be asked before an image is built or a folder uploaded, so it
does not ask for either: a document with `build` and no `image` is valid here
(`deploy` refuses it), and a static application needs no `?static=`. An agent
that predates the endpoint answers `404 ENDPOINT_NOT_FOUND`; deploy without
asking then.

### A document without a name in the address

`POST /applications` is `POST /applications/:name/deploy`, and `POST /validate`
is `POST /applications/:name/validate`, for a caller that has a document and
has not parsed it: the application is the one the document's `name` says.
Body, query (`?static=<digest>` for a static application, `?plain=`), answers
and errors are those of the endpoints they stand for.

```bash
curl -X POST http://localhost:9000/api/v1/applications \
  -H "Authorization: Bearer $SHIPWICK_AGENT_TOKEN" \
  --data-binary @deploy.yaml
```

The name is read from the document before the request is authorized, and
nothing else of the document is: a token [limited to
applications](#authentication) is checked against it, and the
[audit trail](#audit-trail) records the deployment under it, as if it had been
in the address. A document whose name cannot be read is answered
`400 INVALID_CONFIG` with the field `name`, like any document that does not
validate; its audit entry has no application. An agent before 0.7 answers
both with `404 ENDPOINT_NOT_FOUND`; use the endpoints with the name then.

### The document of what runs

`GET /applications/:name/config` writes the configuration of the active
deployment as the deploy.yaml that describes it, in the key order and style
`shipwick init` uses:

```json
{
  "data": {
    "application": "my-api",
    "deployment_id": 12, "sequence": 7, "version": "1.4.2",
    "document": "# A value shown as \"********\" was given when …\n\nname: my-api\n\nimage: ghcr.io/company/my-api:1.4.2\n\n…",
    "masked": ["env.API_KEY", "proxy.basic_auth[0].password"],
    "plain": ["env.LOG_LEVEL"]
  }
}
```

`document`, for an application deployed with `?plain=env.LOG_LEVEL`, one
reference and one value nobody spoke for:

```yaml
# A value shown as "********" was given when the application was deployed and
# is not handed out. Write it again, or store it on the server with
# shipwick secret set NAME and refer to it as ${NAME}. A deployment of this
# file is refused until every such value has been replaced.

name: my-api

image: ghcr.io/company/my-api:1.4.2

port: 8080

replicas: 1

env:
  API_KEY: "********" # not handed out: write the value again, or refer to a secret as ${NAME}
  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
  LOG_LEVEL: debug

restart:
  policy: always
```

The secret values of a configuration — `env` values and the passwords of
`proxy.basic_auth` — are never in it, and an `env` value is one unless its
deployment said otherwise:

- A value the deployed document wrote with a `${NAME}` that the agent filled
  in from its [secrets](#secrets) is that text again, the placeholders in
  place. The agent keeps it next to the deployment, encrypted like a secret;
  a redeploy and a rollback carry it on, and so do a key rotation, an export
  and an import.
- An `env` value that the deployment named in `?plain=` is the value, a
  literal `${NAME}` in it written `$${NAME}`. `plain` names those fields,
  sorted; it is `[]` when there are none, and absent from an agent before
  0.8. The statement is kept and carried on like a reference. A deployment
  of the document that names the same fields again keeps them plain; one
  that does not masks them from then on. `shipwick deploy` names the values
  that stand in the file as it sends them; the dashboard names the ones it
  was handed out as plain and that were not edited.
- Every other value — filled in by the CLI from its environment or
  `--env-file`, or sent by a client that said nothing, which the agent cannot
  tell apart from a password — is the mask, `"********"`, with a comment on
  its line. `masked` names those fields, in the order of the document; it is
  `[]` when there are none. The comment at the top is there only when there
  are some. A basic-auth password is never plain.

The text around a reference and a plain value are given back as they arrived, which
`GET /applications/:name` masks along with the rest. That is why the endpoint
takes `deploy`, and a limited token gets the documents of its applications
only: whoever may deploy an application can read its environment by running a
command in it.

Deployed again unchanged — to `POST /applications` or to
`POST /applications/:name/deploy` — a document without masks gives the same
configuration, its references filled in from the secrets as they are then. A
document that holds a mask is refused, by every endpoint that takes a
document and whoever wrote it:

```json
{ "error": { "code": "INVALID_CONFIG", "message": "invalid deploy.yaml",
             "details": { "fields": [ {
               "field": "env.API_KEY",
               "message": "******** is what the server shows in the place of this value, not the value",
               "expected": "the value itself, or ${NAME} with the value stored by shipwick secret set NAME" } ] } } }
```

So `"********"` cannot be a value of `env` or a basic-auth password; a stored
secret may hold it.

`?escape=true` writes the document for a file that `shipwick deploy` reads:
the CLI fills in `${NAME}` everywhere, the agent only in the secret values, so
a literal `${NAME}` elsewhere — in a `command`, say — is written `$${NAME}`.
Without it the document is what the agent itself reads, and what goes back to
`POST /applications`. The secret values are the same in both.

For a static application `static_digest` is the folder the active deployment
serves (`"sha256:…"`); a deployment of the document names it in `?static=`.
The field is absent otherwise. `404 NOT_FOUND` for an application the server
does not know, `409 NOT_DEPLOYED` for one that has no active deployment, and
`404 ENDPOINT_NOT_FOUND` from an agent before 0.7.

### Paths and the proxy block

`path` and `proxy` in the deploy.yaml are part of the stored configuration,
and come back in every `spec`:

```json
{
  "domain": "example.com",
  "path": "/api",
  "proxy": {
    "strip_prefix": true,
    "headers": { "X-Frame-Options": "DENY" },
    "basic_auth": [
      { "path": "/api/admin", "username": "admin", "password": "********" }
    ],
    "redirects": [
      { "from": "/api/old", "to": "/api/new", "status": 308 }
    ]
  },
  "static": { "dir": "dist", "fallback": "index.html" }
}
```

Each of `path`, `proxy` and its four members is left out when it is not set,
and so is `static.fallback`; `basic_auth[].path` is left out for an account
of the whole application; `redirects[].status` is always present, `308` when
the document named none. `path: /` is stored as no path.

`init: true` in the deploy.yaml comes back as `"init": true` in the `spec`;
the key is left out when it is not set or `false`. A redirect hostname of an
application with a `path` is answered with `308` to
`https://<domain><path>` plus the request's path and query.

**A password is always `********`** in a response, like an `env` value. In
the request it is the password, or a `${NAME}` that the agent fills in from
the stored [secrets](#secrets) exactly as it does for `env`; `$${NAME}` is a
literal `${NAME}`. The record holds the value, encrypted. A name that is not
stored is refused with `field` `proxy.basic_auth[i].password` and the same
message and `expected` as for an `env` value; a stored value that cannot be a
password — shorter than 8 characters, longer than 72 bytes — is refused under
the same field, `with ${NAME} filled in from the server's secrets, the
password is too short: at least 8 characters`, without repeating it.

Applications may share a hostname when their paths differ. `400
INVALID_CONFIG` when they do not: `field` is `path` when this application
names one — `example.com/api is already served by application "api";
applications share a domain under different paths` — and `domain` or
`aliases[i]` when it names none and another application has the rest of the
hostname. A hostname in another application's `redirects`, and the agent's
and the dashboard's own, cannot be shared under any path. Paths are compared
without regard to case.

The application views carry `path` next to `domain`, left out when there is
none; the address of an application is `https://<domain><path>`. The step
event of a deployment with a path reads `Routed https://example.com/api to 2
replicas`.

### The security block

`security` in the deploy.yaml comes back in every `spec` as it was
understood:

```json
{
  "user": "1000:1000",
  "security": {
    "read_only": true,
    "tmpfs": [
      { "path": "/tmp", "size_bytes": 67108864 },
      { "path": "/var/cache/api", "size_bytes": 209715200 }
    ],
    "capabilities": [],
    "non_root": true
  }
}
```

`security` is left out for an application without the block, and for a block
that asks for nothing. `read_only` and `non_root` are left out when `false`,
`tmpfs` when empty; `size_bytes` is always present, `67108864` when the
document named no size. `capabilities` has three states: left out, the
containers keep Docker's default set; `[]`, the document said `none` and they
keep nothing; a list, they keep those, in upper case without `CAP_` and in
alphabetical order whatever the document wrote. Nothing in the block is a
secret, and nothing is masked. A client that reads a missing `capabilities`
as an empty list turns "everything" into "nothing".

A document is refused with `400 INVALID_CONFIG` and the usual `fields` for a
key of the block that does not exist (`field` `security`), a capability
outside Docker's default set or listed twice (`security.capabilities[i]`), an
empty list (`security.capabilities`: `none` is how to say it), a `tmpfs` path
that is not absolute and clean, is `/`, or is mounted already by a volume or
another entry (`security.tmpfs[i].path`), a `size` outside 1 MB to 1 GB
(`security.tmpfs[i].size`), more than 10 entries (`security.tmpfs`), the
block next to `static`, and — under `non_root: true` — a `user` that is
`root`, the id 0 or a name (`user`).

What only the image can say is checked when the image is on the server. A
deployment under `non_root: true` whose image names no user, the user
`root`, the id 0 or a user by name, and whose spec has no `user`, is
accepted with `202` and fails: `status` `FAILED`, no container created, the
previous version untouched, and in `error`:

```text
security.non_root refuses image nginx:1.27: it names no user, and a container without one runs as root; set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000", or build the image with a USER instruction
```

A job or a command that would create such a container later — the
tag names another image by then — fails the same way: the run is `failed`
with the sentence in its `output`.

An agent before 0.8 answers a document with the block as it answers every
key it does not know: `400 INVALID_CONFIG`, `field` `line N`, `message`
`unknown field "security"`.

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

### The log archive

The agent keeps the last output of every container whose run ended — a
replica that crashed, was restarted, stopped or replaced, the replicas of a
deployment that failed, a run of a job or a command — for
`SHIPWICK_LOG_RETENTION_DAYS` and within `SHIPWICK_LOG_RETENTION_SIZE`
(docs/handbook.md, "Output that outlives its container"). The three endpoints
need the `read` role, like the logs, and an agent older than 0.7 answers them
`404 ENDPOINT_NOT_FOUND`.

`GET /applications/:name/logs/archive` lists the entries, newest first and
without their lines. `kind=replica|run`, `deployment=<id>`, `replica=<n>` and
`run=<id>` narrow the list; `limit` is 50 by default and at most 500;
`before=<id>` continues after the entry with that id.

```json
{
  "data": [
    {
      "id": 41,
      "application": "my-api",
      "kind": "replica",
      "deployment_id": 12,
      "deployment": 3,
      "version": "1.4.2",
      "replica": 2,
      "job": "",
      "run_id": null,
      "container": "shipwick_my-api_3_2",
      "reason": "crashed",
      "exit_code": 1,
      "oom_killed": false,
      "ended_at": "2026-10-03T23:18:58.930797736Z",
      "first_line_at": "2026-10-03T23:18:53.929370317Z",
      "last_line_at": "2026-10-03T23:18:58.929928842Z",
      "lines": 153,
      "bytes": 20311,
      "stored_bytes": 3100,
      "truncated": false
    }
  ]
}
```

| Field | |
|---|---|
| `kind` | `replica`: one run of a replica's container, from a start to the stop after it. `run`: a run of a job, a pre-deploy command or a one-off command |
| `deployment_id`, `deployment`, `version` | The deployment the container belonged to: its id, its number in the application's history (the `sequence` of a deployment) and its version. `null`, `0` and `""` once the deployment is gone |
| `replica` | The replica's index; `0` for a run |
| `job`, `run_id` | The job's name (`pre-deploy` and `run` included) and the run, for a run; `""` and `null` for a replica |
| `reason` | Why the run ended, see below |
| `exit_code` | The process's exit code; `null` when it was still running as its container was removed, or when the agent did not see it exit |
| `oom_killed` | The process was killed for exceeding its memory limit |
| `ended_at` | When the run ended |
| `first_line_at`, `last_line_at` | The times of the first and the last line kept; `null` when `lines` is 0 |
| `lines`, `bytes` | How many lines are kept, and the size of their text |
| `stored_bytes` | What they take on the server's disk, compressed |
| `truncated` | The run printed more than is kept; these are its last lines. A replica's run keeps 2,000 lines and 1 MB, a job's 10,000 lines and 4 MB; a line longer than 16 KB is cut |

`reason` of a replica:

| `reason` | |
|---|---|
| `crashed` | The process exited by itself with a code other than 0 |
| `exited` | The process exited by itself with code 0 |
| `oom_killed` | Killed for exceeding its memory limit |
| `unhealthy` | Restarted by the agent after failing its health check |
| `stopped` | The application was stopped |
| `replaced` | A newer deployment took its place |
| `deployment_failed` | A replica of a deployment that failed, removed with it; `exit_code` and `oom_killed` say whether it had died by itself |
| `removed` | Removed for another reason: a leftover, a rollback that failed |
| `restarted` | The run ended while no agent was running, and the container was running again when one started; why it ended is not known |

For a run, `reason` is the run's `status`: `succeeded`, `failed`, `timed_out`
or `interrupted`. An entry with `lines: 0` is a replica that died without
printing anything.

`GET /applications/:name/logs/archive/:id` is one entry with its lines,
oldest first, each a [`LogLine`](../pkg/api/types.go) as the live logs return
them; `tail=<n>` (1 to 10000) keeps the last `n`:

```json
{
  "data": {
    "id": 41,
    "application": "my-api",
    "kind": "replica",
    "…": "the entry's fields, as in the list",
    "output": [
      {"replica": 2, "container": "shipwick_my-api_3_2", "stream": "stderr", "time": "2026-10-03T23:18:58.929928842Z", "message": "panic: connection refused"}
    ]
  }
}
```

`404 NOT_FOUND` for an entry that does not exist, belongs to another
application, or has aged out.

`GET /applications/:name/logs/search` looks through the archive and through
the logs of the replicas that exist, from where their last archived run
ended, so that no line is found twice:

| Parameter | |
|---|---|
| `q` | Text to look for inside lines, whatever its case; at most 256 bytes, one line. Without it every line matches |
| `since`, `until` | Only lines at or after, at or before, a time in RFC 3339 |
| `deployment`, `replica`, `run` | Only the output of that deployment (its id), replica or run. With `run`, and with a `deployment` that is not the active one, only the archive is read |
| `limit` | Lines in one answer: 200 by default, at most 1000 |
| `cursor` | The `next` of the answer this one continues |

```json
{
  "data": {
    "lines": [
      {
        "replica": 1,
        "container": "shipwick_my-api_4_1",
        "stream": "stdout",
        "time": "2026-10-03T23:19:03.850907531Z",
        "message": "dial tcp 10.0.0.5:5432: connect: connection refused",
        "archive_id": null,
        "deployment_id": 14,
        "deployment": 4,
        "job": "",
        "run_id": null
      },
      {
        "replica": 2,
        "container": "shipwick_my-api_3_2",
        "stream": "stderr",
        "time": "2026-10-03T23:18:58.929928842Z",
        "message": "panic: connection refused",
        "archive_id": 41,
        "deployment_id": 12,
        "deployment": 3,
        "job": "",
        "run_id": null
      }
    ],
    "next": "a41.151",
    "sources": 3,
    "bytes": 21544
  }
}
```

- A line is a `LogLine` with where it comes from: `archive_id` is the entry
  that holds it, `null` for a line read from a container that exists.
- The order is by source, newest first, and within a source by time, newest
  first: the replicas that exist, from the highest index down, then the
  archive's entries from the newest to the oldest. Read backwards, an answer
  is in the order things were printed in each container.
- `next` is `""` when everything the question admits has been looked at.
  Otherwise the search goes on with `cursor=<next>` and the same other
  parameters. An answer is bounded twice — by `limit`, and by how much it
  reads, about 128 MB of output — so a page can hold fewer lines than `limit`,
  none included, and still have a `next`: keep asking until it is empty.
- `sources` and `bytes` are how many containers' output this request read
  and how much of it.
- Of a container that exists, its last 50,000 lines are looked at. The
  container of a job that is still running is not searched; its output is in
  the archive when the run has ended.
- `400 INVALID_REQUEST` for a `q` that is too long or has a line break, a
  time that is not RFC 3339, `until` before `since`, and a `cursor` no answer
  returned; `404 NOT_FOUND` for an unknown application and for a `deployment`
  of another one.

Nothing of what these endpoints return is written to the agent's log, to
events or to the audit trail; the request log holds the path, not the query.
Deleting an application removes its entries and their files.

`GET /server` reports the archive as a whole:

```json
"log_archive": {"enabled": true, "entries": 278, "bytes": 63974240, "max_bytes": 1073741824, "retention_days": 14}
```

`enabled` is `false` when `SHIPWICK_LOG_RETENTION_SIZE` is `0`; `bytes` is
what the entries take on the disk. An agent older than 0.7 leaves the field
out.

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

### Backups the agent takes

Where the endpoints above hand an archive to the caller, these are about the
backups the agent takes and keeps itself: on the schedule under `backups` in
deploy.yaml, or on request. A *backup* is one run:

```json
{ "id": 12, "trigger": "schedule", "status": "succeeded",
  "started_at": "2026-03-01T03:00:00Z", "completed_at": "2026-03-01T03:00:41Z",
  "volumes": [{ "volume": "data", "size_bytes": 2254857830 }],
  "destinations": ["local", "s3"], "encrypted": true, "error": "",
  "activity": "", "verified_at": "2026-03-01T09:12:00Z", "verify_error": "",
  "restored_at": null, "restore_error": "" }
```

`trigger` is `schedule`, `manual`, or `adopted` for a backup that was recorded
from its files (below). `status` is `running`, `succeeded` or
`failed`; a failed backup has `error` set, empty `volumes` and `destinations`,
and no files anywhere. `size_bytes` is the tar archive's size, before
encryption. `destinations` lists where it went, of `local` (the agent's backup
directory) and `s3`; `encrypted` says whether it was written with the agent's
passphrase. `activity` is `verify` or `restore` while one of them is in
progress and `""` otherwise. `verified_at` is when the backup last proved to
restore and `verify_error` why its last verification failed; one of them at
most is set, and the same goes for `restored_at` and `restore_error`.
`GET …/backups/:id` adds `verify_output`: the last 200 lines (64 KB) the
verification's container wrote.

`POST /applications/:name/backups` answers `202` with the backup (`status:
"running"`) and a `Location` to poll until `completed_at` is set. It works
without a `backups` block in deploy.yaml; with one, its `before` command runs
first, for at most `before_timeout` (`"1h0m0s"` in the stored configuration
unless set; an older deployment has no such field, and an hour),
and `stop` is honoured. `before_in` says where the command runs: absent from
the stored configuration, inside the replica, where the command is not ended
at the limit (`error`: `backups.before did not finish within 1h; the backup
was given up …`); `"container"`, in a container of its own beside the
replica, which is stopped and removed at the limit (`… did not finish within
1h and was stopped; …`). The application is held until the archives are
written: other operations get `409 DEPLOYMENT_IN_PROGRESS`, as during a
deployment, and wait up to 30 seconds instead when the backup was started by
the schedule. `409 NO_VOLUMES` if the application has none.

`POST …/backups/:id/verify` restores the backup into scratch volumes, starts
one container of the application's current image on them and holds it to the
application's health check, as a deployment holds a new replica; without a
health check, to staying up for the stabilization window. `:id` may be
`latest`: the newest successful backup, `404` if there is none. The answer is
`202` with the backup, `activity: "verify"`; poll until `activity` is empty,
then read `verified_at` or `verify_error`. The container and the volumes are
removed either way.

`POST …/backups/:id/restore` replaces the application's volumes with the
backup's archives, one after the other. As with `PUT …/archive`, the
application must be stopped (`409 APPLICATION_RUNNING`) and stays stopped, and
each restored volume adds the same application event. `202` with `activity:
"restore"`; poll until it is empty, then read `restored_at` or
`restore_error`. A backup holding a volume the active deployment no longer
mounts is `404`.

`GET …/backups/:id/volumes/:volume/archive` streams the archive like
`GET …/volumes/:volume/archive` does, decrypted, with `Content-Length` set to
its size and `Content-Disposition: attachment;
filename="<app>-<volume>-backup-<id>.tar"`. A backup that does not decrypt is
`409 BACKUP_NOT_USABLE` when that shows in its first chunk, and a connection
cut short of `Content-Length` when it shows later.

`verify`, `restore` and the archive refuse a backup that failed with `409
BACKUP_NOT_USABLE`; they and `DELETE` refuse one that is still running, or
being verified or restored, with `409 BACKUP_BUSY`. `DELETE` removes the
backup's files from the directory and the bucket, then its record. A failed backup adds an event of `type: "backup"` to
the application's feed and is sent to the webhook as `backup.failed`; a
verification adds an event either way.

**The agent's own state** — its database and the key that encrypts the secrets
in it — is backed up daily under the same shape, with `volumes` naming the two
files (`shipwick.db`, `encryption.key`). `POST /server/backups` takes one now:
`202`, poll `GET /server/backups/:id` until `completed_at` is set. It is `409
BACKUPS_NOT_ENCRYPTED` without `SHIPWICK_BACKUP_PASSPHRASE`: the key is never
written unencrypted. There is no endpoint that restores the state; that is
done with the agent stopped (handbook, *Restoring the agent's state*).

**Adopting backups.** A database restored from a backup of the agent's state
does not know the backups taken after it, whose files are still in the
directory and in the bucket. `POST /server/backups/adopt` records them. The
body is optional: `{"application": "postgres"}` looks at one application's
backups; without it, at every application's, the agent's state and the
exports. The answer is `200`, once the destinations have been listed:

```json
{ "data": { "adopted": [
    { "kind": "application", "application": "postgres",
      "backup": { "id": 13, "trigger": "adopted", "status": "succeeded",
        "started_at": "2026-03-02T03:00:41Z", "completed_at": "2026-03-02T03:00:41Z",
        "volumes": [{ "volume": "data", "size_bytes": 2254857830 }],
        "destinations": ["s3"], "encrypted": true, "error": "",
        "activity": "", "verified_at": null, "verify_error": "",
        "restored_at": null, "restore_error": "" } },
    { "kind": "state", "application": "", "backup": { "id": 14, "trigger": "adopted", "…": "…" } }
  ],
  "skipped": [
    { "kind": "state", "application": "", "id": 15,
      "reason": "encryption.key.enc is missing: the backup was not finished" }
  ] } }
```

`kind` is `application`, `state` or `export`; `application` is empty for the
last two, whose backups are then listed by `GET /server/backups` and
`GET /exports`. A backup is recorded under the id its files carry, with what
they say: `volumes` from their names, `size_bytes` from their sizes — for an
encrypted file the size of what it holds — `started_at` and `completed_at`
both from when the newest of them was written, `destinations` from where all
of them are, `encrypted` from their names. `skipped` lists the runs whose
files are not a backup that can be recorded — one of the agent's state or an
export that lacks a file, files that are partly encrypted, a file that has
one size in the directory and another in the bucket — and nothing is changed
for them. Both lists are empty when the database knows everything; nothing is
adopted twice, and no file is written, moved or removed. An adopted backup is
verified, restored, downloaded, removed and counted towards `backups.keep`
like any other. Whether an application's backup was finished cannot be told
from its files: it is adopted with the volumes that are there.

`409 FOREIGN_BUCKET` when the bucket, under the agent's prefix, is marked by
another installation; `409 BACKUP_NOT_USABLE` on an agent without a backup
destination; `400 INVALID_REQUEST` for a name that is not an application's. A
bucket that cannot be listed is a `500` with the service's answer.

`GET /server` summarizes it:

```json
"backups": { "destination": "s3", "encrypted": true,
             "state_last_at": "2026-03-01T03:17:04Z", "state_error": "" }
```

`destination` is `s3` when a bucket is configured, `local` when backups stay
in the agent's directory, and `none` for an agent started without anywhere to
keep them. `state_last_at` is the last successful backup of the agent's state,
`null` if there has been none. `state_error` is why there is none — no
passphrase — or why the last attempt failed, and empty when the last attempt
succeeded. Agents older than this feature send no `backups` object.

### Export, import and the standby

An export is everything the server would need to be built again elsewhere:
every application's active configuration with its values in clear, the stored
secrets, registry credentials and certificates, the images that exist only on
the server, the folders of static applications and an archive of every volume.
It exists only encrypted, in the format backups use
([architecture.md](architecture.md#moving-a-server-and-standing-by) has its
layout). Everything here takes `admin`, except `GET /standby` and
`GET /standby/promotion`.

**`POST /export`** streams one:

```json
{ "passphrase": "at least twelve characters", "applications": ["db", "api"] }
```

`applications` is optional and limits the export to those; an unknown name is
`404 NOT_FOUND`. The answer is `200` with `Content-Type:
application/octet-stream` and `Content-Disposition: attachment;
filename="shipwick-export-<time>.swexport"`, sent chunked: the length is not
known beforehand. What fails before the first byte is an ordinary error — an
application that is being deployed when the export begins is `409
DEPLOYMENT_IN_PROGRESS`, with its name in the message. What fails later ends the body without the file's last
chunk, so that it does not decrypt, and says why in the trailer
`X-Shipwick-Export-Error`. A client should read what it received to its end
with the passphrase before calling it an export. Nothing is written on the
server, and the passphrase is not kept.

**`POST /import?stopped=false&overwrite=false`** takes an export as the body
(`Content-Type: application/octet-stream`) and its passphrase in the header
`X-Shipwick-Passphrase`, base64-encoded. The agent imports the file as it
arrives — nothing of it is stored — and answers when every application in it
has been dealt with, which takes as long as their deployments; a client must
not time the request out. `stopped=true` deploys every application without
starting it and then replaces only applications that are stopped;
`overwrite=true` replaces what exists under the same name, applications with
their volumes. The answer is `200` with the import, also when applications in
it failed:

```json
{
  "status": "failed",
  "source": "upload",
  "stopped": false,
  "overwrite": false,
  "started_at": "2026-03-01T04:10:00Z",
  "completed_at": "2026-03-01T04:11:32Z",
  "exported_at": "2026-03-01T04:00:00Z",
  "secrets": 2,
  "registries": 1,
  "certificates": 0,
  "applications": [
    { "name": "postgres", "status": "imported", "version": "17", "deployment_id": 1,
      "volumes": ["data"], "message": "" },
    { "name": "my-api", "status": "skipped", "version": "1.4.2", "deployment_id": null,
      "volumes": [], "message": "it exists on this server and was left as it is; import with --overwrite to replace it and its volumes" }
  ],
  "warnings": ["DB_PASSWORD: a secret by that name exists on this server and was kept; --overwrite replaces it"],
  "error": ""
}
```

`status` is `running`, `succeeded` or `failed` — failed when an application
failed or the import could not go on, which `error` then explains. An
application's `status` is `pending`, `importing`, `imported`, `skipped` (it
exists, or runs, and was left alone) or `failed`; `message` says why and what
to do. `volumes` are the volumes that were filled before the application
first started. A deployment made by an import has the `kind` `import`, or
`standby` when it was deployed stopped for a standby.

The configuration of every application in an export is validated by the
rules a deploy.yaml is held to, all of them, before anything of the
application is touched; one that does not pass is `failed` with the fields
and what is wrong with each in `message`, and the import goes on with the
next.

`400 INVALID_EXPORT`: the body is not an export, the passphrase does not
match, or the file breaks off; what had been imported until then stays, and
`GET /import` shows it. `409 IMPORT_IN_PROGRESS`: a server takes one import
at a time. `409 PROMOTION_IN_PROGRESS`: nothing is imported into a server
while it is being promoted.

**`GET /import`** is the import that is running, or ran last, in the same
shape: poll it from a second connection to follow an upload. `404 NOT_FOUND`
when the server has run none. The record outlives the agent that wrote it.
An import does not: it ends with the upload or the download that feeds it,
so an import the agent was restarted under is `failed`, and says so. A
deployment it had begun is resumed like any other, and an application that
was being deployed stopped stays stopped.

**Exports on the server.** `POST /exports` writes an export of the whole
server to where backups go, encrypted with the agent's
`SHIPWICK_BACKUP_PASSPHRASE` (`409 BACKUPS_NOT_ENCRYPTED` without one, `409
EXPORT_IN_PROGRESS` while one is being written): `202` with a `Location`, and
a record shaped like a [backup](#backups-the-agent-takes) whose one "volume"
is `export.tar`. Poll `GET /exports/:id` until `completed_at` is set;
`GET /exports?limit=50` lists them, newest first. `SHIPWICK_EXPORT_SCHEDULE`
does the same on a schedule, with the trigger `schedule`.

**The standby.** `GET /standby` (`read`) says what the server holds for the
day it has to take over:

```json
{
  "applications": [
    { "name": "postgres", "version": "17", "hostnames": [], "imported_at": "2026-03-01T04:15:02Z" },
    { "name": "my-api", "version": "1.4.2", "hostnames": ["api.example.com"], "imported_at": "2026-03-01T04:15:09Z" }
  ],
  "records": [{ "hostname": "api.example.com", "type": "A", "value": "203.0.113.77" }],
  "pull": { "schedule": "15 * * * *", "last_at": "2026-03-01T04:15:11Z", "last_export": 42, "last_error": "" },
  "promotion": null
}
```

`applications` were imported stopped and are still stopped, in the order a
promotion starts them. `records` are the DNS records a promotion will ask
for; `value` is empty when the agent does not know its own address. `pull` is
`null` unless `SHIPWICK_STANDBY_SCHEDULE` is set; `last_export` is the id of
the export imported last, `0` if none; both survive a restart of the agent,
so an export is imported once. `promotion` is the promotion that is running
or ran last, as `GET /standby/promotion` returns it, and `null` on a server
that was never promoted (an agent before 0.6 leaves the field out).

`POST /standby/pull` imports the newest export in the bucket now, stopped
and overwriting: `202` with the import as it begins and `Location:
/api/v1/import` to poll. `409 STANDBY_NOT_CONFIGURED` without a bucket to read
from, `404 NOT_FOUND` when the bucket holds no export.

**Promoting.** `POST /standby/promote?wait=false` begins a promotion: those
applications are started in order, each waited for until it is ready or has
spent its startup budget. The promotion runs on the server, whatever becomes
of the request, and has a record. The answer is `202` with that record as the
promotion begins and `Location: /api/v1/standby/promotion`:

```json
{
  "id": 1,
  "status": "running",
  "started_at": "2026-03-01T09:30:00Z",
  "completed_at": null,
  "applications": [
    { "name": "postgres", "status": "pending", "message": "" },
    { "name": "my-api", "status": "pending", "message": "" }
  ],
  "records": [{ "hostname": "api.example.com", "type": "A", "value": "203.0.113.77" }]
}
```

**`GET /standby/promotion`** (`read`) returns the record while the promotion
runs and after it has ended; poll it until `completed_at` is set. `404
NOT_FOUND` on a server that was never promoted.

- `status` is `running`, then `succeeded`, or `failed` when at least one
  application could not be started.
- An application's `status` is `pending` (not reached yet), `starting`
  (being started, or waited for), `running` (ready), `started` (not ready
  within its startup budget; the supervisor has it, and `message` says what
  it last answered) or `failed` (it could not be started; `message` says
  why). One application that fails does not stop the promotion.
- `records` are the DNS records to change, fixed when the promotion begins.
- `id` counts the promotions of the server. A client that lost the answer
  to its `POST` reads the record: an `id` it has not seen means the request
  arrived.

`409 PROMOTION_IN_PROGRESS` when a promotion is running: there is one at a
time, and the record of the one that runs is the one to follow. `409
IMPORT_IN_PROGRESS` while an import runs. With nothing to promote, no
promotion begins: the answer is `200` with a record that has completed,
`"id": 0` and no applications, and the record of the last promotion stays.

A promotion outlives the agent. One that an agent was restarted under is
resumed by the agent that starts next, from its record: applications it had
finished with keep their outcome, the one it was at is started if it is not
running and waited for again, the rest follow. While the agent is away the
proxy answers `502`, `503` or `504` in its place, and an agent that is
shutting down answers `503`; a client keeps polling.

Without `wait=false` the request is held until the promotion has ended and
answered `200` with the completed record, as agents before 0.6 answered —
they have no record: no `id`, `status` or times in the answer, and
`404 ENDPOINT_NOT_FOUND` for `GET /standby/promotion`. The promotion is the
same one either way; a held request that loses its connection loses only the
answer, which `GET /standby/promotion` still has. `503` when the agent is
stopped while the request is held.

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
   {"data": {"image": "shipwick.local/my-api:20260927-153000-a1b2", "size_bytes": 68800000}}
   ```

3. `POST /applications/:name/deploy` with `image` set to that reference. A
   `build` document without an `image` is `400 INVALID_REQUEST`.

Between 1 and 2 a client may ask what it need not send.
`POST /applications/:name/images/missing` takes the image's layers as diff
IDs, base layer first — `RootFS.Layers` of `docker image inspect` — 1 to 256
of them, each `sha256:<64 hex characters>`:

```json
{"layers": ["sha256:74d9…f711", "sha256:ba32…b114", "sha256:2e1f…4057"]}
```

and answers with those the server's Docker does not have, in the same order;
an empty list when it has them all. It reads the daemon and changes nothing.

```json
{"data": {"missing": ["sha256:2e1f…4057"]}}
```

A layer is on the server when an image there starts with the same diff IDs up
to and including it, so the answer is always the end of the list. The archive
sent in step 2 may then leave out the files of the other layers
(`blobs/sha256/<digest>`, as `manifest.json` in the archive lists them, in the
same order as the diff IDs) and nothing else: the manifests and the image's
configuration always travel. The answer is advice. If the archive leaves out a
layer the daemon cannot produce — the image that held it was pruned in the
meantime, or the client left out more than it was told — the upload is `409
IMAGE_INCOMPLETE`, nothing stays loaded, and the whole archive is to be sent.
An agent before 0.5 answers the question with `404 ENDPOINT_NOT_FOUND`; send
the whole archive.

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
index.html…`. With `static: {dir, fallback}` the agent looks for that file
too — `Found 200.html, the fallback page`, or `FAILED: the folder has no
200.html, which static.fallback names…` — and the proxy answers every path
that names no file with it, status 200. A digest the agent has no upload for is `404 NOT_FOUND` before
anything is recorded; a static deploy.yaml without `?static=` is `400
INVALID_REQUEST`.

Redeploy and rollback need no upload: the proxy keeps the folder of the
serving version and of the one before it, and both re-route to a kept folder.
A deployment that runs containers, for an application that was a folder,
keeps the folder it replaced — the default rollback target — and removes the
others (`Removed 1 folder of older versions`); the container deployment after
it removes that one too.
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

Every application, in the list and on its own, says whether it wants a look:

```json
{ "name": "my-api", "status": "HEALTHY", "…": "…",
  "certificate_problem": { "hostname": "www.example.com", "status": "waiting_for_dns",
    "message": "does not resolve yet; create an A record for it that points to 203.0.113.10" },
  "alert_count": 2, "alert_severity": "critical" }
```

- `certificate_problem` is `null` while no certificate of the application is
  known to be out of order. Otherwise it names one hostname — the domain, an
  alias or a redirect — with the `status` and `message` that
  [`certificates`](#certificate-status) carries for it: the worst among them,
  `waiting_for_dns` before `expiring` before `obtaining`, and of two equally
  bad the first in the order of `certificates`. `unknown` (no proxy, not
  checked yet, the proxy does not answer) is not a problem.
- `alert_count` is how many [alerts](#alerts-and-disk) about the application are
  active and `alert_severity` the highest severity among them: `warning`,
  `critical`, or `""` when there is none. The alerts themselves are in
  `GET /server`.

Both are read from what the agent has in memory; listing costs no more for
them. An agent older than 0.6 sends neither field.

Each entry of `containers` in the application detail carries what the
supervisor knows about it:

| Field | |
|---|---|
| `health` | `""` no health check configured · `unknown` not probed yet, e.g. right after an agent restart (counts as healthy) · `starting` (re)started, within its startup budget · `healthy` · `unhealthy` |
| `restarts` | Restarts performed by the supervisor over the container's lifetime |
| `crash_loop` | Restarts of this replica are currently rate-limited |
| `stopping` | `true` for a container the agent is retiring in the background: replaced by a deployment, or left over. It is out of rotation, has been sent `SIGTERM`, and is gone once its process exits or its `deploy.stop_timeout` is over. It is not a replica any more — `replicas` never counted it — and its `health` and `restarts` are not kept up. Absent from agents older than 0.6 |

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
    "token": "swk_Xk3nM9…", "applications": [], "expires_at": null
  }
}
```

Two optional fields narrow the token:

```bash
curl -X POST …/tokens -d '{"name": "ci", "role": "deploy",
  "applications": ["my-api", "web"], "expires_at": "2027-01-01T00:00:00Z"}'
```

`applications` limits a `deploy` token to those applications (see
[Authentication](#authentication)): one to 50 application names, which need
not exist yet; with another role it is `400`. The answer carries them sorted,
each once. `expires_at` is an RFC 3339 time in the future, after which the
token is `401 TOKEN_EXPIRED`; a time that has passed is `400`. Neither can be
changed afterwards. An agent before 0.6 answers a request with either field
`400 INVALID_REQUEST`, `unknown field`, and creates nothing.

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
    "last_used_at": "2026-03-01T10:42:00Z",
    "applications": ["my-api", "web"], "expires_at": "2027-01-01T00:00:00Z" }
] }
```

`last_used_at` is `null` until the token is first used, and is then kept to
the minute: it says whether a token is still in use, not what it did last; a
request refused because the token had expired is not a use.
`applications` is empty for a token that is not limited, `expires_at` is
`null` for one that does not expire. A token whose `expires_at` has passed
stays listed until it is revoked.
The root token is not listed: it is configured on the agent, not stored.

`DELETE /tokens/ci` → `204`; requests with the token are `401` from then on.
`DELETE /tokens/root` is `400`. A token may revoke itself.

`PUT /tokens/ci` changes what the body names and leaves the rest:

```bash
curl -X PUT …/tokens/ci -d '{"applications": ["my-api", "web", "worker"]}'
curl -X PUT …/tokens/ci -d '{"applications": []}'                      # lifts the limit
curl -X PUT …/tokens/ci -d '{"expires_at": "2027-04-01T00:00:00Z"}'
curl -X PUT …/tokens/ci -d '{"never_expires": true}'
```

| Field | |
|---|---|
| `applications` | Replaces the list the token is limited to; `[]` lifts the limit. Checked as on `POST /tokens`: valid names, at most 50, and only for a `deploy` token |
| `expires_at` | Moves the end; it must be in the future. A token that has expired works again from its next request |
| `never_expires` | `true` takes the end away. Not together with `expires_at` |

The answer is the token as `GET /tokens` lists it → `200`. The value and
the role do not change, and neither is accepted in the body: unknown fields
are rejected. The change holds from the token's next request. A body that
names none of the three is `400`, as is `PUT /tokens/root`; an unknown token
is `404 NOT_FOUND`. In the audit trail the action is `token.update`, the
target the token, and the detail what changed, as it was and as it is:
`applications my-api -> my-api web worker`, `applications my-api web -> all`,
`expires 2026-10-02T12:00:00Z -> 2027-04-01T00:00:00Z`, `expires … -> never`,
or `nothing changed`. An agent before 0.7 answers `404 ENDPOINT_NOT_FOUND`.

### Audit trail

Every request that changes something leaves an entry, whatever became of it.
`GET /audit` lists them, newest first:

```json
{ "data": [
  { "id": 42, "at": "2026-10-03T17:20:17.276658220Z",
    "actor": { "kind": "token", "name": "ci" },
    "address": "172.18.0.3", "forwarded_for": "203.0.113.40",
    "action": "deploy", "application": "my-api", "target": "",
    "outcome": "ok", "status": 202, "code": "", "detail": "deployment 12" }
], "more": true }
```

`more` says whether entries older than the last one match as well: `false`
on the last page, also when that page is exactly full. An agent before 0.7
leaves it out, and there a page shorter than `limit` is the only sign of
the end.

| Field | |
|---|---|
| `actor` | Who made the request: `kind` is `"token"` and `name` the token's name (`root` for the agent's own), or `kind` is `"user"` and `name` the name of a person who signed in: the e-mail address, or what `SHIPWICK_OIDC_NAME_CLAIM` names them by |
| `address` | Where the connection came from. Behind the proxy this is the proxy |
| `forwarded_for` | The client address the proxy reported in `X-Forwarded-For`, `""` without one. On a connection that does not come through the proxy the header is the caller's own word, which is why both are kept |
| `action` | What was asked for, see below |
| `application` | The application it was about, `""` for what is not about one |
| `target` | What else the request named: the job, the volume, the backup's id, the secret, the registry, the hostname of a certificate, the token |
| `outcome` | `ok`: answered 2xx — for what runs in the background, accepted. `refused`: `403`, the role or the application limit. `failed`: anything else |
| `status`, `code` | The HTTP status of the answer, and the error code of one that was not 2xx |
| `detail` | What was started (`deployment 12`, `run 4`, `backup 7`, `export 3`), or what was asked for: a created token's role, applications and expiry; the volume of a backup download; `stopped` and `overwrite` of an import; the applications of an export |

Actions: `deploy`, `redeploy`, `rollback`, `stop`, `start`,
`application.delete`, `run`, `job.run`, `image.upload`, `static.upload`,
`backup.create`, `backup.verify`, `backup.restore`, `backup.download`,
`backup.delete`, `server.backup`, `backup.adopt`, `volume.download`, `volume.restore`,
`volume.delete`, `secret.set`, `secret.delete`, `registry.login`,
`registry.logout`, `certificate.set`, `certificate.delete`, `token.create`,
`token.update`, `token.revoke`, `key.rotate`, `export.download` (`POST /export`),
`export.create` (`POST /exports`), `import`, `standby.pull`,
`standby.promote`, `audit.export`, and `signin`, `signout`, `access.grant`,
`access.revoke` and `access.signout`, see [Signing in](#signing-in). Every
endpoint that is not a `GET` is recorded, except
`validate` and `images/missing`, which change nothing; of the `GET`s, the
three that hand data out whole — a volume's archive, a backup's, and the
export of this trail — are.

Not recorded: reading; a request without a valid token, which has no actor
(it is in the agent's log, and counted by the rate limit); what the agent
does on its own — scheduled jobs, backups and exports, restarts by the
supervisor. Nothing from a request's body is kept except names: no `env`, no
secret's value, no command, no passphrase.

Query parameters, all optional, each narrowing the others:

| Parameter | |
|---|---|
| `application`, `actor` | Match exactly |
| `actor_kind` | `token` or `user` |
| `action` | An action as listed above, or the start of a family with its dot: `token.` is `token.create`, `token.update` and `token.revoke`. Several are given by repeating the parameter or separated by commas, at most 20; an entry matches when one of them does |
| `outcome` | `ok`, `refused`, `failed`; several the same way |
| `since` | A number of days or hours back (`7d`, `24h`), a date (`2026-09-01`, from its start in UTC) or an RFC 3339 time |
| `limit` | 1 to 500, 50 by default |
| `before` | The `id` of the last entry of the page before: continues after it |

A value that is none of these is `400 INVALID_REQUEST`. An agent before 0.7
does not know `actor_kind`, `action` and `outcome` and ignores them: a
client that sends one and finds no `more` in the answer has been given the
unfiltered trail.

Entries are kept for a year, and the newest 100,000 at most.

`GET /audit/export?format=csv` streams everything that matches — the same
filters, without `limit` and `before`, which are `400` here — newest first,
as it is read from the database, 500 entries at a time:

```
id,at,actor_kind,actor,address,forwarded_for,action,application,target,outcome,status,code,detail
42,2026-10-03T17:20:17Z,token,ci,172.18.0.3,203.0.113.40,deploy,my-api,,ok,202,,deployment 12
41,2026-10-03T17:19:02Z,token,root,172.18.0.3,203.0.113.9,token.revoke,,'=HYPERLINK(…),failed,400,INVALID_REQUEST,
```

| `format` | `Content-Type` | |
|---|---|---|
| `csv` | `text/csv; charset=utf-8` | A header row, then one row per entry with the columns above; `at` in UTC to the second. A cell that begins with `=`, `+`, `-`, `@`, a tab or a carriage return gets an apostrophe in front, so that a spreadsheet shows it as text instead of running it as a formula |
| `json` | `application/x-ndjson` | One entry per line, the object `GET /audit` returns, values as recorded. Nothing matching is an empty body |

`format` is required. The response carries
`Content-Disposition: attachment; filename="shipwick-audit-<time>.csv"`
(`.ndjson`) and is not wrapped in `data`. An export that breaks off after
its first byte cannot change its status any more: it announces the trailer
`X-Shipwick-Export-Error` and sets it, as [`POST /export`](#export-import-and-the-standby)
does, and a client must treat a body with that trailer as incomplete.
Errors before the first byte are regular error responses. The export is
recorded as `audit.export` with the format, the filters and the number of
entries in `detail` (`csv, action token., since 2026-01-01T00:00:00Z, 214
entries`); the entry is written when the export ends and is not part of it.

### Signing in

With an OpenID Connect provider configured on the agent
(`SHIPWICK_OIDC_ISSUER`, `SHIPWICK_OIDC_CLIENT_ID`,
`SHIPWICK_OIDC_CLIENT_SECRET`), a person signs in at the provider and the
agent issues a **session**: a credential that is presented exactly like a
token, `Authorization: Bearer sws_…`, and is the same kind of caller —
`kind: "user"`, `name` the e-mail address, a role, optionally a list of
applications, `expires_at`. Everything under [Authentication](#authentication)
holds for it: the role table, the application limit, `GET /server` → `token`,
the audit trail. Tokens are not affected.

The flow is the authorization-code flow with PKCE, and it is the dashboard's
server that drives it; the client secret never leaves the agent. Two
endpoints take no token.

`GET /auth` says where to send a browser. All of it is public:

```json
{ "data": { "configured": true,
            "issuer": "https://accounts.example.com",
            "authorization_endpoint": "https://accounts.example.com/authorize",
            "client_id": "shipwick",
            "scopes": ["openid", "email", "profile"],
            "redirect_uri": "https://dashboard.example.com/auth/callback" } }
```

Without a provider it answers `{"configured": false, …}` with the other
fields empty; `502 SIGN_IN_UNAVAILABLE` when the provider's discovery
document cannot be read. `redirect_uri` is `https://` +
`SHIPWICK_DASHBOARD_DOMAIN` + `/auth/callback`, from the agent's
configuration and never from a request.

The caller generates three random values of 32 bytes each, base64url
without padding — `state`, `nonce` and the PKCE `code_verifier` — keeps them
with the browser, and redirects it to `authorization_endpoint` with
`response_type=code`, `client_id`, `redirect_uri`, `scope` (the scopes joined
by spaces), `state`, `nonce`, `code_challenge` (the base64url SHA-256 of the
verifier) and `code_challenge_method=S256`. When the provider sends the
browser back with `code` and `state`, the caller compares `state` with what
it kept and hands the rest to the agent:

```http
POST /api/v1/auth/exchange
Content-Type: application/json

{ "code": "…", "code_verifier": "…", "nonce": "…",
  "redirect_uri": "https://dashboard.example.com/auth/callback" }
```

```json
{ "data": { "session": "sws_Zm9v…",
            "identity": { "kind": "user", "name": "ada@example.com", "role": "deploy",
                          "applications": ["my-api"], "expires_at": "2026-10-03T19:00:00Z" } } }
```

`session` is in this response and nowhere else; the agent keeps its SHA-256.
The agent redeems the code at the provider's token endpoint with the client
secret and the verifier, and believes the ID token only if it is signed by
one of the provider's keys (RS256 or ES256), was issued by the configured
issuer for this client, is within its time and at most ten minutes old, and
carries `nonce`. It accepts each nonce once. An `email_verified: false` is
refused; a provider that does not send the claim is taken at its word. Then
the [access rules](#access-rules) are asked what the person may do.

The person's name — `identity.name`, the actor in the audit trail, `by` on
a deployment — is the `email` claim in lowercase, or the value of the claim
the agent is configured with (`SHIPWICK_OIDC_NAME_CLAIM`; `sign_in.name_claim`
in `GET /server`), which is kept as the provider wrote it: at most 254
characters of letters, digits and `. _ % + ' @ | : = # ~ -`. With another
claim than `email`, `email_verified` is not looked at.

With Microsoft Entra's issuer for several tenants
(`https://login.microsoftonline.com/organizations/v2.0`, or `common`), whose
discovery document names `https://login.microsoftonline.com/{tenantid}/v2.0`
as its issuer, "issued by the configured issuer" reads: the token's `iss` is
that template with the token's own `tid` in the placeholder's place, `tid`
is a tenant id, a key that names the issuer it signs for names that one,
and `tid` is among `SHIPWICK_OIDC_TENANTS`. No other provider's discovery
document may name an issuer other than the configured one.

| HTTP | `code` | |
|---|---|---|
| 400 | `INVALID_REQUEST` | A field is missing or malformed (`code_verifier`: 43 to 128 characters; `nonce`: at least 22 of base64url), or `redirect_uri` is not the configured one (`details: {redirect_uri}`) |
| 401 | `SIGN_IN_FAILED` | `details.reason`: `code_rejected` — the provider refused the code: used before, expired, issued to another client or for another verifier; `invalid_id_token` — signature, issuer, audience or times; `nonce_mismatch` — the ID token answers another sign-in; `nonce_reused` — this sign-in was completed before; `email_missing` — the ID token names no usable address; `email_not_verified`; `name_missing` — people are named by another claim than `email`, and the ID token lacks it or holds something that cannot be a name (`details.claim` says which claim); `tenant_not_allowed` — the account's tenant is not among `SHIPWICK_OIDC_TENANTS` |
| 403 | `ACCESS_NOT_GRANTED` | The person is who they say, and no rule gives them a role; `details: {name, email}`, both the person's name (`email` is kept for clients from before 0.7). The message is written to be forwarded to an admin |
| 409 | `SIGN_IN_NOT_CONFIGURED` | The agent has no provider |
| 429 | `RATE_LIMITED` | Failed sign-ins count like wrong tokens, see [Authentication](#authentication); a limited address is answered without the provider being asked |
| 502 | `SIGN_IN_UNAVAILABLE` | The provider could not be reached, did not answer like one, calls itself by another issuer, or refused the agent's client id or secret |

A session lasts ten hours from the sign-in, whatever its use. It rests on
what the rules gave the person then: every request asks the rules again, and
a session whose answer has changed is ended with that request, not at its
expiry. A session that no longer works says why, to its holder only — like
an expired token, and like it not counted by the rate limit:

```json
{ "error": { "code": "SESSION_ENDED",
             "message": "no rule on this server gives ada@example.com a role any more. An admin grants one with: shipwick access grant ada@example.com --role read",
             "details": { "name": "ada@example.com", "reason": "rule_removed" } } }
```

`reason` is `rule_removed`, `access_changed` (the rules give the person
something else now: sign in again to get it), `signed_out` (an admin ended
it) or `sign_in_changed` (the agent now names people by another claim than
when the session began, or no longer accepts the tenant it came from). A
session past its time is `401 SESSION_EXPIRED` with
`details: {name, expired_at}`. A session nobody issued is a wrong token:
`401 UNAUTHORIZED`, counted.

`DELETE /auth/session`, sent with the session itself, forgets it → `204`.
Sent with a token it is `400`: a token is revoked, not signed out.

Recorded in the [audit trail](#audit-trail): `signin` (the actor is the
person; `ok` with what they got in `detail`, or `refused` with
`ACCESS_NOT_GRANTED`; a sign-in the provider did not vouch for has no actor
and is not recorded) and `signout`.

### Access rules

Who may sign in, and as what. `GET /access/rules`:

```json
{ "data": [
  { "id": 1, "kind": "email",  "subject": "ada@example.com", "role": "admin",  "applications": [],
    "created_at": "2026-10-03T09:00:00Z", "created_by": "root" },
  { "id": 2, "kind": "group",  "subject": "backend",         "role": "deploy", "applications": ["my-api", "worker"],
    "created_at": "2026-10-03T09:01:00Z", "created_by": "ada@example.com" },
  { "id": 3, "kind": "domain", "subject": "example.com",     "role": "read",   "applications": [],
    "created_at": "2026-10-03T09:02:00Z", "created_by": "root" }
] }
```

| `kind` | `subject` | Matches |
|---|---|---|
| `email` | an address, lowercase | the person whose name is that address, in any case |
| `group` | a group's name as the provider sends it, case and all | a person whose groups claim (`SHIPWICK_OIDC_GROUPS_CLAIM`, `groups` by default) lists it. Not read where every tenant may sign in (`SHIPWICK_OIDC_TENANTS=*`) |
| `domain` | the part after the `@` | every name that is an address at exactly that domain; a subdomain is another domain |
| `name` | a name as the provider's claim holds it, case and all | the person whose name is exactly that: for names that are not addresses (`SHIPWICK_OIDC_NAME_CLAIM`) |

The name is the `email` claim unless the agent is configured otherwise; a
name that does not read as an address is matched by `name` and `group`
rules only.

The most specific kind that matches decides, and the others are not looked
at: the rule for the name, else the rule for the address, else the rules
for the person's groups, else the rule for the domain. A rule for an address can therefore give one person
less than their group has. Of several groups the highest role counts; where
that is `deploy`, a group without `applications` lifts the limit, and the
lists of the others add up. Nobody without a matching rule gets in.

`POST /access/rules` with `{"kind", "subject", "role", "applications"?}`
creates the rule → `201`, or replaces the one with the same kind and subject
→ `200`; either way the rule is the answer. `applications` is for `deploy`
only, as on a token. At most 200 rules. `DELETE /access/rules/:id` removes
one → `204`. Rules can be written before a provider is configured.

`GET /access/sessions` lists who is signed in right now:

```json
{ "data": [ { "id": 7, "email": "ada@example.com", "role": "deploy", "applications": ["my-api"],
              "created_at": "2026-10-03T09:00:00Z", "expires_at": "2026-10-03T19:00:00Z",
              "last_used_at": "2026-10-03T09:41:00Z" } ] }
```

`email` is the person's name: the address, or with another name claim what
that claim holds. The field keeps its name from when there was only one.

`DELETE /access/sessions/:email` ends every session of that person →
`{"sessions": 2}`, the number ended. The path takes the name as the list
shows it; an address is brought to lowercase where people are named by
`email`, and any other name must match exactly. It signs out and does not
keep out: while a rule covers the person, they can sign in again.

Audit actions: `access.grant` (target: the subject as the CLI writes it —
`ada@example.com`, `group:backend`, `*@example.com`, `name:svc-deploy`;
detail: the role and applications), `access.revoke`, `access.signout`
(target: the name). A `signin` from a provider with tenants has
`tenant <id>` at the end of its detail.

### Secrets

A secret is a value for `${NAME}` in the `env` values of a deploy.yaml and in
the passwords of its `proxy.basic_auth`, kept on the server so that no client
has to hold it.

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

### Registry credentials

A credential the agent sends when it pulls an image from a private registry.

```bash
curl -X PUT …/registries/ghcr.io -d '{"username": "octocat", "password": "ghp_…"}'
```

`204 No Content`, whether the credential was created or replaced. `:registry`
is a hostname with an optional port, as image references name it —
`ghcr.io`, `registry.example.com:5000` — compared in lower case;
`index.docker.io` and `registry-1.docker.io` are `docker.io`. A scheme or a
path is `400 INVALID_REQUEST`. The username is at most 255 characters without
a colon or control characters; the password at most 16 KB, not empty, without
NUL bytes; unknown fields are rejected. At most 50 registries are stored; the
51st is `400 INVALID_REQUEST`.

Before anything is stored the agent has the Docker daemon check the
credential against the registry (the Engine API's login, which keeps
nothing). A registry that says no, or that cannot be reached within 30
seconds, is `400`:

```json
{ "error": { "code": "REGISTRY_LOGIN_FAILED",
             "message": "ghcr.io refused the login: denied: denied",
             "details": { "registry": "ghcr.io", "refused": true } } }
```

`message` ends with the registry's own answer. `refused` is `false` when the
registry could not be asked — a name that does not resolve, a registry that
is down — and the message then says why.

The password is written encrypted (AES-256-GCM, bound to the registry's
name) and is never returned, logged or repeated in an error.
`GET /registries` lists what is stored, by name:

```json
{ "data": [
  { "registry": "ghcr.io", "username": "octocat",
    "created_at": "2026-03-01T10:00:00Z", "updated_at": "2026-03-01T10:42:00Z" }
] }
```

`DELETE /registries/ghcr.io` → `204`; `404 NOT_FOUND` when no credential is
stored for it.

Every pull — a deployment, a rollback, a job, a replica whose image is gone —
looks the credential up by the image's registry. Without one, the Docker
configuration file on the server is consulted, as before. A pull the registry
refuses for authentication fails the deployment with an `error` that names
the command: `pull access denied for ghcr.io/company/api: run shipwick
registry login ghcr.io (or check the image name)`.

### Key rotation

```bash
curl -X POST …/server/rotate-key
```

The agent generates a new encryption key, re-encrypts every stored `env`
value, secret and registry password under it in one transaction, and uses it
from then on. No application is touched. `200`:

```json
{ "data": { "values": 3, "deployments": 12,
            "key_source": "file", "key_file": "/var/lib/shipwick/encryption.key" } }
```

`values` counts the re-encrypted secrets, registry passwords, certificate
keys and the references kept for deployments (one for each deployment whose
document referred to a secret, see
[The document of what runs](#the-document-of-what-runs)),
`deployments` the deployment records whose `env` was re-encrypted.
`key_source` is `file` when the agent keeps the key in its data directory;
it has then replaced `key_file`, and the key is not in the response.

With the key in `SHIPWICK_ENCRYPTION_KEY`, `key_source` is `environment`: the
agent cannot change its own environment, so the response carries the new key
— this once — for the operator to put there, and `key_file` is a copy the
agent keeps in its data directory until it has been started with the new key:

```json
{ "data": { "values": 3, "deployments": 12, "key_source": "environment",
            "key": "5f0c…64 hexadecimal characters",
            "key_file": "/var/lib/shipwick/encryption.key.new" } }
```

The agent keeps working with the new key. Started again with the old one in
its environment, it refuses to start and says where the new key is. Until it
has been restarted with the new key, another rotation is
`409 KEY_ROTATION_PENDING` with `details: {key_file}`.

The rotation is recorded in the agent's log with the name of the token that
asked for it. It is not an application event, and the key is never logged.

### Certificates

A certificate of the operator's own, for hostnames whose certificate does not
come from the proxy's authority. It belongs to the server: every hostname it
covers, of any application, is served with it, is not asked of any authority
and does not wait for DNS.

```bash
curl -X PUT …/certificates/example.com \
  -d '{"certificate": "-----BEGIN CERTIFICATE-----\n…", "key": "-----BEGIN PRIVATE KEY-----\n…"}'
```

`:hostname` is a hostname as in deploy.yaml, lower-cased; a wildcard
certificate is stored under the wildcard, `/certificates/*.example.com`.
`certificate` is the chain in PEM, the hostname's own certificate first;
`key` its private key in PEM, without a passphrase. Each is at most 64 KB,
the body at most 513 KB; unknown fields are rejected. At most 50 certificates
are stored; the 51st is `400 INVALID_REQUEST`.

The agent checks before it stores, and answers `400 INVALID_CERTIFICATE` with
the reason as `message`: the chain is not PEM or holds something other than
certificates; a certificate in it cannot be read; the key is not PEM, is
protected by a passphrase, or does not belong to the first certificate; the
certificate has no DNS names, does not cover the hostname (`the certificate
does not cover example.org: it is for example.com, *.example.com`), has
expired (`the certificate expired on 2026-09-01`) or is not valid yet. A
wildcard name covers exactly one label, and a wildcard hostname is covered
only by the same wildcard. Whether the chain leads to an authority browsers
trust is not checked: a private authority's certificate is a use of this.

The answer to a `PUT`, and each entry of `GET /certificates` (in hostname
order):

```json
{ "data": {
  "hostname": "example.com",
  "subjects": ["example.com", "*.example.com"],
  "issuer": "Corp Issuing CA",
  "not_before": "2026-09-01T00:00:00Z",
  "not_after": "2027-09-01T00:00:00Z",
  "created_at": "2026-10-03T10:00:00Z",
  "updated_at": "2026-10-03T10:00:00Z"
} }
```

`subjects` are the DNS names of the chain's first certificate, `issuer` its
issuer's common name (the organization, when it has none). The key is written
encrypted (AES-256-GCM, the hostname as additional data) and is never
returned, logged or repeated in an error; the PEM is not returned either.
The proxy is updated before the request is answered. An expired certificate
stays listed, and served, until it is replaced or removed.

`DELETE /certificates/example.com` → `204`; `404 NOT_FOUND` for a hostname
nothing is stored under. The hostnames the certificate covered go back to
certificates the proxy obtains, and to waiting for DNS. A deployed wildcard
hostname that loses its certificate this way is no longer served while the
agent has no DNS challenge; a new deployment that names one is refused:

```json
{ "error": { "code": "INVALID_CONFIG", "message": "invalid deploy.yaml",
             "details": { "fields": [ {
               "field": "domain",
               "message": "a certificate for a wildcard is issued only through a DNS record, and the agent is not set up for that",
               "expected": "SHIPWICK_CLOUDFLARE_API_TOKEN on the agent, or a certificate of your own: shipwick cert set '*.example.com' --cert fullchain.pem --key privkey.pem" } ] } } }
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
- `unenforced_limits` is present when the server's Docker has no cgroup
  controller for a limit — rootless Docker without delegation — and lists
  which: `["memory", "cpu"]`. The limits are then what `deploy.yaml` asks
  for and nothing holds a replica to; with both listed on a rootless daemon,
  the usage is that of everything the daemon runs, not the replica's. Absent
  where Docker applies the limits, and from an agent older than 0.8.
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

### Alerts and disk

`GET /server` carries the conditions that hold right now, and the disk two of
them are about:

```json
{
  "data": {
    "…": "…",
    "disk": { "total_bytes": 42949672960, "used_bytes": 37580963840 },
    "alerts": [
      { "kind": "disk", "severity": "warning", "application": "", "replica": 0,
        "message": "The server's disk is 87% full (5 GB of 40 GB free). See what takes the space with: docker system df",
        "since": "2026-03-01T09:41:30Z" },
      { "kind": "memory", "severity": "warning", "application": "my-api", "replica": 1,
        "message": "my-api replica 1 is at 93% of its memory limit (240 MB of 256 MB). At the limit it is killed and restarted; raise resources.memory in deploy.yaml, or watch it with: shipwick status my-api",
        "since": "2026-03-01T09:58:00Z" }
    ]
  }
}
```

- `kind` is `memory`, `disk`, `restarts`, `unhealthy` or, from 0.8, `docker`;
  `severity` is `warning` or `critical` (`disk` and `unhealthy` become
  critical; `docker` always is).
  `memory` and `restarts` name an `application` and a `replica`; `unhealthy`
  an application, with `replica` 0; `disk` and `docker` neither.
- `docker` is raised when the Docker daemon has not answered for 30 seconds
  and cleared by its first answer. While it stands, `GET /server` itself is
  answered `503 RUNTIME_UNAVAILABLE`, so this alert is read from the webhook
  and from [`/metrics`](#prometheus-metrics), which never asks Docker.
- Only active alerts are listed, oldest first; `[]` when there are none.
  `since` is when the alert was raised, and stays when a warning turns
  critical. They are kept in memory: an agent that restarts raises again
  what still holds.
- `disk` is the filesystem that holds the agent's data directory.
  `used_bytes / total_bytes` is the percentage `df` shows: `total_bytes` leaves
  out the blocks reserved for root. It is `null` where the agent cannot
  measure it — a development build off Linux.
- An alert about an application is also in its events, with type `alert`:
  level `warn` or `error` when raised, `info` when cleared.
- An agent older than 0.5 sends neither field.

The webhook gets two more events, `alert.raised` and `alert.cleared`. Their
JSON form has one more field, absent from the other events:

```json
{
  "event": "alert.raised",
  "application": "my-api",
  "deployment_id": null,
  "version": "",
  "message": "my-api replica 1 is at 93% of its memory limit (240 MB of 256 MB). …",
  "at": "2026-03-01T09:58:00Z",
  "server": "vps-1",
  "alert": { "kind": "memory", "severity": "warning", "replica": 1 }
}
```

`application` is empty for a `disk` alert. `alert.raised` is sent again when a
warning turns critical; `alert.cleared` carries the severity the alert had.

### Network

`GET /server` says how the server reaches what is outside it:

```json
{
  "data": {
    "…": "…",
    "network": {
      "proxy": "proxy.example.com:3128",
      "docker_proxy": false,
      "ca_file": true,
      "dns_resolvers": ["system"],
      "acme_directory": "https://ca.example.internal/acme/acme/directory"
    }
  }
}
```

- `proxy` is the proxy the agent's own requests go through — the webhook, the
  bucket — as `host:port`, from `HTTPS_PROXY` or `HTTP_PROXY`; `""` when there
  is none. The proxy's URL may hold a password, which is never part of it.
- `docker_proxy` says whether the Docker daemon has a proxy configured.
  Images are pulled by the daemon: `proxy` set and `docker_proxy` false is the
  usual reason a pull fails behind a proxy.
- `ca_file` is true when certificate authorities of the operator's own are
  trusted in addition to the system's (`SHIPWICK_CA_FILE`).
- `dns_resolvers` are the name servers asked whether a hostname points at the
  server: addresses with their port, or `["system"]`. `[]` means the public
  ones.
- `acme_directory` is the ACME server certificates are obtained from; `""`
  is Let's Encrypt.
- An agent older than 0.6 sends no `network`.

A pull that fails before the registry answers — no route, a proxy that
refuses, a certificate the daemon does not trust — fails the deployment with
the daemon's message followed by what to change on the server; it has no
error code of its own.

### A newer release

`GET /server` says whether a newer release than the agent exists. The agent
asks GitHub itself, once a day (handbook §4):

```json
{
  "data": {
    "…": "…",
    "agent_version": "v0.7.0",
    "update": {
      "enabled": true,
      "latest_version": "v0.7.1",
      "checked_at": "2026-10-04T09:00:00Z",
      "available": true
    }
  }
}
```

- `enabled` is false when the agent was told not to ask
  (`SHIPWICK_UPDATE_CHECK=off`); the other three are then `""`, `null` and
  `false`.
- `latest_version` is the tag of the latest release that is not a
  pre-release, with its `v`; `""` until the agent has been able to ask —
  the first seconds after its first start, or for as long as it cannot reach
  GitHub.
- `checked_at` is when GitHub last answered, `null` until then. An attempt
  that failed does not move it: a server that lost its way out keeps the last
  answer and the time it is from.
- `available` is true when `latest_version` is newer than `agent_version` by
  semantic versioning. It is false for an agent that runs the latest release,
  a pre-release of a later one, or a development build.
- An agent older than 0.7 sends no `update`.

Role `read`, like the rest of `GET /server`. Nothing can be asked of the
agent to make it check now.

### Prometheus metrics

`GET /metrics` — not under `/api/v1` — with `Authorization: Bearer <token>`,
role `read`. The answer is `text/plain; version=0.0.4`, the Prometheus text
exposition format; errors (`401`, `403`, `429`) are the JSON envelope as
everywhere else.

| Series | Type | |
|---|---|---|
| `shipwick_agent_info{version}` | gauge | Always 1 |
| `shipwick_application_status{application, status}` | gauge | 1 for the current status, in lower case: `healthy`, `degraded`, `down`, `crash_loop`, `stopped`, `deploying`, `failed` |
| `shipwick_application_replicas{application, state}` | gauge | `state` is `desired`, `running` or `healthy` |
| `shipwick_replica_cpu_ratio{application, replica}` | gauge | CPU in cores, from the last sample: 1 is one core kept busy |
| `shipwick_replica_memory_bytes{application, replica}` | gauge | Working set, from the last sample |
| `shipwick_replica_memory_limit_bytes{application, replica}` | gauge | Absent for a replica without a limit |
| `shipwick_replica_restarts_total{application, replica}` | counter | Restarts by the supervisor; starts again at 0 with each deployment |
| `shipwick_deployments_total{application, status}` | counter | Finished deployments; `status` is `succeeded`, `failed` or `rolled_back` |
| `shipwick_deployment_last_duration_seconds{application}` | gauge | From start to completion of the most recently completed deployment |
| `shipwick_disk_bytes{state}` | gauge | `state` is `total` or `used`, as `disk` above; absent where it cannot be measured |
| `shipwick_alerts{kind, severity}` | gauge | Number of active alerts; every combination is present, 0 when none |

- Every family has its `# HELP` and `# TYPE` lines, families come in the
  order above and series sorted by application and label: two scrapes of the
  same state are the same bytes.
- A scrape reads the database in one transaction and never asks Docker.
  Status and the running and healthy counts are as the supervisor saw them at
  its last pass, a second ago at most; during a deployment they are those
  from before it began. CPU and memory are the last 30-second sample of each
  replica; a replica without a sample in the last 75 seconds — stopped, or
  started less than a minute ago — has no CPU and memory series.
- In the first second after the agent starts, an application the supervisor
  has not looked at yet has only its `desired` replicas and no status.
- `rate(shipwick_deployments_total{status="failed"}[1d])` behaves: the three
  outcomes only ever grow, and are present from an application's first
  deployment. Deleting an application removes its series.

### Traffic

`GET /applications/:name/traffic?since=1h`

What the proxy's access log says about the application's requests.

```json
{
  "data": {
    "application": "my-api", "since": "2026-03-01T09:00:00Z", "step_seconds": 60,
    "totals": {
      "requests": 12480, "status_2xx": 12300, "status_3xx": 40, "status_4xx": 120, "status_5xx": 20,
      "bytes": 123456789, "p50_ms": 12.4, "p95_ms": 48, "p99_ms": 210.5
    },
    "points": [
      { "t": "2026-03-01T09:00:00Z",
        "requests": 208, "status_2xx": 205, "status_3xx": 1, "status_4xx": 2, "status_5xx": 0,
        "bytes": 2057600, "p50_ms": 11.9, "p95_ms": 45.2, "p99_ms": 180 }
    ]
  }
}
```

- `since` is `1h` (the default), `24h` or `7d`; anything else is
  `400 INVALID_REQUEST`. The agent picks the step: `60`, `300` and `3600`
  seconds respectively, so a series is at most a few hundred points. The
  window starts on a step boundary, so `since` is up to one step earlier than
  asked.
- A request belongs to the application whose `domain`, alias or redirect it
  was sent to; where several applications share a hostname by `path`, to the
  one with the longest path the request is under. Requests the proxy answered
  by itself count too: a redirect's `308`, the `503` of a stopped
  application, the files of a static one. Requests for the agent's and the
  dashboard's own hostnames are not recorded.
- `bytes` are response bodies as sent, after compression. Statuses outside
  200–599 are in `requests` only.
- The percentiles are estimated from a histogram of the proxy's durations —
  from the first byte of the request to the last of the response — with
  bounds at 1, 2.5, 5, 10, 25, 50, 100, 250, 500 ms and 1, 2.5, 5, 10, 30 s,
  and are as exact as a bucket is wide; a request slower than 30 s reads
  `30000`. They are `0` when there were no requests.
- `t` is the start of the step. A step without a request has no point: the
  series is sparse, and a missing point is zero. The current minute is
  included; minutes that have ended are kept for seven days.
- An application without a domain, or one nobody has asked anything of,
  answers zero totals and no points. `409 TRAFFIC_UNAVAILABLE` when there is
  no access log to read: the agent has no proxy, or the proxy is not the
  `caddy` container of the agent's compose project.

### Requests

`GET /applications/:name/requests?tail=50`

```json
{
  "data": [
    { "time": "2026-03-01T10:00:00.412365Z", "method": "GET", "path": "/api/users",
      "status": 200, "duration_ms": 12.431, "bytes": 2048, "client": "203.0.113.7" }
  ]
}
```

The application's most recent requests, oldest first; `tail` is at most
`200`, which is all the agent keeps per application — in memory, so the list
starts empty after an agent restart. `path` carries no query string and
nothing of the request's or the response's headers is kept: the proxy does
not write them to its log in the first place. `client` is the address the
proxy saw. Attribution and `409 TRAFFIC_UNAVAILABLE` are as for
[Traffic](#traffic).

### Certificate status

The application detail carries the certificate the proxy presents for each
hostname the application answers to — its domain, aliases and redirects:

```json
"certificates": [
  { "hostname": "api.example.com", "status": "ok", "issuer": "Let's Encrypt E7",
    "not_after": "2026-05-20T08:00:00Z", "message": "" },
  { "hostname": "www.example.com", "status": "waiting_for_dns", "issuer": "", "not_after": null,
    "message": "does not resolve yet; add an A record: www.example.com → 203.0.113.10 (DNS only, not proxied)" }
]
```

| `status` | Meaning |
|---|---|
| `ok` | The proxy presents a certificate for this name with more than 14 days left |
| `expiring` | 14 days or less left, or already expired; `message` says how long (`expires in 9 days, on 2026-03-10`). The proxy renews with a third of the lifetime to go, so this means renewal is failing |
| `obtaining` | The proxy serves the hostname but has no certificate for it yet: the handshake fails, or presents one for another name |
| `waiting_for_dns` | The hostname does not point at this server and is not handed to the proxy; `message` is the DNS gate's reason, with the record to create |
| `unknown` | Not looked at yet, the proxy did not answer, or the agent has no proxy; `message` says which |

`issuer` and `not_after` are set once a certificate has been seen, `message`
for every status but `ok`. The agent looks once a minute per hostname — every
ten seconds while it is `obtaining` — by connecting to the proxy with the
hostname as the server name; it reports the certificate and does not verify
its chain. A certificate that lives less than 56 days is `expiring` in the
last quarter of its lifetime rather than the last 14 days. The list is empty
for an application without a domain.

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
