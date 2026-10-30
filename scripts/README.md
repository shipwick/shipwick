# Scripts

## install.sh

The installer behind `curl -fsSL https://get.shipwick.com | sh`.

```bash
sh install.sh          # server: agent + socket proxy + Caddy + dashboard (as root), and the CLI
sh install.sh --cli    # the CLI only
```

What it does on a server:

1. Checks for Linux, root, a running Docker and the Compose plugin. It does
   **not** install Docker: that is the server owner's decision.
   Then, before it writes anything, that the HTTP and HTTPS ports are free —
   unless its own Caddy holds them, which is an upgrade.
2. Writes `/opt/shipwick/compose.yml`: the release's `compose.production.yml`,
   in which the three Shipwick images — agent, dashboard and the proxy,
   Shipwick's build of Caddy — are pinned to the release's version, and the
   socket proxy's image, which the agent reaches Docker through, to a version
   of its own. Verified against
   the release's `checksums.txt` before it replaces anything — and
   `checksums.txt` against the release's signature first, when cosign is
   installed (`verify_signature`). Compose is asked to read the new file
   before it replaces the one in use (`check_compose_file`): one older than
   2.23.1 cannot, and the installer stops there with the version it needs.
3. On first run only, writes `/opt/shipwick/.env` (mode `0600`, directory
   `0700`) with a freshly generated API token and the two optional hostnames —
   asked for in a terminal, or taken from `SHIPWICK_AGENT_DOMAIN` /
   `SHIPWICK_DASHBOARD_DOMAIN`. Hostnames are validated before `.env` is written.
   Settings given for the run are kept in it too — every variable `--help`
   lists under *Environment* except the installer's own (`SHIPWICK_VERSION`,
   `SHIPWICK_REQUIRE_SIGNATURE`, `SHIPWICK_BUNDLE`, the directories): the
   ports, images of your own, the webhook, `SHIPWICK_CLOUDFLARE_API_TOKEN`
   (certificates through Cloudflare DNS, for hostnames behind Cloudflare's
   proxy), alerts, backups, exports, the standby, the proxy, sign-in, the log
   archive and the update check. `cli/internal/commands/serverconfigure.go`
   keeps a copy of the list, and a test fails when the two drift apart.
4. Pulls the images, starts the stack, waits for the agent to report healthy.
5. Installs `shipwick`, verified the same way. **A checksum mismatch installs
   nothing** and leaves a running installation as it was.
6. With a hostname for the API, signs that `shipwick` in: it saves
   `https://<SHIPWICK_AGENT_DOMAIN>` and the token from `.env` as a context of
   the user running the installer. The CLI writes its own file
   (`shipwick login --token-stdin --no-check`, the token on standard input);
   the installer never writes its YAML. Without a hostname nothing is saved:
   the agent publishes no port for the CLI to reach.
7. Prints the token (once, and only if it was generated in this run) and the
   next steps. With `SHIPWICK_CLOUDFLARE_API_TOKEN` in `.env` and the CLI
   signed in, also what the agent found out about the token — the lines of
   `shipwick doctor` about it, run on the server (`cloudflare_findings`); an
   older CLI or agent has none, and nothing is printed.

Properties worth keeping if you change it:

- **Idempotent.** Running it again is how you upgrade. An existing `.env` —
  the token — is never rewritten.
- **The CLI's contexts belong to the user.** The installer writes only a
  context whose URL is this server's — the token from `.env`, again — or the
  first one, `default`, when none is saved. Other contexts and the choice of
  the current one are left as they are, and when only other servers are saved
  it adds nothing. It finds the context by reading `shipwick context ls`; a
  test in `cli/internal/commands` holds that output's shape.
