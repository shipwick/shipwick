# The Shipwick handbook

Everything Shipwick does, in one document: installation and configuration,
`deploy.yaml`, how deployments, rollbacks, health checks and routing work, and
what the security model is. The [README](../README.md) is the short version;
[shipwick.com/docs](https://shipwick.com/docs/) has the same material with a
page per topic.

## 1. What is Shipwick?

A single agent that runs on your VPS and turns a `deploy.yaml` into running,
supervised containers. It is for developers and small teams running 1–20
applications on Hetzner, DigitalOcean, OVH, EC2 or similar, who want Docker in
production without hand-rolling deploy scripts, restart logic, health checks,
rollbacks and reverse-proxy config.

- **One binary, one SQLite file.** No cluster, no control plane, no external database.
- **Docker is the runtime.** Anything that runs with `docker run` runs on Shipwick.
- **Safe by default.** A failed deployment never takes down the version that works.

## 2. Why one server?

A single server is a lot of computer. A few cores, a few gigabytes of memory
and a good network run a company's whole product for the price of a lunch a
month, and most products never need more than that. What such a server lacks
is not power but the platform around it: deploying without downtime,
restarting what crashes, knowing what is healthy, rolling back a bad release,
serving HTTPS, keeping secrets out of files, running the nightly job, taking
the backup. Teams build that platform themselves, in deploy scripts and cron
entries, and every one is different.

Shipwick is that platform, built for one server on purpose. One process, one
SQLite file, one YAML file per application. The focus is where its guarantees
come from: one lock per application, one way replicas come to exist, a proxy
that is never reloaded during a rollout, a failed deployment that never takes
down the version that works.

| | |
|---|---|
| Unit of thought | An application: one `deploy.yaml` |
| To run it | One process, one SQLite file |
| To operate it | `shipwick` in a terminal or a pipeline, a dashboard, an HTTP API |
| Scope | One server, by design |

Shipwick schedules nothing across machines. When one server is no longer
enough, you have outgrown Shipwick, and the `deploy.yaml` you wrote says
everything about the application that the next platform will ask.

## 3. Architecture

```text
              shipwick CLI / dashboard
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
Details: [docs/architecture.md](architecture.md) · API: [docs/api.md](api.md).

## 4. Installation

On the server (Linux, with Docker already installed, ports 80 and 443 free —
no other web server or reverse proxy), as root:

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

  From your laptop or CI:   shipwick login --url https://agent.example.com
  Dashboard:                https://dashboard.example.com
```

Point DNS for those hostnames, and for your applications' domains, at the
server; open ports 80 and 443 and nothing else. Certificates take care of
themselves. Running the installer again upgrades; it never touches an existing
`.env`, and so never changes your token. What it does, step by step, and how to
test it: [scripts/README.md](../scripts/README.md).

**From your laptop.** The same installation, without logging in to the server
yourself:

```bash
shipwick server install root@203.0.113.10 --agent-domain agent.example.com --dashboard-domain dashboard.example.com
```

It connects with your `ssh` (a key that logs in without a password), installs
Docker when it is missing, runs the installer with those hostnames, saves the
token it prints as a context named after the host (`--context prod` names it
otherwise) and makes it current, and ends with the DNS records to create:

```text
✓ Connected to root@203.0.113.10 (x86_64)
✓ Docker 29.8.0
Running the Shipwick installer...
  ✓ Installed /opt/shipwick/compose.yml
  ✓ Wrote /opt/shipwick/.env
  ✓ Started the Shipwick services
  ✓ The agent is healthy
  ...
✓ Shipwick is running on root@203.0.113.10
✓ Saved the API token as context 203.0.113.10 (https://agent.example.com), now current

Create these DNS records, DNS only (not proxied):
  A     agent.example.com  →  203.0.113.10
  A     dashboard.example.com  →  203.0.113.10

Next: in your project, run: shipwick init
      once the records exist, check the setup with: shipwick doctor
```

Running it again upgrades the server; the token is unchanged then, is not
printed again, and the context keeps the one it has. `--version v0.4.0`
installs that release. Without `--agent-domain` the API is not exposed and the
context points at `http://127.0.0.1:9000`, for the tunnel described below.

On your laptop or in CI, only the CLI:

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli
```

Or with Homebrew, on macOS and Linux: `brew install shipwick/tap/shipwick`.

**Upgrading.** The server is upgraded by running the installer again, on the
server: it needs Docker there, which the CLI does not have. The CLI upgrades
itself with `shipwick upgrade`: it fetches the latest release, verifies it
against the release's checksums and swaps the binary in place; a binary that
came from Homebrew or winget is left to them, and the command prints the
`brew upgrade` or `winget upgrade` line instead. Either way it then compares
the server's version and tells you when the server is behind. `--check` only
reports.

Everything the installer fetches comes from one
[release](https://github.com/shipwick/shipwick/releases) and is verified against
its checksums; the images are pinned to that release, so a server runs the
version it installed until you run the installer again. A specific version:
`curl -fsSL https://get.shipwick.com | SHIPWICK_VERSION=v0.3.0 sh`.

> **From source**, without a release: build the images on the server —
> `docker build -t ghcr.io/shipwick/agent .` and
> `docker build -t ghcr.io/shipwick/dashboard dashboard/` — then run
> `sh scripts/install.sh` from the checkout; build the CLI with `make build`.

**Without the installer**, the same setup is one file:
[configs/compose.production.yml](../configs/compose.production.yml), plus a `.env`
with `SHIPWICK_AGENT_TOKEN` and the hostnames.

**No hostname to spare for the API?** Leave `SHIPWICK_AGENT_DOMAIN` empty,
publish the API on the server's loopback only, and reach it through
`ssh -L 9000:127.0.0.1:9000 user@server`.

**Your own changes** — that loopback port, a mount for registry credentials —
go into `/opt/shipwick/compose.override.yml`. Compose merges it with
`compose.yml`; the installer replaces `compose.yml` on every upgrade and never
touches the override:

```yaml
services:
  agent:
    ports: ["127.0.0.1:9000:9000"]
```

The agent also runs as a plain binary on Linux (`make build`), next to a Caddy
installed on the host: `SHIPWICK_CADDY_ADMIN=http://127.0.0.1:2019`.

| Variable | Default | |
|---|---|---|
| `SHIPWICK_AGENT_TOKEN` | generated | API bearer token, min. 16 characters |
| `SHIPWICK_LISTEN_ADDR` | `127.0.0.1:9000` | Loopback by default, on purpose. The container image sets `0.0.0.0:9000`, reachable on the Docker network only: the compose file publishes no port |
| `SHIPWICK_DATA_DIR` | `/var/lib/shipwick` | SQLite database, token hash, encryption key, and the uploaded folders of static applications (`uploads/`) until they are deployed. Off Linux, the default is `shipwick` in the user's configuration directory |
| `SHIPWICK_ENCRYPTION_KEY` | generated | Key that encrypts `env` values in the database, 64 hex characters. Unset: `encryption.key` in the data directory, created on first start (§12) |
| `SHIPWICK_DOCKER_NETWORK` | `shipwick` | Network application containers join |
| `SHIPWICK_CADDY_ADMIN` | — | Caddy's admin endpoint: `unix//run/caddy/admin.sock` (recommended) or `http://127.0.0.1:2019`. Unset: domains are recorded but not served |
| `SHIPWICK_AGENT_DOMAIN` | — | Serve the agent's API over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_DOMAIN` | — | Serve the dashboard over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_UPSTREAM` | `dashboard:3000` | Where Caddy reaches the dashboard |
| `SHIPWICK_WEBHOOK_URL` | — | Where to post notifications: a Slack or Discord webhook, or any HTTPS endpoint. See [Being told](#7-deployment) |
| `SHIPWICK_WEBHOOK_SECRET` | — | Signs each notification (`X-Shipwick-Signature: sha256=<HMAC of the body>`), so your endpoint can tell it came from the agent |
| `SHIPWICK_LOG_LEVEL` | `info` | `debug` `info` `warn` `error` |
| `SHIPWICK_LOG_FORMAT` | `text` | `text` `json` |
| `DOCKER_HOST`, `DOCKER_CONFIG` | Docker defaults | Standard Docker variables are honored |

## 5. Quick start

With the CLI installed (above; on Windows, download `shipwick_windows_amd64.exe`
from the [releases](https://github.com/shipwick/shipwick/releases)), in your
application's repository:

```bash
shipwick login     # once: agent URL + token
shipwick init      # writes Dockerfile, .dockerignore and deploy.yaml
shipwick deploy
```

`shipwick init` looks at the directory first. A Nuxt or Next application, a
Node server (Express, Fastify, Koa, Hono, or a `start` script), a .NET project
(`*.csproj`), a Go program (`go.mod` with a `main` package at the root or under
`cmd/`) or a Python application (`pyproject.toml` or `requirements.txt`;
uvicorn or gunicorn when they are listed) gets a multi-stage `Dockerfile` on a
small runtime image with a non-root user and the port exposed, a
`.dockerignore`, and a `deploy.yaml` with `build: .`, so that `shipwick deploy`
builds the image on your machine and sends it to the server. An `index.html` at
the root or in `dist/`, `build/`, `out/` or `public/`, and a Vite or Astro
project that builds one, gets `static: <dir>` instead: the proxy serves the
files, there is no container. Nothing recognised: it asks for a name, an image,
a port and a domain. An existing `Dockerfile` or `.dockerignore` is kept as it
is; `--image` skips detection and `--static <dir>` forces the static kind.
Review what it wrote — a health check other than `/` is left commented until
your application answers it — then deploy.

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

Then `shipwick status`, `shipwick logs -f`, `shipwick ps`. The full command
reference, and how the CLI finds the agent (SSH tunnel, CI variables), is in
[cli/README.md](../cli/README.md).

A first deployment ends with the two commands to run next, `shipwick logs -f
my-api` and `shipwick status my-api`. `shipwick open` opens the application's
address in the browser. Run in a directory without a `deploy.yaml`,
`shipwick deploy` writes one first, the way `init` does, when a terminal is
attached.

When something is not right — no certificate arrives, a domain shows someone
else's page — `shipwick doctor` checks the whole path in one screen, each line
with what to do about it, and exits non-zero when something is broken:

```text
! shipwick v0.3.1; v0.4.0 is available. Upgrade with: shipwick upgrade
✓ Agent https://agent.example.com runs v0.4.0, the latest release
✓ Token laptop (admin)
✓ Docker 29.8.0 on the server
✓ Proxy serving 2 domains
✓ agent.example.com → 203.0.113.10
✓ Port 80 open on 203.0.113.10
✗ Port 443 is not reachable on 203.0.113.10: open it in the server's firewall; certificates are issued and renewed through ports 80 and 443
✗ api.example.com → 104.16.0.1, which is not the server (203.0.113.10). Point the record at the server; if it is proxied through a CDN, turn the proxy off (DNS only)
✓ https://api.example.com/ answers HTTP 200

2 problems found.
```

The hostnames are resolved through public resolvers, as the agent does (§11);
the server's address is the one the agent's own hostname resolves to, so
through an SSH tunnel the ports and the records' targets are not checked.

**Several servers.** Each `shipwick login` saves a server under a name, a
context; `shipwick login --context staging --url https://staging.example.com`
adds a second one and makes it current. Commands talk to the current context,
`--context staging` (or `SHIPWICK_CONTEXT`) picks another for one command, and
`shipwick context ls | use | rm | current` manage them. Nothing changes for a
single server: it is the context `default`.

In CI, keep `deploy.yaml` in the repository and pass the image you just built:

```bash
shipwick deploy --image ghcr.io/company/my-api:$GIT_SHA
```

It exits non-zero if the deployment fails, so it works as a pipeline gate.

**Several applications.** A project with more than one service — a database,
an API, a front end — describes them all in one `shipwick.yaml`: an `apps` list
in which every entry is a complete `deploy.yaml` plus `after`, the names of the
entries it must wait for (annotated example:
[configs/shipwick.example.yaml](../configs/shipwick.example.yaml)).

```yaml
apps:
  - name: postgres
    image: postgres:17
    port: 5432
    volumes: [{ name: data, path: /var/lib/postgresql/data }]
    health: { tcp: 5432 }
    deploy: { strategy: recreate }
  - name: api
    image: ghcr.io/company/api:2.3.0
    port: 8080
    domain: api.example.com
    after: [postgres]
  - name: web
    image: ghcr.io/company/web:2.3.0
    port: 3000
    domain: example.com
```

`shipwick deploy` uses it when there is no `deploy.yaml` in the directory
(`-f shipwick.yaml` names it explicitly; it cannot be combined with other `-f`
files). Applications whose dependencies are done start at once, up to four at
a time (`--parallel N`): `postgres` and `web` above start together, `api` when
`postgres` has deployed. Every line of output carries the name of the
application it belongs to. An application whose dependency did not deploy is
skipped, the others finish, and the command exits non-zero if any failed or
was skipped. `${NAME}` placeholders work in every entry, and `shipwick
validate` checks the file and prints the order. Several `deploy.yaml` files —
`shipwick deploy -f api/deploy.yaml -f web/deploy.yaml` — still deploy one
after the other, in the order given, stopping at the first failure.

**Secrets.** A value that must not be in the file — a password, an API key —
is written as `${DATABASE_PASSWORD}` and filled in when you deploy. Two places
can fill it in. Your own machine: the CLI takes the value from its environment
or from `--env-file`. Or the server: store the value there once, and every
deploy from every laptop and pipeline gets it, with no env file anywhere:

```bash
shipwick secret set DATABASE_PASSWORD     # asks for the value without echo; or pipe it in
shipwick secret ls                        # NAME  CREATED  UPDATED — never values
shipwick secret rm DATABASE_PASSWORD
```

The CLI fills in what it can and leaves the rest of the `env` values to the
agent; `shipwick validate` lists what it left. A name that neither side has
stops the deployment before anything is recorded, naming the variable and the
command to run. Only `env` values work this way: a `${TAG}` in `image` must be
set where `shipwick` runs. A changed secret applies from the next deployment
on; running containers keep the value they were started with, and a rollback
restores the value that deployment used. Secrets are encrypted at rest like
env values are ([Security](#12-security)).

### Dashboard

The same things in a browser, at the dashboard hostname you gave the installer
(or http://localhost:3000 in development): every application with its status,
live CPU and memory with a week of history, replicas and their health,
deployment history with the origin of each entry and who made it, the
supervisor's event feed, followed logs, scheduled jobs and their runs, volume
backups — and the everyday actions: deploy another image, roll back, stop,
start, delete, run a command, restore a backup, manage tokens.

Sign in with an API token; what it may do follows the token's role. The
browser never holds it: the dashboard's own server keeps it in an `httpOnly`
cookie and relays requests to the agent, so the agent needs no CORS and can
stay off the public internet. It has no database and no state of its own —
anything it does, `shipwick` and `curl` can do too.
Details: [dashboard/README.md](../dashboard/README.md).

## 6. deploy.yaml

Only `name` and `image` — or `build` in its place — are required; a folder the
proxy serves itself needs `name`, `static` and `domain`. Annotated example:
[configs/deploy.example.yaml](../configs/deploy.example.yaml).

| Field | Default | |
|---|---|---|
| `name` | — | Lowercase letters, digits, dashes; starts and ends with a letter or digit; max 63. Other applications reach this one at `http://<name>:<port>`. `agent`, `caddy`, `dashboard` and `localhost` are taken |
| `image` | — | Any Docker image reference. Its tag becomes the deployment's version |
| `build` | — | Build the image on your machine instead of naming one: `build: .`, or `{context, dockerfile}` (default `Dockerfile`, relative to the context); paths are relative to deploy.yaml and stay inside its directory. `shipwick deploy` runs `docker build` and sends the image to the server; no `image` and no registry — see [Deployment](#7-deployment) |
| `static` | — | A folder, relative to deploy.yaml, served by Caddy as it is: a built frontend. No container: `image`, `port`, `replicas`, `env`, `health`, `resources`, `volumes`, `publish`, `entrypoint`, `command`, `user`, `logging`, `pre_deploy` and `jobs` do not apply. Needs `domain` — see [Deployment](#7-deployment) |
| `entrypoint` / `command` | the image's | Replace the image's `ENTRYPOINT` / `CMD`. A list of arguments; a string is one argument and is never split — see below |
| `user` | the image's | User the process runs as: `app`, `1000`, `1000:1000` |
| `port` | — | Port the app listens on. Required with `domain` or `health` |
| `domain` | — | Public hostname, served over HTTPS by Caddy |
| `aliases` | — | Up to 20 more hostnames served exactly like `domain`. Needs `domain` |
| `redirects` | — | Up to 20 hostnames redirected (308) to `https://<domain>`, path and query kept: `www.example.com`, an old domain. Needs `domain` |
| `replicas` | `1` | 1–50; must be 1 with `volumes` or `publish` |
| `env` | — | Environment variables; values are never logged or returned by the API. `${NAME}` is filled in when you deploy — by the CLI from its environment or `--env-file`, else by the agent from the secrets stored with `shipwick secret set` — so secrets stay out of the file. `$${NAME}` is a literal `${NAME}` |
| `health.path` | — | Must answer 2xx. One of `path`, `tcp`, `command` |
| `health.tcp` | — | A container port that must accept a TCP connection; `port` is not needed |
| `health.command` | — | A command run inside the replica, as a list; exit 0 is healthy |
| `health.interval` / `timeout` / `retries` | `10s` / `3s` / `3` | |
| `health.start_period` | `0s` | Extra time a replica gets to come up before failed checks count, on top of `interval × retries`; up to `30m` — see [Health checks](#9-health-checks) |
| `resources.cpu` | unlimited | Cores; `0.5`, `2`, … |
| `resources.memory` | unlimited | `128mb`, `512mb`, `1gb`, … |
| `volumes[].name` / `path` | — | A named Docker volume and where it is mounted. Data outlives deployments, rollbacks and `delete`. Needs `deploy.strategy: recreate` |
| `publish[].port` / `host` / `address` / `protocol` | — / same as `port` / every address / `tcp` | A container port published on a port of the server itself, for services that are not HTTP. Needs `deploy.strategy: recreate` and one replica; 80 and 443 are the proxy's — see [Deployment](#7-deployment) |
| `logging.driver` | `json-file` | `json-file` `local` `syslog` `journald` `gelf` `fluentd` `awslogs` `splunk` — see below |
| `logging.options` | — | The driver's options, handed to Docker as given. Collector addresses must be `scheme://host:port`; no option may name a file or socket on the server |
| `pre_deploy.command` / `timeout` | — / `10m` | Run from the new image, with the app's env, before any replica of it starts; a non-zero exit fails the deployment. Timeout 1s–1h — see [Deployment](#7-deployment) |
| `jobs[].name` / `schedule` / `command` / `timeout` | — / — / — / `1h` | A command run on a cron schedule (five fields, **UTC**) in a one-off container from the image. Name: lowercase, digits, dashes, max 40. Timeout 1s–24h |
| `restart.policy` | `always` | `always` `on-failure` `never` |
| `deploy.strategy` | `rolling` | `rolling` `recreate` — see [Deployment](#7-deployment) |
| `after` | — | `shipwick.yaml` only: names of the applications in the same file that must have deployed before this one starts — see [Quick start](#5-quick-start) |

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

**Running something other than the image's default.** `entrypoint`, `command`
and `user` replace the image's `ENTRYPOINT`, `CMD` and `USER`, the way
`docker run --entrypoint`, its trailing arguments and `--user` do — a worker
from the same image as the API, say:

```yaml
image: ghcr.io/company/my-api:1.4.2
command: ["node", "worker.js"]
user: "1000:1000"
```

Each is a list of arguments, passed to the container exactly as written. A
string is one argument, spaces included: there is no shell in between, so
`command: node worker.js` starts a program called `node worker.js`. Use a list
for several. Whatever runs, runs inside the container, as the image's own
command would; nothing in `deploy.yaml` runs on the server.

**Shipping logs elsewhere.** Replica logs go to the server's disk, capped at
3 × 10 MB per container, and `shipwick logs` reads them there. `logging`
hands them to a Docker logging driver instead:

```yaml
logging:
  driver: gelf
  options:
    gelf-address: udp://logs.example.com:12201
    tag: "{{.Name}}"
```

The options are the driver's own and reach Docker as written; Shipwick checks
only that a collector address is `scheme://host:port` (a socket is a path on
the server, and Shipwick never touches those) and that no option names a file
on the server. With `json-file` or `local` the caps stay unless you set
`max-size` and `max-file` yourself. With a remote driver, `shipwick logs`
keeps working through the local copy Docker keeps for `docker logs` — its
dual logging, on by default since Docker 20.10; if it was turned off
daemon-wide, `shipwick logs` shows nothing for that application.

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

**`recreate`, for what cannot run twice.** A database, or anything that holds a
lock, cannot have two versions running at once. With `deploy.strategy:
recreate` the running version is taken out of the proxy and stopped *first*,
then the new one is started and verified; the application is down for as long
as the new version takes to start. If the new version fails, the old
containers — kept, stopped — are started again. Applications with `volumes`
must use it.

**Old images are removed.** After a successful deployment, the images that only
retired deployments of the application refer to are removed from the server, so
a server that deploys daily does not fill its disk with versions nobody can
return to. Two are always kept per application: the one running, and the one
of the most recent earlier version — the rollback target, so a rollback never
waits for a pull. Images another application uses, or a container started
outside Shipwick, are never touched.

**Images built where you are.** With `build: .` in place of `image`, there is
no registry in the picture. `shipwick deploy` runs `docker build` on your
machine — the project and Docker are already there — for the server's
architecture (`--platform`, asked of the agent, so a laptop of one kind builds
for a server of another), tags the result `shipwick.local/<name>:<UTC
stamp>-<4 hex>`, saves it and streams the archive to the agent, which loads it
and deploys it like any other image. The build's output is shown as it runs.
`shipwick.local` is a host that does not exist, on purpose: nothing can pull
from it, so such an image is either on the server or it is not, and a
deployment whose image is gone (pruned, or a rollback to a version that was
never sent to this server) says so and asks for another `shipwick deploy` from
the project. Old local images are pruned like any other; the two that matter
stay. The whole image is sent each time; sending only the layers the server
lacks is on the roadmap. The server never builds: a Dockerfile runs whatever
it likes, with the network and CPU of the machine it runs on, and that machine
should be yours. `docker` must be installed where `shipwick deploy` runs.
`shipwick validate` describes the build and does not run it; `--image` does
not apply to an application with `build:`.

**Volumes.** `volumes` mounts named Docker volumes into the replica:

```yaml
name: postgres
image: postgres:17
port: 5432
replicas: 1
env:
  POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
volumes:
  - name: data
    path: /var/lib/postgresql/data
deploy:
  strategy: recreate
```

The volume is `shipwick_postgres_data` on the server. It belongs to the
application, not to a deployment: a redeploy, a rollback and even
`shipwick delete` leave it where it is. `shipwick volumes` lists every volume
on the server with the application it belongs to, how much it holds, and
whether that application still exists; `shipwick volumes rm
shipwick_postgres_data` removes one whose application was deleted, after
asking, and refuses one whose application still exists — that data is the
application's, and `shipwick restore` is the way to replace it. Other
applications reach the database at `postgres:5432` — see [Caddy](#11-caddy).
Volumes are named volumes only; a path on the host cannot be mounted.

**Several applications at once.** A `shipwick.yaml` (see [Quick
start](#5-quick-start)) is deployed as several ordinary deployments, one per
application, each with its own record, lock, health checks and rollback; the
agent never sees the file. The CLI holds the order: an application starts when
every name in its `after` has completed an `ACTIVE` deployment in this run, at
most four run at a time (`--parallel`), and one that waits for an application
that failed, or was itself skipped, is skipped — a failure is reported, never
propagated by timing. `after` is about readiness, not reachability: names on
the services network resolve whatever the order, so `after: [postgres]`
belongs on an application that would exit without its database, not on every
consumer of another service.

**Ports that are not HTTP.** The proxy speaks HTTP. A service that does not —
a database a laptop connects to, a game server — is published on the server's
own ports with `publish`:

```yaml
name: postgres
image: postgres:17
replicas: 1
env:
  POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
volumes:
  - name: data
    path: /var/lib/postgresql/data
publish:
  - port: 5432        # inside the container
    host: 15432       # on the server; default: the same as port
    address: 10.0.0.5 # optional; default: every address of the server
    protocol: tcp     # or udp
deploy:
  strategy: recreate
```

Nothing else changes: the container is on the same networks, other
applications still reach it at `postgres:5432`, and the port comes and goes
with the replica. A published port needs `deploy.strategy: recreate` and one
replica — a server port has one holder, so the old version must be gone before
the new one binds it — and 80 and 443 are the proxy's. The ports the proxy and
the agent listen on (80, 443, 8080, 8443 and the agent's own), and a port
another application already publishes, are refused
before anything is started. `shipwick validate` lists the published ports in its
summary.

**Docker's published ports bypass the host firewall.** On most distributions
Docker inserts its own iptables rules ahead of ufw's or firewalld's, so a port
published on every address is reachable from the internet whatever the
firewall says. Publish only what must be reachable from outside the server,
bind it to a private address where one exists (`address: 10.0.0.5` — a VPN or
private-network interface), and keep what only other applications need
unpublished: they reach it by name on the `shipwick` network.

**Backups.** `shipwick backup` downloads every volume of an application as a
tar archive, `shipwick restore` puts one back:

```bash
shipwick backup postgres                       # postgres-data-20260927-153000.tar
shipwick backup postgres --volume data -o /srv/backups
shipwick stop postgres
shipwick restore postgres postgres-data-20260927-153000.tar
shipwick start postgres
```

A backup is taken while the application runs, unless it is stopped. A database
that is being written to may not be consistent in the copy — the CLI says so
when the application is running; stop it first. A restore replaces *everything*
in the volume with the archive's contents, so it requires the application to be
stopped and leaves it stopped.
The archives are plain tar files holding the volume's contents, relative to
the mount point; anything that can write such a tar can be restored.

**A folder instead of a container.** A built frontend — the `dist/` of a Vite
or Nuxt site, the `out/` of a Next export, any folder of HTML, CSS and
JavaScript — needs no container of its own; Caddy serves files. `static` names
the folder, relative to `deploy.yaml`:

```yaml
name: web
static: dist/
domain: example.com
redirects: [www.example.com]
```

`shipwick deploy` sends the folder as it is — run the build first — and the
agent puts it in front of Caddy and routes the domain to it:

```text
✓ Validated deploy.yaml
✓ Uploaded dist/: 42 files, 3.1 MB
✓ Received 42 files (3.1 MB)
✓ Copied 42 files into the proxy
✓ Found index.html
✓ Routed https://example.com to the uploaded files
✓ Deployment successful
```

The folder must hold an `index.html`; a request for a directory gets it, a
request for a path that names no file gets a `404` — the fallback route of a
single-page application is not assumed. A folder may be up to 512 MB. Nothing
that describes a container applies — there is none: no `image`, `port`,
`replicas`, `env`, `health`, `resources`, `volumes`, `jobs`; `domain`,
`aliases` and `redirects` work as for any application, and so do `stop`
(the domain answers `503`), `start`, `rollback` and `delete`. `shipwick logs`,
`metrics` and `run` have nothing to show and say so. The version of a static
deployment is the first twelve characters of the folder's digest: the same
files make the same version, on any machine. The agent keeps the folder that
serves and the one before it — the rollback target — and removes older ones;
`shipwick rollback` needs no upload.

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
queued. Every attempt is kept as history, visible in `shipwick status`.

If the agent crashes or restarts mid-deployment, it marks that deployment
`FAILED` on the next start and removes its leftovers. Running applications are
not affected by agent restarts or upgrades.

### Before the replicas start: migrations

`pre_deploy` runs a command from the **new** image, with the application's
environment and limits, once the image is pulled and before any replica of the
new version exists:

```yaml
pre_deploy:
  command: ["dotnet", "Migrate.dll"]
  timeout: 10m # default 10m
```

The deployment shows `Running pre-deploy command` and `Pre-deploy command
finished (12s)`. If the command exits non-zero or outlives its timeout, the
deployment is `FAILED` before anything was started, and the last lines of its
output are in the deployment's events — `shipwick deploy` prints them under the
error. The version that is serving is not touched.

The command runs **next to the running version**, under `recreate` too, whose
replicas stop only afterwards. What it does must therefore be safe next to the
old code: add a column, do not drop one — the same backward compatibility a
rolling update asks of migrations anyway. The container gets no `volumes`: a
replica may be writing them.

### Scheduled jobs and one-off commands

`jobs` runs commands on a schedule, each in a fresh container from the
application's image, with its environment, limits and network — it reaches the
database at `postgres:5432` like a replica does — and without its volumes:

```yaml
jobs:
  - name: nightly-report
    schedule: "0 3 * * *" # minute hour day-of-month month day-of-week, UTC
    command: ["node", "report.js"]
    timeout: 1h # default 1h
```

Schedules are the five cron fields — `*`, values, ranges, lists, steps such as
`*/15`, month and weekday names — and are read in **UTC**, whatever the server's
time zone. A job runs once per firing, at most one run of it at a time: a run
that is still going when the schedule fires again is left alone and the firing
is skipped. A run that outlives its timeout is stopped. A stopped application
runs no jobs. A job of the previous version that is still running when a
deployment finishes runs to its end.

```text
$ shipwick jobs my-api
NAME            SCHEDULE     LAST RUN  STATUS           NEXT (UTC)
nightly-report  0 3 * * *    21h ago   succeeded        2026-03-02 03:00
cleanup         */15 * * * * 4m ago    failed (exit 1)  2026-03-01 12:15
```

`shipwick jobs run my-api nightly-report` starts a job now and waits for it;
`shipwick jobs logs my-api cleanup` shows the last run's output. Failed and
timed-out runs appear in `shipwick status` as events; successful ones only in
the history, since jobs run often. The last 50 runs of each job are kept.

`shipwick run` does the same for a command you type — a migration by hand, a
console script, a look around:

```text
$ shipwick run my-api -- rails db:migrate
== 20260301 AddIndexToOrders: migrating ===
== 20260301 AddIndexToOrders: migrated (0.0412s) ===
```

The command follows `--` and runs on the server, for at most an hour; its
output is printed when it finishes, and `shipwick` exits with its exit code.
Several commands may run at once. Every run keeps the last 200 lines (64 KB) of
what the command wrote; the container itself is removed. A job's output goes
through Docker's default log driver, whatever `logging` says: that copy is
where the run's output is read from.

If the agent restarts while a job runs, the run is marked `interrupted` and
its container removed on the next start; the job runs again at its next
scheduled time. A firing that fell while the agent was down is not caught up.

**Being told.** Set `SHIPWICK_WEBHOOK_URL` in the agent's `.env` and every
deployment's outcome is posted there, as is an application whose replicas have
all stopped serving, and its recovery: `deployment.succeeded`,
`deployment.failed`, `deployment.rolled_back` (automatic, or `shipwick
rollback`), `application.down`, `application.recovered`, `job.failed`. Nothing
else — a single replica restarting is in `shipwick status`, not in your chat. A
Slack (`hooks.slack.com`) or Discord (`discord.com/api/webhooks/…`) URL gets a
plain message:

```text
my-api deployment of 1.4.3 failed: replica 1 exited with code 1 shortly after start. my-api is still running 1.4.2; the failed deployment did not affect it
```

Any other URL gets the same sentence with the facts beside it:

```json
{
  "event": "deployment.succeeded",
  "application": "my-api",
  "deployment_id": 42,
  "version": "1.4.2",
  "message": "my-api is running 1.4.2, replacing 1.4.1",
  "at": "2026-03-01T10:00:00Z",
  "server": "vps-1"
}
```

With `SHIPWICK_WEBHOOK_SECRET` set, each request carries
`X-Shipwick-Signature: sha256=<hex HMAC-SHA256 of the body>`. The URL must be
HTTPS unless it points at the server itself or a private address. A delivery
that fails is retried after 1, 5 and 25 seconds; a webhook that is down never
holds up a deployment, and the agent's log names only the webhook's host — a
Slack URL is a credential.

## 8. Rollback

**Automatic:** a deployment that fails after it has already replaced some
replicas is rolled back on its own — see [Deployment](#7-deployment).

**On request:**

```bash
shipwick rollback            # to the version that ran before this one
shipwick rollback --to 3     # to deployment #3, as numbered by `shipwick status`
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

`shipwick redeploy [--image …]` is the same idea applied to the *running*
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
before the deployment may go live. It has `start_period + interval × retries`
to do so (30s by default) and is probed every second meanwhile, so a fast
application is confirmed in about a second, while a slow starter gets its
time — set `start_period: 1m` for a JVM or an app that migrates its database
on boot, rather than raising `retries`, which would also make a running
replica's failures take longer to notice. A replica the supervisor restarts
gets the same time to come up again. A replica that never answers fails the
deployment, and tells you why:

```text
✗ Deployment failed

  replica 1 did not become healthy within 30s: GET /health on port 8080: connection refused
```

Without a `health` block, a deployment only verifies that replicas start and
stay up for a few seconds.

### Checks that are not HTTP

A database does not answer `GET /health`. A check is one of three kinds;
`interval`, `timeout` and `retries` mean the same for each:

```yaml
health:
  path: /health          # HTTP GET, 2xx is healthy
health:
  tcp: 5432              # a TCP connection to this container port is accepted
health:
  command: ["pg_isready", "-U", "postgres"]   # exit 0 inside the replica
```

`path` is for anything that speaks HTTP. `tcp` needs no `port` and nothing from
the image: it says the process is listening, which for a queue or a cache is
usually all there is to know. `command` asks the application itself — a
database can be listening and still refuse connections while it recovers —
and needs the tool to exist in the image; `pg_isready`, `mysqladmin ping`,
`redis-cli ping` ship with theirs. The command is a list, run as given, without
a shell; on failure Shipwick reports the exit code and the last line it
printed:

```text
  replica 1 did not become healthy within 30s: command exited 2: pg_isready: no response
```

**Afterwards**, the supervisor keeps watching every replica:

| What happens | What Shipwick does |
|---|---|
| A replica exits | Restarts it, as `restart.policy` allows: `always` (default), `on-failure` (non-zero exit or out of memory only), `never` |
| A replica runs but fails `retries` checks in a row | Marks it `unhealthy` and restarts it — the classic cure for a deadlocked process — under every policy except `never`. A single failed check changes nothing |
| A replica keeps dying | Backs off: **1s, 2s, 5s, 10s, 30s**. After five restarts that did not hold, the application is `CRASH_LOOP` and is retried **every 5 minutes** — never in a hot loop, but if the cause goes away (the database comes back), it recovers on its own |
| A replica runs for a minute since its last restart, and is not failing its health check | Its restart history is forgiven; the next crash starts again at 1s |
| A replica's container is gone — `docker rm`, `docker system prune`, anything that is not Shipwick | Recreates it from the deployment's stored configuration, within about a second: desired 3, present 2 → create 1 |

`shipwick status` shows each replica's health and restart count, and a feed of
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
$ shipwick status
my-api  ● HEALTHY

Replicas   2/2 healthy
CPU        42% / 400%
Memory     412 MB / 2 GB
```

CPU is in percent of one core, as in `docker stats`: two replicas limited to
`cpu: 2` each may use up to 400%. Memory is the working set — what the limit is
enforced against — not including page cache the kernel would give back. The
numbers come straight from Docker's stats API and agree with `docker stats`.

**History.** Every 30 seconds the agent records the CPU and memory of every
running replica and keeps a week of it, in its own database. The dashboard
charts the last hour, day or week per replica, next to the limits;
`GET /applications/:name/metrics/history?since=1h|24h|7d` serves the same
series ([docs/api.md](api.md#metrics-history)).

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
Internet ──▶ Caddy :443 ──▶ my-api:8080 ──┬──▶ shipwick_my-api_7_1
                                          └──▶ shipwick_my-api_7_2
```

Caddy is told a *name*, not a list of containers. Every application has one on
the server's `shipwick-services` network — its own name — and a replica answers
to it exactly while it is ready for traffic: it takes the name once it passed
its health check, and loses it the moment it stops. Docker's DNS returns one
address per replica that carries the name, and Caddy asks it for every request.

- **Only healthy replicas receive traffic.** A replica that fails its health
  check is taken off the name within a second and put back once it passes
  again; one that crashes disappears from it at once. Requests are spread
  round-robin.
- **A deployment does not touch the proxy.** A new replica takes the name only
  once it is healthy; the old one it replaces keeps answering until it is
  stopped, and taking it off the name is the same thing as stopping it. Caddy's
  configuration changes only when a domain or a port does, so a rollout, a
  crash and a restart never reload it — and a reload is the one thing that
  costs requests: Caddy resets connections that are being established at that
  instant. Measured with a new connection per request, 12 clients and 50 ms of
  added latency, through 18 consecutive rolling redeploys: 15,774 of 15,774
  requests answered, 0 reloads.
- **No healthy replica, or `shipwick stop`:** the domain answers `503` rather
  than timing out, and keeps its certificate. A hostname nothing serves has no
  certificate, so an HTTPS connection to it fails before any answer, and plain
  HTTP is redirected to HTTPS first; a request that does get through with an
  unknown hostname answers `404`.
- **One hostname, one application.** A second application claiming a hostname
  in use — as its domain, an alias or a redirect — is refused as a config error
  before anything is started.
- Application containers publish no host ports, unless `publish` asks for
  some (see [Deployment](#7-deployment)). The proxy is the only other way in
  from outside.
- **Responses are compressed** with zstd or gzip when the browser asks for
  it, for text, JSON, JavaScript, SVG and the other types that shrink, from a
  kilobyte up. What the application already compressed, or does not ask to
  have compressed, leaves as it came. The agent's own API and the dashboard
  stream logs, and are left uncompressed.

What a crash costs: requests in flight on the replica that died are lost with
it, and a request that arrives in the second before its name is dropped may
get a `503`; everything after it goes to the surviving replicas.

**DNS first.** A hostname is handed to Caddy only once it resolves to this
server. Deploying before the DNS record exists is fine: the deployment
succeeds, and instead of `Routed https://…` it prints a warning that names the
record to create — `Routing https://api.example.com is waiting for DNS: does
not resolve yet; add an A record: api.example.com → 62.238.109.115 (DNS only,
not proxied). It is served, and its certificate obtained, once the record
points at this server` — with an AAAA line as well when the server has an IPv6
address. A record that points at another server is told to change; one that
points at Cloudflare's proxy is recognised — `resolves to Cloudflare's proxy
(104.21.5.6), not to this server: turn the proxy off for this record (DNS
only), or wait for Cloudflare support in a later release` — because the record
itself is right there, and the orange cloud is what breaks the certificate.
The agent checks again every 10 seconds, and as soon as the record is right
the hostname is served, the certificate is obtained, and the application's
events say so.
The reason is Let's Encrypt's rate limit: five failed authorizations per
hostname per hour. Caddy asks for a certificate the moment it hears of a
hostname, and one that does not resolve fails within seconds — deployed before
its DNS, a domain would use up the five in minutes and stay without a
certificate for the rest of the hour, however quickly the record was fixed.
The agent asks public resolvers (Cloudflare's, Google's, Quad9's) rather
than the server's own, which remembers that a record did not exist for as long
as the zone's negative TTL says — half an hour on Cloudflare — and would keep
saying so after the record was created; a certificate authority looks from
the outside too. When none of them can be reached, the server's resolver
decides. The agent's and the dashboard's own hostnames are never held back.

**Several hostnames, and www.** An application can answer to more than one
hostname:

```yaml
domain: example.com
aliases: [api.example.com]                  # served exactly like example.com
redirects: [www.example.com, example.net]   # 308 → https://example.com/<same path>
```

Aliases share the domain's route and replicas. Redirects need no replica at
all: `https://www.example.com/docs?x=1` is answered with a `308` to
`https://example.com/docs?x=1` whether or not the application is running, and
the method is kept. Every hostname gets its own certificate; point each one's
DNS at the server. A hostname belongs to one application, in any role.

**Reaching one application from another.** The same names serve the
applications themselves. A replica is on the `shipwick-services` network from
its first second, so `orders` reaches `payments` at `http://payments:8080` —
the port is the one in `payments`' `deploy.yaml` — and gets a healthy replica
of whatever version is current, without a domain, a certificate or a trip
through the proxy. A database run by Shipwick (see [Deployment](#7-deployment))
is reached the same way, `postgres:5432`. Nothing outside the server can reach
these names.

**A folder served by Caddy itself.** A static application
(`static: dist/`, see [Deployment](#7-deployment)) has no replica and no name
on the network: its route is a Caddy `file_server` whose root is the folder
the agent copied into Caddy's own container, `/srv/shipwick/<app>/<digest>`,
on the `caddy-static` volume of the compose setup. Aliases and redirects work
as above. Because the root changes with every new folder, deploying a static
application reloads Caddy once — the one kind of change a rollout of
containers never causes.

**The agent owns Caddy's configuration entirely** — it regenerates and reloads
the full config whenever a domain or a port changes, and restores it within
seconds should Caddy ever come back without it. Do not edit it by hand: it
would be overwritten. Custom Caddy directives are not supported yet.

**The agent's own API over HTTPS.** Set `SHIPWICK_AGENT_DOMAIN=agent.example.com`
and the API is served there through Caddy — the way to reach it from a laptop
or CI without an SSH tunnel:

```bash
shipwick login --url https://agent.example.com
```

Try it locally: `*.localhost` resolves to your machine and Caddy issues
certificates for it from its own local CA, so with the development stack up,
`domain: hello.localhost` is reachable at `https://hello.localhost:8443`
(`curl -k`, or trust Caddy's root certificate).

## 12. Security

**The Docker socket is root.** The agent needs `/var/run/docker.sock`, and
anyone who controls the Docker daemon controls the host: they can start a
container that mounts `/`. Consequently:

- **An admin token is equivalent to root SSH access to the server.** Treat it so.
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

**Tokens and roles.** The token the installer prints is the *root* token:
`admin`, and the one to keep for yourself. Create more, each with the role it
needs — `read` sees everything, `deploy` also deploys, rolls back, stops and
starts, `admin` also deletes applications and manages tokens and secrets:

```bash
shipwick token create ci --role deploy      # printed once; put it in the CI secret
shipwick token ls                           # NAME  ROLE  CREATED  LAST USED
shipwick token revoke ci
```

Give CI a `deploy` token and people `admin` ones. A token with too small a role
is told so, and what it needs; `shipwick server status` shows which token and
role you are using. Deployments record which token made them (`by` in the API
and the dashboard's history), and a stop or start by a token other than root says so in the
application's events.

What Shipwick does:

- Only the SHA-256 of each token is kept, in memory and on disk; comparison is
  constant-time. If you let the agent generate its token, it is printed once
  and cannot be recovered — but note that under Docker "printed" means it is
  in the container's log (`docker logs`) for as long as that container
  exists. The installer avoids this by generating the token itself and
  passing it in; do the same if you set things up by hand.
- Tokens, `Authorization` headers, request bodies and env values are never
  logged. Env values are masked in every API response. `${NAME}` placeholders
  keep secrets out of `deploy.yaml` and out of your repository; the CLI fills
  them in from its environment or `--env-file` at deploy time, the agent fills
  the remaining `env` values in from the secrets stored on the server, and a
  name neither has stops the deployment.
- A secret stored with `shipwick secret set` is read by nobody afterwards: the
  API lists names and dates only, the value goes into containers and nowhere
  else. Its value is never an argument (arguments leak through `ps` and shell
  history): it is asked for without echo, piped in, or read from a file.
- `shipwick` never takes the token as a flag (arguments leak through `ps` and
  shell history), stores it `0600`, refuses to send a saved token to a
  different agent than the one it was saved for, and warns before a token
  crosses the network over plain HTTP.
- Guessing is answered: after 20 failed authentications within a minute
  from one address, the agent answers wrong tokens from it with `429` for
  the next minute. A valid token is never refused and `GET /health` is not
  affected: behind Caddy every client shares the proxy's address, so a
  guesser must not be able to lock anyone else out, and only failures count,
  so a mistyped token does not reach the limit.
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
  mounts and publish no host ports unless `publish` lists some, and then only
  those. Their logs are size-capped.
- The agent image is distroless: no shell, no package manager.


**Secrets at rest.** The `env` values of every deployment, and the secrets
stored with `shipwick secret set`, are encrypted before they are written to
the database (AES-256-GCM; everything else in the record, including the
variable names, stays readable). The key is `encryption.key` in
the data directory, created by the agent on its first start, or the value of
`SHIPWICK_ENCRYPTION_KEY` (64 hex characters) if you prefer to manage it
yourself. This protects a copy of the database file without the key: a backup,
a snapshot, a disk that left the building. It does not protect against root on
the server, who can read the key file and the agent's memory, and it does not
touch the values inside running containers, which `docker inspect` shows to
anyone with the socket. **Back up `encryption.key` together with
`shipwick.db`**: without it, the database cannot be read, and the agent refuses
to start against it with a message that says so. If a database written by an
earlier release is found on start, its values are encrypted in place, once.
Rotating the key is not supported yet.

Not yet: roles per application rather than per server. Protect the data
directory regardless; it is created `0700`.

Found a vulnerability? Please report it privately: [SECURITY.md](../SECURITY.md).

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
cd /tmp && shipwick init --name hello --image nginx:alpine --port 80 --domain hello.localhost
shipwick deploy
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
no Docker at all: [dashboard/README.md](../dashboard/README.md). Layout and design
notes for everything else: [docs/architecture.md](architecture.md). Before
opening a pull request: [CONTRIBUTING.md](../CONTRIBUTING.md).

## 14. Roadmap

What the current version does is this document; what changed between versions
is in the [changelog](../CHANGELOG.md); what comes next is in
[ROADMAP.md](../ROADMAP.md). Out of scope, and likely to stay there: multi-node
scheduling, building images, anything that requires an external database or
queue. Shipwick is for one server; when you outgrow that, you have outgrown
Shipwick, and that is fine.

