# shipwick

The Shipwick command-line client.

```bash
curl -fsSL https://get.shipwick.com | sh -s -- --cli    # Linux, macOS
brew install shipwick/tap/shipwick                      # Homebrew
go build -o bin/shipwick ./cli/cmd/shipwick             # from source; or: make build
```

On Windows, download `shipwick_windows_amd64.exe`, or `shipwick_windows_arm64.exe`
for a machine with an Arm processor, from the
[releases](https://github.com/shipwick/shipwick/releases).

## Commands

| Command | |
|---|---|
| `shipwick init [dir]` | Recognise the project (Nuxt, Next, SvelteKit, Remix, Astro, Node, .NET, Go, Python, or a folder of static files) and write a `Dockerfile`, a `.dockerignore` and a `deploy.yaml` with `build: .` or `static: <dir>`; existing Dockerfiles are kept. A Node project whose Dockerfile it writes gets `init: true`. Otherwise prompts in a terminal; `--image` skips detection and makes it non-interactive, `--static <dir>` forces a static site. Next to a `shipwick.yaml` it appends an entry to that file instead, for the project here or in `dir` |
| `shipwick validate` | Check `deploy.yaml` offline — placeholders filled in, defaults applied — and show how it will be applied. A `shipwick.yaml` entry by entry, with the order the applications deploy in |
| `shipwick deploy` | Deploy and wait for the result. With `build:` in the file, builds the image here with `docker build` and sends it to the server first; a `static:` folder is uploaded first, then deployed. `--image` overrides the image (CI; not with `build:`), `--no-wait` returns at once, `--verbose` shows everything `docker build` prints, `--env-file` supplies `${NAME}` values. A `shipwick.yaml` deploys several applications at the same time, `--parallel N` at once; `-f` repeated deploys several `deploy.yaml` in order. Without a `deploy.yaml`, in a terminal, it runs `init` first |
| `shipwick rollback [app]` | Go back to the previous successful deployment, or `--to N` (the #number from `status`). A full, ordinary deployment of the stored configuration |
| `shipwick redeploy [app]` | Deploy the running configuration again, `--image` to change the image. Needs no `deploy.yaml` |
| `shipwick config <app> [-o file]` | Print the `deploy.yaml` of what the application runs, or write it to a file (`--force` to overwrite): the file back when it was lost. A value that referred to a secret on the server is that `${NAME}` again; any other env value or basic-auth password is `"********"` and is listed on standard error — write it again or store it as a secret, `deploy` refuses the file until then. Needs a token that may deploy the application |
| `shipwick status [app]` | Version, CPU and memory, replica health and restart counts, recent deployments, and what the supervisor has been doing. A replaced container that is still on its way out is listed as `stopping`, below the replicas. A hostname whose certificate is being obtained, waiting for DNS or about to expire gets a line; `--verbose` lists every hostname's certificate |
| `shipwick ps` | All applications on the server. One with a certificate that is not in order, or with active alerts, says so at the end of its line: `certificate waiting for DNS, 2 alerts` |
| `shipwick logs [app]` | `-n 100` lines, `-f` to follow, `-t` for timestamps |
| `shipwick logs [app] --previous` | The output of the last container that ended — crashed, restarted, replaced — kept by the agent; `-p` for short. `--list` shows what is kept, `--id <n>` one entry, `--run <id>` the output of a job's or command's run |
| `shipwick logs [app] --search TEXT` | Lines that contain the text, whatever its case, in the kept output and the running containers; `--since 2h`, `--until`, `--deployment <#>` and `--replica <n>` narrow it, and work without `--search` |
| `shipwick traffic [app]` | What the proxy saw. Without an application: requests per minute, 5xx, p95 and bytes of every application over the last hour. With one: its totals for `--since 1h\|24h\|7d`, and the slowest and failing paths among its recent requests. `--requests` lists the most recent requests (`-n 50`, at most 200), `-f` follows them |
| `shipwick run [app] -- <command>` | Run a command in a one-off container of the application (its image, env and limits, no volumes), print its output, exit with its exit code |
| `shipwick jobs [app]` | The scheduled jobs with their last and next run (UTC). `jobs run <app> <job>` starts one now and waits; `jobs logs <app> <job>` shows the last run's output, `--run N` another's |
| `shipwick stop\|start [app]` | Stop an application / start it again |
| `shipwick delete <app>` | Remove an application, its containers and its history. Asks for the name; `--yes` to skip. Its volumes stay |
| `shipwick volumes` | Every volume on the server: name, application, size, and whether the application still exists. `volumes rm <name>` removes a volume of a deleted application; asks first, `--yes` to skip |
| `shipwick backups [app]` | The backups the server takes and keeps of an application's volumes: when, how large, where, and whether one has been verified. `backups run <app>` takes one now; `backups verify <app> [id]` proves that one restores, the latest by default; `backups restore <app> <id>` puts one back into a stopped application (asks for the name, `--yes` to skip); `backups download <app> <id> [-o dir]` fetches its archives; `backups rm <app> <id>` removes it; `backups decrypt <file>` decrypts a file from the server or the bucket on this machine, with `SHIPWICK_BACKUP_PASSPHRASE` or a prompt; `backups adopt [app]` records the backups that the server's directory and bucket hold and its database has forgotten, as after the agent's state was restored (needs an `admin` token) |
| `shipwick backup [app]` / `shipwick restore [app] <archive.tar>` | The other kind: download the volumes as they are now to this machine (`--volume`, `-o dir`), and put such an archive back into a stopped application |
| `shipwick server backup` | Back up the agent's own database and encryption key now, as it does daily. Needs `SHIPWICK_BACKUP_PASSPHRASE` on the agent |
| `shipwick export [-o file] [--app name]…` | Write everything the server runs — configurations, secrets, registry credentials, certificates, images built by `deploy`, static folders, an archive of every volume — into one file, encrypted with a passphrase (`SHIPWICK_EXPORT_PASSPHRASE`, or asked for twice). The file is read back before it is kept. `--to-backups` writes it on the server instead, next to its backups; `--list` shows those. Needs an `admin` token |
| `shipwick import <file>` | Take an export in on the server of the current context: store what it holds, restore each application's volumes before it first starts, and deploy the applications in the export's order, waiting for each. Leaves what exists alone unless `--overwrite` (asks; `--yes`); `--stopped` deploys without starting; `--status` shows the import that is running or ran last. Exits non-zero when an application failed |
| `shipwick standby` | What a standby server holds: the applications that were imported stopped, and how its scheduled import from the bucket is doing. `standby pull` imports the newest export now; `standby promote` starts the applications in order, follows the promotion across a lost connection or a restart of the agent, and prints the DNS records to change (asks; `--yes`) |
| `shipwick server status` | Is the agent reachable, what does it run on, how full is its disk, which token and role am I using, which alerts are active, and whether a newer release exists (the agent asks GitHub once a day) |
| `shipwick server install <user@host>` | Install or upgrade the server over SSH, from here: Docker when it is missing, then the installer with `--agent-domain` and `--dashboard-domain`; saves the token as a context (`--context` names it) and prints the DNS records to create. `--version` picks a release |
| `shipwick server bundle` | Make one file that installs or upgrades a server with no connection: the release's compose file, installer and checksums, the `shipwick` binary and the three images for `--arch amd64` or `arm64`. `--version` picks a release, `-o` the file. The archive of images is checked against the digests the release published (0.7.0 and later) before the bundle is written; `--no-pull` takes the images this machine has, unchecked. Needs Docker here; on the server, unpack it and run its `install.sh` |
| `shipwick server rotate-key` | Replace the key the server encrypts env values, secrets and registry passwords with; the running agent re-encrypts everything and nothing restarts. With the key in `SHIPWICK_ENCRYPTION_KEY`, prints the new key once for you to put there |
| `shipwick doctor` | One screen: CLI and agent versions against the latest release, token, Docker, proxy, ports 80 and 443, the server's active alerts, and for every domain whether DNS points at the server — or at Cloudflare's proxy, which is in order when the agent has a Cloudflare token — and `https://` answers. Exits non-zero when something is broken |
| `shipwick open [app]` | Open `https://<domain>` in the browser; `--dashboard`, or no application and no `deploy.yaml` here, opens the server's dashboard |
| `shipwick login` | Save the agent URL and token; `--context <name>` saves them under that name and makes it current; `--no-check` saves without asking the agent |
| `shipwick context ls\|use\|rm\|current` | List the saved servers (`*` marks the current one), switch, forget one (`--yes` skips the question), print the current name |
| `shipwick token create <name> --role read\|deploy\|admin` | Create a token; its value is printed once. `read` looks, `deploy` also changes what runs, `admin` does everything. `--app <name>` (repeatable) limits a `deploy` token to those applications: it changes them and reads the rest. `--expires 90d`, `12h` or a date gives it an end |
| `shipwick token ls` | The tokens, their roles, the applications each is limited to, when each expires — marked within 14 days of it — and when it was last used |
| `shipwick token revoke <name>` | Revoke a token. Asks for the name; `--yes` to skip |
| `shipwick token update <name>` | Change a token without changing its value: `--app <name>` (repeatable) replaces the applications it is limited to, `--all-apps` lifts the limit, `--expires 90d` moves its end — an expired token works again — and `--no-expiry` takes the end away. The role is not changed. Recorded in the audit trail with what it was before |
| `shipwick audit [--app name] [--actor name] [--action a]… [--outcome o] [--since 7d] [-n 50]` | Who changed what, newest first: the token or the person, the time, the address, the action, what it was about and how it was answered. `--action` takes an action or a family (`token.`), repeatable; `--outcome` takes `ok`, `refused`, `failed`; `--actor-kind token|person`. `--before <id>` continues an earlier page. `--format csv|json` writes everything that matches instead of a page, to standard output or to `--output <file>`; cells a spreadsheet would run as formulas are written as text. Needs an `admin` token |
| `shipwick access grant <who> --role read\|deploy\|admin` | Say what a person may do who signs in to the dashboard with the company's accounts (an OpenID Connect provider configured on the agent): `<who>` is an address (`ada@example.com`), a group as the provider names it (`group:backend`), a domain (`*@example.com`), or `name:<name>` for a person whose name is not an address (the agent's `SHIPWICK_OIDC_NAME_CLAIM`). `--app <name>` (repeatable) limits `deploy` to those applications. Granting again replaces the rule. The most specific rule decides: name, then address, then groups, then domain |
| `shipwick access ls` | The rules: who, which role, which applications, granted when and by whom |
| `shipwick access revoke <who>` | Remove a rule; sessions that rested on it end with their next request |
| `shipwick access sessions` | Who is signed in right now, as what, and until when |
| `shipwick access signout <address | name>` | End every session of a person, named as `access sessions` lists them. They can sign in again while a rule covers them |
| `shipwick secret set <NAME>` | Store a value on the server for `${NAME}` in `env` and in `proxy.basic_auth` passwords. Asked without echo in a terminal, else read from stdin; `--from-file` reads a file. Never an argument |
| `shipwick secret ls` | The secrets on the server: names and dates, never values |
| `shipwick secret rm <NAME>` | Remove a secret; `--yes` skips the question |
| `shipwick registry login <registry> --username <name>` | Store the credential the server pulls private images with. The password or token is asked without echo in a terminal, else read from stdin (`--password-stdin` forces that); never an argument. The agent checks it against the registry first |
| `shipwick registry ls` | The registries the server has a credential for: registry, username, last change; never passwords |
| `shipwick registry logout <registry>` | Remove a registry's credential |
| `shipwick cert set <hostname> --cert <file> --key <file>` | Serve the hostnames a certificate covers with that certificate instead of one the server obtains: the PEM chain (the hostname's certificate first) and its key. A wildcard is stored under `'*.example.com'`. The server checks them and says why it refuses |
| `shipwick cert ls` | The certificates you supplied: hostname, issuer, expiry, and the names each covers. Never a key |
| `shipwick cert rm <hostname>` | Remove a certificate; the server obtains its own for those hostnames again. `--yes` skips the question |
| `shipwick upgrade` | Replace this binary with the latest release, verified against its checksums, and report whether the server is behind. `--check` only reports |

Commands taking `[app]` default to the application named in `./deploy.yaml`
(`-f`/`--file` selects another file; for `logs`, where `-f` means `--follow`,
only the long form `--file`). Without a `deploy.yaml`, a `shipwick.yaml` that
describes one application names it; one that describes several makes the
command list them and show itself with a name (`shipwick status api`).
`delete` is the deliberate exception: it always wants the name spelled out. So is
`traffic`, the other way round: without a name it lists every application.

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

Behind a proxy, `shipwick` follows `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`
for the agent and for GitHub. `SHIPWICK_CA_FILE` names a PEM file of
certificate authorities trusted in addition to the system's, for an agent
whose certificate a company's own authority issued; a file that cannot be
used stops the command with what is wrong with it.

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

On the server itself there is nothing to set up when the API has a hostname:
the installer saves it, with the token, as a context of the user who ran it
(root), and `shipwick ps` works there. Without a hostname the agent publishes
no port, and a command run on the server says so instead of suggesting a
tunnel; the handbook's *Reach the API without a hostname* has the ways in.

Values that must not be in the file are written as `${NAME}` and filled in
from the environment or `--env-file` before the file is sent. An `env` value
or a `proxy.basic_auth` password whose name is set nowhere here is left to the server, which fills it in from
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
  the Docker API and streamed to the agent, without the layers the agent says
  the server already has (the whole image when it cannot say, or refuses what
  it got). In a terminal the build is one progress line (`Building the image
  (12s) — <the last line docker printed>`) and its whole output is shown only
  when it fails; `--verbose`, and any run whose output is not a terminal,
  shows every line, dimmed.
  `docker` must be installed here; the server never builds.
- **`deploy` asks before it builds or uploads.** With `build:` or `static:`,
  the agent validates the file first (`POST /applications/{name}/validate`),
  so a domain another application serves, a published port that is taken or a
  secret that is not stored stops the command before the build and the upload,
  with the message a refused deployment has. An agent older than that
  operation is asked only whether the domain is free.
- **`init` next to a `shipwick.yaml`** appends the entry as text at the end
  of the `apps` list, indented like the entry before it, and changes nothing
  else. When `apps` is not the last key of the file, or the list is not
  written one entry per dash, the file is left alone and the entry is printed
  to add by hand; a name the file already has is refused.
- **`init` without a lock file** in a Node project writes a Dockerfile that
  runs `npm install` and says that the build is not reproducible until a lock
  file is committed. A SvelteKit project needs `adapter-node` (a server) or
  `adapter-static` (files); with neither, init says which to install.
- **Piped output is plain:** no colors, no progress line (`NO_COLOR` is honored
  too). Warnings go to stderr, so stdout stays parseable.
- **`logs -f` ends by itself** when the containers are replaced by a new
  deployment; run it again to follow the new ones. In a terminal, when nothing
  has arrived after two seconds, it says once on stderr that it is following
  and that Ctrl-C stops it.
- **`upgrade` replaces only itself.** The new binary is written next to the
  old one and renamed over it once its SHA-256 matches the release's
  `checksums.txt`; a binary under Homebrew's Cellar or winget's Packages
  directory is recognized by its path and left to the package manager. The
  server is upgraded by the installer, on the server: it needs Docker there,
  which the CLI does not have.
- **`server install` runs your `ssh`** as a program with arguments, never
  through a shell, with `BatchMode=yes` so that it fails instead of asking for
  a password, and `StrictHostKeyChecking=accept-new` so that a server's key is
  taken on first contact (a fresh server has one nobody has seen) and a key
  that changed is refused. The remote commands are fixed; the hostnames and the version are
  validated first and go in as environment assignments of the installer. The
  token is read from the installer's summary. When the installer prints none —
  an upgrade — nothing is fetched from the server: the context keeps the token
  it had, or is saved without one and the command says how to log in.
- **`doctor` asks public resolvers** (1.1.1.1, 8.8.8.8, 9.9.9.9) before this
  machine's, the same view of DNS the agent takes; the server's address is
  learned by resolving the agent's own hostname, so through a tunnel
  (`127.0.0.1`) ports and record targets are not checked.
  A record that points at Cloudflare's proxy is recognised from Cloudflare's
  published ranges, the list the agent uses, and reported in the agent's
  words; whether that is a problem is the agent's to say (`proxy.dns_challenge`
  in `GET /server`). An agent that is itself behind the proxy hides the
  server's address, and ports and record targets are not checked then either.
- **`cert set` reads two files and sends them as they are.** Whether they
  belong together and cover the hostname is checked by the agent, which
  answers with a sentence; the key is never printed. Quote a wildcard, or the
  shell expands it: `shipwick cert set '*.example.com' …`.

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