- **`compose.yml` belongs to the installer, `compose.override.yml` to the
  user.** The first is replaced on every run; the second is never touched, and
  Compose merges the two because the installer runs `docker compose` without
  `-f`. Do not add `-f` back. The one thing the installer says about the
  override (`warn_stale_proxy_override`): when it defines the `socket-proxy`
  service with an allow-list of its own — the overlay of 0.8 to 1.0, saved
  there by hand — that list replaces the release's, and the user is told to
  take it out. `test-upgrade.sh --old-proxy-overlay` walks that server.
- **A release that cannot run here is undone.** The compose file in use is
  copied before it is replaced. An older agent refuses a database a newer
  release has added to; `start_previous_release` reads that in its output
  while the installer waits for it, puts the copy back, starts it and stops
  with the reason. `test-upgrade.sh --back` walks into it as soon as the
  checkout has a migration the last release lacks.
- **Safe to pipe.** Everything is in functions and `main "$@"` is the last
  line: a download cut off half-way executes nothing.
- **POSIX `sh`**, checked with `shellcheck -s sh` in CI. No bashisms.
- Downloads are HTTPS-only (`--proto '=https'`), written to a temporary name
  and moved into place, so an interrupted download never leaves a half file.
- **Everything comes from one release** — `SHIPWICK_VERSION`, by default the
  latest — never from a branch, so the compose file, the images and the CLI
  always belong together. `latest` is resolved to a tag before the first
  download (`resolve_latest`: the `Location` of `/releases/latest`, as
  `install.ps1` does), so every file is fetched by that tag and the signature
  is held to it; fetched through `latest` one by one, two files could come
  from two releases, and the files of an older, signed release put in the
  latest one's place would pass as signed.
- **What the pull brought is held against the release.**
  `verify_pulled_images` fetches the release's `image-digests.txt` after
  `docker compose pull` and compares each image's `RepoDigests` with its
  `index` line — the digest of the manifest list the tag pointed at. An image
  that does not match is untagged, the compose file that ran is put back and
  the installer stops; nothing is started. Images `.env` names in place of
  the release's have no line and are said to be not compared. Without the
  file — a release before 0.7.0 — it warns and goes on, or stops under
  `SHIPWICK_REQUIRE_SIGNATURE=1`.
- **A bundle is the same release, brought along.** With `--bundle <directory
  or .tar.gz>` (or run from inside an unpacked bundle, made by `shipwick
  server bundle`) the installer makes no network call: `fetch_release_asset`
  copies from the bundle and verifies against the bundle's `checksums.txt`,
  `images.tar` is checked against `images.tar.sha256` before anything is
  changed and handed to `docker load` before the compose file is replaced,
  and nothing is pulled. Every other step is shared; keep it that way.
- **What Docker loaded is held against the release.** With
  `image-digests.txt` in the bundle, `verify_bundle_images` compares the ID
  of each loaded image with the digests the release published for the
  server's platform: the configuration's with the classic image store, the
  manifest's with containerd's. A bundle made with the classic store and
  loaded into containerd's has a manifest `docker save` wrote; it is read
  from the archive by its digest and must name the published configuration.
  An image that fails is untagged and the installer stops before the compose
  file is touched. Without the file — `--no-pull`, a release before 0.7.0 —
  it warns and goes on. To try a change, make bundles with both stores
  (`DOCKER_HOST` at a `docker:dind` started with and without
  `--feature containerd-snapshotter=false`; the integration test in
  `cli/internal/commands` makes one from `./dist`) and install each on both.
- **The signature is checked with cosign or not at all.** `verify_signature`
  runs once, when `checksums.txt` has been downloaded: `cosign verify-blob`
  with the release's `checksums.txt.sigstore.json` and the identity of the
  release workflow — at the tag `SHIPWICK_VERSION` names or `latest` was
  resolved to; a bundle's release is not named, so any plain version tag
  there. A signature that does not verify stops the
  installer. No cosign, or a release without a signature (before 0.8.0), is
  one line of output; with `SHIPWICK_REQUIRE_SIGNATURE=1` either stops it
  too. The installer never installs cosign. With a bundle it verifies only
  when that variable is set: cosign fetches Sigstore's keys, which a server
  with no way out cannot. Do not make the check quieter: "was not checked"
  must stay in the output of an installation that downloaded and did not
  check. A bundle installation says nothing about it; `shipwick server
  bundle` said it where the bundle was made.
