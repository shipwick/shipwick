#!/bin/sh
# Runs Shipwick behind the socket proxy of configs/compose.socket-proxy.yml,
# in a Docker daemon of its own, and puts it through everything an
# installation does (test-cycle.sh). It passes when
#
#   - every operation works with the agent holding only the filtered socket,
#   - the proxy refused nothing the agent asked for,
#   - the proxy refuses what is not on its list, and a bind mount,
#
# and it shows what the proxy lets through all the same: a privileged
# container. See docs/handbook.md, "Less than the whole socket".
#
#   sh scripts/test-socket-proxy.sh
#
# Needs Docker, and Go to build the CLI. The daemon's container pulls the
# proxy's image and three small test images. Variables: scripts/test-lib.sh.
set -eu

# shellcheck source=scripts/test-lib.sh
. "$(dirname "$0")/test-lib.sh"

prepare shipwick-test-socket-proxy compose.socket-proxy.yml
start_daemon docker:dind
install

say "starting Shipwick behind the socket proxy"
compose up -d --quiet-pull
await_agent

mounts=$(in_daemon docker inspect shipwick-agent-1 --format '{{range .Mounts}}{{.Source}} {{end}}')
case "$mounts" in
    *docker.sock*) die "the agent still mounts the Docker socket: $mounts" ;;
esac
echo "the agent's mounts: $mounts"

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

printf '\nShipwick works behind the socket proxy.\n'
