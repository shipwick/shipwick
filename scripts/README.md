# Scripts

## install.sh

The installer behind `curl -fsSL https://get.shipwick.com | sh`.

```bash
sh install.sh          # server: agent + Caddy + dashboard (as root), and deployctl
sh install.sh --cli    # deployctl only
```

What it does on a server:

1. Checks for Linux, root, a running Docker and the Compose plugin. It does
   **not** install Docker: that is the server owner's decision.
2. Writes `/opt/shipwick/compose.yml`: the release's `compose.production.yml`,
   in which both images are pinned to the release's version. Verified against
   the release's `checksums.txt` before it replaces anything.
3. On first run only, writes `/opt/shipwick/.env` (mode `0600`, directory
   `0700`) with a freshly generated API token and the two optional hostnames —
   asked for in a terminal, or taken from `SHIPWICK_AGENT_DOMAIN` /
   `SHIPWICK_DASHBOARD_DOMAIN`. Hostnames are validated before anything is written.
4. Pulls the images, starts the stack, waits for the agent to report healthy.
5. Installs `deployctl`, verified the same way. **A checksum mismatch installs
   nothing** and leaves a running installation as it was.
6. Prints the token (once, and only if it was generated in this run) and the
   next steps.

Properties worth keeping if you change it:

- **Idempotent.** Running it again is how you upgrade. An existing `.env` —
  the token — is never rewritten.
- **Safe to pipe.** Everything is in functions and `main "$@"` is the last
  line: a download cut off half-way executes nothing.
- **POSIX `sh`**, checked with `shellcheck -s sh` in CI. No bashisms.
- Downloads are HTTPS-only (`--proto '=https'`), written to a temporary name
  and moved into place, so an interrupted download never leaves a half file.
- **Everything comes from one release** — `SHIPWICK_VERSION`, by default the
  latest — never from a branch, so the compose file, the images and the CLI
  always belong together.

Testing it without a server — it only needs a Docker socket. With
`SHIPWICK_COMPOSE_FILE` set (or run from a checkout) it uses that compose file
instead of downloading one, and with it images you built yourself:

```bash
docker build -t ghcr.io/shipwick/agent . && docker build -t ghcr.io/shipwick/dashboard dashboard/
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$PWD:/src:ro" \
  -e SHIPWICK_COMPOSE_FILE=/src/configs/compose.production.yml \
  -e SHIPWICK_HTTP_PORT=8080 -e SHIPWICK_HTTPS_PORT=8443 \
  -e SHIPWICK_AGENT_DOMAIN=agent.localhost \
  docker:cli sh /src/scripts/install.sh
```

Clean up with `docker compose -p shipwick down -v`.

## build-release.sh, release-notes.sh

What a release consists of, and its notes — run by the release workflow, and by
you to see what it would publish. The asset names are a contract with
`install.sh`. See [docs/releasing.md](../docs/releasing.md).

```bash
sh scripts/build-release.sh v0.2.0    # → ./dist
sh scripts/release-notes.sh v0.2.0
```