- **Behind a proxy** it reads `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` in
  either case, exports both spellings (`wget` reads the lower one only), and
  writes them to a new `.env`. When a pull fails while the installer has a
  proxy and `docker info` reports none, it says that the daemon needs its
  own.

Testing it without a server — it only needs a Docker socket. With
`SHIPWICK_COMPOSE_FILE` set (or run from a checkout) it uses that compose file
instead of downloading one, and with it images you built yourself:

```bash
docker build -t ghcr.io/shipwick/agent . && docker build -t ghcr.io/shipwick/dashboard dashboard/
docker build -t ghcr.io/shipwick/caddy -f Dockerfile.caddy .
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$PWD:/src:ro" \
  -e SHIPWICK_COMPOSE_FILE=/src/configs/compose.production.yml \
  -e SHIPWICK_HTTP_PORT=8080 -e SHIPWICK_HTTPS_PORT=8443 \
  -e SHIPWICK_AGENT_DOMAIN=agent.localhost \
  docker:cli sh /src/scripts/install.sh
```

Clean up with `docker compose -p shipwick down -v`.

The CLI it installs is the latest release's. To test a CLI you built — what
the installer saves for it, what it prints on the server — put the binary in
the container's `/usr/local/bin` and set `SHIPWICK_REPO` to a repository
without releases: the download fails with a warning and yours stays. For the
saved context to reach the agent, the container must own ports 80 and 443 and
trust Caddy's local authority: run the installer inside a `docker:dind`
container started with `--add-host agent.localhost:127.0.0.1`, copy
`/data/caddy/pki/authorities/local/root.crt` out of the Caddy container and
point `SSL_CERT_FILE` at it.

## install.ps1

The Windows counterpart of `install.sh --cli`, behind
`irm https://get.shipwick.com/windows | iex`:

```powershell
irm https://get.shipwick.com/windows | iex                   # the latest release
$env:SHIPWICK_VERSION = 'v0.8.1'; irm https://get.shipwick.com/windows | iex
.\install.ps1 -Version v0.8.1 -Destination C:\tools\shipwick  # from a file
& ([scriptblock]::Create((irm https://get.shipwick.com/windows))) -Destination C:\tools\shipwick
```

What it does:

1. Names the build for the machine: `amd64` or `arm64`. PowerShell 7 asks
   .NET (`RuntimeInformation.OSArchitecture`), which names the machine even
   from an emulated x64 process on Windows on Arm; Windows PowerShell reads
   the system's `PROCESSOR_ARCHITECTURE` from the registry, which is the
   machine's whatever the process is, and falls back to the process's own.
2. Resolves `latest` to a tag: GitHub answers `/releases/latest` with a
   redirect to the release's page, and the tag is its last segment — one
   request, never a pre-release, and no API with an hourly limit. A version
   given by hand must look like one before it goes into a URL.
3. Downloads `checksums.txt` and `shipwick_windows_<arch>.exe` from that
   release, each through a temporary name, and verifies the binary's SHA-256
   against the line in `checksums.txt`. **A mismatch removes the download
   and installs nothing.**
4. Where `cosign` is on the `PATH`, verifies `checksums.txt` against the
   release's `checksums.txt.sigstore.json` first — `cosign verify-blob` with
   the identity of the release workflow at exactly that tag, as `shipwick
   upgrade` does — and stops if it is not the workflow's. Without cosign it
   says in one line that it did not check; a release without a signature
   (before 0.8.0) is a warning. `SHIPWICK_REQUIRE_SIGNATURE=1` turns both
   into a refusal, as for `install.sh`.
