# shipwick

The Shipwick command-line client.

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli    # Linux, macOS
brew install shipwick/tap/shipwick                      # Homebrew
go build -o bin/shipwick ./cli/cmd/shipwick             # from source; or: make build
```

On Windows, download `shipwick_windows_amd64.exe` from the
[releases](https://github.com/shipwick/shipwick/releases).

## Commands

| Command | |
|---|---|
| `shipwick init` | Recognise the project (Nuxt, Next, Node, .NET, Go, Python, or a folder of static files) and write a `Dockerfile`, a `.dockerignore` and a `deploy.yaml` with `build: .` or `static: <dir>`; existing Dockerfiles are kept. Otherwise prompts in a terminal; `--image` skips detection and makes it non-interactive, `--static <dir>` forces a static site |
| `shipwick validate` | Check `deploy.yaml` offline — placeholders filled in, defaults applied — and show how it will be applied. A `shipwick.yaml` entry by entry, with the order the applications deploy in |
| `shipwick deploy` | Deploy and wait for the result. With `build:` in the file, builds the image here with `docker build` and sends it to the server first; a `static:` folder is uploaded first, then deployed. `--image` overrides the image (CI; not with `build:`), `--no-wait` returns at once, `--env-file` supplies `${NAME}` values. A `shipwick.yaml` deploys several applications at the same time, `--parallel N` at once; `-f` repeated deploys several `deploy.yaml` in order. Without a `deploy.yaml`, in a terminal, it runs `init` first |
| `shipwick rollback [app]` | Go back to the previous successful deployment, or `--to N` (the #number from `status`). A full, ordinary deployment of the stored configuration |
| `shipwick redeploy [app]` | Deploy the running configuration again, `--image` to change the image. Needs no `deploy.yaml` |
| `shipwick status [app]` | Version, CPU and memory, replica health and restart counts, recent deployments, and what the supervisor has been doing |
| `shipwick ps` | All applications on the server |
| `shipwick logs [app]` | `-n 100` lines, `-f` to follow, `-t` for timestamps |
| `shipwick run [app] -- <command>` | Run a command in a one-off container of the application (its image, env and limits, no volumes), print its output, exit with its exit code |
| `shipwick jobs [app]` | The scheduled jobs with their last and next run (UTC). `jobs run <app> <job>` starts one now and waits; `jobs logs <app> <job>` shows the last run's output, `--run N` another's |
| `shipwick stop\|start [app]` | Stop an application / start it again |
| `shipwick delete <app>` | Remove an application, its containers and its history. Asks for the name; `--yes` to skip. Its volumes stay |
| `shipwick volumes` | Every volume on the server: name, application, size, and whether the application still exists. `volumes rm <name>` removes a volume of a deleted application; asks first, `--yes` to skip |
| `shipwick server status` | Is the agent reachable, what does it run on, and which token and role am I using |
| `shipwick server install <user@host>` | Install or upgrade the server over SSH, from here: Docker when it is missing, then the installer with `--agent-domain` and `--dashboard-domain`; saves the token as a context (`--context` names it) and prints the DNS records to create. `--version` picks a release |
| `shipwick doctor` | One screen: CLI and agent versions against the latest release, token, Docker, proxy, ports 80 and 443, and for every domain whether DNS points at the server and `https://` answers. Exits non-zero when something is broken |
| `shipwick open [app]` | Open `https://<domain>` in the browser |
| `shipwick login` | Save the agent URL and token; `--context <name>` saves them under that name and makes it current |
| `shipwick context ls\|use\|rm\|current` | List the saved servers (`*` marks the current one), switch, forget one (`--yes` skips the question), print the current name |
| `shipwick token create <name> --role read\|deploy\|admin` | Create a token; its value is printed once. `read` looks, `deploy` also changes what runs, `admin` does everything |
| `shipwick token ls` | The tokens, their roles and when each was last used |
| `shipwick token revoke <name>` | Revoke a token. Asks for the name; `--yes` to skip |
| `shipwick secret set <NAME>` | Store a value on the server for `${NAME}` in `env`. Asked without echo in a terminal, else read from stdin; `--from-file` reads a file. Never an argument |
| `shipwick secret ls` | The secrets on the server: names and dates, never values |
| `shipwick secret rm <NAME>` | Remove a secret; `--yes` skips the question |
| `shipwick upgrade` | Replace this binary with the latest release, verified against its checksums, and report whether the server is behind. `--check` only reports |

Commands taking `[app]` default to the application named in `./deploy.yaml`
(`-f`/`--file` selects another file; for `logs`, where `-f` means `--follow`,
only the long form `--file`). `delete` is the deliberate exception: it always
wants the name spelled out.

## Connecting to the agent

| | URL | Token |
|---|---|---|
| 1. Flag | `--url` | — never a flag: arguments show up in `ps` and shell history |
| 2. Environment | `SHIPWICK_AGENT_URL` | `SHIPWICK_AGENT_TOKEN` |
| 3. Saved by `shipwick login` | ✓ | ✓ |
| 4. Default | `http://127.0.0.1:9000` | |

The saved token belongs to the saved URL: point `--url` at a different agent
and the saved token is **not** sent there.

