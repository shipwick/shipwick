# Scripts

## install.sh

The installer behind `curl -fsSL https://get.shipwick.com | sh`.

```bash
sh install.sh          # server: agent + Caddy + dashboard (as root), and the CLI
sh install.sh --cli    # the CLI only
```

What it does on a server:

1. Checks for Linux, root, a running Docker and the Compose plugin. It does
   **not** install Docker: that is the server owner's decision.
   Then, before it writes anything, that the HTTP and HTTPS ports are free —
   unless its own Caddy holds them, which is an upgrade.
2. Writes `/opt/shipwick/compose.yml`: the release's `compose.production.yml`,
   in which the three images — agent, dashboard and the proxy, Shipwick's
   build of Caddy — are pinned to the release's version. Verified against
   the release's `checksums.txt` before it replaces anything — and
   `checksums.txt` against the release's signature first, when cosign is
   installed (`verify_signature`). Compose is asked to read the new file
   before it replaces the one in use (`check_compose_file`): one older than
   2.23.1 cannot, and the installer stops there with the version it needs.
3. On first run only, writes `/opt/shipwick/.env` (mode `0600`, directory
   `0700`) with a freshly generated API token and the two optional hostnames —
   asked for in a terminal, or taken from `SHIPWICK_AGENT_DOMAIN` /
   `SHIPWICK_DASHBOARD_DOMAIN`. Hostnames are validated before anything is written.
   Settings given for the run are kept in it too: the ports, images of your
   own, the webhook, and `SHIPWICK_CLOUDFLARE_API_TOKEN` (certificates through
   Cloudflare DNS, for hostnames behind Cloudflare's proxy).
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
   next steps.

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
  `-f`. Do not add `-f` back.
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
  always belong together.
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
  release workflow — at the tag `SHIPWICK_VERSION` names, or at any plain
  version tag for `latest`. A signature that does not verify stops the
  installer. No cosign, or a release without a signature (before 0.8.0), is
  one line of output; with `SHIPWICK_REQUIRE_SIGNATURE=1` either stops it
  too. The installer never installs cosign. With a bundle it verifies only
  when that variable is set: cosign fetches Sigstore's keys, which a server
  with no way out cannot. Do not make the check quieter: "was not checked"
  must stay in the output of an installation that did not check.
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

## test-install.sh

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
modes of `/opt/shipwick` and `.env`, the token, that three services run,
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
release's CLI and the checkout's both deploy and roll back, the token works,
the job runs, the backup verifies, the proxy runs this version's
configuration, and that `shipwick doctor` finds no problem. Requests go to the
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
## test-socket-proxy.sh, test-rootless.sh

The two arrangements of the handbook's *Less than the whole socket*, each
started in a Docker daemon of its own and put through everything an
installation does:

```bash
sh scripts/test-socket-proxy.sh              # the agent behind configs/compose.socket-proxy.yml
sh scripts/test-rootless.sh                  # on docker:dind-rootless, with configs/compose.rootless.yml
sh scripts/test-rootless.sh --socket-proxy   # both
```

Each builds the three images and the CLI from the checkout (Docker, and Go
for the CLI), starts `docker:dind` or `docker:dind-rootless` as one
privileged container, loads the images into it, starts the production
compose file with the overlay there, and runs `test-cycle.sh` inside that
container: deploy, rollout, rollback, stop and start, a hook, a job, a
one-off command, a backup with its verification, a restore, a static
folder, a built image, an export and an import, deletion, with the fixtures
of `scripts/testdata`. Nothing else of the Docker they are started from is
touched; the container is removed at the end unless `SHIPWICK_TEST_KEEP=1`.
`test-lib.sh` holds what the two share and lists the variables. They take a
few minutes each, most of it pulling inside the new daemon, and are not part
of CI.

- `test-socket-proxy.sh` fails when the proxy refused the agent anything:
  **a new Engine API call in `agent/internal/docker` needs its line in the
  allow-list of `configs/compose.socket-proxy.yml` and in the handbook's
  table**, and this is the test that notices. It then tries what the proxy
  must refuse, and what it is documented not to.
- `test-rootless.sh` first starts the stack with ports below 1024 closed to
  ordinary users, which must fail with Docker's message, and after that
  recreates it: the refused start leaves the proxy's container on one of its
  two networks. Its daemon has no cgroups, so it also holds the agent to
  saying that limits are not enforced.

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