5. Puts the binary at `%LOCALAPPDATA%\Programs\shipwick\shipwick.exe` (or
   `-Destination`). A `shipwick.exe` that is there and running cannot be
   overwritten but can be renamed: it moves aside as `shipwick.old.exe`,
   which shipwick removes on its next run, the way `shipwick upgrade` does.
6. Adds the directory to the user's `PATH` once: in `HKCU\Environment`,
   appended to the value as it is (unexpanded, so `%USERPROFILE%\bin` stays
   as written), in the current process, and announced with
   `WM_SETTINGCHANGE` so that terminals opened from now on carry it. The
   output says that terminals already open do not. A `shipwick.exe` that
   `PATH` finds ahead of the new one is named, because it is the one that
   would go on running.

Properties worth keeping if you change it:

- **Safe to pipe.** Everything is in functions and the last line is the one
  call, as in `install.sh`: a download cut off half-way executes nothing.
  Nothing is assigned at the top level, because `iex` runs the script in the
  caller's scope: `$ErrorActionPreference` and the like are set inside the
  entry function and go no further.
- **A failure is a terminating error, never `exit`.** Piped into `iex`,
  `exit` would close the window. The entry function prints the `✗` line and
  throws; a prompt gets its prompt back, a script that ran the installer
  stops with a non-zero exit code, and so does `powershell -File`.
- **ASCII only.** Windows PowerShell reads a file without a byte-order mark
  in the system code page; the check marks are written as `[char]0x2713`.
- **Both editions.** It runs under Windows PowerShell 5.1 and PowerShell 7,
  and the differences are in one place each: the architecture (above), the
  redirect (one .NET returns a response that was not followed, the other
  throws an exception that carries it), and `-UseBasicParsing`, which 5.1
  needs and 7 ignores. `Invoke-WebRequest`'s progress bar is turned off for
  the run: under 5.1 the 14 MB binary of 0.9.1 took ten seconds to download
  with it (9.4 and 9.8 in two runs) and under one without (0.6 and 0.5), on
  one machine.
- **Checked with PSScriptAnalyzer** (`Invoke-ScriptAnalyzer -Path
  scripts/install.ps1`, in CI with every rule at every severity); the one
  suppression is `PSAvoidUsingWriteHost`, because an installer talks to the
  person running it.

Testing it: run it with `-Destination` into a scratch directory, under both
`powershell` and `pwsh`, and run the installed `shipwick.exe --version`. The
`PATH` step writes to the registry; read `HKCU\Environment\Path` first and
put it back afterwards. The signature paths can be walked with a `cosign.cmd`
on the `PATH` that records its arguments and exits as told.

## test-install.sh

The test scripts below run from Git Bash on Windows as well, with
`MSYS_NO_PATHCONV=1` so that the paths handed to Docker are not rewritten.
Under it, every variable that carries a host path — `TMPDIR`,
`SHIPWICK_PACKAGES`, `SHIPWICK_CLI` — must be in Windows form (`C:/Users/…`): a
`/c/Users/…` path reaches Docker as `C:\c\Users\…` and is not found.

The installer, run as a new server would run it and then again as an
upgrade, in a container of each distribution named, against a Docker daemon
started for it:

```bash
sh scripts/test-install.sh debian:13 rockylinux/rockylinux:9 alpine:latest
SHIPWICK_TEST_DOCKER=20.10 sh scripts/test-install.sh debian:12
sh scripts/test-install.sh --version v0.8.0 --cosign debian:13
```

The server is a container of the distribution on the network of a
`docker:dind` container, with the `docker` and `compose` programs copied
from that image and `curl` installed where the image has no downloader
(Alpine keeps its BusyBox `wget`). The installer downloads the release from
GitHub and pulls its images, as on a real server; the script then checks the
modes of `/opt/shipwick` and `.env`, the token, that every service of the
compose file runs,
that the installed `shipwick` is the release's and is signed in, and that a
second run keeps `.env` and the token.

