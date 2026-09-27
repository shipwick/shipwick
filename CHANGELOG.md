# Changelog

Notable changes to Shipwick. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, a minor version may
change the API, `deploy.yaml` or the on-disk format; when it does, the entry
says so under **Changed** and explains how to upgrade.

## [Unreleased]

### Fixed

- The installer's removal of earlier releases' images did nothing: it asked
  `docker images` for two repositories at once, which it refuses. It now asks
  for each in turn. Run the installer again to reclaim the space.

## [0.4.0] - 2026-09-27

### Added

- `build: .` in deploy.yaml, in place of `image`: `shipwick deploy` builds the
  image on your machine with `docker build`, for the server's architecture,
  and sends it to the agent, which loads it — no registry, no `docker login`.
  The image is tagged `shipwick.local/<name>:<stamp>`; the agent never pulls
  from that host and never builds. `POST /applications/:name/images` takes
  the archive.
- `static: dist/` in deploy.yaml: a built frontend served by Caddy itself, with
  no container. `shipwick deploy` uploads the folder (`PUT …/static`, up to
  512 MB) and deploys it by its digest; the agent copies it into the proxy,
  checks for `index.html` and routes the domain to it. Rollback and redeploy
  re-use the kept folder. New error code `STATIC_APPLICATION` for logs, metrics
  and commands, which such an application does not have. The compose files
  give Caddy a `caddy-static` volume for the folders.
- `shipwick init` recognises the project in the current directory — a Nuxt,
  Next or Node application, a .NET project, a Go program, a Python application
  — and writes a multi-stage `Dockerfile` (small runtime image, non-root user,
  port exposed), a `.dockerignore` and a `deploy.yaml` with `build: .`. A
  folder of static files, or a Vite or Astro project that builds one, gets
  `static: <dir>` and no Dockerfile. Existing `Dockerfile` and `.dockerignore`
  files are kept; `--image` keeps the old behaviour; `--static <dir>` forces
  the static kind.
- `shipwick server install user@host` installs or upgrades the server over
  SSH from your machine: Docker when it is missing, then the installer with
  `--agent-domain` and `--dashboard-domain`. The token is saved as a context
  and the DNS records to create are printed.
- `shipwick doctor` checks the setup in one screen — CLI and agent versions
  against the latest release, token, Docker, proxy, ports 80 and 443, and for
  every domain whether DNS points at the server and HTTPS answers — and exits
  non-zero when something is broken.
- `shipwick open [app]` opens the application's address in the browser.
- `shipwick deploy` in a directory without a `deploy.yaml` writes one first,
  the way `init` does, when a terminal is attached.
- A first deployment ends with the two commands to run next: `logs -f` and
  `status`.
- **Several applications in one file.** `shipwick.yaml` holds an `apps` list
  in which every entry is a complete `deploy.yaml`; `after: [postgres]` names
  the entries one must wait for. `shipwick deploy` uses it when there is no
  `deploy.yaml`, `shipwick validate` checks it and prints the order.
  Annotated example: `configs/shipwick.example.yaml`.
- **Faster deployments.** The applications of a `shipwick.yaml` deploy at the
  same time, in dependency order, up to four at once (`--parallel N`); every
  line of output carries its application's name. An application whose
  dependency did not deploy is skipped, the rest finishes, and the command
  exits non-zero if any failed or was skipped. Several `deploy.yaml` files
  given with `-f` still deploy one after the other.
- Secrets kept on the server. `shipwick secret set DATABASE_PASSWORD` stores a
  value, encrypted like `env` values are, and `${DATABASE_PASSWORD}` in an
  `env` value of `deploy.yaml` is filled in by the agent when you deploy;
  `--env-file` becomes optional. `shipwick secret ls` lists names and dates,
  `shipwick secret rm` removes one. A name that is set neither where `shipwick`
  runs nor on the server is refused before anything is recorded, with the
  command to run. `$${NAME}` in an `env` value reaches the agent as written
  and becomes the literal `${NAME}` there. API: `GET /secrets`,
  `PUT /secrets/:name`, `DELETE /secrets/:name`.
