#!/bin/sh
# Installs the .deb and the .rpm built from this checkout where they are meant
# for, Debian and Rocky Linux, and runs an installation through each: the
# agent as a program of the host, the proxy from the package's compose file,
# and everything test-cycle.sh asks of them.
#
#   sh scripts/test-package.sh
#   SHIPWICK_PACKAGES=dist sh scripts/test-package.sh    # packages built already
#
# The server is a Docker daemon in a container of its own (docker:dind). The
# host the package is installed on is a container of the distribution inside
# it that shares the daemon's network namespace, holds the daemon's socket
# and /run/shipwick: to the agent, a server with Docker installed. A container
# has no systemd, so the agent is started by hand with the unit's environment.
#
# What this proves: the packages install with dpkg and rpm and their scripts
# run; the settings file is written with a token; the agent of the package
# runs against the host's Docker and the package's proxy, on the three
# networks it creates, and does everything an installation does. What it does
# not: systemd, SELinux enforcing, a host firewall, the dashboard (its image
# is not built here). docs/handbook.md, "Installation from a package", says
# what SELinux is expected to ask for.
#
# Needs Docker, with privileged containers, and Go. Variables:
# scripts/test-lib.sh (SHIPWICK_TEST_NAME, SHIPWICK_TEST_KEEP,
# SHIPWICK_CADDY_IMAGE, SHIPWICK_CLI); SHIPWICK_PACKAGES, a directory that
# holds the packages instead of building them; SHIPWICK_DEB_IMAGE and
# SHIPWICK_RPM_IMAGE, the distributions.
set -eu

# shellcheck source=scripts/test-lib.sh
. "$(dirname "$0")/test-lib.sh"

DEB_IMAGE="${SHIPWICK_DEB_IMAGE:-debian:stable-slim}"
RPM_IMAGE="${SHIPWICK_RPM_IMAGE:-rockylinux/rockylinux:9}"

# As test-lib.sh's prepare, without the agent's and the dashboard's images:
# the agent is in the packages, and the dashboard is not started.
name="${SHIPWICK_TEST_NAME:-shipwick-test-package}"
command -v docker >/dev/null || die "docker is not on the PATH"
command -v go >/dev/null || die "go is not on the PATH"
cd "$(dirname "$0")/.." || exit
work=$(mktemp -d)
if command -v cygpath >/dev/null 2>&1; then
    work=$(cygpath -m "$work")
fi
trap cleanup EXIT
trap 'exit 130' INT TERM

arch=$(docker version --format '{{.Server.Arch}}')
caddy_image="${SHIPWICK_CADDY_IMAGE:-}"
if [ -z "$caddy_image" ]; then
    say "building the proxy"
    caddy_image=shipwick-test-caddy
    docker build -q -t "$caddy_image" -f Dockerfile.caddy .
fi
if [ -n "${SHIPWICK_CLI:-}" ]; then
    cp "$SHIPWICK_CLI" "$work/shipwick"
else
    say "building the CLI"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$work/shipwick" ./cli/cmd/shipwick
fi
packages="${SHIPWICK_PACKAGES:-}"
if [ -z "$packages" ]; then
    say "building the packages"
    # It hands paths to go, which under Git Bash wants them converted; the
    # library turned that off for docker's sake.
    (unset MSYS_NO_PATHCONV; sh scripts/build-packages.sh v0.0.0-test)
    packages=dist
fi
for file in "shipwick-agent_$arch.deb" "shipwick-agent_$arch.rpm"; do
    [ -s "$packages/$file" ] || die "$packages/$file is not there"
done

start_daemon docker:dind

