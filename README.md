# Shipwick

**Production deployments, without Kubernetes.**

Shipwick runs your Docker applications on your own server — health checks, zero-downtime
deploys, rollbacks, resource limits and HTTPS — from one small config file.

```yaml
# deploy.yaml
name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
domain: api.example.com
replicas: 2
```

```bash
deployctl deploy
```

> **Status: 0.x.** Shipwick is young. Before 1.0, a minor version may still
> change the API, `deploy.yaml` or the on-disk format; the
> [changelog](CHANGELOG.md) will say so when it happens, and how to upgrade.

---

## 1. What is Shipwick?

A single agent that runs on your VPS and turns a `deploy.yaml` into running,
supervised containers. It is for developers and small teams running 1–20
applications on Hetzner, DigitalOcean, OVH, EC2 or similar, who want Docker in
production without hand-rolling deploy scripts, restart logic, health checks,
rollbacks and reverse-proxy config.

- **One binary, one SQLite file.** No cluster, no control plane, no external database.
- **Docker is the runtime.** Anything that runs with `docker run` runs on Shipwick.
- **Safe by default.** A failed deployment never takes down the version that works.

## 2. Why not Kubernetes?

Kubernetes solves scheduling across fleets of machines. If you have one server,
or three, you inherit all of its concepts — pods, services, ingresses,
controllers, CRDs, a control plane to keep alive — and use almost none of its
power.

| | Kubernetes | Shipwick |
|---|---|---|
| Unit of thought | Pod, Deployment, Service, Ingress, … | Application |
| To run it | A cluster | One process |
| State | etcd | A SQLite file |
| Config for one app | Several manifests | ~10 lines of YAML |
| Multi-node scheduling | Yes | No — by design |

Shipwick is not a smaller Kubernetes. It deliberately does not do multi-node
scheduling, service meshes, or custom resources. If you need those, you need
Kubernetes.

## 3. Architecture

```text
                 deployctl / dashboard
                          │  HTTP + token
                          ▼
                    Shipwick Agent
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
        Docker          Caddy           SQLite
          │
          ▼
      Containers
```

The agent owns the deployment lifecycle. Every deployment is an immutable record
moving through a state machine:

```text
PENDING → BUILDING → STARTING → HEALTH_CHECKING → HEALTHY → ACTIVE
                          any step can → FAILED
```

Replicas are replaced one at a time, each new one only after it proved healthy;
if a deployment fails half-way, the previous version is restored.
Details: [docs/architecture.md](docs/architecture.md) · API: [docs/api.md](docs/api.md).

## 4. Installation

On the server (Linux, with Docker already installed), as root:

```bash
curl -fsSL https://get.shipwick.com | sh
```

It asks for two optional hostnames — one for the API, one for the dashboard —
and then sets up three containers in `/opt/shipwick`: the **agent**, **Caddy**,
and the **dashboard**. When it is done it prints the API token, once.

```text
✓ Docker 29.8.0 with Compose 5.5.1
✓ Installed /opt/shipwick/compose.yml
✓ Wrote /opt/shipwick/.env
✓ Started the Shipwick services
✓ The agent is healthy

Shipwick is running.

  From your laptop or CI:   deployctl login --url https://agent.example.com
  Dashboard:                https://dashboard.example.com
```

Point DNS for those hostnames, and for your applications' domains, at the
server; open ports 80 and 443 and nothing else. Certificates take care of
themselves. Running the installer again upgrades; it never touches an existing
`.env`, and so never changes your token. What it does, step by step, and how to
test it: [scripts/README.md](scripts/README.md).

On your laptop or in CI, only the CLI:

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli
```

Everything the installer fetches comes from one
[release](https://github.com/shipwick/shipwick/releases) and is verified against
its checksums; the images are pinned to that release, so a server runs the
version it installed until you run the installer again. A specific version:
`curl -fsSL https://get.shipwick.com | SHIPWICK_VERSION=v0.1.0 sh`.

> **From source**, without a release: build the images on the server —
> `docker build -t ghcr.io/shipwick/agent .` and
> `docker build -t ghcr.io/shipwick/dashboard dashboard/` — then run
> `sh scripts/install.sh` from the checkout; build the CLI with `make build`.

