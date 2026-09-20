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
| `shipwick init` | Create a `deploy.yaml`. Prompts in a terminal; `--image` makes it non-interactive |
| `shipwick validate` | Check `deploy.yaml` offline and show how it will be applied, defaults included |
| `shipwick deploy` | Deploy and wait for the result. `--image` overrides the image (CI), `--no-wait` returns at once |
| `shipwick rollback [app]` | Go back to the previous successful deployment, or `--to N` (the #number from `status`). A full, ordinary deployment of the stored configuration |
| `shipwick redeploy [app]` | Deploy the running configuration again, `--image` to change the image. Needs no `deploy.yaml` |
| `shipwick status [app]` | Version, CPU and memory, replica health and restart counts, recent deployments, and what the supervisor has been doing |
| `shipwick ps` | All applications on the server |
| `shipwick logs [app]` | `-n 100` lines, `-f` to follow, `-t` for timestamps |
| `shipwick stop\|start [app]` | Stop an application / start it again |
| `shipwick delete <app>` | Remove an application, its containers and its history. Asks for the name; `--yes` to skip |
| `shipwick server status` | Is the agent reachable, and what does it run on |
| `shipwick login` | Save the agent URL and token |

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

The config file lives at `<user config dir>/shipwick/config.yaml`
(`SHIPWICK_CONFIG` overrides it) and is written `0600`.

Typical setups:

```bash
# Your laptop → a server: keep the API on loopback and tunnel to it.
ssh -N -L 9000:127.0.0.1:9000 user@server &
shipwick login                      # URL defaults to the tunnel; token is asked without echo

# CI: no login, just two secrets.
export SHIPWICK_AGENT_URL=https://agent.example.com
export SHIPWICK_AGENT_TOKEN=…
shipwick deploy --image ghcr.io/company/my-api:$GIT_SHA
```

The CLI warns whenever a token is about to travel over plain HTTP to
anything other than this machine.

## Behavior worth knowing

- **Exit codes:** `0` success, `1` anything else — including a deployment that
  was accepted but failed. Safe to use as a CI gate.
- **`deploy` waits for `completed_at`**, not for the first `ACTIVE`: when it
  returns, the next `deploy` is guaranteed not to hit "operation in progress".
- **Ctrl+C during `deploy`** stops the waiting, not the deployment.
- **`deploy --image`** edits the YAML document in memory; the agent still
  receives one plain deploy.yaml, and the file on disk is untouched.
- **Piped output is plain:** no colors, no progress line (`NO_COLOR` is honored
  too). Warnings go to stderr, so stdout stays parseable.
- **`logs -f` ends by itself** when the containers are replaced by a new
  deployment; run it again to follow the new ones.

## Layout

```text
cmd/shipwick          entrypoint: signals, exit code
internal/commands      cobra command tree, error rendering
internal/client        agent API client
internal/cliconfig     URL/token resolution, config file
internal/ui            colors, tables, progress line
```

The CLI shares `pkg/spec` (validation) and `pkg/api` (wire types) with the
agent and cannot import anything else from it.