- `health.start_period`: extra time, up to 30 minutes, that a replica gets to
  come up before failed health checks count — during a deployment and after a
  restart by the supervisor — so a slow starter no longer has to raise
  `retries`.
- `shipwick volumes` lists every volume on the server with the application it
  belongs to, how much it holds and whether that application still exists;
  `shipwick volumes rm <name>` removes a volume of a deleted application. The
  API gained `GET /volumes` and `DELETE /volumes/:name` (`409 VOLUME_IN_USE`
  while the application exists).
- The proxy compresses application responses with zstd or gzip when the
  client asks for it, for compressible content types from a kilobyte up.
- A deployment whose hostname is not ready spells out the record to create:
  `add an A record: api.example.com → 62.238.109.115 (DNS only, not proxied)`,
  with an AAAA record when the server has an IPv6 address; a hostname behind
  Cloudflare's proxy is told to turn the proxy off for the record instead.
- The API limits failed authentications: after 20 within a minute from one
  address, its wrong tokens are answered `429 RATE_LIMITED` with `Retry-After`
  for the next minute. A valid token is never refused.
- Dashboard: a **Secrets** page lists the secrets kept on the server, and an
  admin adds, replaces or removes one there; values are never shown.
- Dashboard: a **Volumes** page lists every volume on the server with its
  application and size, and an admin removes the volume of a deleted
  application there.
- Dashboard: a static application shows what it serves (`42 files, 3.1 MB,
  served by the proxy`) in place of replicas, logs and metrics; an application
  with `build` says its image is built by `shipwick deploy`, and the deploy
  dialog offers no image field for either. `health.start_period` is shown with
  the health check.

### Changed

- The database gains two migrations, applied when the 0.4 agent first starts:
  the `secrets` table and the columns of static deployments. A 0.3 agent
  refuses a database that has them, so upgrade with the installer and do not
  go back. The 0.3 CLI keeps working against the 0.4 agent; the new commands
  and `deploy.yaml` keys need the 0.4 CLI (`shipwick upgrade`).
- The compose file gives Caddy a `caddy-static` volume for the folders of
  static applications, so the upgrade recreates the Caddy container once:
  connections open at that moment are reset, applications keep running.
- The installer removes the agent and dashboard images of earlier releases
  after an upgrade; they used to stay behind, a few hundred megabytes per
  release.

### Fixed

- An empty `entrypoint` or `command` argument (`command: ""`) is refused instead
  of being handed to Docker as an empty argument.
- After a rollback, and after `shipwick start`, a replica that had just passed
  its health check could be reported as `starting` — and the application as
  `DOWN` — until the supervisor's next probe. A passed check now counts at once.
- `shipwick restore --volume nope` and `shipwick jobs logs` for an unknown job
  said the server did not know the application; they now say what was not
  found.

## [0.3.1] - 2026-09-27

Found on a real server the day 0.3.0 shipped. Upgrade by running the installer
again; nothing else changes.

### Fixed

- The DNS check that holds a hostname back until it points at the server asks
  public resolvers (1.1.1.1, 8.8.8.8, 9.9.9.9) instead of the server's own,
  which remembers that a record did not exist for the zone's negative TTL —
  half an hour on Cloudflare — and kept the hostname waiting that long after
  the record was created.
- An application could show as `FAILED` for one poll while its first deployment
  was being recorded or the moment a deployment succeeded: the application and
  its in-flight deployments were read separately, and a commit in between made
  it look as if it had neither. They are read in one transaction now.

## [0.3.0] - 2026-09-27

Before 1.0 a minor version may change how things work under the hood. This one
adds to the database and to the data directory. Upgrading is still running the
installer again: the agent applies four migrations on its first start, creates
`encryption.key` next to `shipwick.db` and encrypts the environment values of
earlier deployments once. **Back up `encryption.key` with the database**;
without it the database cannot be read. The token from the installer keeps
working as the root token, application containers are not touched, and the
compose file gains two optional variables for notifications.

