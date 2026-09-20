# Changelog

Notable changes to Shipwick. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, a minor version may
change the API, `deploy.yaml` or the on-disk format; when it does, the entry
says so under **Changed** and explains how to upgrade.

## [Unreleased]

## [0.1.0]

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
- **CLI** (`deployctl`): `init`, `validate`, `deploy`, `redeploy`, `rollback`,
  `status`, `ps`, `logs`, `stop`, `start`, `delete`, `login`, `server status`.
  Made for terminals and for CI (`deployctl deploy --image …:$GIT_SHA`).
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

[Unreleased]: https://github.com/shipwick/shipwick/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/shipwick/shipwick/releases/tag/v0.1.0