say "loading the proxy's image, the packages, the CLI and the fixtures into it"
docker save "$caddy_image" | in_daemon docker load >/dev/null
as_root mkdir -p /test/dist
docker cp -q "$packages/shipwick-agent_$arch.deb" "$name:/test/dist/"
docker cp -q "$packages/shipwick-agent_$arch.rpm" "$name:/test/dist/"
docker cp -q "$work/shipwick" "$name:/usr/local/bin/shipwick"
docker cp -q scripts/test-cycle.sh "$name:/test/test-cycle.sh"
docker cp -q scripts/testdata "$name:/test/"
as_root chmod 0755 /usr/local/bin/shipwick
as_root chmod -R a+rX /test
# The hostnames the fixtures are served at, for the requests the cycle makes.
as_root sh -c 'echo "127.0.0.1 agent.localhost hello.localhost site.localhost" >> /etc/hosts'

# on_host <command...>: on the host the package is installed on.
on_host() { in_daemon docker exec -i server "$@"; }

# installed IMAGE PACKAGE INSTALL-COMMAND...: the package on that
# distribution, from the installation through the cycle and back.
installed() {
    image="$1"
    package="$2"
    shift 2
    say "$package on $image"
    in_daemon docker pull -q "$image" >/dev/null
    # The host: the distribution with the daemon's socket, in the daemon's
    # network namespace — the agent listens on the server's own addresses,
    # loopback and the one on the control network — and with /run/shipwick,
    # where the proxy's admin socket is.
    in_daemon docker run -d --name server --network host \
        -v /var/run/docker.sock:/var/run/docker.sock -v /run/shipwick:/run/shipwick -v /test/dist:/dist:ro \
        "$image" sleep infinity >/dev/null
    # The agent routes a hostname once it resolves to the server.
    on_host sh -c 'echo "127.0.0.1 agent.localhost hello.localhost site.localhost" >> /etc/hosts'
    on_host "$@" "/dist/$package"

    say "what the package put in place"
    on_host shipwick-agent version
    [ "$(on_host stat -c %a /etc/shipwick/agent.env)" = 600 ] || die "/etc/shipwick/agent.env is not mode 600"
    on_host grep -Eq '^SHIPWICK_AGENT_TOKEN=[0-9a-f]{64}$' /etc/shipwick/agent.env || die "agent.env was written without a token"
    on_host test -f /usr/lib/systemd/system/shipwick-agent.service || die "the unit is not in place"
    on_host test -f /usr/share/shipwick/compose.yml || die "the compose file is not in place"
    token=$(on_host sed -n 's/^SHIPWICK_AGENT_TOKEN=//p' /etc/shipwick/agent.env)
    # The hostname of the API, as the installer would write it, and the proxy
    # built here in place of the release's.
    on_host sh -c "printf 'SHIPWICK_AGENT_DOMAIN=agent.localhost\nSHIPWICK_CADDY_IMAGE=%s\n' '$caddy_image' >> /etc/shipwick/agent.env"

    say "starting the agent with the unit's environment, and the proxy from the package's compose file"
    in_daemon docker exec -d server sh -c 'set -a; . /etc/shipwick/agent.env; SHIPWICK_DATA_DIR=/var/lib/shipwick HOME=/root exec /usr/bin/shipwick-agent >/var/log/shipwick-agent.log 2>&1'
    await_agent
    # In that order: the agent creates the networks the compose file joins.
    in_daemon docker cp server:/usr/share/shipwick/compose.yml /test/compose.yml
    in_daemon docker cp server:/etc/shipwick/agent.env /test/agent.env
    in_daemon docker compose --env-file /test/agent.env -f /test/compose.yml up -d --quiet-pull caddy

    cycle

    say "taking it down"
    in_daemon docker compose --env-file /test/agent.env -f /test/compose.yml down -v
    in_daemon docker rm -f server >/dev/null
    as_root rm -f /run/shipwick/admin.sock /test/agent.env /test/compose.yml
}

installed "$DEB_IMAGE" "shipwick-agent_$arch.deb" dpkg -i
installed "$RPM_IMAGE" "shipwick-agent_$arch.rpm" rpm -i

printf '\nThe packages work on Debian and Rocky Linux.\n'