It proves that the installer of this checkout installs a published release
with that distribution's `sh`, `awk`, `sed`, coreutils and downloader, on the
architecture of the machine it runs on. It does not prove anything about the
distribution's kernel, its packaging of Docker, systemd, firewalls or
SELinux — a container has its host's kernel and Docker's own daemon — nor
about the images of the checkout: it installs the release's. `--cosign` puts
cosign into the server and sets `SHIPWICK_REQUIRE_SIGNATURE`, for a release
from 0.8.0 on. CI runs one distribution on a pull request and all of them,
on amd64 and arm64, on `main` and before a release. Needs privileged
containers; leaves nothing behind.
## test-upgrade.sh

Walks an upgrade from a release to the checkout, on a server that exists for
the length of the run: a `docker:dind` container, in which the release is
installed by its own installer and put to use with its own CLI.

```bash
sh scripts/test-upgrade.sh v0.6.0          # from v0.6.0 to the checkout
sh scripts/test-upgrade.sh --back v0.7.0   # and back to v0.7.0 with the installer
sh scripts/test-upgrade.sh --old-proxy-overlay v1.0.0   # a server that had the release's compose.socket-proxy.yml as compose.override.yml
```

It needs Docker and Go here, and a way to github.com, ghcr.io and quay.io;
a run takes three to four minutes. What it puts on the release, as far as
that release has it: an application of two replicas behind a hostname, with a
health check and an env value from a stored secret, deployed twice and rolled
back; etcd on a volume with `deploy.strategy: recreate`, a key written through
its hostname, a scheduled job and a backup; a token with the deploy role. The
checkout's three images are built, pinned in a compose file the way
`build-release.sh` pins a release's, loaded into the server's Docker, and
installed with the checkout's `install.sh`.

Afterwards it checks that the agent runs the new version, that the replicas
are the containers the release started, that both hostnames answer, the key is
in the volume, the history is whole, the secret resolves on a redeploy, the
release's CLI redeploys and the checkout's deploys and rolls back, the token works,
the job runs, the backup verifies, the proxy runs this version's
configuration, the agent gave up the Docker socket to the socket proxy and
says so, and that `shipwick doctor` finds no problem. Requests go to the
application about ten times a second while the installer runs; how many were
not answered is printed, not judged.

With `--back` the installer then installs the release again. Either the
release's agent can read the database, and the same things are checked on it;
or the checkout has added a migration, the installer starts the checkout
again and says why, and that is what is checked.

`SHIPWICK_TEST_KEEP=1` keeps the server for looking around;
`SHIPWICK_TEST_SERVER` names the container, and with it the tag of the images
built here, for two runs at once. CI runs it for the last two releases on
`main` and before a release. What every release's walk found is in the
handbook, §4, *Upgrading*.
## test-socket-proxy.sh, test-docker-authz.sh, test-rootless.sh

The arrangements of the handbook's *The Docker socket* — the socket proxy
every installation runs, the authorization plugin at the daemon, rootless
Docker — each started in a Docker daemon of its own and put through
everything an installation does:

```bash
sh scripts/test-socket-proxy.sh              # compose.production.yml as it is: the agent behind the socket proxy, then compose.direct-socket.yml
sh scripts/test-docker-authz.sh              # a daemon that asks opa-docker-authz, with configs/docker-authz.rego
sh scripts/test-rootless.sh                  # on docker:dind-rootless, with configs/compose.rootless.yml
```