Saved servers are *contexts*. `--context <name>`, then `SHIPWICK_CONTEXT`,
then the current context (set by the last `login` or `context use`) decides
which one a command means. The config file lives at
`<user config dir>/shipwick/config.yaml` (`SHIPWICK_CONFIG` overrides it), is
written `0600`, and looks like this — a file from before contexts existed,
with `url` and `token` at the top level, still loads, as the context `default`:

```yaml
current: prod
contexts:
  prod:    { url: https://agent.example.com, token: … }
  staging: { url: http://127.0.0.1:9000, token: … }
```

Typical setups:

```bash
# Your laptop → a server: keep the API on loopback and tunnel to it.
ssh -N -L 9000:127.0.0.1:9000 user@server &
shipwick login                      # URL defaults to the tunnel; token is asked without echo

# A second server, and switching between them.
shipwick login --context staging --url https://staging.example.com
shipwick deploy --context prod
shipwick context use prod

# CI: no login, just two secrets.
export SHIPWICK_AGENT_URL=https://agent.example.com
export SHIPWICK_AGENT_TOKEN=…
shipwick deploy --image ghcr.io/company/my-api:$GIT_SHA
```

Values that must not be in the file are written as `${NAME}` and filled in
from the environment or `--env-file` before the file is sent. An `env` value
whose name is set nowhere here is left to the server, which fills it in from
the secrets stored with `shipwick secret set`; `validate` lists those, and a
name the server does not have either stops the deployment. Anywhere else —
an image tag — an unset name stops it here.

Several applications in one `shipwick.yaml` — an `apps` list of complete
`deploy.yaml` entries; `after: [postgres]` on one makes it wait for another —
deploy at the same time, in dependency order, at most four at once
(`--parallel N`). An application whose dependency did not deploy is skipped
and the command exits non-zero; every line of output carries its application's
name. The file is used when there is no `deploy.yaml`. Several `deploy.yaml`
files deploy in the order given, one after the other, stopping at the first
failure:

```bash
shipwick secret set DATABASE_PASSWORD     # once, on the server
shipwick deploy                                                     # shipwick.yaml
shipwick deploy -f api/deploy.yaml -f worker/deploy.yaml
```

The CLI warns whenever a token is about to travel over plain HTTP to
anything other than this machine.

## Behavior worth knowing

- **Exit codes:** `0` success, `1` anything else — including a deployment that
  was accepted but failed. Safe to use as a CI gate.
- **`deploy` waits for `completed_at`**, not for the first `ACTIVE`: when it
  returns, the next `deploy` is guaranteed not to hit "operation in progress".
- **Ctrl+C during `deploy`** stops the waiting, not the deployment.
- **`deploy` of a `static:` folder** sends the folder as it is, up to 512 MB,
  as a tar archive with fixed metadata: the same files make the same digest
  and the same version everywhere. Run the build first; a folder without an
  `index.html` is refused before anything is sent. A symbolic link that leads
  out of the folder is skipped, with a warning.
- **`deploy --image`** edits the YAML document in memory; the agent still
  receives one plain deploy.yaml, and the file on disk is untouched.
- **`deploy` with `build:`** runs `docker build` on this machine, as an argv,
  in the directory of the deploy.yaml, for the architecture the agent reports;
  the image is tagged `shipwick.local/<app>:<UTC stamp>-<4 hex>`, saved through
  the Docker API and streamed to the agent. The build's output is shown dimmed.
  `docker` must be installed here; the server never builds.
- **Piped output is plain:** no colors, no progress line (`NO_COLOR` is honored
  too). Warnings go to stderr, so stdout stays parseable.
- **`logs -f` ends by itself** when the containers are replaced by a new
  deployment; run it again to follow the new ones.
- **`upgrade` replaces only itself.** The new binary is written next to the
  old one and renamed over it once its SHA-256 matches the release's
  `checksums.txt`; a binary under Homebrew's Cellar or winget's Packages
  directory is recognized by its path and left to the package manager. The
  server is upgraded by the installer, on the server: it needs Docker there,
  which the CLI does not have.
- **`server install` runs your `ssh`** as a program with arguments, never
  through a shell, with `BatchMode=yes` so that it fails instead of asking for
  a password. The remote commands are fixed; the hostnames and the version are
  validated first and go in as environment assignments of the installer. The
  token is read from the installer's summary. When the installer prints none —
  an upgrade — nothing is fetched from the server: the context keeps the token
  it had, or is saved without one and the command says how to log in.
- **`doctor` asks public resolvers** (1.1.1.1, 8.8.8.8, 9.9.9.9) before this
  machine's, the same view of DNS the agent takes; the server's address is
  learned by resolving the agent's own hostname, so through a tunnel
  (`127.0.0.1`) ports and record targets are not checked.

## Layout

```text
cmd/shipwick          entrypoint: signals, exit code
internal/commands      cobra command tree, error rendering
internal/client        agent API client
internal/cliconfig     contexts: URL/token resolution, config file
internal/ui            colors, tables, progress line
```

The CLI shares `pkg/spec` (validation) and `pkg/api` (wire types) with the
agent and cannot import anything else from it.