**Without the installer**, the same setup is one file:
[configs/compose.production.yml](configs/compose.production.yml), plus a `.env`
with `SHIPWICK_AGENT_TOKEN` and the hostnames.

**No hostname to spare for the API?** Leave `SHIPWICK_AGENT_DOMAIN` empty,
publish the API on the server's loopback only (see the comment in the compose
file), and reach it through `ssh -L 9000:127.0.0.1:9000 user@server`.

The agent also runs as a plain binary on Linux (`make build`), next to a Caddy
installed on the host: `SHIPWICK_CADDY_ADMIN=http://127.0.0.1:2019`.

| Variable | Default | |
|---|---|---|
| `SHIPWICK_AGENT_TOKEN` | generated | API bearer token, min. 16 characters |
| `SHIPWICK_LISTEN_ADDR` | `127.0.0.1:9000` | Loopback by default, on purpose |
| `SHIPWICK_DATA_DIR` | `/var/lib/shipwick` | SQLite database and token hash |
| `SHIPWICK_DOCKER_NETWORK` | `shipwick` | Network application containers join |
| `SHIPWICK_CADDY_ADMIN` | — | Caddy's admin endpoint: `unix//run/caddy/admin.sock` (recommended) or `http://127.0.0.1:2019`. Unset: domains are recorded but not served |
| `SHIPWICK_AGENT_DOMAIN` | — | Serve the agent's API over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_DOMAIN` | — | Serve the dashboard over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_UPSTREAM` | `dashboard:3000` | Where Caddy reaches the dashboard |
| `SHIPWICK_LOG_LEVEL` | `info` | `debug` `info` `warn` `error` |
| `SHIPWICK_LOG_FORMAT` | `text` | `text` `json` |
| `DOCKER_HOST`, `DOCKER_CONFIG` | Docker defaults | Standard Docker variables are honored |

## 5. Quick start

