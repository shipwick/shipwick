#!/bin/sh
# Runs Shipwick in a Docker daemon of its own that asks an authorization
# plugin about every request — opa-docker-authz with the policy of
# configs/docker-authz.rego — and puts it through everything an installation
# does (test-cycle.sh). It passes when
#
#   - every operation works, which is to say the agent never made a request
#     the policy refuses,
#   - the policy refuses what the socket proxy lets through: a privileged
#     container, a directory of the server mounted, a namespace of the server,
#     a capability beyond Docker's default set, a device, a privileged command
#     in a container, a volume made of a directory of the server, Swarm, and a
#     request whose body the plugin cannot read.
#
# See docs/handbook.md, "Less than the whole socket".
#
#   sh scripts/test-docker-authz.sh
#
# Needs Docker, and Go to build the CLI. The daemon's container pulls the
# plugin from ghcr.io and three small test images. Variables:
# scripts/test-lib.sh, and SHIPWICK_AUTHZ_PLUGIN for another build of the
# plugin.
set -eu

# shellcheck source=scripts/test-lib.sh
. "$(dirname "$0")/test-lib.sh"

plugin="${SHIPWICK_AUTHZ_PLUGIN:-ghcr.io/open-policy-agent/opa-docker-authz:v0.11}"

prepare shipwick-test-docker-authz ""
start_daemon docker:dind

say "installing the authorization plugin: $plugin"
# The plugin's filesystem has /etc/docker of the daemon's host at /opa, read
# only, and the policy file is read again for every request.
as_root mkdir -p /etc/docker/policies
docker cp -q configs/docker-authz.rego "$name:/etc/docker/policies/authz.rego"
in_daemon docker plugin install --grant-all-permissions "$plugin" opa-args="-policy-file /opa/policies/authz.rego" >/dev/null
# authorization-plugins is among the settings dockerd reads again on SIGHUP.
printf '{"authorization-plugins":["%s"]}\n' "$plugin" | as_root sh -c 'cat > /etc/docker/daemon.json'
as_root pkill -HUP -x dockerd
in_daemon docker pull -q alpine:3.22 >/dev/null
n=0
while in_daemon docker run --rm --privileged alpine:3.22 true >/dev/null 2>&1; do
    n=$((n + 1))
    [ "$n" -lt 30 ] || die "the daemon still starts privileged containers: the plugin is not in effect"
    sleep 1
done
echo "in effect: a privileged container is refused"

install

say "starting Shipwick behind the authorization plugin"
compose up -d --quiet-pull
await_agent

cycle

# refuses <docker arguments...>: the daemon answers "authorization denied".
refuses() {
    if out=$(in_daemon docker "$@" 2>&1); then
        die "docker $* was allowed"
    fi
    case "$out" in
        *"authorization denied"*) echo "refused: docker $*" ;;
        *) die "docker $* failed, but not at the plugin: $out" ;;
    esac
}

say "what the policy refuses anyone who holds the socket"
refuses run --rm --privileged alpine:3.22 true
refuses create -v /:/host alpine:3.22
refuses create --mount type=bind,src=/etc,dst=/host alpine:3.22
refuses create --volumes-from shipwick-agent-1 alpine:3.22
refuses create --pid=host alpine:3.22
refuses create --net=host alpine:3.22
refuses create --ipc=host alpine:3.22
refuses create --cap-add SYS_ADMIN alpine:3.22
refuses create --cap-add ALL alpine:3.22
refuses create --device /dev/null:/dev/host-device alpine:3.22
refuses create --security-opt seccomp=unconfined alpine:3.22
refuses exec --privileged shipwick-caddy-1 true
refuses volume create -o type=none -o o=bind -o device=/ host-root
refuses swarm init
refuses plugin ls

say "a request whose body the plugin cannot read"
# The daemon takes a body sent as `application/json; charset=utf-8`; the
# plugin decodes `application/json` and no other spelling, and hands the
# policy no body. The policy refuses what it would judge by a body it has not
# been given.
in_daemon apk add -q curl
code=$(in_daemon curl -s -o /dev/null -w '%{http_code}' --unix-socket /var/run/docker.sock \
    -H 'Content-Type: application/json; charset=utf-8' \
    -d '{"Image":"alpine:3.22","HostConfig":{"Privileged":true}}' http://localhost/containers/create)
[ "$code" = 403 ] || die "a create whose body the plugin cannot read was answered $code"
echo "refused: 403"

say "what it does not refuse"
for flags in "--cap-add CHOWN" "-v named:/data" "-v /var/run/docker.sock:/var/run/docker.sock"; do
    # shellcheck disable=SC2086 # several flags in one entry
    id=$(in_daemon docker create $flags alpine:3.22) || die "docker create $flags was refused: the policy allows it"
    in_daemon docker rm "$id" >/dev/null
    echo "accepted: docker create $flags"
done
in_daemon docker volume rm named >/dev/null

printf '\nShipwick works behind the authorization plugin.\n'