### Added

- **A hostname is served once its DNS points at the server.** Deploying before
  the record exists no longer costs the certificate: Caddy is told about a
  domain, alias or redirect only when it resolves to this server, so Let's
  Encrypt's five-failures-per-hour limit is never spent on a hostname that
  cannot pass yet. Until then `shipwick deploy` warns — `does not resolve yet`,
  or `resolves to 104.21.5.6, not to this server` — instead of claiming the
  domain is routed; the record is checked again every 10 seconds, and the
  application's events say when the hostname is being served. The server's own
  addresses are learned from `SHIPWICK_AGENT_DOMAIN` and
  `SHIPWICK_DASHBOARD_DOMAIN`; with neither set, resolving at all is enough.
- **Old images are removed.** After a successful deployment, and after `shipwick
  delete`, the images only retired deployments refer to are untagged. The running
  version and the rollback target are always kept, and so is any image another
  application or a container outside Shipwick uses.
- winget manifests for the CLI under `packaging/winget/`, generated from a
  release's checksums.
- **`publish`** in `deploy.yaml`: a container port published on a port of the
  server itself, for services the proxy cannot serve because they are not
  HTTP — a database reached from a laptop, a game server. TCP or UDP, on one
  address of the server or on all; needs the recreate strategy and one replica.
  A port the agent, the proxy or another application already holds is refused
  before anything is started.
- **Several hostnames per application.** `aliases` lists hostnames served
  exactly like `domain`; `redirects` lists hostnames answered with a `308` to
  `https://<domain>` with the same path and query — `www.example.com`, an old
  domain. Redirects work while the application is stopped. A hostname in use
  anywhere, in any role, is refused, and the error names the offending line
  (`aliases[1]`).
- **Health checks for applications that are not HTTP.** `health.tcp: 5432`
  counts a replica healthy when the port accepts a connection;
  `health.command: ["pg_isready", "-U", "postgres"]` runs the command inside
  the replica and reads its exit code. `path`, `tcp` and `command` are
  alternatives; `interval`, `timeout` and `retries` apply to all three. A
  failed command check reports the exit code and the last line it printed.
- **`env` values are encrypted in the database.** Every deployment's environment
  values are stored as AES-256-GCM ciphertext; names, images and everything else
  stay readable. A copy of `shipwick.db` without the key reveals no secrets. The
  values of deployments made by earlier releases are encrypted on the first
  start after upgrading. The key comes from `SHIPWICK_ENCRYPTION_KEY` (64 hex
  characters) when set.
- **`entrypoint`, `command` and `user`** in `deploy.yaml` replace what the image
  declares, for running a worker or a second program from the same image. A
  string is one argument, a list is several; nothing goes through a shell.
- **`logging`** in `deploy.yaml` ships replica logs through a Docker logging
  driver (`gelf`, `syslog`, `fluentd`, `awslogs`, `splunk`, `journald` or
  `local`) instead of the server's disk. `shipwick logs` keeps working through
  the local copy Docker keeps.
- **Several API tokens, with roles.** `shipwick token create ci --role deploy`
  makes a token that can deploy, roll back, stop and start but not delete
  applications or manage tokens; `read` only looks; `admin` does everything.
  The token from the installer is the root token, admin, and stays as it was.
  `shipwick token ls` shows when each token was last used, `shipwick token
  revoke` ends it. A token asked to do more than its role allows is told which
  role it needs.
- Deployments record which token made them (`by` in the API and the dashboard's
  history), and a stop or start by a token other than root says so in the
  application's events.
- **`shipwick upgrade`** replaces the CLI with the latest release, verified
  against the release's checksums, and says when the server is behind (the
  server is still upgraded by running the installer there). A CLI installed
  with Homebrew or winget is left to the package manager; the command prints
  the line to run. `--check` only reports.
