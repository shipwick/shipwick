#!/bin/sh
# What test-socket-proxy.sh and test-rootless.sh share: a Docker daemon of
# its own in a container, the images and the CLI built from this checkout and
# put into it, and the compose files started there. Sourced, not run.
#
# Nothing touches the containers, images or networks of the Docker it is
# started from, apart from the one container named $name and, unless images
# are given, three images tagged shipwick-test-*.
#
#   SHIPWICK_TEST_NAME      the container's name (default: the one the script gives it)
#   SHIPWICK_TEST_KEEP=1    leave it running afterwards, to look around
#   SHIPWICK_AGENT_IMAGE, SHIPWICK_CADDY_IMAGE, SHIPWICK_DASHBOARD_IMAGE
#                           images to test instead of building them here
#   SHIPWICK_CLI            a linux binary of the CLI to use instead of building one

# Git Bash would turn /test into a Windows path on its way to docker.
export MSYS_NO_PATHCONV=1

say() { printf '\n== %s\n' "$*"; }
die() { printf 'FAILED: %s\n' "$*" >&2; exit 1; }

# in_daemon <command...>: inside the daemon's container, as the user the
# daemon runs as, with the docker CLI pointed at it.
in_daemon() {
    if [ -n "$daemon_socket" ]; then
        docker exec -i -e "DOCKER_HOST=unix://$daemon_socket" "$name" "$@"
    else
        docker exec -i "$name" "$@"
    fi
}

as_root() { docker exec -i -u 0 "$name" "$@"; }

cleanup() {
    status=$?
    if [ "${SHIPWICK_TEST_KEEP:-}" = 1 ]; then
        printf '\n%s is left running: docker exec -it %s sh, then docker rm -f -v %s\n' "$name" "$name" "$name"
    else
        docker rm -f -v "$name" >/dev/null 2>&1 || true
    fi
    rm -f "$work/shipwick" "$work/env"
    rmdir "$work" 2>/dev/null || true
    exit "$status"
}

# prepare <name> <overlays>: the images and the CLI of this checkout, for a
# daemon in a container of that name and the compose overlays to start on
# top of compose.production.yml.
prepare() {
    name="${SHIPWICK_TEST_NAME:-$1}"
    overlays="$2"
    command -v docker >/dev/null || die "docker is not on the PATH"
    cd "$(dirname "$0")/.." || exit
    work=$(mktemp -d)
    # Under Git Bash docker is a Windows program and takes the directory by
    # its Windows name.
    if command -v cygpath >/dev/null 2>&1; then
        work=$(cygpath -m "$work")
    fi
    trap cleanup EXIT
    trap 'exit 130' INT TERM

    agent_image="${SHIPWICK_AGENT_IMAGE:-}"
    caddy_image="${SHIPWICK_CADDY_IMAGE:-}"
    dashboard_image="${SHIPWICK_DASHBOARD_IMAGE:-}"
    if [ -z "$agent_image" ]; then
        say "building the agent"
        agent_image=shipwick-test-agent
        docker build -q -t "$agent_image" .
    fi
    if [ -z "$caddy_image" ]; then
        say "building the proxy"
        caddy_image=shipwick-test-caddy
        docker build -q -t "$caddy_image" -f Dockerfile.caddy .
    fi
    if [ -z "$dashboard_image" ]; then
        say "building the dashboard"
        dashboard_image=shipwick-test-dashboard
        docker build -q -t "$dashboard_image" dashboard
    fi

    if [ -n "${SHIPWICK_CLI:-}" ]; then
        cp "$SHIPWICK_CLI" "$work/shipwick"
    else
        command -v go >/dev/null || die "go is not on the PATH; build the CLI for linux and name it in SHIPWICK_CLI"
        say "building the CLI"
        arch=$(docker version --format '{{.Server.Arch}}')
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$work/shipwick" ./cli/cmd/shipwick
    fi
}

# start_daemon <image> [socket]: a daemon of its own. The socket is given for
# a daemon that does not listen at /var/run/docker.sock.
start_daemon() {
    daemon_socket="${2:-}"
    say "starting a Docker daemon in a container: $1"
    docker rm -f -v "$name" >/dev/null 2>&1 || true
    docker run -d --privileged --name "$name" -e DOCKER_TLS_CERTDIR= "$1" >/dev/null
    n=0
    until in_daemon docker info >/dev/null 2>&1; do
        n=$((n + 1))
        [ "$n" -lt 90 ] || die "the daemon in $name did not come up: docker logs $name"
        sleep 1
    done
    in_daemon docker info --format 'Docker {{.ServerVersion}}, cgroup driver {{.CgroupDriver}}, {{.SecurityOptions}}'
}

# install: the images, the CLI, the compose files and the fixtures.
install() {
    say "loading the images into it"
    docker save "$agent_image" "$caddy_image" "$dashboard_image" | in_daemon docker load

    token=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    {
        echo "SHIPWICK_AGENT_TOKEN=$token"
        echo "SHIPWICK_AGENT_IMAGE=$agent_image"
        echo "SHIPWICK_CADDY_IMAGE=$caddy_image"
        echo "SHIPWICK_DASHBOARD_IMAGE=$dashboard_image"
        echo "SHIPWICK_AGENT_DOMAIN=agent.localhost"
        [ -z "$daemon_socket" ] || echo "SHIPWICK_DOCKER_SOCKET=$daemon_socket"
    } > "$work/env"

    as_root mkdir -p /test/configs
    docker cp -q "$work/env" "$name:/test/env"
    docker cp -q "$work/shipwick" "$name:/usr/local/bin/shipwick"
    docker cp -q configs/. "$name:/test/configs/"
    docker cp -q scripts/test-cycle.sh "$name:/test/test-cycle.sh"
    docker cp -q scripts/testdata "$name:/test/"
    as_root chmod 0755 /usr/local/bin/shipwick
    as_root chmod -R a+rX /test
    # The hostnames the fixtures are served at, for the requests the cycle makes.
    as_root sh -c 'echo "127.0.0.1 agent.localhost hello.localhost site.localhost" >> /etc/hosts'
}

# compose <arguments...>: docker compose in the daemon, on the production
# file, the overlays named in $overlays and the test's own additions.
compose() {
    files="-f /test/configs/compose.production.yml"
    for overlay in $overlays; do
        files="$files -f /test/configs/$overlay"
    done
    # shellcheck disable=SC2086 # the list of files is meant to be split
    in_daemon docker compose --env-file /test/env $files -f /test/testdata/compose.test.yml "$@"
}

# await_agent: the API answers.
await_agent() {
    n=0
    until in_daemon wget -q -T 2 -O /dev/null http://127.0.0.1:9000/api/v1/health 2>/dev/null; do
        n=$((n + 1))
        [ "$n" -lt 60 ] || die "the agent did not come up: docker exec $name docker logs shipwick-agent-1"
        sleep 1
    done
}

# cycle: everything an installation does, once through.
cycle() {
    in_daemon sh /test/test-cycle.sh "$token"
}

# shipwick <arguments...>: the CLI in the daemon's container, against the
# agent there and with a configuration file of its own.
shipwick() {
    in_daemon env SHIPWICK_CONFIG=/tmp/shipwick-test.yaml SHIPWICK_AGENT_URL=http://127.0.0.1:9000 "SHIPWICK_AGENT_TOKEN=$token" shipwick "$@"
}
