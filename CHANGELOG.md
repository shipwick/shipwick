# Changelog

Notable changes to Shipwick. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, a minor version may
change the API, `deploy.yaml` or the on-disk format; when it does, the entry
says so under **Changed** and explains how to upgrade.

## [Unreleased]

### Added

- **Old images are removed.** After a successful deployment, and after `shipwick
  delete`, the images only retired deployments refer to are untagged. The running
  version and the rollback target are always kept, and so is any image another
  application or a container outside Shipwick uses.
- winget manifests for the CLI under `packaging/winget/`, generated from a
  release's checksums.

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

[Unreleased]: https://github.com/shipwick/shipwick/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/shipwick/shipwick/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/shipwick/shipwick/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shipwick/shipwick/releases/tag/v0.1.0
