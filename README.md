# Shipwick

**Production deployments on your own server.**

One Linux server, one small file per application, one command. Shipwick pulls
the image, starts the replicas, waits until each one is healthy, moves traffic
over and retires the old version. If the new version does not come up, the one
that works keeps serving. HTTPS, scheduled jobs, backups, secrets, access for
a team and a dashboard are included.

<p align="center"><img src="docs/images/deploy.png" alt="shipwick deploy: pulled image, started container, replicas passed health checks, routed https://api.example.com, deployment successful" width="820"></p>

**[Documentation](https://shipwick.com/docs/)** · [Handbook](docs/handbook.md) · [Roadmap](ROADMAP.md) · [Changelog](CHANGELOG.md)

## Five minutes to a running application

You need:

- a Linux server you can reach as root over SSH (Docker is installed for you
  if it is missing);
- a domain, with two DNS records pointing at the server: one for the API, one
  for the dashboard, and later one per application;
- your project, on your laptop, with Docker running there.

**1. Install the CLI** on your laptop:

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli
```

Or `brew install shipwick/tap/shipwick`. On Windows, download
`shipwick_windows_amd64.exe` (`shipwick_windows_arm64.exe` on Arm) from the
[latest release](https://github.com/shipwick/shipwick/releases/latest).

**2. Install the server**, from your laptop:

```bash
shipwick server install root@203.0.113.10 \
  --agent-domain agent.example.com \
  --dashboard-domain dashboard.example.com
```

It connects over SSH, runs the installer, saves the API token on your laptop
and prints the DNS records to create. Nothing to type on the server. (On the
server itself, the same thing is `curl -fsSL https://get.shipwick.com | sh`.)

```bash
shipwick doctor    # the agent, the token, Docker, the proxy, ports and DNS, one line each
```

**3. Describe your application.** In your project's directory:

```bash
shipwick init
```

```text
✓ Recognised a Node.js application
✓ Wrote Dockerfile, .dockerignore and deploy.yaml

Review them, then run: shipwick deploy
```

It recognises Node, Next.js, Nuxt, SvelteKit, Remix, Astro, .NET, Go, Python
and folders of static files. `deploy.yaml` is the whole description; add the
hostname it should answer to:

```yaml
# deploy.yaml
name: my-api
build: .                  # built on your machine and sent to the server; no registry
port: 3000
domain: api.example.com   # served over HTTPS, certificate included
health:
  path: /health           # traffic moves over only once this answers 200
```

**4. Deploy:**

```bash
shipwick deploy
```

```text
✓ Validated deploy.yaml
✓ Built shipwick.local/my-api:20261003-143029-3125 for linux/amd64
✓ Sent image to the server (57.9 MB)
✓ Started 1 container
✓ Replica 1 passed health checks
✓ Replica 1/1 is serving 20261003-143029-3125
✓ Routed https://api.example.com to 1 replica
✓ Deployment successful

my-api 20261003-143029-3125  deployed in 3.7s
1/1 replicas healthy
https://api.example.com
```

That is the whole deployment. The next one sends only the layers that
changed, a few kilobytes for a code change.

Already have an image in a registry? Name it instead of building:
`image: ghcr.io/company/my-api:1.4.2`. A private registry needs
`shipwick registry login ghcr.io` once.

**5. Look at it:**

```bash
shipwick ps               # every application on the server
shipwick status my-api    # replicas, health, CPU and memory, certificates, history
shipwick logs -f my-api
shipwick logs -p my-api    # the last output of a replica that died
shipwick open my-api      # in the browser; --dashboard opens the dashboard
```

The dashboard shows the same, live, with the everyday actions: deploy a
version, roll back, stop, read logs, check last night's backup.

## Two applications and a database

Every application reaches the others by name on the server: `postgres:5432`,
`api:8080`. No domain is needed for that, and nothing outside the server can
reach those names. Several applications live in one `shipwick.yaml`:

```yaml
# shipwick.yaml
apps:
  - name: postgres
    image: postgres:17
    port: 5432
    env:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}   # filled in when you deploy, never in the file
    volumes:
      - name: data
        path: /var/lib/postgresql/data
    health:
      tcp: 5432
    deploy:
      strategy: recreate                        # a database cannot run twice

  - name: api
    image: ghcr.io/company/api:2.3.0
    port: 8080
    domain: api.example.com
    env:
      DATABASE_URL: postgres://app:${POSTGRES_PASSWORD}@postgres:5432/app
    health:
      path: /health
    pre_deploy:
      command: ["./migrate", "up"]              # runs from the new image before any replica starts
    after: [postgres]                           # not before the database is up

  - name: web
    image: ghcr.io/company/web:2.3.0
    port: 3000
    domain: example.com
    redirects: [www.example.com]
```

```bash
shipwick secret set POSTGRES_PASSWORD     # once; asked without echo, kept encrypted on the server
shipwick deploy
```

`postgres` and `web` deploy at the same time, `api` once `postgres` is done.
If one fails, what depends on it is skipped and the rest finishes.

## A new version, and back again

```bash
shipwick deploy api --image ghcr.io/company/api:2.3.1  # api and nothing else; rolling: one replica at a time
shipwick rollback api                                   # the previous version, with its whole configuration
```

A deployment that fails is undone on its own. The version that works keeps
serving throughout, and `shipwick deploy` exits non-zero, so it works as a gate
in CI:

```text
✗ Deployment failed

  replica 1 did not become healthy within 30s: GET /health on port 8080: HTTP 500

  Last output of replica 1:
  Error: DATABASE_URL is not set

api is still running 2.3.0; the failed deployment did not affect it.
```

## Keeping it up to date

```bash
shipwick upgrade                  # the CLI on this machine
shipwick server install root@203.0.113.10   # the server: the same command upgrades it
```

The server keeps its token, its applications and their data; they go on
serving while the three Shipwick containers are replaced. What changed between
versions, and what to do about it, is in the [changelog](CHANGELOG.md).
`shipwick server status` and the dashboard say when a newer release exists.

## What you get

- **Rolling deployments** with health checks, at most one extra container at a time, and automatic rollback.
- **HTTPS** for every domain, certificates included, plus aliases and `www` redirects; behind Cloudflare's proxy, with wildcards, or with a certificate of your own.
- **Routing per application**: several applications on one domain by `path`, response headers, basic authentication and redirects in `proxy`.
- **Supervision**: crashed replicas are restarted with backoff; a container that disappears is recreated.
- **Logs that outlive the container**: the last output of every replica that crashed, was killed for memory or was replaced is kept and searched, so "why did it die?" has an answer the next morning.
- **Scheduled jobs** and one-off commands from the application's image: `jobs`, `shipwick run app -- rails db:migrate`.
- **Volumes and backups**: on a schedule, encrypted, to an S3-compatible bucket, and verified by restoring them: `backups` in deploy.yaml, `shipwick backups verify`.
- **Moving house**: `shipwick export` and `shipwick import` take every application, its secrets and its data to another server; a second server can be kept ready and promoted by hand.
- **The configuration back from the server**: `shipwick config my-api` prints the `deploy.yaml` of what runs, for the day the file is lost.
- **Secrets** kept out of files with `${NAME}`, encrypted at rest on the server under a key that can be rotated; **registry credentials** stored the same way.
- **Access for a team**: tokens with roles — `deploy` for CI, limited to the applications it deploys and with an end date if you like; `read` for a teammate; `admin` for you — sign-in to the dashboard with the company's accounts through OpenID Connect, and an audit trail of who did what.
- **Notifications** and **alerts** to Slack, Discord or any webhook; **metrics** and **traffic** with a week of history, and `GET /metrics` for Prometheus.
- **A dashboard**, a CLI and a REST API: what is running, what is wrong and where to look, and the everyday actions; one dashboard can show several servers.
- **At home in a company network**: behind an HTTP proxy, with a certificate authority of your own, and installable on a server that has no way out.

## Learn more

| | |
|---|---|
| [Install on a server](https://shipwick.com/docs/getting-started/install) · [Install the CLI](https://shipwick.com/docs/getting-started/install-cli) · [Your first deployment](https://shipwick.com/docs/getting-started/first-deployment) | Getting started |
| [deploy.yaml](https://shipwick.com/docs/reference/deploy-yaml) · [CLI](https://shipwick.com/docs/reference/cli) · [REST API](https://shipwick.com/docs/reference/api) · [Agent configuration](https://shipwick.com/docs/reference/agent-configuration) | Every field, command, variable and endpoint |
| [Deployments](https://shipwick.com/docs/concepts/deployments) · [Rollback](https://shipwick.com/docs/concepts/rollback) · [Health checks](https://shipwick.com/docs/concepts/health-and-supervision) · [Routing and HTTPS](https://shipwick.com/docs/concepts/routing-and-https) | How it works, and why |
| [Deploy from CI](https://shipwick.com/docs/tasks/deploy-from-ci) · [Jobs](https://shipwick.com/docs/tasks/jobs) · [Backups](https://shipwick.com/docs/tasks/backups) · [Tokens](https://shipwick.com/docs/tasks/tokens) · [Notifications](https://shipwick.com/docs/tasks/notifications) | Tasks |
| [Security](https://shipwick.com/docs/security) | Read this before a server that matters |

The same material, as one document in this repository: [docs/handbook.md](docs/handbook.md).
How it is built: [docs/architecture.md](docs/architecture.md).

## One server, on purpose

Shipwick schedules nothing across machines. A single server runs the 1 to 20
applications of most products with room to spare; what it lacks is the platform
around them, and Shipwick is that platform: one process, one SQLite file, one
YAML file per application. When one server is no longer enough, you have
outgrown Shipwick.

**Security in one sentence:** an admin token is equivalent to root on the
server, so keep the API behind HTTPS or an SSH tunnel and give CI a `deploy`
token. Details in the [handbook](docs/handbook.md#12-security); report
vulnerabilities privately per [SECURITY.md](SECURITY.md).

## Contributing and license

Bugs and ideas: [issues](https://github.com/shipwick/shipwick/issues). How to
work on the code: [CONTRIBUTING.md](CONTRIBUTING.md). Apache License 2.0; the
license of every dependency is listed in [NOTICE](NOTICE).
