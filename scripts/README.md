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
   the release's `checksums.txt` before it replaces anything.
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

## build-release.sh, release-notes.sh

What a release consists of, and its notes — run by the release workflow, and by
you to see what it would publish. The asset names are a contract with
`install.sh`. See [docs/releasing.md](../docs/releasing.md).

```bash
sh scripts/build-release.sh v0.2.0    # → ./dist
sh scripts/release-notes.sh v0.2.0
```
