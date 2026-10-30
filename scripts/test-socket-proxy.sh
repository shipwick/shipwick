#!/bin/sh
# Runs Shipwick as compose.production.yml arranges it — the agent behind the
# socket proxy, with no Docker socket of its own — in a Docker daemon of its
# own, and puts it through everything an installation does (test-cycle.sh).
# It passes when
#
#   - the agent's container mounts no Docker socket, and says that it reaches
#     Docker through the proxy,
#   - every operation works with the agent holding only the filtered socket,
#   - the proxy refused nothing the agent asked for: the allow-list is complete,
#   - the proxy refuses what is not on its list, and a bind mount,
#   - the overlay that gives the agent the socket itself
#     (compose.direct-socket.yml) takes the proxy out, and the agent says so,
#
# and it shows what the proxy lets through all the same: a privileged
# container. See docs/handbook.md, "The Docker socket".
#
#   sh scripts/test-socket-proxy.sh
#
# A new Engine API call in agent/internal/docker needs its line in the
# allow-list of configs/compose.production.yml (and docker-compose.yml) and
# its row in the handbook's table; this is the test that notices one missing.
#
# Needs Docker, and Go to build the CLI. The daemon's container pulls the
# proxy's image and three small test images. Variables: scripts/test-lib.sh.
set -eu

# shellcheck source=scripts/test-lib.sh
. "$(dirname "$0")/test-lib.sh"

prepare shipwick-test-socket-proxy ""
start_daemon docker:dind
install

# mounts_of <container>: the sources of its mounts.
mounts_of() { in_daemon docker inspect "$1" --format '{{range .Mounts}}{{.Source}} {{end}}'; }

say "starting Shipwick as the production compose file arranges it"
compose up -d --quiet-pull
await_agent

mounts=$(mounts_of shipwick-agent-1)
case "$mounts" in
    *docker.sock*) die "the agent mounts the Docker socket: $mounts" ;;
esac
echo "the agent's mounts: $mounts"
case "$(mounts_of shipwick-socket-proxy-1)" in
    */var/run/docker.sock*) echo "the proxy's mounts: $(mounts_of shipwick-socket-proxy-1)" ;;
    *) die "the proxy does not mount the Docker socket" ;;
esac
status=$(shipwick server status)
echo "$status" | grep '^Docker'
echo "$status" | grep -q '^Docker .*behind the socket proxy' || die "shipwick server status does not say that the agent is behind the socket proxy"
if shipwick doctor 2>&1 | grep -q 'holds the Docker socket'; then
    die "shipwick doctor says the agent holds the Docker socket, behind the proxy"
fi

cycle

say "what the proxy refused the agent"
refused=$(in_daemon docker logs shipwick-socket-proxy-1 2>&1 | grep 'blocked request' || true)
[ -z "$refused" ] || die "the agent made calls that are not on the list:
$refused"
echo "nothing"

# The filtered socket, as the agent has it, from where the docker CLI is.
socket="unix://$(in_daemon docker volume inspect shipwick_docker-proxy --format '{{.Mountpoint}}')/docker.sock"

# refuses <docker arguments...>: the proxy answers 403.
refuses() {
    if out=$(in_daemon docker -H "$socket" "$@" 2>&1); then
        die "docker $* went through the proxy"
    fi
    case "$out" in
        *Forbidden*) echo "refused: docker $*" ;;
        *) die "docker $* failed, but not at the proxy: $out" ;;
    esac
}

say "what the proxy refuses anyone who holds its socket"
in_daemon docker -H "$socket" ps -q >/dev/null || die "the filtered socket does not answer"
refuses swarm init
refuses plugin ls
refuses kill shipwick-caddy-1
refuses update --memory 1g shipwick-caddy-1
refuses commit shipwick-caddy-1 copy
refuses network rm shipwick-services
refuses system prune -f
refuses create -v /:/host alpine:3.22
refuses create --mount type=bind,src=/etc,dst=/host alpine:3.22
refuses create --volumes-from shipwick-agent-1 alpine:3.22
refuses volume create -o type=none -o o=bind -o device=/ host-root

say "what it does not refuse"
in_daemon docker pull -q alpine:3.22 >/dev/null
for flags in "--privileged" "--pid=host --cap-add SYS_ADMIN" "--device /dev/null:/dev/host-device"; do
    # shellcheck disable=SC2086 # several flags in one entry
    id=$(in_daemon docker -H "$socket" create $flags alpine:3.22) || die "docker create $flags was refused: the handbook says it is not"
    in_daemon docker -H "$socket" rm "$id" >/dev/null
    echo "accepted: docker create $flags"
done
echo "The proxy reads a request's path and its bind mounts, not the rest of its body:"
echo "whoever holds the filtered socket can still become root on the server."

say "the way out: compose.direct-socket.yml gives the agent the socket itself"
overlays="compose.direct-socket.yml"
compose up -d --quiet-pull --remove-orphans
await_agent
mounts=$(mounts_of shipwick-agent-1)
case "$mounts" in
    */var/run/docker.sock*) echo "the agent's mounts: $mounts" ;;
    *) die "with the overlay the agent still has no Docker socket: $mounts" ;;
esac
if in_daemon docker ps -q --filter label=com.docker.compose.service=socket-proxy | grep -q .; then
    die "the proxy's container is still running under the overlay"
fi
echo "the proxy's container is gone"
status=$(shipwick server status)
echo "$status" | grep '^Docker'
echo "$status" | grep -q '^Docker .*holding the Docker socket' || die "shipwick server status does not say that the agent holds the Docker socket"
shipwick doctor 2>&1 | grep 'holds the Docker socket itself' || die "shipwick doctor does not say that the agent holds the Docker socket"
shipwick deploy -f /test/testdata/web/deploy.yaml >/dev/null
shipwick delete web --yes >/dev/null
echo "a deployment works on the socket itself"

printf '\nShipwick works behind the socket proxy, and without it where that is asked for.\n'
