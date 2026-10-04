# Contributing to Shipwick

Thanks for considering it. Bug reports, documentation fixes and code are all
welcome.

**Security problems do not belong in issues or pull requests** — see
[SECURITY.md](SECURITY.md).

Everyone taking part is expected to follow the
[code of conduct](https://github.com/shipwick/.github/blob/main/CODE_OF_CONDUCT.md).
Who decides what, and how one becomes a maintainer, is in
[GOVERNANCE.md](GOVERNANCE.md). If you run Shipwick in production, add
yourself to [ADOPTERS.md](ADOPTERS.md).

## Before you write code

For anything beyond a bug fix, open an issue first and describe the problem you
want solved. Shipwick stays small on purpose, and "no" is a common answer to
features; it is better to hear it before the work than after.

What guides those answers:

- **Simplicity over completeness.** One server, 1–20 applications. No
  scheduler, no cluster, no plugin system.
- **Nothing to operate besides Docker.** No external database, queue or cache;
  the agent is one static binary with a SQLite file.
- **Few dependencies.** A new one needs a reason the standard library cannot
  answer.
- **No shell, ever.** The agent talks to the Docker Engine API; nothing from a
  user's configuration is executed or interpolated into a command.
- **One way to do a thing.** Deploy, redeploy and rollback share one engine;
  there is one function that creates replicas. A second path for the same job
  is a bug waiting to diverge.

Out of scope, and likely to stay there: multi-node scheduling, building images,
anything that requires an external service. The [roadmap](ROADMAP.md)
lists what may come.

## Getting started

Go 1.27+, Docker, and Node 24 for the dashboard.

```bash
make dev    # dashboard :3000, agent :9000, Caddy :8080/:8443
```

The [Development section of the handbook](docs/handbook.md#13-development) covers the
rest, including running without `make` on Windows.
[docs/architecture.md](docs/architecture.md) explains how the pieces fit and
why they are the way they are — read the part you are about to change. The
dashboard has its own guide: [dashboard/README.md](dashboard/README.md).

## Checks

CI runs these on every pull request; running them first saves a round trip.

```bash
make lint               # gofmt, go vet
make test               # unit tests: no Docker, a few seconds
make test-race          # or test-race-docker on machines without cgo
make test-integration   # needs a Docker daemon
make test-full-disk     # what a disk without room does, on a small filesystem in a container
make test-dashboard     # if you touched dashboard/
```

### What CI runs where

A pull request runs the checks above on Ubuntu 24.04 (amd64) with the Docker
of GitHub's image, builds the images and the packages, and runs the
installer once, on Alpine. `main` and every release also run four wider
jobs. None of them can be run on a contributor's machine as a whole; each
cell can (`scripts/test-install.sh`, or `DOCKER_HOST` at a `docker:<version>-dind`
container for the integration tests).

| Job | Cells | What a cell proves | What it does not |
|---|---|---|---|
| Installer | Debian 12, 13 · Ubuntu 22.04, 24.04, 26.04 · Rocky Linux 8, 9, 10 · Fedora · Alpine, each on amd64 and arm64 | `scripts/install.sh` of the commit installs and then upgrades the latest release with that distribution's `sh`, `awk`, `sed`, coreutils and `curl` (BusyBox `wget` on Alpine), on that architecture | The distribution's kernel, its own Docker packages, systemd, firewall, SELinux: the distribution is a container, the daemon is Docker's image. The agent, dashboard and proxy of the commit: the images installed are the release's |
| Installer, older Engines | Debian 12 with Docker 20.10 and the current Compose plugin; Debian 13 with Docker 26.1 and its Compose (amd64) | The same installation against the Engine version that distribution packages | The distribution's build of that Engine: the daemon is Docker's image of the version |
| Docker versions | Docker 20.10, 26.1, 28, 29, each on amd64 and arm64 | The integration tests — the agent's use of the Engine API — pass against a daemon of that version | Distribution patches to Docker; the kernel is the runner's, Ubuntu 24.04's |
| Integration | Ubuntu 22.04 on amd64 and arm64, Ubuntu 24.04 on arm64 | The same tests on that image's kernel and its Docker, on real arm64 hardware | Other distributions' kernels: GitHub hosts Ubuntu only |

A cell of the last two fails when a test is skipped: the tests skip without
a daemon, and a cell that tested nothing must not be green.

## What a good change looks like

- **It comes with a test that fails without it.** The engine is tested against
  in-memory fakes of Docker and Caddy and a synthetic clock, so timing-dependent
  behavior — backoff, reconciliation, rollouts — is tested in milliseconds and
  without `time.Sleep`. Keep it that way.
- **Behavior claimed in the README is tested and was tried for real.** If you
  change what Shipwick promises, say in the pull request how you verified it
  against a real Docker.
- **Errors tell the user what to do**, in their terms: the field in
  `deploy.yaml`, the command to run next. Secrets — tokens, env values,
  `Authorization` headers — never appear in logs, errors or API responses.
- **Everything the agent receives is validated by the agent**, whatever the CLI
  already checked.
- **Documentation moves with the code**: the README for behavior users see,
  [docs/api.md](docs/api.md) for the API (together with the dashboard and its
  mock agent, which implement the same contract), `CHANGELOG.md` under
  *Unreleased*.
  The website, [shipwick.com](https://shipwick.com), is a repository of its own,
  [shipwick/website](https://github.com/shipwick/website): a change to a
  command, a `deploy.yaml` field, the API or the installer needs a pull request
  there too.
- Code reads like the code around it. Comments explain why, not what.

Keep pull requests to one concern; a refactoring and a behavior change are two
pull requests.

## License

Shipwick is licensed under the [Apache License 2.0](LICENSE). By submitting a
contribution you agree that it is licensed under the same terms (section 5 of
the license); you keep the copyright to your work.