With the CLI installed (above; on Windows, download `deployctl_windows_amd64.exe`
from the [releases](https://github.com/shipwick/shipwick/releases)), in your
application's repository:

```bash
deployctl login     # once: agent URL + token
deployctl init      # writes deploy.yaml
deployctl deploy
```

```text
Deploying my-api...

✓ Validated deploy.yaml
✓ Pulled image ghcr.io/company/my-api:1.4.2
✓ Started 1 container
✓ Replica 1 passed health checks
✓ Replica 1/2 is serving 1.4.2; its 1.4.1 predecessor is retired
✓ Replica 2 passed health checks
✓ Replica 2/2 is serving 1.4.2; its 1.4.1 predecessor is retired
✓ Routed https://api.example.com to 2 replicas
✓ Deployment successful

my-api 1.4.2  deployed in 6.1s
2/2 replicas healthy
https://api.example.com
```

Then `deployctl status`, `deployctl logs -f`, `deployctl ps`. The full command
reference, and how deployctl finds the agent (SSH tunnel, CI variables), is in
[cli/README.md](cli/README.md).

In CI, keep `deploy.yaml` in the repository and pass the image you just built:

```bash
deployctl deploy --image ghcr.io/company/my-api:$GIT_SHA
```

It exits non-zero if the deployment fails, so it works as a pipeline gate.

### Dashboard

The same things in a browser, at the dashboard hostname you gave the installer
(or http://localhost:3000 in development): every application with its status,
live CPU and memory, replicas and their health, deployment history with the
origin of each entry, the supervisor's event feed, followed logs — and the
everyday actions: deploy another image, roll back, stop, start, delete.

Sign in with the API token. The browser never holds it: the dashboard's own
server keeps it in an `httpOnly` cookie and relays requests to the agent, so the
agent needs no CORS and can stay off the public internet. It has no database and
no state of its own — anything it does, `deployctl` and `curl` can do too.
Details: [dashboard/README.md](dashboard/README.md).

## 6. deploy.yaml

Only `name` and `image` are required. Annotated example:
[configs/deploy.example.yaml](configs/deploy.example.yaml).

| Field | Default | |
|---|---|---|
| `name` | — | Lowercase letters, digits, dashes; max 63 |
| `image` | — | Any Docker image reference. Its tag becomes the deployment's version |
| `port` | — | Port the app listens on. Required with `domain` or `health` |
| `domain` | — | Public hostname, served over HTTPS by Caddy |
| `replicas` | `1` | 1–50 |
| `env` | — | Environment variables; values are never logged or returned by the API |
| `health.path` | — | Must answer 2xx |
| `health.interval` / `timeout` / `retries` | `10s` / `3s` / `3` | |
| `resources.cpu` | unlimited | Cores; `0.5`, `2`, … |
| `resources.memory` | unlimited | `128mb`, `512mb`, `1gb`, … |
| `restart.policy` | `always` | `always` `on-failure` `never` |
| `deploy.strategy` | `rolling` | |

Mistakes are reported all at once, by field:

```text
invalid deploy.yaml

port:
  invalid value 99999
  expected: a number between 1 and 65535

resources.memory:
  invalid value "abc"
  expected: 128mb, 512mb, 1gb, ...
```

**Private images:** run `docker login <registry>` once on the server. The agent
reads the same `~/.docker/config.json` (credential helpers are not supported).

## 7. Deployment

Deployments are **rolling**: replicas are replaced one at a time.

```text
v1.4.1  ├── replica 1 ──▶ v1.4.2 replica 1 starts ─▶ healthy ─▶ takes traffic ─▶ old replica 1 retired
        ├── replica 2 ──▶ v1.4.2 replica 2 …
        └── replica 3 ──▶ v1.4.2 replica 3 …
```

1. The image is pulled. If the registry is unreachable but the image exists
   locally, the local copy is used and a warning is recorded.
2. For each replica in turn: the new one is started and must pass its
   [health check](#9-health-checks) (or, without one, stay up through a short
   stabilization window). Only then does it [join the rotation](#11-caddy) in
   place of its predecessor, which is taken out of rotation first and stopped
   gracefully second.
3. When every replica has been replaced, the deployment becomes `ACTIVE` in a
   single database transaction.

**Never more than one extra container.** Starting the whole new version next to
the old one would be simpler — and would need twice the memory for the duration,
which is exactly what a small server does not have. Rolling peaks at N+1
containers, and serving capacity never drops below N. Replicas with nothing to
replace (a first deployment, scaling up) start together; when scaling down, the
surplus replicas are retired last. During a rollout, both versions serve side
by side for a moment — as with any rolling update, the two must be able to
coexist (database migrations, above all, must be backward compatible).

**A deployment that fails is undone.** If a new replica crashes or never
becomes healthy, its last log lines are saved with the deployment, the new
containers are removed, and:

- if it was the *first* replica — the usual case; a bad image rarely survives
  its first health check — nothing of the old version was touched: `FAILED`;
- if some old replicas had already been replaced, they are **recreated from the
  previous deployment's stored configuration**, verified, and traffic returns
  to them: `ROLLED_BACK`. The new replicas that were already serving keep
  serving until the restored ones are ready, so capacity does not dip twice.

Measured on a real server stack, under constant load: a rolling redeploy of 3
replicas — 100 of 100 requests `200`, 3–4 containers throughout; a rollout
sabotaged at its second replica — rolled back, 76 of 76 requests `200`.

When a deployment fails, you are told why, and whether users were affected:

```text
✗ Deployment failed

  replica 1 exited with code 1 shortly after start

  Last output of replica 1:
  panic: DATABASE_URL is not set

my-api is still running 1.4.1; the failed deployment did not affect it.
```

One deployment per application at a time; a second is refused rather than
queued. Every attempt is kept as history, visible in `deployctl status`.

If the agent crashes or restarts mid-deployment, it marks that deployment
`FAILED` on the next start and removes its leftovers. Running applications are
not affected by agent restarts or upgrades.

## 8. Rollback

**Automatic:** a deployment that fails after it has already replaced some
replicas is rolled back on its own — see [Deployment](#7-deployment).

**On request:**

```bash
deployctl rollback            # to the version that ran before this one
deployctl rollback --to 3     # to deployment #3, as numbered by `deployctl status`
```

```text
Rolling back my-api to 1.4.1  (deployment #3)...

✓ Replica 1/2 is serving 1.4.1; its 1.4.2 predecessor is retired
✓ Replica 2/2 is serving 1.4.1; its 1.4.2 predecessor is retired
✓ Deployment successful
```

A rollback is not a special mechanism — it is a deployment whose configuration
comes from the history instead of from a file, going through **the same
engine**: rolled out replica by replica, health-checked, zero-downtime, and, if
the old version no longer comes up today, undone like any other failed
deployment. What that buys you:

- **The whole configuration returns**, not just the image: env values,
  replicas, limits, domain, health check — as they were stored with that
  deployment. (Secrets included; they never leave the server to do so.)
- **History is appended to, never rewritten.** A rollback is a new entry,
  marked `rollback`, pointing at the deployment it re-used.
- Only deployments that once served successfully are targets. A `FAILED`
  attempt is not a version to return to.

`deployctl redeploy [--image …]` is the same idea applied to the *running*
configuration: deploy it again, optionally with another image, without needing
the `deploy.yaml` at hand.

## 9. Health checks

```yaml
port: 8080
health:
  path: /health    # must answer 2xx; redirects do not count
  interval: 10s
  timeout: 3s
  retries: 3
```

**During a deployment**, every new replica must answer `GET path` with a 2xx
before the deployment may go live. It has `interval × retries` to do so (30s by
default) and is probed every second meanwhile, so a fast application is
confirmed in about a second, while a slow starter gets its time — raise
`retries` for a JVM or an app that migrates its database on boot. A replica
that never answers fails the deployment, and tells you why:

```text
✗ Deployment failed

  replica 1 did not become healthy within 30s: GET /health on port 8080: connection refused
```

Without a `health` block, a deployment only verifies that replicas start and
stay up for a few seconds.

**Afterwards**, the supervisor keeps watching every replica:

| What happens | What Shipwick does |
|---|---|
| A replica exits | Restarts it, as `restart.policy` allows: `always` (default), `on-failure` (non-zero exit or out of memory only), `never` |
| A replica runs but fails `retries` checks in a row | Marks it `unhealthy` and restarts it — the classic cure for a deadlocked process. A single failed check changes nothing |
| A replica keeps dying | Backs off: **1s, 2s, 5s, 10s, 30s**. After five restarts that did not hold, the application is `CRASH_LOOP` and is retried **every 5 minutes** — never in a hot loop, but if the cause goes away (the database comes back), it recovers on its own |
| A replica stays healthy for a minute | Its restart history is forgiven; the next crash starts again at 1s |
| A replica's container is gone — `docker rm`, `docker system prune`, anything that is not Shipwick | Recreates it from the deployment's stored configuration, within about a second: desired 3, present 2 → create 1 |

`deployctl status` shows each replica's health and restart count, and a feed of
what the supervisor did and why:

```text
my-api  ● CRASH_LOOP

REPLICA   CONTAINER            STATE     HEALTH      RESTARTS         STARTED
1         shipwick_my-api_3_1   running   healthy     0                2h ago
2         shipwick_my-api_3_2   running   unhealthy   5 (crash loop)   34s ago

WHEN      EVENT
28s ago   Replica 2 did not become healthy within 30s of starting: HTTP 503
34s ago   Replica 2 is crash-looping: 5 restarts without staying up. Retrying every 5m
```

Deploying is always the way out of a crash loop: a deployment replaces the
containers, and new containers start with a clean slate.

Applications keep running while the agent is down or being upgraded — but
nothing restarts them during that time. After a server reboot, the agent brings
every application back up according to its restart policy.

> **The agent must be able to reach container addresses** to probe them. In a
> container (the recommended setup) it joins the application network by itself.
> As a host process this works on Linux, but not with Docker Desktop on
> macOS/Windows, where the agent warns at startup.

## 10. Resource limits

```yaml
resources:
  cpu: 2
  memory: 1gb
```

Applied per replica as Docker limits. Memory is a hard cap: swap does not
extend it, and a container exceeding it is OOM-killed (reported as such, and
restarted per `restart.policy`). Without a `resources` block a replica may use
whatever the server has.

Usage is one command away, and live in the dashboard:

```text
$ deployctl status
my-api  ● HEALTHY

Replicas   2/2 healthy
CPU        42% / 400%
Memory     412 MB / 2 GB
```

CPU is in percent of one core, as in `docker stats`: two replicas limited to
`cpu: 2` each may use up to 400%. Memory is the working set — what the limit is
enforced against — not including page cache the kernel would give back. The
numbers come straight from Docker's stats API and agree with `docker stats`;
nothing is sampled while nobody is looking.

## 11. Caddy

```yaml
port: 8080
domain: api.example.com
```

That is the whole configuration. Point the domain's DNS at the server, deploy,
and [Caddy](https://caddyserver.com) serves it over HTTPS: the certificate is
obtained and renewed automatically, and plain HTTP is redirected. Shipwick does
not reimplement any of that — it only tells Caddy what to route where.

```text
Internet ──▶ Caddy :443 ──▶ shipwick_my-api_7_1:8080
                       └──▶ shipwick_my-api_7_2:8080
```

- **Only healthy replicas receive traffic.** A replica that crashes or fails
  its health check leaves the rotation within a second and rejoins once it
  passes again. Requests are spread round-robin.
- **Deployments are zero-downtime.** A new replica joins the rotation only once
  it is healthy, and the old one it replaces leaves the rotation *before* it is
  stopped. If the proxy cannot be updated, the deployment fails and is undone.
  (Measured: a rolling redeploy under constant load, 100 of 100 requests `200`.)
- **No healthy replica, or `deployctl stop`:** the domain answers `503` rather
  than timing out, and keeps its certificate. An address no application serves
  answers `404`.
- **One domain, one application.** A second application claiming a domain in
  use is refused as a config error before anything is started.
- Application containers publish no host ports. Caddy reaches them over the
  private `shipwick` network, by container name.

What a crash costs: requests in flight on the replica that died are lost with
it, and the one request that discovers it is gone may get a `502` within half a
second; everything after it goes to the surviving replicas.

**The agent owns Caddy's configuration entirely** — it regenerates and reloads
the full config (gracefully; connections are not dropped) whenever routing
changes, and restores it within seconds should Caddy ever come back without it.
Do not edit it by hand: it would be overwritten. Custom Caddy directives are
not supported yet.

**The agent's own API over HTTPS.** Set `SHIPWICK_AGENT_DOMAIN=agent.example.com`
and the API is served there through Caddy — the way to reach it from a laptop
or CI without an SSH tunnel:

```bash
deployctl login --url https://agent.example.com
```

Try it locally: `*.localhost` resolves to your machine and Caddy issues
certificates for it from its own local CA, so with the development stack up,
`domain: hello.localhost` is reachable at `https://hello.localhost:8443`
(`curl -k`, or trust Caddy's root certificate).

## 12. Security

**The Docker socket is root.** The agent needs `/var/run/docker.sock`, and
anyone who controls the Docker daemon controls the host: they can start a
container that mounts `/`. Consequently:

- **The API token is equivalent to root SSH access to the server.** Treat it so.
- Whoever can write a `deploy.yaml` to your agent can run arbitrary images on
  your server. Shipwick is not a multi-tenant sandbox.
- The API speaks plain HTTP and binds to `127.0.0.1` by default. Serve it over
  HTTPS through Caddy (`SHIPWICK_AGENT_DOMAIN`), or reach it over an SSH tunnel
  or a private network (WireGuard, Tailscale). **Never expose port 9000
  directly to the internet.**
- **Caddy's admin API is as sensitive as the agent's**: whoever reaches it
  controls all routing and the certificate store. Shipwick talks to it over a
  unix socket in a volume shared by the agent and Caddy only. Do not move it to
  a TCP port on the application network — every application container could
  then reconfigure the proxy.

What Shipwick does:

- Only the SHA-256 of the token is kept, in memory and on disk; comparison is
  constant-time. If you let the agent generate its token, it is printed once
  and cannot be recovered — but note that under Docker "printed" means it is
  in the container's log (`docker logs`) for as long as that container
  exists. The installer avoids this by generating the token itself and
  passing it in; do the same if you set things up by hand.
- Tokens, `Authorization` headers, request bodies and env values are never
  logged. Env values are masked in every API response.
- `deployctl` never takes the token as a flag (arguments leak through `ps` and
  shell history), stores it `0600`, refuses to send a saved token to a
  different agent than the one it was saved for, and warns before a token
  crosses the network over plain HTTP.
- No shell, anywhere. The agent talks to the Docker Engine API directly and
  nothing from `deploy.yaml` is ever executed or interpolated into a command.
- Strict validation of names, images, domains and health paths — the inputs
  that end up in container names, URLs and proxy configuration. The agent
  re-validates everything the CLI sends.
- The proxy configuration is built as data and serialized, never assembled
  from strings, so even input that slipped past validation could not change
  its structure. An application cannot claim a domain that another
  application — or the agent itself — is served on.
- Containers are never privileged, run with `no-new-privileges`, get no host
  mounts and publish no host ports. Their logs are size-capped.
- The agent image is distroless: no shell, no package manager.

Not yet: secrets are stored unencrypted in the SQLite file (protect the data
directory; it is created `0700`), and there is a single token rather than users
and roles.

Found a vulnerability? Please report it privately: [SECURITY.md](SECURITY.md).

## 13. Development

Requirements: Go 1.27+, Docker; Node 24 for the dashboard.

```bash
make dev    # = docker compose up --build
```

| | |
|---|---|
| Dashboard | http://localhost:3000 — sign in with `dev-token-do-not-use-in-production` |
| Agent API | http://localhost:9000 |
| Caddy | http://localhost:8080 · https://localhost:8443 |

Deploy something routed without touching DNS: `*.localhost` resolves to your
machine, and Caddy issues certificates for it from its own local CA.

```bash
export SHIPWICK_AGENT_TOKEN=dev-token-do-not-use-in-production
cd /tmp && deployctl init --name hello --image nginx:alpine --port 80 --domain hello.localhost
deployctl deploy
curl -k https://hello.localhost:8443
```

Tests:

```bash
make test               # unit tests: no Docker, no network, a few seconds
make test-race          # …under the race detector (needs cgo)
make test-race-docker   # …the same in a Linux container, for machines without cgo
make test-integration   # real container lifecycle against the local Docker daemon
make test-dashboard     # dashboard: typecheck, unit tests, production build
make lint
```

Without `make` (Windows), the targets are one-liners — see the [Makefile](Makefile).
CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs all of the above,
plus `govulncheck`, `shellcheck`, image builds and cross-compilation.

How the engine is tested is worth knowing before changing it: the deployment
engine talks to Docker and Caddy through two small interfaces, and the tests
drive it with in-memory fakes and a **synthetic clock** — the supervisor's
entire backoff schedule, twelve simulated minutes, runs in milliseconds. Every
behavior claimed in this README (zero-downtime switch, rollback, crash-loop
backoff, reconciliation) has a test that would fail without it; the numbers
quoted were additionally measured against a real Docker and Caddy.

Running the agent outside a container also works, including on Windows and
macOS with Docker Desktop — handy for a fast edit-run loop:

```bash
SHIPWICK_AGENT_TOKEN=dev-token-0123456789 SHIPWICK_DATA_DIR=./data go run ./agent/cmd/shipwick-agent
```

One limitation there: Docker Desktop does not route from the host to container
addresses, so applications with a `health` block cannot be deployed by a
host-process agent on macOS/Windows. Use `docker compose up` for those.

The dashboard has its own development loop, including a mock agent that needs
no Docker at all: [dashboard/README.md](dashboard/README.md). Layout and design
notes for everything else: [docs/architecture.md](docs/architecture.md). Before
opening a pull request: [CONTRIBUTING.md](CONTRIBUTING.md).

## 14. Roadmap

What 0.1.0 does is this document; what changed between versions is in the
[changelog](CHANGELOG.md).

Next, roughly in this order:

- **Encrypted secrets.** `env` values are stored in plain text in the agent's
  SQLite file today.
- **Volumes**, so that stateful applications can be deployed too.
- A **`recreate` strategy** for applications that cannot run two versions side
  by side.
- **Registry credential helpers** (`credsStore`), not only `auths` entries.
- **Internal service discovery** between applications on the same server.
- **Custom Caddy directives** per application: headers, redirects, basic auth.
- **Several servers** from one CLI configuration and one dashboard.

Out of scope, and likely to stay there: multi-node scheduling, building images,
anything that requires an external database or queue. Shipwick is for one
server; when you outgrow that, you have outgrown Shipwick, and that is fine.

## License

[Apache License 2.0](LICENSE). Third-party software included in the binaries
and images is listed in [NOTICE](NOTICE).
