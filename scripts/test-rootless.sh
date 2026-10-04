#!/bin/sh
# Runs Shipwick on a rootless Docker daemon of its own and puts it through
# everything an installation does (test-cycle.sh). It passes when
#
#   - the proxy cannot take port 80 until the server lets ordinary users have
#     it, and Docker says which setting that is,
#   - every operation works: deployments, the two networks and the names on
#     them, a published port, volumes, backups, an export,
#   - the agent says that the daemon is rootless, and — on a daemon without
#     cgroups, which is what this script starts — that limits are not
#     enforced: on the deployment, in the server's status and in doctor.
#
#   sh scripts/test-rootless.sh                  # the agent holds the daemon's socket
#   sh scripts/test-rootless.sh --socket-proxy   # behind the socket proxy as well
#
# The daemon is docker:dind-rootless: rootless Docker without systemd, and so
# without cgroups. Limits that are enforced need a server with systemd and
# delegated controllers, which a container of this kind is not; see
# docs/handbook.md, "Less than the whole socket", for how that was tested.
#
# Needs Docker, and Go to build the CLI. Variables: scripts/test-lib.sh.
set -eu

overlays="compose.rootless.yml"
case "${1:-}" in
    "") ;;
    --socket-proxy) overlays="compose.socket-proxy.yml" ;;
    *) echo "usage: test-rootless.sh [--socket-proxy]" >&2; exit 2 ;;
esac

# shellcheck source=scripts/test-lib.sh
. "$(dirname "$0")/test-lib.sh"

prepare shipwick-test-rootless "$overlays"
# The image's user is 1000, and its daemon listens in that user's runtime directory.
start_daemon docker:dind-rootless /run/user/1000/docker.sock
install

case "$(in_daemon docker info --format '{{.SecurityOptions}}')" in
    *name=rootless*) ;;
    *) die "the daemon is not rootless" ;;
esac

say "ports 80 and 443 while they belong to root"
as_root sysctl -q -w net.ipv4.ip_unprivileged_port_start=1024
if out=$(compose up -d --quiet-pull 2>&1); then
    die "the proxy took port 80 as an ordinary user"
fi
case "$out" in
    *"cannot expose privileged port 80"*ip_unprivileged_port_start*) echo "refused, and Docker names the setting: net.ipv4.ip_unprivileged_port_start" ;;
    *) die "the stack did not start, but not for the port: $out" ;;
esac

say "starting Shipwick with the ports open to ordinary users"
as_root sysctl -q -w net.ipv4.ip_unprivileged_port_start=80
# The refused start left the proxy's container on one of its two networks,
# and a plain `up` would start it like that: it has to be made again.
compose up -d --quiet-pull --force-recreate
await_agent

cycle

say "what the agent says about the daemon"
status=$(shipwick server status)
echo "$status" | grep '^Docker'
echo "$status" | grep -q '^Docker .*rootless' || die "shipwick server status does not say that Docker is rootless"

if [ "$(in_daemon docker info --format '{{.MemoryLimit}}')" = true ]; then
    echo "$status" | grep -q 'not enforced' && die "the daemon enforces limits and the agent says it does not"
    echo "this daemon enforces limits"
else
    echo "$status" | grep -q '^Docker .*memory and CPU limits are not enforced' || die "the daemon enforces no limits and shipwick server status does not say so"
    out=$(shipwick deploy -f /test/testdata/web/deploy.yaml 2>&1) || die "deploy: $out"
    echo "$out" | grep 'does not enforce resources.memory and resources.cpu' || die "the deployment does not say that its limits are not enforced: $out"
    shipwick status web | grep '^Limits' | grep 'does not enforce the memory and CPU limits' || die "shipwick status does not say that the limits are not enforced"
    shipwick doctor 2>&1 | grep 'rootless and does not enforce memory and CPU limits' || die "shipwick doctor does not say that limits are not enforced"
    shipwick delete web --yes
fi

printf '\nShipwick works on rootless Docker.\n'
