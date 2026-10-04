#!/bin/sh
# Runs the installer in this checkout as a new server would, once for each
# distribution named, and then again as an upgrade:
#
#   sh scripts/test-install.sh debian:13 rockylinux/rockylinux:9 alpine:latest
#   sh scripts/test-install.sh --version v0.8.0 --cosign debian:13
#
# For each image it starts a Docker daemon of its own in a container, and a
# container of the distribution that shares the daemon's network and has the
# docker and compose programs: a server with Docker installed and nothing
# else. In it the installer does everything it does on a server: downloads
# the release from GitHub, verifies it, pulls the images, starts the three
# services, waits for the agent, installs the CLI and signs it in.
#
# What this proves: that scripts/install.sh, as it is here, installs a
# published release (the latest, or --version) with each distribution's sh,
# awk, sed, coreutils and downloader — dash, bash and BusyBox do not agree on
# everything — on amd64 or arm64, whichever machine runs it. That is the
# promise get.shipwick.com makes: the installer of main installs the latest
# release.
#
# What it does not: the distribution's kernel (a container runs on its
# host's), its packaging of Docker (the daemon is Docker's own image; choose
# its version with SHIPWICK_TEST_DOCKER, such as 20.10 or 26.1), systemd, a
# host firewall, SELinux. And not the agent, dashboard and proxy of this
# checkout: the images are the release's.
#
# The docker and compose programs are those of the daemon's image. Docker's
# images of old versions carry a Compose too old for the release's compose
# file; SHIPWICK_TEST_CLIENT=latest takes the two programs from the current
# image instead: an old Engine with a current Compose plugin.
#
# --cosign puts cosign into the server and sets SHIPWICK_REQUIRE_SIGNATURE:
# the installation succeeds only if the release's signature verifies. For a
# release from 0.8.0 on.
#
# Needs Docker, with privileged containers, and a way to github.com and
# ghcr.io. Leaves nothing behind.

set -eu

VERSION="latest"
COSIGN=""
# Pinned like the syft of scripts/build-sbom.sh, and moved by hand.
COSIGN_IMAGE="${SHIPWICK_COSIGN_IMAGE:-ghcr.io/sigstore/cosign/cosign:v3.1.3}"
DAEMON_IMAGE="docker:${SHIPWICK_TEST_DOCKER:+$SHIPWICK_TEST_DOCKER-}dind"
CLIENT_IMAGE="${SHIPWICK_TEST_CLIENT:+docker:$SHIPWICK_TEST_CLIENT}"
NAME="${SHIPWICK_TEST_NAME:-shipwick-test-install}-$$"

usage() { echo "usage: $0 [--version vMAJOR.MINOR.PATCH] [--cosign] <image>..." >&2; exit 2; }

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version) [ "$#" -ge 2 ] || usage; VERSION="$2"; shift ;;
        --cosign) COSIGN=1 ;;
        -*) usage ;;
        *) break ;;
    esac
    shift
done
[ "$#" -gt 0 ] || usage
case "$VERSION" in
    latest|v[0-9]*.[0-9]*.[0-9]*) ;;
    *) usage ;;
esac

cd "$(dirname "$0")/.."
command -v docker >/dev/null 2>&1 || { echo "docker is needed: the daemon and the server are containers" >&2; exit 1; }
# Git Bash would rewrite the containers' paths into Windows ones.
export MSYS_NO_PATHCONV=1

daemon="$NAME-daemon"
server="$NAME-server"
cleanup() {
    docker rm -f -v "$server" >/dev/null 2>&1 || true
    docker rm -f -v "$daemon" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "FAIL ($image): $*" >&2; exit 1; }

# on_server SCRIPT — runs SCRIPT with the server's own sh.
on_server() { docker exec -e VERSION="$VERSION" "$server" sh -c "$1"; }

# copy_from CONTAINER FILE DIRECTORY — one file from a container into a
# directory of the server, as a tar stream: nothing is mounted.
copy_from() { docker cp "$1:$2" - | docker cp - "$server:$3"; }

install() { # install LABEL — one run of the installer; its output is $output
    output="$(docker exec -e SHIPWICK_VERSION="$VERSION" -e SHIPWICK_AGENT_DOMAIN=agent.localhost \
        -e SHIPWICK_REQUIRE_SIGNATURE="$COSIGN" "$server" sh /install.sh 2>&1)" || {
        printf '%s\n' "$output"
        fail "the installer failed ($1)"
    }
    printf '%s\n' "$output" | sed 's/^/    /'
}

