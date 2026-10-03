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
✓ Installed the shipwick CLI to /usr/local/bin/shipwick
✓ shipwick on this server is signed in

Shipwick is running.

  From your laptop or CI:   shipwick login --url https://agent.example.com
  On this server:           shipwick ps
  Dashboard:                https://dashboard.example.com
```

Point DNS for those hostnames, and for your applications' domains, at the
server; open ports 80 and 443 and nothing else. Certificates take care of
themselves. At Cloudflare the records must be *DNS only*, unless the agent has
a Cloudflare API token — `SHIPWICK_CLOUDFLARE_API_TOKEN`, given to the
installer like the hostnames, or added to `.env` later: see
[Behind Cloudflare](#11-caddy). Running the installer again upgrades; it never
touches an existing `.env`, and so never changes your token. What it does, step
by step, and how to test it: [scripts/README.md](../scripts/README.md).

**On the server**, `shipwick` is installed too, and with a hostname for the
API it is signed in: the installer saves `https://<that hostname>` and the
token as a context of the user it runs as, root, in the CLI's own
configuration file. An upgrade puts the token from `.env` back into a context
that points at this server and leaves any other context, and which one is
current, alone; when only other servers are saved there, it adds nothing and
prints the `shipwick login` line instead. The context is saved without a
request to the agent, so it is there before the hostname's DNS record and
certificate are; until they are, commands on the server fail the way they
would on a laptop.

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
`brew upgrade` or `winget upgrade` line instead. Upgrading the server
restarts the agent; applications keep running, and a deployment that is under
way at that moment [continues afterwards](#7-deployment). Either way it then compares
the server's version and tells you when the server is behind. `--check` only
reports.

Everything the installer fetches comes from one
[release](https://github.com/shipwick/shipwick/releases) and is verified against
its checksums; the images are pinned to that release, so a server runs the
version it installed until you run the installer again. A specific version:
`curl -fsSL https://get.shipwick.com | SHIPWICK_VERSION=v0.3.0 sh`.

> **From source**, without a release: build the images on the server —
> `docker build -t ghcr.io/shipwick/agent .`,
> `docker build -t ghcr.io/shipwick/dashboard dashboard/` and
> `docker build -t ghcr.io/shipwick/caddy -f Dockerfile.caddy .` — then run
> `sh scripts/install.sh` from the checkout; build the CLI with `make build`.

**Without the installer**, the same setup is one file:
[configs/compose.production.yml](../configs/compose.production.yml), plus a `.env`
with `SHIPWICK_AGENT_TOKEN` and the hostnames.

**Reach the API without a hostname.** Leave `SHIPWICK_AGENT_DOMAIN` empty,
publish the API on the server's loopback only (the override below), and reach
it through `ssh -L 9000:127.0.0.1:9000 user@server`. Until that port is
published the agent has none: the installer saves no context, and `shipwick`
run on the server itself finds nothing at `http://127.0.0.1:9000` and says
so. With the port published it works there without a tunnel, after `shipwick
login`. To give the API a hostname later, set `SHIPWICK_AGENT_DOMAIN` in
`/opt/shipwick/.env` and run the installer again.

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
| `SHIPWICK_ENCRYPTION_KEY` | generated | Key that encrypts `env` values, stored secrets and registry passwords in the database, 64 hex characters. Unset: `encryption.key` in the data directory, created on first start and replaced by `shipwick server rotate-key` (§12) |
| `SHIPWICK_DOCKER_NETWORK` | `shipwick` | Network application containers join |
| `SHIPWICK_CADDY_ADMIN` | — | Caddy's admin endpoint: `unix//run/caddy/admin.sock` (recommended) or `http://127.0.0.1:2019`. Unset: domains are recorded but not served |
| `SHIPWICK_AGENT_DOMAIN` | — | Serve the agent's API over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_DOMAIN` | — | Serve the dashboard over HTTPS at this hostname, through Caddy |
| `SHIPWICK_DASHBOARD_UPSTREAM` | `dashboard:3000` | Where Caddy reaches the dashboard |
| `SHIPWICK_PROXY_TLS_ADDR` | `caddy:443` | Where the agent reaches Caddy's TLS port, to report each hostname's certificate (§11) |
| `SHIPWICK_WEBHOOK_URL` | — | Where to post notifications: a Slack or Discord webhook, or any HTTPS endpoint. See [Being told](#7-deployment) |
| `SHIPWICK_WEBHOOK_SECRET` | — | Signs each notification (`X-Shipwick-Signature: sha256=<HMAC of the body>`), so your endpoint can tell it came from the agent |
| `SHIPWICK_ALERT_MEMORY_PERCENT` | `90` | A replica at or above this share of its `resources.memory` raises an alert, see [Alerts](#7-deployment). A whole number from 50 to 100 |
| `SHIPWICK_ALERT_DISK_PERCENT` | `85` | The disk that holds the data directory raises an alert when it is this full. A whole number from 50 to 94; at 95 the alert turns critical, whatever is set here |
| `SHIPWICK_CLOUDFLARE_API_TOKEN` | — | A Cloudflare API token with *Zone → Zone → Read* and *Zone → DNS → Edit* on your zones. Certificates are then obtained through a DNS record: hostnames can stay behind Cloudflare's proxy, and can be wildcards. Needs `SHIPWICK_CADDY_ADMIN` — see [Behind Cloudflare](#11-caddy) |
| `SHIPWICK_BACKUP_DIR` | `<data dir>/backups` | Where the backups the agent takes are kept on the server. See [Backups the server takes](#backups-the-server-takes) |
| `SHIPWICK_BACKUP_PASSPHRASE` | — | Encrypts every backup, and is what allows the agent's own database and encryption key to be backed up at all. At least 12 characters. Keep a copy somewhere that is not this server: without it the backups cannot be read |
| `SHIPWICK_BACKUP_S3_ENDPOINT` | — | An S3-compatible service the backups are also sent to: `https://s3.eu-central-1.amazonaws.com`, `https://<account>.r2.cloudflarestorage.com`, … Needs the next three as well |
| `SHIPWICK_BACKUP_S3_BUCKET` | — | The bucket; it must exist |
| `SHIPWICK_BACKUP_S3_ACCESS_KEY_ID`, `SHIPWICK_BACKUP_S3_SECRET_ACCESS_KEY` | — | Credentials that may put, get, list and delete objects in the bucket. Never logged |
| `SHIPWICK_BACKUP_S3_REGION` | `auto` | The region requests are signed for; AWS wants the bucket's own |
| `SHIPWICK_BACKUP_S3_PREFIX` | — | Put in front of every object's key: one bucket for several servers, each under its own prefix |
| `SHIPWICK_EXPORT_SCHEDULE` | — | A five-field cron expression, UTC: when an [export of the whole server](#moving-to-a-new-server) is written to where backups go. Needs `SHIPWICK_BACKUP_PASSPHRASE` |
| `SHIPWICK_EXPORT_KEEP` | `3` | How many of those exports are kept |
| `SHIPWICK_STANDBY_SCHEDULE` | — | Makes the server a [standby](#a-second-server-kept-ready): when it imports the newest export from the bucket, with every application stopped. Needs the `SHIPWICK_BACKUP_S3_*` variables and the passphrase of the server it stands by for; its own backups then stay on its disk. Not together with `SHIPWICK_EXPORT_SCHEDULE` |
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
SvelteKit application with `adapter-node`, a Remix application, an Astro
application with the Node adapter, a Node server (Express, Fastify, Koa, Hono,
or a `start` script), a .NET project (`*.csproj`), a Go program (`go.mod` with
a `main` package at the root or under `cmd/`) or a Python application
(`pyproject.toml` or `requirements.txt`; uvicorn or gunicorn when they are
listed; `uv sync --frozen` when there is a `uv.lock`) gets a multi-stage
`Dockerfile` on a small runtime image with a non-root user and the port
exposed, a `.dockerignore`, and a `deploy.yaml` with `build: .`, so that
`shipwick deploy` builds the image on your machine and sends it to the server.
An `index.html` at the root or in `dist/`, `build/`, `out/` or `public/`, and
a project whose build writes one — Vite, Astro without an adapter, SvelteKit
with `adapter-static`, Next with `output: "export"` — gets `static: <dir>`
instead: the proxy serves the files, there is no container. Nothing
recognised: it asks for a name, an image, a port and a domain. An existing
`Dockerfile` or `.dockerignore` is kept as it is; `--image` skips detection
and `--static <dir>` forces the static kind. The Dockerfile installs from the
lock file it finds (npm, pnpm, yarn); without one it runs `npm install`, and
init says that the build is not reproducible until a lock file is committed.
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
[cli/README.md](../cli/README.md). Following an application that has printed
nothing yet, `logs -f` says so after two seconds — `Following my-api; nothing
printed yet. Ctrl-C stops.` — in a terminal, on standard error, once.

A first deployment ends with the two commands to run next, `shipwick logs -f
my-api` and `shipwick status my-api`, and with the dashboard's address when
the server has one. `shipwick open` opens the application's address in the
browser; `shipwick open --dashboard` opens the dashboard, and so does
`shipwick open` in a directory without a `deploy.yaml`. `shipwick server
status` shows the dashboard's address as well. Run in a directory without a
`deploy.yaml`, `shipwick deploy` writes one first, the way `init` does, when
a terminal is attached.

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
✗ api.example.com resolves to Cloudflare's proxy (104.16.0.1), not to the server: turn the proxy off for this record (DNS only), or set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent to keep it on
✓ https://api.example.com/ answers HTTP 200

2 problems found.
```

The hostnames are resolved through public resolvers, as the agent does (§11);
the server's address is the one the agent's own hostname resolves to, so
through an SSH tunnel the ports and the records' targets are not checked.
A record that points at Cloudflare's proxy is named as such, in the agent's
words. When the agent has a Cloudflare API token (§11) it is in order —
`✓ api.example.com → Cloudflare's proxy (104.16.0.1)` — and if the agent's own
hostname is proxied too, the server's address cannot be learned from the
outside: ports 80 and 443 and the records' targets are then not checked, and
`doctor` says so.

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

The commands that take the application's name from `deploy.yaml` — `status`,
`logs`, `stop`, `start`, `rollback`, `redeploy`, `run`, `jobs`, `open`,
`backup`, `restore` — take it from `shipwick.yaml` when that is the file in
the directory (or the one `-f` names) and it describes one application. When
it describes several they ask which, with the names and the command to type:

```text
shipwick.yaml describes 3 applications: postgres, api, web

Say which one, e.g.: shipwick status postgres
```

`shipwick init` in such a directory adds an entry to the file instead of
writing a `deploy.yaml` next to it: `shipwick init web` recognises the project
in `web/`, writes `web/Dockerfile` and `web/.dockerignore`, and appends

```yaml
  - name: web
    build: ./web
    port: 3000
    health:
      path: /
```

to the end of the `apps` list, indented like the entry before it (`static:
web/dist/` for a built site, `image:` with `--image`). Without a directory it
is the project next to `shipwick.yaml` itself. The rest of the file — comments,
order, spacing — stays as it is. A name the file already has is refused. When
`apps` is not the last key of the file, or its list is not written one entry
per dash, the file is left alone and init prints the entry to add by hand.

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

An application's page also shows what the proxy saw of it — requests, errors
and durations over an hour, a day or a week, and the most recent requests —
the certificate of each hostname when it is not in order, its `proxy` block,
and, for an application with volumes, the backups the agent took: take one,
verify one, download a volume of one, restore one into the stopped
application. The server's page shows the disk, the alerts that hold right
now (they are marked on every page), where backups go and whether the agent's
own state is backed up, the exports in the backups, and on a standby what
waits there and the button that promotes it. Registry credentials and
supplied certificates have a page each, next to secrets; the encryption key
is rotated from the server's page. What needs a file or a passphrase of your
own stays with the CLI: the first deployment of an application, `shipwick
export` to a file, and `shipwick import`.

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
| `image` | — | Any Docker image reference. Its tag becomes the deployment's version. A private registry needs a credential on the server: `shipwick registry login`, see [Private images](#6-deployyaml) below |
| `build` | — | Build the image on your machine instead of naming one: `build: .`, or `{context, dockerfile}` (default context `.`, the directory of deploy.yaml; default `Dockerfile`, relative to the context); paths are relative to deploy.yaml and stay inside its directory. `shipwick deploy` runs `docker build` and sends the image to the server; no `image` and no registry — see [Deployment](#7-deployment) |
| `static` | — | A folder, relative to deploy.yaml, served by Caddy as it is: a built frontend. `static: dist/`, or `{dir, fallback}`. No container: `image`, `port`, `replicas`, `env`, `health`, `resources`, `volumes`, `publish`, `entrypoint`, `command`, `user`, `logging`, `pre_deploy`, `jobs`, `backups` and `deploy.stop_timeout` do not apply. Needs `domain` — see [Deployment](#7-deployment) |
| `static.fallback` | — | A file in the folder answered, with status 200, for every path that names no file: `index.html` for a single-page application. Without it such a path is a `404` |
| `entrypoint` / `command` | the image's | Replace the image's `ENTRYPOINT` / `CMD`. A list of arguments; a string is one argument and is never split — see below |
| `user` | the image's | User the process runs as: `app`, `1000`, `1000:1000` |
| `port` | — | Port the app listens on. Required with `domain` or `health` |
| `domain` | — | Public hostname, served over HTTPS by Caddy. May be a wildcard, `"*.example.com"`: every name one label below the domain — see [Wildcards](#11-caddy) |
| `aliases` | — | Up to 20 more hostnames served exactly like `domain`, wildcards included. Needs `domain` |
| `redirects` | — | Up to 20 hostnames redirected (308) to `https://<domain>`, path and query kept: `www.example.com`, an old domain. Needs `domain` |
| `path` | — | Serve only this path of the domain and everything under it: `/api` takes `example.com/api` and `example.com/api/users`, not `/apix`. Several applications share a domain under different paths; the longest path wins and an application without one takes the rest. Starts with `/`, no trailing slash, letters, digits and `. _ ~ -` between the slashes, at most 200 characters. Needs `domain` — see [Caddy](#11-caddy) |
| `proxy.strip_prefix` | `false` | Remove `path` from a request before the application sees it: `/api/users` arrives as `/users`. Needs `path`. A static application's files are always looked up without it |
| `proxy.headers` | — | Up to 50 response headers, set on every response the application or its folder gives, over what the application sent under the same name. Not the ones that belong to the connection (`Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, …) |
| `proxy.basic_auth[].path` / `username` / `password` | everything / — / — | Up to 20 accounts asked for by the proxy before a request reaches the application. The password is a secret like an `env` value — at least 8 characters, `${NAME}` filled in the same way — and is never returned by the API. Where paths overlap, the accounts of the longest decide |
| `proxy.redirects[].from` / `to` / `status` | — / — / `308` | Up to 100 paths answered with a redirect: `from` is one exact path, `to` a path on the same host or an `https://` URL, `status` one of 301, 302, 307, 308. The query is kept unless `to` has its own |
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
| `backups.schedule` | — | When the agent backs up the application's volumes: a cron schedule (five fields, **UTC**). Needs `volumes` — see [Backups the server takes](#backups-the-server-takes) |
| `backups.keep` | `7` | How many successful backups are kept, 1–365; the oldest go once a new one has succeeded |
| `backups.before` | — | A command run inside the running replica before the archive is taken, as a list: a dump, a checkpoint. A non-zero exit fails the backup and nothing is archived |
| `backups.stop` | `false` | Stop the application while the archive is taken, and start it again whatever happens |
| `restart.policy` | `always` | `always` `on-failure` `never` |
| `deploy.strategy` | `rolling` | `rolling` `recreate` — see [Deployment](#7-deployment) |
| `deploy.stop_timeout` | `10s` | How long a replica that is being stopped gets between `SIGTERM` and `SIGKILL`, 1s–10m: raise it for WebSockets or long uploads — see [Deployment](#7-deployment). Not for a static application |
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

**Private images.** An image in a private registry needs a credential on the
server. Give the agent one, from wherever you run `shipwick`:

```bash
shipwick registry login ghcr.io --username octocat   # asks for the token without echo; or pipe it in
shipwick registry ls                                 # REGISTRY  USERNAME  UPDATED — never passwords
shipwick registry logout ghcr.io
```

The registry is named as image references name it — `ghcr.io`,
`registry.example.com:5000`, `docker.io` for Docker Hub — and there is nothing
to add to `deploy.yaml`. The password or token is never an argument: it is
asked for without echo or read from standard input (`--password-stdin` is
accepted for those used to Docker's flag). The agent checks the credential
against the registry before it stores it, so a mistyped token is refused then
and not by the next deployment; it keeps the password encrypted like a secret
([Security](#12-security)) and sends it with every pull from that registry:
deployments, rollbacks, jobs, a replica whose image was pruned. Use a token
that may only read. Up to 50 registries; logging in and out needs `admin`.

A pull that the registry refuses for want of a credential fails the deployment
with the command to run:

```
pull access denied for ghcr.io/company/api: run shipwick registry login ghcr.io (or check the image name)
```

The alternative is Docker's own login. For a registry without a stored
credential the agent reads `~/.docker/config.json` of the user it runs as
(`DOCKER_CONFIG` is honored), so `docker login <registry>` on the server works
too; where the agent runs in a container, as the installer sets it up, that
file has to be mounted into it, in `/opt/shipwick/compose.override.yml`:

```yaml
services:
  agent:
    volumes:
      - /root/.docker/config.json:/root/.docker/config.json:ro
```

A stored credential takes precedence over that file. Either way, only a
username with a password or token works: credential helpers (`credsStore` in
the Docker configuration, `docker-credential-*` programs) are not supported.
A helper is a program on the server, which the agent's container does not
have, and the agent executes nothing. Registries whose tokens come from such
a program and expire within hours need the token renewed with
`shipwick registry login` from a scheduled job of your own.

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
   gracefully second. The next replica starts once the old one is gone.
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

**Time to finish.** A replica that has been replaced is sent `SIGTERM` and
then has `deploy.stop_timeout` — 10 seconds unless you set it, up to 10
minutes — to finish what it has in hand before it is killed. The deployment
does not wait for the last of them: once every new replica serves, it is
`ACTIVE` and done, and the old containers leave in the background. (A
deployment started while one is still leaving waits for it before it starts a
replica, and says how long it waited.) The same grace period applies whenever
Shipwick stops a replica of the application: `shipwick stop`, `delete`, a
restart of an unhealthy replica — and those do wait for it.

```yaml
deploy:
  stop_timeout: 2m   # WebSockets, long uploads: let them finish
```

What the time is used for is the application's business. A process that
handles `SIGTERM` — stops accepting, finishes its requests, exits — leaves
cleanly and usually at once. One that does not handle it keeps running, and
keeps receiving a share of the requests, until the time is up and it is
killed with whatever it was serving; a program started as the container's
first process (`CMD ["node", "server.js"]`) ignores `SIGTERM` unless it
installs a handler. Shipwick tells you when that happened:

```text
The replaced container shipwick_my-api_6_1 did not exit within 10s of SIGTERM and was killed. To let it finish its requests, handle SIGTERM in the application; to give it longer, set deploy.stop_timeout
```

The line appears among the application's events (`shipwick status`, the
dashboard) once the old container is gone, which is after the deployment has
completed. Handle the signal, or start the program under an init such as
`tini` in your image.

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
outside Shipwick, are never touched. An image `shipwick deploy` built and
sent for a deployment the agent then refused is removed by the next sweep
once it is ten minutes old, and by `delete` at any age. A deployment that
fails, or is rolled back, removes the image it named as soon as it has
ended — unless that image is the one running or the rollback target.

**Images built where you are.** With `build: .` in place of `image`, there is
no registry in the picture. `shipwick deploy` runs `docker build` on your
machine — the project and Docker are already there — for the server's
architecture (`--platform`, asked of the agent, so a laptop of one kind builds
for a server of another), tags the result `shipwick.local/<name>:<UTC
stamp>-<4 hex>`, saves it and streams the archive to the agent, which loads it
and deploys it like any other image. Before it builds, it has the agent
validate the file — `POST /applications/{name}/validate`, the checks of a
deployment without the deployment — so that what only the server can know (a
domain another application serves, a port already published, a secret that is
not stored) is said before the build and the upload rather than after them; an
agent older than that operation is asked whether the domain is free, as
before. In a terminal the build is one line that moves — `Building the image
(12s) — #9 [build 4/6] RUN npm ci` — and its whole output appears only when it
fails; `shipwick deploy --verbose` shows every line as it is printed, and so
does any run whose output is not a terminal, since a pipeline's log is where a
build is read. Several applications of a `shipwick.yaml` building at once in a
terminal are silent until each build ends.
`shipwick.local` is a host that does not exist, on purpose: nothing can pull
from it, so such an image is either on the server or it is not, and a
deployment whose image is gone (pruned, or a rollback to a version that was
never sent to this server) says so and asks for another `shipwick deploy` from
the project. Old local images are pruned like any other; the two that matter
stay. The first deployment sends the whole image; after that only the layers
the server does not have travel. The CLI asks the agent which of the
image's layers its Docker lacks and leaves the others out of the archive, so a
change to your code costs the size of the layer that holds it, not of the base
image beneath it:

```
✓ Sent image to the server (22 KB; the server had the rest of 57.9 MB)
```

(a Node application on `node:22-alpine`, measured.) This works with both of
Docker's image stores, on your machine and on the server. It is an economy,
never a condition: with an agent older than 0.5, an archive in a layout the
CLI does not know (Docker before 25), or a server that lost a layer in the
meantime, the whole image is sent as before. The server never builds: a Dockerfile runs whatever
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

**Backups.** There are two kinds, and they are for different things.

*The server takes them*: on the schedule under `backups` in deploy.yaml, or
when you say `shipwick backups run`. They stay on the server and, if the agent
has a bucket, in the bucket; the agent counts them, removes the oldest, and can
prove that one restores. This is the backup that happens without anyone
remembering, and it has a section of its own:
[Backups the server takes](#backups-the-server-takes).

*You take one*: `shipwick backup` downloads every volume of an application to
your machine as a tar archive, as it is right now, and `shipwick restore` puts
one back. The server keeps nothing. This is for moving data between servers,
for a copy before a risky change, for restoring an archive that came from
somewhere else:

```bash
shipwick backup postgres                       # postgres-data-20260927-153000.tar
shipwick backup postgres --volume data -o /srv/backups
shipwick stop postgres
shipwick restore postgres postgres-data-20260927-153000.tar
shipwick start postgres
```

Such a backup is taken while the application runs, unless it is stopped. A
database that is being written to may not be consistent in the copy — the CLI
says so when the application is running; stop it first. A restore replaces
*everything* in the volume with the archive's contents, so it requires the
application to be stopped and leaves it stopped.
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
agent puts it in front of Caddy and routes the domain to it. The agent is
asked to validate the file before the folder is uploaded, as before a build.
Everything sent
is served to anyone who asks for its path, so what describes the site stays
out: `deploy.yaml` and `shipwick.yaml`, `.git` and `.env` files, wherever
they are in the folder. Other dotfiles go in; `.well-known` is content.

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
request for a path that names no file gets a `404`. A single-page application,
whose router reads the path in the browser, names the page that answers
those instead:

```yaml
static:
  dir: dist/
  fallback: index.html
```

Every path that names no file is then answered with that page and status 200,
a missing image or script included. The file must be in the folder: the CLI
looks before it uploads, the agent before it routes. `shipwick init` writes
the line for a project built by Vite alone.

A folder may be up to 512 MB. Nothing
that describes a container applies — there is none: no `image`, `port`,
`replicas`, `env`, `health`, `resources`, `volumes`, `jobs`; `domain`,
`aliases` and `redirects` work as for any application, and so do `stop`
(the domain answers `503`), `start`, `rollback` and `delete`. `shipwick logs`,
`metrics` and `run` have nothing to show and say so. The version of a static
deployment is the first twelve characters of the folder's digest: the same
files make the same version, on any machine. The agent keeps the folder that
serves and the one before it — the rollback target — and removes older ones;
`shipwick rollback` needs no upload. An application that stops being a folder
— the next deployment names an image — keeps the last folder for as long as a
plain `shipwick rollback` would return to it, which is until its second
container version; after that nothing of it is left in the proxy.

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

**Deployments survive an agent restart.** Upgrading Shipwick restarts the
agent, and so may a reboot or a crash. A deployment that was running at that
moment is not failed: the agent picks it up where it was when it starts —
containers it had created are kept, replicas that were already serving keep
serving, only what was not done is done — and the deployment's events say
`Resumed after the agent restarted`. `shipwick deploy` waits through it: it
reports that the agent is not responding, and goes on when it is back. The
exception is a `pre_deploy` command that was running when the agent stopped.
It is not run a second time; the deployment fails and says so, nothing of the
running version has been touched, and you deploy again once you have looked
at what the command left behind. Running applications are not affected by
agent restarts or upgrades.

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
rollback`), `application.down`, `application.recovered`, `job.failed`,
`backup.failed` (a scheduled backup, or the daily one of the agent's own
state), and `certificate.expiring` when a hostname's certificate has 14 days
left and again at 3 ([Caddy](#11-caddy)). Beyond those only the alerts below — a
single replica restarting is in `shipwick status`, not in your chat. A
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

**Alerts.** A notification says that something happened. An alert says that
something is the case and, left alone, ends badly. There are four:

| Alert | Raised when | Cleared when |
|---|---|---|
| `memory` | A replica is at or above 90% of its `resources.memory` for three samples in a row (a minute and a half). Applications without a memory limit have no such alert | It is below 80% |
| `disk` | The disk that holds the agent's data directory — in the standard installation the disk Docker keeps images and volumes on — is 85% full: a warning. At 95%: critical | It is below 80% |
| `restarts` | The supervisor has restarted the same replica three times within ten minutes. Held back while the application is down or the replica crash-looping | Fewer than three restarts in the last ten minutes |
| `unhealthy` | An application has had fewer healthy replicas than it should for five minutes: a warning. After an hour: critical | Every replica is healthy and has stayed up for a minute |

Each is raised once and cleared once, however long it lasts; a warning that
turns critical is told a second time. The two thresholds are
`SHIPWICK_ALERT_MEMORY_PERCENT` and `SHIPWICK_ALERT_DISK_PERCENT` (§4); an
alert is cleared ten points below the memory threshold and five below the
disk's, so a value that hovers at the threshold is one alert. `application.down`
and `application.recovered` are sent at once, as before; `unhealthy` is what
follows when the outage lasts, and it also covers an application that is only
degraded, which nothing else reports.

Alerts go to the webhook as `alert.raised` and `alert.cleared`, with the same
kind of sentence —

```text
my-api replica 1 is at 93% of its memory limit (240 MB of 256 MB). At the limit it is killed and restarted; raise resources.memory in deploy.yaml, or watch it with: shipwick status my-api
```

— and, in the JSON form, `"alert": {"kind": "memory", "severity": "warning",
"replica": 1}` beside it. One about an application is in its events too. What
is active right now is shown by `shipwick server status`, under the disk's
usage, and by `shipwick doctor`, where a critical alert counts as a problem:

```text
$ shipwick server status
…
Token          laptop (admin)
Disk           35 GB of 40 GB used (87%)

! The server's disk is 87% full (5 GB of 40 GB free). See what takes the space with: docker system df
```

Active alerts are kept in the agent's memory: after the agent restarts, one
whose condition still holds is raised again. Stopping or deleting an
application drops its alerts without a message.

### Backups the server takes

An application with volumes gets a `backups` block, and the agent archives its
volumes on that schedule:

```yaml
name: postgres
image: postgres:17
volumes:
  - name: data
    path: /var/lib/postgresql/data
deploy:
  strategy: recreate
backups:
  schedule: "0 3 * * *"     # five cron fields, UTC, like jobs
  keep: 7                   # successful backups kept; default 7
  before: ["pg_dump", "-U", "postgres", "-f", "/var/lib/postgresql/data/backup.sql", "app"]
  stop: false               # stop the application for the archive; default false
```

A backup is one tar archive per volume, the same archive `shipwick backup`
downloads. Two things decide whether what is in it can be trusted:

- `before` runs inside the running replica first, as a list of arguments, never
  through a shell: a dump written into the volume, a checkpoint. It has an
  hour. If it exits non-zero the backup fails and nothing is archived — an
  archive without the dump it was meant to hold is not the backup you asked
  for.
- `stop: true` stops the application for as long as the archive takes and
  starts it again whatever happens, a failed backup included. This is the one
  way to a consistent copy of files a process keeps open and has no dump tool
  for. The application is down meanwhile, and its event feed says so.

Both may be given: the command runs, then the application stops. Without
either, the archive is taken from under the running process, which is fine for
uploads and not for a database.

```text
$ shipwick backups postgres
ID   WHEN      TRIGGER    SIZE      WHERE                   STATUS      VERIFIED
5    25s ago   schedule   39 MB     local, s3 (encrypted)   succeeded   -
4    42s ago   manual     39 MB     local, s3 (encrypted)   succeeded   -
3    1m ago    schedule   39.1 MB   local, s3 (encrypted)   succeeded   1m ago
```

| | |
|---|---|
| `shipwick backups <app>` | The backups the server keeps |
| `shipwick backups run <app>` | Take one now, as the schedule would; works without a `backups` block too |
| `shipwick backups verify <app> [id]` | Prove that a backup restores; the latest unless an id is given |
| `shipwick backups restore <app> <id>` | Replace the volumes with a backup's contents. The application must be stopped and stays stopped; asks for its name |
| `shipwick backups download <app> <id> [-o dir]` | Fetch a backup's archives, decrypted, as `<app>-<volume>-backup-<id>.tar` |
| `shipwick backups rm <app> <id>` | Remove one, from the server and the bucket |
| `shipwick backups decrypt <file>` | Decrypt a file taken from the server or the bucket, on your machine |

`shipwick status` has a line for it — `Backups   daily at 03:00 UTC, last 5h
ago (2.1 GB), 7 kept`, or the failure — and a scheduled backup that fails is
posted to the webhook as `backup.failed`.

**Where they go.** Always to a directory on the server:
`<data dir>/backups/<application>/<id>/<volume>.tar`, which with the
installer's setup is inside the agent's data volume (`SHIPWICK_BACKUP_DIR`
moves it — to a mount of another disk, say, added in
`compose.override.yml`). **A backup on the same disk protects against a bad
deployment, a dropped table, a mistake; it does not protect against losing the
server.** For that, give the agent a bucket on any S3-compatible service —
AWS S3, Cloudflare R2, Backblaze B2, MinIO — in `/opt/shipwick/.env`:

```bash
SHIPWICK_BACKUP_PASSPHRASE=<a long passphrase; keep a copy off the server>
SHIPWICK_BACKUP_S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
SHIPWICK_BACKUP_S3_BUCKET=shipwick-backups
SHIPWICK_BACKUP_S3_ACCESS_KEY_ID=…
SHIPWICK_BACKUP_S3_SECRET_ACCESS_KEY=…
```

then `cd /opt/shipwick && docker compose up -d`. Every backup then goes to the
directory *and* to the bucket, under `<prefix>/<application>/<id>/`, and one
that did not reach both has failed: nothing is kept of it. An archive larger
than 5 GB does not fit one S3 upload and fails the same way; such a volume is
backed up to the server only, until multipart uploads exist. `keep` applies in
both places, and only successful backups count towards it: a week of failures
never pushes out the one backup that worked.

With `SHIPWICK_BACKUP_PASSPHRASE` set, everything is encrypted before it is
written anywhere — AES-256-GCM with a key derived from the passphrase; the
format is in [architecture.md](architecture.md#backups) — and the files are
named `<volume>.tar.enc`. The agent decrypts when it restores, verifies or
serves a download; `shipwick backups decrypt` does it on your machine for a
file you fetched from the bucket yourself. Backups written before the
passphrase was set stay readable; if the passphrase changes, the ones written
with the old one are not.

**A bucket belongs to one server.** The agent marks the bucket, under its
prefix, the first time it writes there, and refuses a bucket marked by another
installation: a server set up afresh counts its backups from 1 again and
would write over the ones it is about to be restored from. Two servers share
a bucket by giving each a `SHIPWICK_BACKUP_S3_PREFIX`.

**While a backup runs** the application is held, as during a deployment: a
`deploy`, `stop` or `restore` asked for meanwhile waits up to 30 seconds for a
scheduled backup to finish, and is refused at once with "another operation is
in progress" during one taken by hand; the supervisor does not restart
replicas of that application until the archive is written. A schedule that fires while the
application is being deployed is not lost: the backup is taken when the
deployment is over. A stopped application is not backed up on schedule;
`backups run` still works on it. Deleting an application keeps its backups,
like its volumes: they are listed again when an application of that name is
deployed.

**Knowing that it restores.** A backup nobody has restored is a hope.
`shipwick backups verify` restores one into scratch volumes, starts a single
container of the application's current image on them, and holds it to the
application's health check exactly as a deployment would — `start_period`
included; without a health check, to staying up for the stabilization window.

```text
$ shipwick backups verify postgres
✓ Backup #5 of postgres restores: a container of the current version came up on its data
```

That container is not a replica: it has no route, no name another application
could find it under, no published ports, and the supervisor ignores it. It and
the scratch volumes are removed whatever happened, and the application keeps
running untouched. On failure you get the reason and the container's last
output. It does run with the application's environment, so an application
that writes somewhere other than its volumes when it starts — migrations
against another database, a queue — does that here too.

**The agent's own state.** `shipwick.db` and `encryption.key` are what the
agent knows: every application's configuration and history, the tokens, and
the secrets, which are unreadable without the key. The agent backs both up
once a day — a consistent copy of the database, not a copy of a file in use —
and keeps seven, under `_agent/` where application backups go. It only ever
writes them encrypted: **without `SHIPWICK_BACKUP_PASSPHRASE` the key is
written nowhere**, and `shipwick doctor` says so:

```text
! The encryption key exists only on this server. Set SHIPWICK_BACKUP_PASSPHRASE (and an S3 bucket) in /opt/shipwick/.env to back it up; losing it loses every secret
✓ Agent state backed up 3h ago to s3
```

`shipwick server backup` takes one now.

**Restoring the agent's state**, on a new server or over a damaged one. You
need the passphrase and the two files of the newest backup: the highest number
under `<prefix>/_agent/` in the bucket, or under
`/var/lib/shipwick/backups/_agent/` if the old disk is what you have.

```bash
# On your machine: decrypt the two files.
export SHIPWICK_BACKUP_PASSPHRASE='…'
shipwick backups decrypt shipwick.db.enc
shipwick backups decrypt encryption.key.enc
scp shipwick.db encryption.key user@server:/tmp/state/

# On the server, installed as usual and with the same lines in .env:
cd /opt/shipwick
docker compose stop agent
docker run --rm -v shipwick_agent-data:/data -v /tmp/state:/restore:ro busybox sh -c '
  rm -f /data/shipwick.db-wal /data/shipwick.db-shm &&
  cp /restore/shipwick.db /restore/encryption.key /data/ &&
  chmod 600 /data/shipwick.db /data/encryption.key'
docker compose start agent
rm -r /tmp/state
```

If `SHIPWICK_ENCRYPTION_KEY` is set in `.env`, put the key file's content
there instead of copying the file. The agent starts as the installation it
was: it knows the applications, pulls their images and starts their
containers again — on empty volumes. Bring the data back per application:

```bash
shipwick stop postgres
shipwick backups postgres               # the backups the restored database knows
shipwick backups restore postgres 12    # read from the bucket
shipwick start postgres
```

What the restored state cannot know is what happened after it was taken. A
backup newer than it is still in the bucket, at
`<prefix>/<application>/<id>/<volume>.tar.enc`: fetch it, `shipwick backups
decrypt` it, and put it back with `shipwick restore`. Static applications are
deployed again from their folders, and API tokens other than the one in `.env`
come back with the database. Until the state is restored, the new server
refuses to write into the old one's bucket and `shipwick doctor` says why;
that refusal is what keeps the backups you are about to need.

### Moving to a new server

Restoring the agent's state makes a new server *be* the old one: same
database, same key, same history. Moving is the other thing — a new
installation that takes over what the old one runs — and it is two commands.

```bash
# Against the old server:
shipwick export -o move.swexport

# Against the new one, installed as usual (shipwick login --context new …):
shipwick --context new import move.swexport
```

**What an export holds.** Every application's active configuration with its
`env` values and basic-auth passwords; the stored secrets, registry
credentials and certificates with their keys; the images that exist nowhere
but on the server, which are the ones `shipwick deploy` built; the folders of
static applications; and a tar archive of every volume. Deployment history,
metrics, events, API tokens and backups stay behind: they describe the old
server. `--app <name>` (repeatable) limits it to some applications; the
secrets, credentials and certificates come along either way.

**It is encrypted, always.** The file holds every secret the server has, in
clear, inside the encryption backups use. The passphrase is asked for twice
without echo, or read from `SHIPWICK_EXPORT_PASSPHRASE`, and must be at least
12 characters; the agent encrypts as it writes, so nothing leaves it
unencrypted and nothing is kept on the server. The secrets in the database
are sealed with the old server's key; they are opened for the export and
sealed again on import with the new server's own key. The old key does not
travel. Exporting and importing take a token with the `admin` role.

**What happens to a running application.** Each one is held while its volumes
are read, the way a backup holds it: a `deploy` asked for meanwhile is told to
wait, and an application that is being deployed fails the export, which names
it. If its deploy.yaml has `backups.before` or `backups.stop`, the export does
the same first. Without either, a database is copied while it writes, and
what arrives is what a power cut would have left. The CLI reads the file back
before it calls it an export; one that was cut off is removed.

**What `import` does**, in the order of the file:

1. Stores the secrets, registry credentials and certificates. A certificate
   that has expired since is refused and named.
2. For each application, one after the other: loads its image if the file
   carries one; fills its volumes from the archives, through a container that
   is created for the purpose and never started; then deploys it like any
   deployment — pulled, health-checked, routed — and waits for it. The
   application's first process finds its data. Its deployment has the kind
   `import`.
3. Applications that were stopped on the old server are deployed stopped.

The order is the export's: applications without a `domain` first — what the
others reach by name, a database or a queue — then those with one, each group
oldest first. An export knows no `after`; that belongs to a `shipwick.yaml`.
If the order is wrong for an application, it fails its health check, the
import says so and goes on, and `shipwick redeploy` brings it up once what it
needs is there.

**Nothing is overwritten unless you say so.** An application that exists on
the new server is skipped and named; so is a secret, a credential or a
certificate of the same name; and a volume left behind by a deleted
application of the same name fails that application's import with the
command that removes it. `--overwrite` replaces all of these — an
application together with its volumes — after asking (`--yes` to skip). An
application that fails does not stop the rest; `shipwick import --status`
shows the import that is running or ran last, and the command exits non-zero
when anything failed.

Images from a registry are pulled on the new server with the credentials the
export brought. `pre_deploy` runs as in any deployment, except for an
application that is deployed stopped. Hostnames wait for DNS as always: the
applications run, and each hostname is served once its record points at the
new server (`shipwick status <app>` names a hostname that is waiting, and
the record it waits for).

If the old server's Docker cannot write an image out, the export says so in
the application's entry instead of failing, and the import skips that
application with what to do: deploy it from its project, stop it, and bring
its volumes over with `shipwick backup` and `shipwick restore`.

An export of the whole server can also be written on the server, to where
its backups go, encrypted with `SHIPWICK_BACKUP_PASSPHRASE`:
`shipwick export --to-backups` now, `SHIPWICK_EXPORT_SCHEDULE` regularly, and
`shipwick export --list` shows them. In the bucket it is
`<prefix>/_export/<id>/export.tar.enc`; fetched and passed to
`shipwick import` with that passphrase, it is the same file. Like every
backup it is one upload, so an export larger than 5 GB fails when a bucket is
configured.

### A second server kept ready

Shipwick does not fail over, and does not pretend to. What it can do is keep
a second server honest: installed, holding a recent copy of everything the
first one runs, deployed and stopped, with one command that starts it. A
person decides when. Expect minutes of downtime — the time to notice, to
decide, and for DNS to follow — and data as old as the last export.

**On the first server**, an export on a schedule, next to its backups:

```bash
# /opt/shipwick/.env
SHIPWICK_BACKUP_PASSPHRASE=…          # and the SHIPWICK_BACKUP_S3_* variables
SHIPWICK_EXPORT_SCHEDULE=0 * * * *    # five fields, UTC
SHIPWICK_EXPORT_KEEP=3
```

**On the second server**, the same bucket variables and passphrase, and a
schedule of its own:

```bash
SHIPWICK_STANDBY_SCHEDULE=15 * * * *
```

At those minutes the standby fetches the newest export from the bucket and
imports it with every application deployed and not started: images pulled or
loaded, volumes filled, containers created, nothing running, nothing routed,
no `pre_deploy`. The next import replaces them the same way. `shipwick standby`
shows what is waiting and when it arrived; `shipwick standby pull` imports
now; `shipwick import <file> --stopped --overwrite` does the same from a file,
for a standby without a bucket. A standby only reads the bucket: its own
backups stay on its disk, so the two servers never write to the same place.
An export the schedule has already imported is not imported again.

Nobody has seen these applications run on the standby until the day they
must. Promote it once on purpose, on a quiet day, before you rely on it.

**When the first server is gone:**

```text
$ shipwick --context standby standby promote
This starts 2 applications on https://deploy2.example.com: postgres, my-api.
The server they were exported from must no longer be serving.
Type promote to confirm: promote
✓ postgres is running
✓ my-api is running

Change these DNS records. Until they have changed, visitors still go to the old server:
HOSTNAME          TYPE   VALUE
api.example.com   A      203.0.113.77
```

The applications are started in the order they were imported, each waited
for until it is ready or its startup budget is spent; one that does not come
up is reported and left to the supervisor, and the rest are started
regardless. The records are every hostname of the promoted applications with
the server's own addresses, which the agent knows when
`SHIPWICK_AGENT_DOMAIN` or `SHIPWICK_DASHBOARD_DOMAIN` is set. Certificates
are obtained when the records point at the server, unless you supplied them:
those came with the export.

Nothing checks that the first server is really gone. If it is not, two
servers run the same applications against the same outside services, and
whichever the DNS names gets the visitors: stop the applications there first
if you can reach it.

After a promotion this server is the service. An import that leaves
applications stopped never touches one that runs, and keeps the secrets the
server has, so a late export from the old server does no harm; remove
`SHIPWICK_STANDBY_SCHEDULE` all the same, and give the server's backups a
bucket prefix of their own before you point `SHIPWICK_BACKUP_S3_*` at a bucket
again. Going back is the same procedure in the other direction.

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

`shipwick status` shows each replica's health and restart count, a line for
every hostname whose certificate is not in order ([Caddy](#11-caddy)), and a
feed of
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
every application back up according to its restart policy, and goes on with a
deployment that the reboot interrupted.

> **The agent must be able to reach container addresses** to probe them. In a
> container (the recommended setup) it joins the application network by itself.
> As a host process this works on Linux, but not with Docker Desktop on
> macOS/Windows, where the agent warns at startup.

An application that stays short of healthy replicas for five minutes raises
the `unhealthy` alert, and raises it again, as critical, after an hour: see
[Alerts](#7-deployment).

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

**Before the limit is hit.** A replica that stays at or above 90% of its memory
limit for three samples raises the `memory` alert, on the webhook and in
`shipwick server status`, and clears it below 80%
([Alerts](#7-deployment)). The same samples watch the server's disk.

**Prometheus.** The agent serves what it knows at `GET /metrics`, in the
Prometheus text format: each application's status and replica counts, each
replica's CPU, memory, memory limit and restarts, deployments by outcome and
the duration of the last one, the disk, and the active alerts. It is
authenticated like the rest of the API; a token of the `read` role is enough:

```bash
shipwick token create prometheus --role read
```

```yaml
scrape_configs:
  - job_name: shipwick
    scheme: https
    static_configs:
      - targets: ["agent.example.com"]   # SHIPWICK_AGENT_DOMAIN
    authorization:
      credentials: swk_…                 # or credentials_file
```

```text
shipwick_application_status{application="my-api",status="healthy"} 1
shipwick_application_replicas{application="my-api",state="desired"} 2
shipwick_application_replicas{application="my-api",state="healthy"} 2
shipwick_application_replicas{application="my-api",state="running"} 2
shipwick_replica_cpu_ratio{application="my-api",replica="1"} 0.21
shipwick_replica_memory_bytes{application="my-api",replica="1"} 2.16006656e+08
shipwick_replica_memory_limit_bytes{application="my-api",replica="1"} 1.073741824e+09
shipwick_replica_restarts_total{application="my-api",replica="1"} 0
shipwick_deployments_total{application="my-api",status="succeeded"} 12
shipwick_deployment_last_duration_seconds{application="my-api"} 14.2
shipwick_disk_bytes{state="used"} 3.758096384e+10
shipwick_alerts{kind="disk",severity="warning"} 1
```

A scrape costs the server next to nothing: it reads the agent's database and
what the supervisor and the sampler last saw, and never asks Docker. CPU and
memory are therefore those of the last 30-second sample, and
`shipwick_replica_cpu_ratio` is in cores — 1 is one core kept busy. The full
list is in [docs/api.md](api.md#prometheus-metrics).

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
- **A hostname and path, one application.** A second application claiming a
  hostname in use — as its domain, an alias or a redirect — is refused as a
  config error before anything is started, unless the two serve different
  `path`s of it. A hostname that is redirected is taken whole.
- Application containers publish no host ports, unless `publish` asks for
  some (see [Deployment](#7-deployment)). The proxy is the only other way in
  from outside.
- **Responses are compressed** with zstd or gzip when the browser asks for
  it, for text, JSON, JavaScript, SVG and the other types that shrink, from a
  kilobyte up. What the application already compressed, or does not ask to
  have compressed, leaves as it came. The agent's own API and the dashboard
  are compressed the same way; a followed log goes around the encoder, which
  would keep the response's first bytes back until the application printed
  its next line.

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
only), or set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent to keep it on` —
because the record itself is right there, and the orange cloud is what breaks
the certificate (see *Behind Cloudflare* below for the second way out).
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

**Behind Cloudflare.** With the orange cloud on, a visitor's connection ends
at Cloudflare, and so does the certificate authority's: Caddy cannot prove
over HTTP or TLS that it serves the hostname. Give the agent a Cloudflare API
token and it proves it through a DNS record instead (the ACME DNS-01
challenge), which needs nothing to reach the server:

```bash
# on a new server
curl -fsSL https://get.shipwick.com | SHIPWICK_CLOUDFLARE_API_TOKEN=... sh

# on an existing one: add the line to /opt/shipwick/.env, then
cd /opt/shipwick && docker compose up -d
```

- **The token.** Cloudflare dashboard → *My Profile* → *API Tokens* → *Create
  Token*, with two permissions on the zones your hostnames are in: *Zone →
  Zone → Read* and *Zone → DNS → Edit*. Not the Global API Key. Caddy uses it
  to create and remove the `_acme-challenge` TXT records, nothing else.
- **SSL/TLS mode: Full (strict).** Set it on the zone. In *Flexible* mode
  Cloudflare talks to the server over plain HTTP, which Caddy redirects to
  HTTPS, which Cloudflare fetches over HTTP again: a redirect loop. *Full*
  works but accepts any certificate from the server; *Full (strict)* checks
  the one Caddy obtained, which is the point of obtaining it.
- **Every certificate then comes through Cloudflare DNS**, also for hostnames
  whose record is DNS only. A hostname in a zone the token cannot edit, or at
  another DNS provider, gets no certificate while the token is set.
- **The DNS gate.** A hostname that resolves to Cloudflare's addresses is
  taken as ready: where Cloudflare sends its traffic cannot be seen from the
  outside, and the certificate no longer depends on it. A hostname that does
  not resolve, or resolves to some other server, waits as before.
- **The visitor's address.** Cloudflare's address ranges become trusted
  proxies of Caddy, so applications find the visitor's address first in
  `X-Forwarded-For`, followed by Cloudflare's; from any other sender the
  header is replaced, since anyone can send one. The ranges are the ones
  Cloudflare publishes, as of 2026-10-03; they are compiled into the agent.
- **Where the token is.** In `/opt/shipwick/.env`, in the agent's environment,
  and inside the configuration the agent loads into Caddy — and therefore in
  Caddy's autosaved copy of it on the `caddy-config` volume. It is never
  logged and never returned by the API; `GET /server` says only that the DNS
  challenge is on (`proxy.dns_challenge`), which `shipwick doctor` reads.

The proxy is Shipwick's own build of Caddy, `ghcr.io/shipwick/caddy`: the
official image plus the Cloudflare DNS module and nothing else, on every
installation whether or not the token is set
([Dockerfile.caddy](../Dockerfile.caddy)).

**Wildcards.** `domain` and `aliases` may be a wildcard — one leading `*`
label, quoted in YAML:

```yaml
domain: example.com
aliases: ["*.example.com"]
```

`*.example.com` is every name one label below the domain: `a.example.com`, and
neither `example.com` nor `a.b.example.com`. An authority issues a wildcard
certificate only through the DNS challenge, so a deployment that names one is
refused unless the agent has `SHIPWICK_CLOUDFLARE_API_TOKEN` or a certificate
of your own covers it (below). `*.example.com` and `api.example.com` are
different hostnames and may belong to different applications; a request for
`api.example.com` goes to the application that names it exactly, every other
name under the domain to the wildcard's. `redirects` cannot be wildcards, and
cannot point at a wildcard `domain`.

**A certificate of your own.** For a hostname whose certificate comes from
somewhere else — a corporate authority, a wildcard bought for the whole
domain — give the agent the certificate and its key:

```bash
shipwick cert set example.com --cert fullchain.pem --key privkey.pem
shipwick cert set '*.example.com' --cert wildcard.pem --key wildcard.key
shipwick cert ls                 # HOSTNAME  ISSUER  EXPIRES  COVERS
shipwick cert rm example.com
```

`--cert` is the chain in PEM, the hostname's certificate first and the
intermediates after it; `--key` its private key, without a passphrase. The
agent checks before it stores anything: the chain parses, the key belongs to
the first certificate, that certificate covers the hostname it is stored
under and has not expired. Each failure is a sentence that says which.

A certificate belongs to the server, not to one application. Every hostname
it covers — its DNS names, a wildcard among them counting for one label — is
served with it, whichever application the hostname belongs to; Caddy asks no
authority for those hostnames, and they do not wait for DNS, since there is no
rate limit to protect. A deployment says so: `Routed https://example.com to 2
replicas, with the certificate you supplied for it`. Replacing a certificate
is `cert set` again. Removing one puts its hostnames back under automatic
certificates, and behind the DNS gate, at once.

The key is encrypted in the database like a secret (§12), and nothing returns
it: `cert ls` and the API show the issuer, the dates and the names. Shipwick
does not renew what it did not obtain — `cert ls` marks a certificate in its
last 30 days and one that has expired, `shipwick doctor` reports both, and
Caddy goes on serving an expired certificate until you replace or remove it. Setting and removing certificates
needs an `admin` token; listing needs `read`. At most 50 can be stored.

**Certificates.** Caddy obtains and renews them without being asked, and says
little while it does. `shipwick status` names every hostname whose certificate
is not in order, and why:

```text
HOSTNAME            CERTIFICATE
www.example.com     waiting for DNS: does not resolve yet; add an A record: www.example.com → 62.238.109.115 (DNS only, not proxied)
new.example.com     being obtained: the proxy has no certificate for it yet; HTTPS connections to it fail until it does
old.example.com     expires in 9 days, on 2026-03-10 (Let's Encrypt E7)
```

`--verbose` lists the ones that are in order too, with their issuer and last
day. The agent learns this the way a browser would: once a minute it connects
to Caddy with the hostname as the server name and reads the certificate it is
handed, without verifying it — on a development machine the issuer is Caddy's
own authority, and is reported as that. A hostname is *waiting for DNS* while
the gate above holds it back, *being obtained* from the moment Caddy serves
it until it presents a certificate for it, and *expiring* with 14 days or
less to go: Caddy renews a ninety-day certificate thirty days before its end,
so a certificate that gets that far is one whose renewal has been failing,
and Caddy's log (`docker logs shipwick-caddy-1`) says why. The application's
events record a certificate being obtained and one running out, and a
[webhook](#7-deployment) is told `certificate.expiring` at 14 days and again
at 3. The agent reaches Caddy at `caddy:443`; `SHIPWICK_PROXY_TLS_ADDR`
changes that.

**Traffic.** What the application writes is in `shipwick logs`; what was
asked of it — how often, how it answered, how long it took — is in
`shipwick traffic`:

```text
$ shipwick traffic
Requests through the proxy over the last hour
APP      REQ/MIN   5XX          P95     BYTES
my-api   208       20 (0.2%)    48ms    118 MB
web      12.4      0            2.1ms   3.2 MB

$ shipwick traffic my-api --since 24h
$ shipwick traffic my-api --requests -f
15:04:05  200  GET /api/users  12ms  2 KB  203.0.113.7
15:04:06  502  POST /checkout  1.2s  45 B  203.0.113.9
```

With an application, the command prints its totals — requests, status
classes, the 50th, 95th and 99th percentile of the duration, bytes sent — and
the slowest and the failing paths among its most recent requests.
`--requests` lists those, one per line, and `-f` keeps printing new ones.

The numbers come from Caddy's access log, which the agent follows. A request
counts for the application whose hostname it was sent to, redirects and the
`503` of a stopped application included; a static application has traffic
like any other. Counts are kept per application and minute for seven days,
next to the metrics; durations are counted into a histogram, so a percentile
is as exact as its bucket is wide (48 ms means "between 25 and 50"). The
requests themselves are the last 200 of each application, in the agent's
memory only, and neither the log nor the agent ever holds a query string or
a header: a path is all that is kept of a URL. The access log is written to
the Caddy container's standard output, where Docker caps it at 3 × 10 MB.
Requests for the agent's and the dashboard's own hostnames are not logged.
Reading the log costs the agent about 27 µs of CPU per request — 3% of one
core at 880 requests a second, measured on the development stack, where
Caddy itself used 15 times as much for the same requests.

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
DNS at the server. A hostname that is redirected belongs to one application;
one that is served can be shared, path by path.

**Paths on one domain.** `path` limits an application to one part of its
domain, so that several applications answer under one hostname:

```yaml
name: api                 # example.com/api and everything under it
domain: example.com
path: /api
```

```yaml
name: web                 # the rest of example.com
domain: example.com
```

The longest path wins: with a third application at `/api/admin`, a request
for `/api/admin/users` goes there, `/api/users` to `api`, and everything else
to `web`. Without an application that takes the rest, the rest is a `404`. A
path matches whole segments — `/api` and `/api/users`, not `/apix` — and
without regard to case, so `/API` and `/api` are one path. Each application
keeps its own replicas, health checks, rollouts and rollbacks. Aliases are
served under the same path; `redirects` remain whole hostnames. Two
applications cannot have the same path of a hostname, or both none: the
second deployment is refused and says which application has it.

The application sees the path as the visitor sent it, `/api/users`. Most
frameworks can be told their base path; for one that cannot,
`proxy.strip_prefix: true` removes it and the application sees `/users` —
its redirects and links must then add the prefix themselves, the proxy does
not rewrite responses. A static application under a path is always served
with the prefix removed, since its files are looked up in its folder, and a
request for the path itself is sent to it with a trailing slash, so that
relative links in the page lead below it.

**Headers, passwords and redirects.** The `proxy` block says what Caddy does
for an application's requests beyond passing them on:

```yaml
domain: example.com
proxy:
  headers:                        # on every response
    X-Frame-Options: DENY
    Strict-Transport-Security: max-age=31536000
  basic_auth:
    - path: /admin                # optional; without it, the whole application
      username: admin
      password: ${ADMIN_PASSWORD}
  redirects:
    - from: /old
      to: /new                    # or https://elsewhere.example/page
      status: 308                 # 301, 302, 307 or 308
```

A request meets them in that order: a redirect answers first, then the proxy
asks for a password, then the application — or the folder of a static one —
answers, and the headers are set on what it sends, replacing a header of the
same name. They are on the redirects too, and not on the `401` that asks for
the password. Every path in the block is a path as the visitor asks for it:
under `path: /api`, the admin area is `/api/admin`, with or without
`strip_prefix`, and paths outside `/api` are refused.

*Passwords.* A password is a secret, treated like an `env` value: write
`${ADMIN_PASSWORD}` and it is filled in by the CLI from its environment or
`--env-file`, else by the agent from the secrets stored with `shipwick secret
set`. It is encrypted in the database, shown as `********` wherever the
configuration is shown, and Caddy is given a bcrypt hash of it, never the
password. It must have at least 8 characters and at most 72 bytes. Several
entries with the same `path` are several users of it. Where the paths of two
entries overlap, the longer one decides: with one account for everything and
another for `/admin`, `/admin` accepts only the second. Basic authentication
sends the password with every request; it is protected by HTTPS, which every
domain has. It keeps a staging site or an admin area closed; it is not a
login system.

*Redirects.* `from` is one path, matched exactly; `to` is a path on the same
host or an absolute `https://` URL. The query string travels along unless `to`
has one. The default status, `308`, keeps the method. A redirect to itself,
or a circle of them, is refused.

*Headers.* Values are sent as written: nothing in them is expanded by the
proxy. The headers that describe a connection or the framing of a message —
`Connection`, `Content-Length`, `Content-Encoding`, `Transfer-Encoding`,
`Upgrade` and their relatives — are Caddy's to write and are refused.

After restarting the agent, Caddy's configuration is loaded once more when
an application has passwords: the hashes are made again, with new salts.

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
on the `caddy-static` volume of the compose setup. Aliases, redirects, `path`
and the `proxy` block work as above. Because the root changes with every new folder, deploying a static
application reloads Caddy once — the one kind of change a rollout of
containers never causes.

**The agent owns Caddy's configuration entirely** — it regenerates and reloads
the full config whenever a domain or a port changes, and restores it within
seconds should Caddy ever come back without it. Do not edit it by hand: it
would be overwritten. Custom Caddy directives are not supported; the `proxy`
block of `deploy.yaml` is the supported way to ask more of it.

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
starts, `admin` also deletes applications, manages tokens, secrets, registry
credentials and certificates, and rotates the encryption key:

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
- A registry password stored with `shipwick registry login` is handled the
  same way: never an argument, never listed, logged or returned, sent to the
  Docker daemon with a pull from its registry and nowhere else.
- The key of a certificate you supply (`shipwick cert set`) is treated the
  same way: read from a file, never logged, never returned. It does leave the
  database for one place, the proxy, which cannot serve the certificate
  without it: it is part of the configuration the agent loads into Caddy.
- `SHIPWICK_CLOUDFLARE_API_TOKEN` can edit the DNS of your zones. The agent
  keeps it in memory, hands it to Caddy inside the configuration, and removes
  it from Caddy's errors before they are logged or shown. Caddy's autosaved
  configuration on the `caddy-config` volume contains it, and the keys of
  supplied certificates: that volume is mounted by Caddy alone, and should
  stay so.
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


**Secrets at rest.** The `env` values of every deployment, the secrets
stored with `shipwick secret set`, the registry passwords stored with
`shipwick registry login` and the keys of the certificates supplied with
`shipwick cert set` are encrypted before they are written to
the database (AES-256-GCM; everything else in the record, including the
variable names, stays readable). The key is `encryption.key` in
the data directory, created by the agent on its first start, or the value of
`SHIPWICK_ENCRYPTION_KEY` (64 hex characters) if you prefer to manage it
yourself. This protects a copy of the database file without the key: a backup,
a snapshot, a disk that left the building. It does not protect against root on
the server, who can read the key file and the agent's memory, and it does not
touch the values inside running containers, which `docker inspect` shows to
anyone with the socket. **`encryption.key` and `shipwick.db` belong in a backup
together**: without the key, the database cannot be read, and the agent refuses
to start against it with a message that says so. The agent takes that backup
itself, daily, once `SHIPWICK_BACKUP_PASSPHRASE` is set, and `shipwick doctor`
says when it is not — see [Backups the server takes](#backups-the-server-takes). If a database written by an
earlier release is found on start, its values are encrypted in place, once.

**Rotating the key.** When the key may have been seen — a backup that held it
went astray, someone who had the server left — replace it:

```bash
shipwick server rotate-key      # needs admin
```

The running agent generates a new key, re-encrypts every stored value under it
in one transaction, and carries on with it: nothing is deployed and nothing
restarts. It then replaces `encryption.key`. The new key is written next to the
file before the database is touched and moved into place afterwards, so a
crash or a power cut at any point leaves a database and a key that fit; the
next start finishes or discards what was interrupted. The agent's log records
the rotation and the token that asked for it, never a key. **Back up the new
`encryption.key`**: backups of the database made from then on need it, and
earlier ones still need the old key. Rotation protects what is written from
now on; a copy of the old database together with the old key stays readable,
so a secret that may have leaked with them has to be changed where it is used
as well.

With the key in `SHIPWICK_ENCRYPTION_KEY` the agent cannot finish the job: it
cannot change the environment it will be started with next. The command then
prints the new key, once, for you to put into `/opt/shipwick/.env` before the
agent restarts. Until then the agent keeps working with the new key and holds
a copy of it in `encryption.key.new` in the data directory, so that a lost
terminal does not lose the key — which also means that, for as long as that
file exists, the data directory holds the key next to the database. Started
with the old key still in its environment, the agent refuses to start and says
exactly that, and where the new key is; started with the new one, it removes
the copy. A second rotation is refused until then.

**Backups.** A backup is the application's data, and the agent's own is every
secret together with the key to them. Without `SHIPWICK_BACKUP_PASSPHRASE`
application backups are plain tar files in the data directory, as readable as
the volumes next to them, and the agent's state is not backed up at all: the
key is never written anywhere unencrypted. With it, everything is encrypted
before it reaches the disk or the bucket, so whoever holds the bucket holds
nothing they can read — and whoever holds the passphrase and the bucket holds
everything, which is why the passphrase is not to be stored in the bucket's
account. Truncating or altering an encrypted backup is detected when it is
read. The passphrase and the bucket's two keys are never logged or returned by
the API; the log names the bucket and its host. Taking and verifying a backup
needs the `deploy` role; downloading, restoring or removing one, and anything
about the agent's state, needs `admin`.

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