- **Several servers.** `shipwick login --context staging` saves a second server
  under a name; `--context` or `SHIPWICK_CONTEXT` picks one for a command, and
  `shipwick context ls | use | rm | current` manage them. An existing config
  file keeps working as the context `default`.
- **Volume backups.** `shipwick backup` downloads an application's volumes as
  tar archives, `shipwick restore` puts one back into a stopped application.
  The agent reads and writes the volume through the replica's container, so
  nothing needs to be installed on the server. New endpoints
  `GET`/`PUT /applications/:name/volumes/:volume/archive` and
  `GET /applications/:name/volumes`; a restore of a running application is
  `409 APPLICATION_RUNNING`.
- **Notifications.** Set `SHIPWICK_WEBHOOK_URL` on the agent and it posts
  every deployment's outcome — succeeded, failed, rolled back — and every
  application that goes down or recovers to a Slack or Discord webhook, or as
  JSON to any HTTPS endpoint; `SHIPWICK_WEBHOOK_SECRET` signs the requests.
  `shipwick server status` shows whether one is configured.
- **Metrics history.** The agent records the CPU and memory of every running
  replica every 30 seconds and keeps a week; `GET
  /applications/:name/metrics/history?since=1h|24h|7d` serves it per replica,
  aggregated to chart size, for the dashboard to draw.
- **`pre_deploy`** in `deploy.yaml`: a command run from the new image, with the
  application's environment, before any replica of the new version starts —
  database migrations. If it fails, the deployment fails before anything was
  touched, and its last output lines are shown with the error.
- **`jobs`** in `deploy.yaml`: commands on a cron schedule (UTC), each in a
  one-off container from the application's image. `shipwick jobs` lists them
  with their last and next run, `shipwick jobs run` starts one now,
  `shipwick jobs logs` shows a run's output.
- **`shipwick run <app> -- <command>`** runs a command in a one-off container
  of the application, prints its output and exits with its exit code.

### Changed

- The agent creates `encryption.key` in its data directory on first start; back
  it up together with `shipwick.db`. Without it the database cannot be read, and
  the agent refuses to start against it.

### Fixed

- The agent, Caddy and dashboard containers of the production compose file now
  cap their logs at 3 × 10 MB each, like application replicas always did.
  Applied on the next `curl -fsSL https://get.shipwick.com | sh`.

## [0.2.0] - 2026-09-27

Before 1.0 a minor version may change how things work under the hood. This one
does: routing. Upgrading is still running the installer again, but this
upgrade recreates the Caddy container once, because it joins a second network.
Established connections through the proxy are cut at that moment; nothing else
is, and applications keep running throughout.

### Added

- **Applications reach each other by name.** Every application is `http://<name>:<port>`
  for the other applications on the server, no domain needed: `orders` calls
  `payments` at `http://payments:8080` and gets a healthy replica of the current
  version. Nothing outside the server can reach these names.
- **`volumes`** in `deploy.yaml`: named Docker volumes mounted into the
  replica, for databases and everything else that must keep its data. A volume
  belongs to the application and survives redeployments, rollbacks and
  `shipwick delete`.
- **`deploy.strategy: recreate`**: the running version is stopped before the
  new one starts, for applications whose two versions cannot run side by side.
  Required with `volumes`. A failed deployment starts the old containers again.
- **`${NAME}` placeholders** in `deploy.yaml` are filled in by the CLI from its
  environment or from `--env-file` before the file is validated or sent, so
  that secrets stay out of the file and the repository. An unset name is an
  error, never an empty value; `$${NAME}` is a literal.
- **Several applications in one command**: `shipwick deploy -f a/deploy.yaml
  -f b/deploy.yaml` deploys them in order and stops at the first failure.
  `validate` takes several files too.

### Changed

- **Rollouts no longer reload the proxy.** Caddy is told an application's
  name and asks Docker's DNS who carries it for every request; replicas take
  the name when they are ready and lose it when they stop. A rollout, a crash
  or a restart therefore changes nothing in Caddy's configuration, which used to
  be reloaded once per replica replaced — and a reload resets connections that
  are being established at that instant. Measured with a new connection per
  request and added latency, 18 consecutive rolling redeploys answered 15,774
  of 15,774 requests. A crashed replica now leaves the rotation at once instead
  of at the supervisor's next look.
