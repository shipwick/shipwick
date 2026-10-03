# Shipwick

**Production deployments on your own server.**

One Linux server, one small file per application, one command. Shipwick pulls
the image, starts the replicas, waits until each one is healthy, moves traffic
over and retires the old version. If the new version does not come up, the one
that works keeps serving. HTTPS, scheduled jobs, backups, secrets, tokens with
roles and a dashboard are included.

<p align="center"><img src="docs/images/deploy.png" alt="shipwick deploy: pulled image, started container, replicas passed health checks, routed https://api.example.com, deployment successful" width="820"></p>

**[Documentation](https://shipwick.com/docs/)** · [Handbook](docs/handbook.md) · [Roadmap](ROADMAP.md) · [Changelog](CHANGELOG.md)

## Five minutes to a running application

You need a Linux server with Docker, a domain whose DNS points at it, and a
Dockerfile or an image of your application.

**1. On the server**, as root:

```bash
curl -fsSL https://get.shipwick.com | sh
```

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

It asks for two hostnames (one for the API, one for the dashboard), sets up
three containers, and prints your API token once.

Or from your laptop, once the CLI from step 2 is installed:
`shipwick server install root@203.0.113.10 --agent-domain agent.example.com --dashboard-domain dashboard.example.com`
runs the same installer over SSH, saves the token for you and prints the DNS
records to create.

**2. On your laptop:**

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli    # or: brew install shipwick/tap/shipwick
shipwick login --url https://agent.example.com           # asks for the token, saves it
```

**3. In your project:**

```bash
shipwick init    # writes a Dockerfile and deploy.yaml for a Node, .NET, Go, Python or static project
```

With `build: .` instead of `image:`, `shipwick deploy` builds the image on your
machine and sends it to the server; no registry needed.

```yaml
# deploy.yaml
name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
domain: api.example.com
replicas: 2
health:
  path: /health
```

```bash
shipwick deploy
```

```text
Deploying my-api...

✓ Validated deploy.yaml
✓ Pulled image ghcr.io/company/my-api:1.4.2
✓ Started 1 container
✓ Replica 1 passed health checks
✓ Replica 1/2 is serving 1.4.2
✓ Replica 2 passed health checks
✓ Replica 2/2 is serving 1.4.2
✓ Routed https://api.example.com to 2 replicas
✓ Deployment successful

my-api 1.4.2  deployed in 6.1s
2/2 replicas healthy
https://api.example.com
```

The certificate is obtained on the first request. That is the whole
deployment.

**4. Look at it:**

```bash
shipwick ps            # every application on the server
shipwick status my-api # replicas, health, CPU and memory, history
shipwick logs -f my-api
```

Or open the dashboard: the same information, live, with the everyday actions.

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
shipwick deploy --image ghcr.io/company/api:2.3.1     # rolling: one replica at a time
shipwick rollback api                                  # the previous version, with its whole configuration
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

## What you get

- **Rolling deployments** with health checks, at most one extra container at a time, and automatic rollback.
- **HTTPS** for every domain, certificates included, plus aliases and `www` redirects; behind Cloudflare's proxy, with wildcards, or with a certificate of your own.
- **Routing per application**: several applications on one domain by `path`, response headers, basic authentication and redirects in `proxy`.
- **Supervision**: crashed replicas are restarted with backoff; a container that disappears is recreated.
- **Scheduled jobs** and one-off commands from the application's image: `jobs`, `shipwick run app -- rails db:migrate`.
- **Volumes and backups**: on a schedule, encrypted, to an S3-compatible bucket, and verified by restoring them: `backups` in deploy.yaml, `shipwick backups verify`.
- **Moving house**: `shipwick export` and `shipwick import` take every application, its secrets and its data to another server; a second server can be kept ready and promoted by hand.
- **Secrets** kept out of files with `${NAME}`, encrypted at rest on the server under a key that can be rotated; **registry credentials** stored the same way.
- **Tokens with roles**: a `deploy` token for CI, `read` for a teammate, `admin` for you.
- **Notifications** and **alerts** to Slack, Discord or any webhook; **metrics** and **traffic** with a week of history, and `GET /metrics` for Prometheus.
- **A dashboard**, a CLI and a REST API: anything one does, the others can.

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