Each builds the three images and the CLI from the checkout (Docker, and Go
for the CLI), starts `docker:dind` or `docker:dind-rootless` as one
privileged container, loads the images into it, starts the production
compose file — with its overlay, where the arrangement has one — there, and
runs `test-cycle.sh` inside that container: deploy, rollout, rollback, stop and start, a hook, a job, a
one-off command, a backup with its verification, a restore, a static
folder, a built image, an export and an import, deletion, with the fixtures
of `scripts/testdata`. Nothing else of the Docker they are started from is
touched, apart from three images tagged `shipwick-test-*` that stay; the
container is removed at the end unless `SHIPWICK_TEST_KEEP=1`.
`test-lib.sh` holds what they share and lists the variables. They take a
few minutes each, most of it pulling inside the new daemon, and are not part
of CI.

- `test-socket-proxy.sh` holds the agent to having no Docker socket and
  saying so, and fails when the proxy refused the agent anything: **a new
  Engine API call in `agent/internal/docker` needs its line in the
  allow-list of the `socket-proxy` service of `configs/compose.production.yml`
  (and its copy in `docker-compose.yml`) and in the handbook's table**, and
  this is the test that notices. It then tries what the proxy must refuse,
  and what it is documented not to, and ends by starting the stack with
  `compose.direct-socket.yml`: the proxy's container goes, the agent has the
  socket, `shipwick server status` and `shipwick doctor` say so, and a
  deployment works.
- `test-docker-authz.sh` installs the plugin in the daemon, points the daemon
  at it through `daemon.json` and `SIGHUP`, and runs the cycle, the agent
  behind the socket proxy as on every server: the policy refuses nothing
  the agent asks for, so an operation that fails is a request the agent did
  not make before. It then tries each refusal the handbook lists, and a
  request whose body the plugin cannot read. `SHIPWICK_AUTHZ_PLUGIN` names
  another build of the plugin.
- `test-rootless.sh` first starts the stack with ports below 1024 closed to
  ordinary users, which must fail with Docker's message, and after that
  recreates it: the refused start leaves the proxy's container on one of its
  two networks. It holds the socket proxy to mounting the rootless daemon's
  socket and the agent to mounting none. Its daemon has no cgroups, so it
  also holds the agent to saying that limits are not enforced.

## test-package.sh

The `.deb` and the `.rpm` of the agent, installed where they are meant for
and put to use:

```bash
sh scripts/test-package.sh                           # builds the packages first (build-packages.sh)
SHIPWICK_PACKAGES=dist sh scripts/test-package.sh    # packages built already
```

It starts `docker:dind` as the server and, inside it, a container of
`debian:stable-slim` and then one of `rockylinux/rockylinux:9` as the host
the package is installed on: in the daemon's network namespace, with the
daemon's socket and `/run/shipwick`, which is what the agent sees of a
server with Docker. In each it installs the package (`dpkg -i`, `rpm -i`),
checks what the package put in place — the binary, the unit, the compose
file, `agent.env` with a token and mode `0600` — starts the agent by hand
with the unit's environment (a container has no systemd), starts the proxy
from the package's compose file with the proxy image built here, and runs
`test-cycle.sh` against it. `SHIPWICK_DEB_IMAGE` and `SHIPWICK_RPM_IMAGE`
name other distributions; the variables of `test-lib.sh` apply. What it does
not run: systemd, SELinux enforcing, a host firewall, the dashboard. Not part
of CI, whose `packages` job installs and removes the packages without
starting the agent.

## measure-limits.sh

The numbers of the handbook's *What one server carries* (§10), and the way
to get them again:

```bash
sh scripts/measure-limits.sh                        # 2 CPUs, 4 GB, everything
sh scripts/measure-limits.sh --cpus 4 --memory 8g   # a larger server
sh scripts/measure-limits.sh --keep requests        # one part, and keep the server
sh scripts/measure-limits.sh --reuse apps database  # more, on the server that was kept
```

The server is a `docker:dind` container given the first `--cpus` processors
of this machine (a cpuset, not a quota) and `--memory` without swap, with the
checkout installed in it the way `test-upgrade.sh` installs it. Four parts,
each of which prints what it did next to what it found:

