# AGENTS.md

Instructions for coding agents working in this repository. Humans: start with
[CONTRIBUTING.md](CONTRIBUTING.md); everything here applies to you as well.

## What this is

Shipwick deploys Docker applications to a single server. Three programs:

| Path | What | Stack |
|---|---|---|
| `agent/` | `shipwick-agent`: runs on the server, drives Docker and Caddy, REST API | Go, SQLite |
| `cli/` | `deployctl`: the command-line client | Go, Cobra |
| `dashboard/` | Web UI, talks to the agent through its own server-side proxy | Nuxt 4, TypeScript, Tailwind |

Shared Go packages are in `pkg/`: `spec` (parsing and validating `deploy.yaml`),
`api` (the API's types, used by agent and CLI alike), `version`. One Go module
at the root. `agent/internal` and `cli/internal` do not import each other.

Read before changing behavior: [docs/architecture.md](docs/architecture.md)
(how it works and why), [docs/api.md](docs/api.md) (the API contract).

## Commands

```bash
make lint               # gofmt + go vet
make test               # Go unit tests; no Docker, a few seconds
make test-race-docker   # the same under the race detector, in a container
make test-integration   # needs a Docker daemon
make test-dashboard     # npm ci, typecheck, vitest, production build
make dev                # the whole stack: dashboard :3000, agent :9000, Caddy :8080/:8443
```

Without `make` (Windows), run the one-liners from the [Makefile](Makefile).
Dashboard alone, without Docker: `cd dashboard && npm run mock` in one terminal,
`npm run dev` in another.

Run `make lint test` before you call a Go change done, `make test-dashboard` for
a dashboard change, and the race detector for anything that touches
`agent/internal/deploy`.

## Rules that are not negotiable

- **No shell.** The agent talks to the Docker Engine API through the SDK. Never
  `exec` the `docker` CLI, never build a command line, never execute or
  interpolate anything that comes from `deploy.yaml` or an API request.
- **Never log secrets.** Tokens, `Authorization` headers, request bodies and
  `env` values do not appear in logs, errors or events. API responses carry
  `spec.App.Redacted()`, never the stored spec.
- **Validate in the agent.** Whatever the CLI or the dashboard checked, the
  agent checks again; names, images, domains and health paths end up in
  container names, URLs and proxy configuration.
- **Proxy configuration is data.** It is built as Go values and marshalled to
  JSON; never assemble Caddy configuration from strings.
- **Containers are unprivileged**: never privileged, `no-new-privileges`, no
  host mounts, no published host ports.
- **No new runtime dependencies** — external database, queue, cache — and no
  new Go or npm dependency without a reason the standard library cannot answer.
- **Deployment records are immutable.** A status changes only along the
  transition table in `agent/internal/deploy/state.go` (`CanTransition`), as a
  compare-and-swap in the store. A rollback is a new record, not an edit.
- **Schema changes are migrations**: append one to the list in
  `agent/internal/store/store.go`; never edit one that has been released.

## How the code is organized, and meant to stay

- **One way to start a deployment**: `Engine.start`. Deploy, redeploy and
  rollback differ only in where the spec comes from.
- **One way replicas come to exist**: `Engine.ensureReplicas`, used by
  rollouts, rollbacks and the supervisor's reconciliation alike.
- **One lock per application**, taken by user operations (which fail fast with
  `ErrBusy` against each other) and by the supervisor (which user operations
  wait for). Look at how existing operations take it before adding one.
- The engine reaches Docker and Caddy only through the `Runtime` and `Proxy`
  interfaces in `agent/internal/deploy`. Tests use the in-memory fakes
  (`agent/internal/docker/dockertest`) and drive the supervisor with a
  synthetic clock: `tick(ctx, now)` takes the time as an argument.
  **Do not add `time.Sleep` to tests**, and do not make them depend on Docker —
  that is what `integration_test.go` (build tag `integration`) is for.
- Clients wait for a deployment by polling `GET /deployments/{id}` until
  `completed_at` is set — not until the status looks final; the application
  refuses new operations until then.
- The API's error envelope is `{"error": {"code", "message", "details"}}` with
  the codes in `pkg/api`. A new failure mode gets a code, and the CLI's
  `Render` gets a sentence that tells the user what to do next.
- An API change is four changes: `pkg/api` and the handler, `docs/api.md`,
  the CLI client, and the dashboard (`app/types/api.ts` **and**
  `mock/agent.mjs`, which must keep answering like the real agent).

## Style

- Go: `gofmt`, standard library first (`net/http` routing, `log/slog`), errors
  wrapped with context and written for the person who will read them. Comments
  explain why; do not narrate what the next line does.
- User-facing text — CLI output, API messages, dashboard copy — is short,
  specific and says what to do next. Claims are derived from current state,
  not assumed ("still running 1.4.2" only if it is).
- Dashboard: Composition API with `<script setup lang="ts">`, no UI library,
  design tokens from `app/assets/css/main.css`. State-changing requests carry
  the `X-Shipwick-Request` header; the token never reaches the browser.
- Shell: POSIX `sh`, `shellcheck -s sh` clean.
- Match the code around you: its naming, its comment density, its idiom.

## Documentation

Behavior users can see is documented in [README.md](README.md); keep it true.
User-visible changes get a line under *Unreleased* in
[CHANGELOG.md](CHANGELOG.md). Measured numbers in the docs were measured; do not
invent new ones. The public documentation at shipwick.com is a separate
repository, `shipwick/website`; say so in your summary when a change makes it
out of date.

## Git

Do not commit, tag, push or open pull requests unless the maintainer explicitly
asks for it in the current task. Leave the working tree changed and say what you
did and how you verified it. Releases are cut by maintainers only:
[docs/releasing.md](docs/releasing.md).