- The names `agent`, `caddy`, `dashboard` and `localhost` can no longer be
  application names: they are Shipwick's own on the network applications share.
- The agent's Docker network is joined by a second one, `<network>-services`,
  created on startup.

## [0.1.1] - 2026-09-21

Found by installing 0.1.0 on real servers. The agent, the CLI and the dashboard
are unchanged; upgrade by running the installer again.

### Added

- The CLI can be installed with Homebrew: `brew install shipwick/tap/shipwick`.

### Fixed

- The installer checks that ports 80 and 443 are free before it changes
  anything, and says what holds them. It used to write its files, start half of
  the services and stop at Docker's error.
- Run again after a first run that did not finish, the installer says where the
  API token is; it used to show it only in the run that generated it.
- `SHIPWICK_HTTP_PORT`, `SHIPWICK_HTTPS_PORT` and the image overrides given to
  the installer are kept in `.env`, so an upgrade does not move the proxy back
  to the default ports.
- The installer works with BusyBox `wget` (Alpine without curl).

### Documentation

- What a deployment costs is stated as measured on a real server: connections
  being established at the instant Caddy's configuration is reloaded are reset —
  5 of 233 requests in the worst case, a new connection per request from 100 ms
  away. The earlier figure, 100 of 100, was measured over loopback, where this
  cannot be seen.

## [0.1.0] - 2026-09-20

The first release: production deployments on a single server, without
Kubernetes.

### Added

- **Agent** (`shipwick-agent`): a single static binary that drives the Docker
  Engine API directly, keeps its state in SQLite, and exposes a token-protected
  REST API. Deployment records are immutable.
- **`deploy.yaml`**: name, image, port, domain, replicas, env, health check,
  CPU and memory limits, restart policy. Strictly validated, with errors that
  name the field and what was expected.
- **Rolling deployments**: replicas are replaced one at a time, each only after
  it proved healthy, with at most one container above the desired count. A
  failed rollout is undone automatically; the application keeps serving.
- **Rollback** to any earlier successful deployment, with its full stored
  configuration, through the same engine as a deployment.
- **Health checks** over HTTP, at deploy time and continuously afterwards.
- **Supervision**: crashed and unhealthy replicas are restarted with backoff
  (1s, 2s, 5s, 10s, 30s), then marked `CRASH_LOOP`; replicas that disappear are
  recreated within about a second.
- **Caddy integration**: automatic HTTPS and routing for every application's
  domain, load balancing across replicas, configuration verified and restored
  continuously. Optionally serves the agent's API and the dashboard over HTTPS.
- **Metrics**: CPU and memory per replica and per application, against their
  limits.
- **CLI** (`shipwick`): `init`, `validate`, `deploy`, `redeploy`, `rollback`,
  `status`, `ps`, `logs`, `stop`, `start`, `delete`, `login`, `server status`.
  Made for terminals and for CI (`shipwick deploy --image …:$GIT_SHA`).
- **Dashboard**: overview, applications, deployments, servers, live logs;
  redeploy and rollback. The token never reaches the browser.
- **Installer**: `curl -fsSL https://get.shipwick.com | sh` sets up the agent,
  Caddy and the dashboard on a server, or only the CLI with `--cli`. Release
  binaries are verified against their checksums.

### Known limitations

- Secrets in `env` are stored unencrypted in the agent's SQLite file.
- One token per agent; no users or roles.
- When a replica crashes, one in-flight request may receive a 502.
- No volumes, and no custom Caddy directives.

[Unreleased]: https://github.com/shipwick/shipwick/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/shipwick/shipwick/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/shipwick/shipwick/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/shipwick/shipwick/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/shipwick/shipwick/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/shipwick/shipwick/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shipwick/shipwick/releases/tag/v0.1.0