- `requests`: [oha](https://github.com/hatoo/oha) in a container next to the
  server, on the processors the server does not have, against one
  application through the proxy over HTTPS: as fast as 16, 64 and 256
  connections allow, kept and new, with one replica and with four; then at a
  fixed rate during a rolling deployment.
- `restore`: `shipwick backups run`, `verify` and `restore` of a volume of
  each size in `SHIPWICK_LIMITS_VOLUMES` (MB), without and with
  `SHIPWICK_BACKUP_PASSPHRASE`; then the agent's own state, backed up and put
  back the way the handbook describes.
- `apps`: applications of two replicas each, in the steps of
  `SHIPWICK_LIMITS_APPS`. At every step the memory of the server and of the
  three services, the CPU used with nothing asked of the server, the time of
  `shipwick ps`, `shipwick status`, three API requests and Docker's own
  container list, one rolling deployment, and how long a killed replica
  stays away. It stops when a deployment fails or nine tenths of the memory
  are in use.
- `database`: twenty deployments and five minutes of one request a minute to
  every application, then every table's rows and the bytes of its pages,
  from SQLite's `dbstat`.

The applications are served at `*.localhost`, which only one of the three
public resolvers the agent asks by default answers; the script runs dnsmasq
on the server and sets `SHIPWICK_DNS_RESOLVERS` to it, so that a lookup is
one question. A run takes about three quarters of an hour, needs Docker with privileged
containers, Go and a way to ghcr.io, and about 25 GB of disk for the 5 GB
volume and its copies. It is not part of CI: what it prints depends on the
machine, and the handbook says which one its numbers are from.

## build-release.sh, release-notes.sh

What a release consists of, and its notes — run by the release workflow, and by
you to see what it would publish. The asset names are a contract with
`install.sh`. See [docs/releasing.md](../docs/releasing.md).

```bash
sh scripts/build-release.sh v0.2.0    # → ./dist
sh scripts/release-notes.sh v0.2.0
```

## build-packages.sh

The `.deb` and the `.rpm` of the agent, for amd64 and arm64, into `./dist`.
`build-release.sh` runs it; run it alone to try a change under
`packaging/linux`:

```bash
sh scripts/build-packages.sh v0.7.0
```

It compiles the agent, pins the images in the package's compose file, and
hands everything to `packaging/linux/build-deb.sh` in a `debian:stable-slim`
container and to `build-rpm.sh` in a `rockylinux/rockylinux:9` one
(`SHIPWICK_DEB_IMAGE`, `SHIPWICK_RPM_IMAGE` choose others). The files go in
and the packages come out as tar streams, so nothing is mounted and the
script runs from Git Bash on Windows as it does on Linux. `postinst.sh`,
`prerm.sh` and `postrm.sh` are shared by both formats: each build script puts
the lines in front that turn its package manager's arguments into `install`,
`upgrade` or `purge`. They must not contain a percent sign, which `rpm` would
expand.

## build-sbom.sh

The bill of materials of one compiled binary, SPDX 2.3 JSON: the Go modules
linked into it and the Go that compiled it, read from the binary by syft in
a container. `build-release.sh` runs it for every CLI binary and
`build-packages.sh` for the agent of each architecture.

```bash
sh scripts/build-sbom.sh v0.8.0 dist/shipwick_linux_amd64 dist/shipwick_linux_amd64.spdx.json
```

The binary goes in as a tar stream and the document comes out on standard
output, so nothing is mounted. syft's version is pinned in the script
(`SHIPWICK_SYFT_IMAGE` chooses another) and moved by hand. A binary in which
syft finds no build information fails the build instead of publishing an
empty list.

## image-digests.sh

The lines of a release's `image-digests.txt` for one image, read from the
registry: the manifest list's digest, and each platform's manifest and
configuration. The release workflow runs it after each push. Needs
`docker buildx` and `jq`.

```bash
sh scripts/image-digests.sh ghcr.io/shipwick/agent:0.6.0
```