expect() { # expect TEXT — the last run of the installer said TEXT
    case "$output" in
        *"$1"*) ;;
        *) fail "the installer did not say: $1" ;;
    esac
}

for image in "$@"; do
    echo "== $image, Shipwick $VERSION, Docker from $DAEMON_IMAGE"
    cleanup

    docker run -d --privileged --name "$daemon" -e DOCKER_TLS_CERTDIR= "$DAEMON_IMAGE" >/dev/null
    i=0
    until docker exec "$daemon" docker info >/dev/null 2>&1; do
        i=$((i + 1))
        [ "$i" -lt 60 ] || fail "the Docker daemon did not start"
        sleep 1
    done

    # On the daemon's network: the ports the proxy takes are this server's,
    # and the daemon is at the address a socket would be.
    docker run -d --name "$server" --network "container:$daemon" \
        -e DOCKER_HOST=tcp://127.0.0.1:2375 "$image" sleep 86400 >/dev/null

    # Docker's programs are static, and the same on every distribution: what
    # differs between distributions is everything else the installer calls.
    on_server 'mkdir -p /usr/local/bin /usr/local/lib/docker/cli-plugins'
    client="$daemon"
    [ -z "$CLIENT_IMAGE" ] || client="$(docker create "$CLIENT_IMAGE")"
    copy_from "$client" /usr/local/bin/docker /usr/local/bin/
    copy_from "$client" /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/
    [ -z "$CLIENT_IMAGE" ] || docker rm "$client" >/dev/null
    if [ -n "$COSIGN" ]; then
        tool="$(docker create "$COSIGN_IMAGE")"
        copy_from "$tool" /ko-app/cosign /usr/local/bin/ || { docker rm "$tool" >/dev/null; fail "could not copy cosign"; }
        docker rm "$tool" >/dev/null
    fi
    # A server has a way to download; the smallest images have none. Where
    # the image brings one, it is the one tested: BusyBox wget on Alpine.
    on_server '
        if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then exit 0; fi
        if command -v apt-get >/dev/null 2>&1; then
            apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends curl ca-certificates >/dev/null 2>&1
        elif command -v dnf >/dev/null 2>&1; then
            dnf install -y -q curl >/dev/null
        else
            echo "this image has neither curl nor wget, and no package manager this test knows" >&2
            exit 1
        fi' || fail "could not install curl"
    tar -C scripts -cf - install.sh | docker cp - "$server:/"

    install "a new server"
    expect "Wrote /opt/shipwick/.env"
    expect "The agent is healthy"
    expect "Installed the shipwick CLI to /usr/local/bin/shipwick"
    expect "shipwick on this server is signed in"
    [ -z "$COSIGN" ] || expect "The release is signed by the release workflow"

    # shellcheck disable=SC2016  # expanded by the shell of the server
    on_server '
        set -e
        test "$(stat -c %a /opt/shipwick)" = 700
        test "$(stat -c %a /opt/shipwick/.env)" = 600
        grep -Eq "^SHIPWICK_AGENT_TOKEN=[0-9a-f]{64}$" /opt/shipwick/.env
        grep -qx "SHIPWICK_AGENT_DOMAIN=agent.localhost" /opt/shipwick/.env
        test "$(docker ps -q --filter label=com.docker.compose.project=shipwick | wc -l)" -eq 3
        installed="$(shipwick --version)"
        test "$VERSION" = latest || test "$installed" = "shipwick version $VERSION"
        shipwick context ls | grep -q "https://agent.localhost"
    ' || fail "the installation is not what the installer said it was"
    token="$(on_server 'grep ^SHIPWICK_AGENT_TOKEN= /opt/shipwick/.env')"

    # Running it again is how a server is upgraded.
    install "an upgrade"
    expect "Keeping the existing /opt/shipwick/.env"
    expect "The agent is healthy"
    expect "Your API token is unchanged"
    [ "$token" = "$(on_server 'grep ^SHIPWICK_AGENT_TOKEN= /opt/shipwick/.env')" ] || fail "the token changed on an upgrade"
    # shellcheck disable=SC2016  # expanded by the shell of the server
    on_server 'test "$(docker ps -q --filter label=com.docker.compose.project=shipwick | wc -l)" -eq 3' \
        || fail "the services are not all running after the upgrade"

    echo "ok: $image"
done
