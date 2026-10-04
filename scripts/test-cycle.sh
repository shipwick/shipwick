#!/bin/sh
# What an installation is asked to do, once through: deploy, roll out, roll
# back, run a command and a job, back up and restore a volume, serve a
# folder, take an image built here, export and import, delete.
#
# test-socket-proxy.sh and test-rootless.sh run it inside the container of
# the daemon they started; it is not meant to be run by hand. It expects the
# agent's API on 127.0.0.1:9000, the proxy on 443, `shipwick` and `docker` on
# the PATH, and the fixtures of scripts/testdata next to it. Everything it
# does goes through the agent, so every Engine API call the agent makes for
# these operations is made.
#
#   sh test-cycle.sh <token>
set -eu

token="${1:?usage: test-cycle.sh <token>}"
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)

# The three together: a saved context of whoever runs this is never read.
export SHIPWICK_CONFIG="$work/config.yaml"
export SHIPWICK_AGENT_URL="http://127.0.0.1:9000"
export SHIPWICK_AGENT_TOKEN="$token"
export SHIPWICK_EXPORT_PASSPHRASE="a passphrase for the test only"

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAILED: %s\n' "$*" >&2; exit 1; }

# fetch <url>: through the proxy, whose certificates for *.localhost come from
# its own authority.
fetch() { wget -q -T 10 -O - --no-check-certificate "$1"; }

# served <url> <text>: the proxy answers with text within half a minute. A
# route exists a moment after the deployment that made it is done.
served() {
    n=0
    until fetch "$1" 2>/dev/null | grep -q "$2"; do
        n=$((n + 1))
        [ "$n" -lt 30 ] || fail "$1 does not answer with \"$2\""
        sleep 1
    done
    echo "$1 answers"
}

# contains <text> <command...>: the command's output has the text in it.
contains() {
    text="$1"; shift
    out=$("$@" 2>&1) || fail "$* exited $?: $out"
    printf '%s\n' "$out"
    printf '%s\n' "$out" | grep -q "$text" || fail "$*: no \"$text\" in the output"
}

step "the server"
shipwick server status

step "deploy: two replicas with limits, behind the proxy"
shipwick deploy -f "$here/testdata/web/deploy.yaml"
served https://hello.localhost/ Hostname
contains "2/2" shipwick status web

step "roll out another image, then roll back"
shipwick redeploy web --image traefik/whoami:v1.10
contains "v1.10" shipwick status web
shipwick rollback web
contains "v1.11" shipwick status web
served https://hello.localhost/ Hostname
contains "Starting up" shipwick logs web

step "stop and start"
shipwick stop web
shipwick start web
served https://hello.localhost/ Hostname

step "deploy: a volume, a published port, a hook before the replicas"
shipwick deploy -f "$here/testdata/data/deploy.yaml"
[ "$(nc -w 5 127.0.0.1 15000 </dev/null)" = "hello from data" ] || fail "the published port 15000 does not answer"
echo "127.0.0.1:15000 answers"
contains "files" shipwick volumes

step "a command and a job"
contains "one-off" shipwick run data -- echo one-off
shipwick jobs run data tick
contains "tick" shipwick jobs logs data tick

step "back up, verify, download, restore"
shipwick backups run data
shipwick backups verify data
shipwick backups data
mkdir "$work/archives"
shipwick backup data -o "$work/archives"
shipwick stop data
shipwick restore data "$work"/archives/data-files-*.tar --yes
shipwick start data
contains "dumped" shipwick run data -- sh -c "echo the volume is not mounted here; echo dumped"

step "deploy: a folder served by the proxy"
shipwick deploy -f "$here/testdata/site/deploy.yaml"
served https://site.localhost/ "served by the proxy itself"

step "deploy: an image built here and sent through the agent"
shipwick deploy -f "$here/testdata/built/deploy.yaml"
contains "built" shipwick ps

step "export, delete, import"
shipwick export -o "$work/server.swexport"
shipwick delete built --yes
shipwick delete data --yes
shipwick volumes rm shipwick_data_files --yes
shipwick import "$work/server.swexport"
contains "built" shipwick ps
contains "data" shipwick ps
[ "$(nc -w 5 127.0.0.1 15000 </dev/null)" = "hello from data" ] || fail "the published port 15000 does not answer after the import"

step "doctor"
shipwick doctor || true

step "delete everything"
for app in web data site built; do
    shipwick delete "$app" --yes
done
shipwick volumes rm shipwick_data_files --yes
contains "No applications" shipwick ps

rm -r "$work"
printf '\nThe cycle passed.\n'
