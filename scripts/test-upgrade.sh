#!/bin/sh
# Walks an upgrade: a released version, installed the way its users installed
# it and put to use, is upgraded to this checkout, and everything it carried
# is looked at afterwards.
#
#   sh scripts/test-upgrade.sh v0.7.0          # from v0.7.0 to the checkout
#   sh scripts/test-upgrade.sh --back v0.7.0   # and back to v0.7.0 with the installer
#
# The server is a container with a Docker daemon of its own (docker:dind),
# made here and removed at the end; nothing on this machine's daemon is
# touched except the three images built from the checkout. It needs Docker,
# Go, and a way to github.com, ghcr.io and quay.io.
#
# What is put on the old version, as far as that version has it:
#   web   two replicas behind web.localhost, a health check, an env value from
#         a secret stored on the server (0.4), a second deployment and a
#         rollback
#   db    etcd behind db.localhost on a volume, deploy.strategy: recreate, a
#         key written through it (0.2), a scheduled job that ran (0.3), a
#         backup the server keeps (0.5)
#   a token with the deploy role (0.3)
#
# Requests are sent to web.localhost ten times a second while the installer
# runs; how many went unanswered, and for how long, is printed and not judged:
# the upgrade replaces the proxy's container.

set -eu

REPO="${SHIPWICK_REPO:-shipwick/shipwick}"
SERVER="${SHIPWICK_TEST_SERVER:-shipwick-upgrade-test}"
# Kept for looking around after a failure:  SHIPWICK_TEST_KEEP=1
KEEP="${SHIPWICK_TEST_KEEP:-}"
# The tag of the images built from the checkout: no release has it.
TAG="$SERVER"
AGENT_URL="http://127.0.0.1:9000"

WEB_IMAGE="ghcr.io/traefik/whoami"
WEB_FIRST="v1.10.1"
WEB_SECOND="v1.10.2"
DB_IMAGE="quay.io/coreos/etcd:v3.5.21"
SECRET_VALUE="kept-across-the-upgrade"
# "upgrade" and "walked", as etcd's JSON API wants them.
DB_KEY="dXBncmFkZQ=="
DB_VALUE="d2Fsa2Vk"

BACK=""
RELEASE=""
for arg in "$@"; do
    case "$arg" in
        --back) BACK=1 ;;
        v[0-9]*) RELEASE="$arg" ;;
        *) echo "usage: sh scripts/test-upgrade.sh [--back] <release, such as v0.7.0>" >&2; exit 2 ;;
    esac
done
[ -n "$RELEASE" ] || { echo "usage: sh scripts/test-upgrade.sh [--back] <release, such as v0.7.0>" >&2; exit 2; }

cd "$(dirname "$0")/.."
WORK="$(mktemp -d)"
FAILED=0
# Set while doctor runs: see there.
CA_FILE=""

step() { printf '%s\n' "✓ $*"; }
fail() { printf '%s\n' "✗ $*"; FAILED=$((FAILED + 1)); }
say()  { printf '\n%s\n' "== $*"; }
die()  { printf '%s\n' "✗ $*" >&2; exit 1; }

cleanup() {
    rm -rf "$WORK"
    if [ -n "$KEEP" ]; then
        echo "The server was kept: docker exec -it $SERVER sh; remove it with: docker rm -fv $SERVER"
    else
        docker rm -fv "$SERVER" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

# --- helpers ----------------------------------------------------------------

# since VERSION — the release under test is that version or a later one.
since() { [ "$(ordinal "$RELEASE")" -ge "$(ordinal "$1")" ]; }
ordinal() { echo "${1#v}" | awk -F. '{ print $1 * 10000 + $2 * 100 + $3 }'; }

# on_server COMMAND... — as root on the server.
on_server() { docker exec -i "$SERVER" "$@"; }

# put FILE — standard input becomes FILE on the server.
# shellcheck disable=SC2016 # expanded by the shell on the server
put() { on_server sh -c 'cat > "$1"' sh "$1"; }

# old ARGS, new ARGS — the release's CLI and the checkout's, on the server,
# with the token in TOKEN. The file SHIPWICK_CONFIG names does not exist:
# neither reads a saved context.
old() { shipwick old "$@"; }
new() { shipwick new "$@"; }
shipwick() {
    binary="/root/shipwick-$1"; shift
    docker exec -i -w /root/apps \
        -e SHIPWICK_CONFIG=/root/no-contexts.yaml -e "SHIPWICK_AGENT_URL=$AGENT_URL" -e "SHIPWICK_AGENT_TOKEN=$TOKEN" \
        -e "SHIPWICK_CA_FILE=$CA_FILE" "$SERVER" "$binary" "$@"
}

# check "what is expected" COMMAND... — a failure is counted and shown, and the
# checks after it still run.
check() {
    expected="$1"; shift
    if "$@" > "$WORK/check.out" 2>&1; then
        step "$expected"
    else
        fail "$expected"
        sed 's/^/    /' "$WORK/check.out"
    fi
}

# eventually COMMAND... — for what takes a moment: a first certificate, a
# proxy that was just replaced, an agent that offers the proxy its
# configuration again a minute after the proxy refused it.
eventually() {
    tries=0
    until "$@"; do
        tries=$((tries + 1))
        [ "$tries" -lt 90 ] || return 1
        sleep 1
    done
}

# answers URL — HTTP 200 through the proxy.
answers() { [ "$(on_server curl -sk -o /dev/null -m 10 -w '%{http_code}' "$1")" = 200 ]; }

agent_version() { on_server curl -fsS "$AGENT_URL/api/v1/health" | sed 's/.*"version":"\([^"]*\)".*/\1/'; }
agent_is() { [ "$(agent_version)" = "$1" ]; }
replicas() { on_server docker ps -q --no-trunc --filter label=com.shipwick.replica | sort; }
replica_of() { on_server docker ps -q --filter "label=com.shipwick.app=$1" --filter label=com.shipwick.replica | head -n 1; }
deployments_of() {
    on_server curl -fsS -H "Authorization: Bearer $TOKEN" "$AGENT_URL/api/v1/deployments?application=$1" \
        | grep -o '"completed_at"' | wc -l | tr -d ' '
}

# What the proxy runs is what it saved last.
proxy_config() { on_server sh -c 'cd /opt/shipwick && docker compose exec -T caddy cat /config/caddy/autosave.json'; }
proxy_has_kept_source() { proxy_config | grep -q '"source":"shipwick"'; }
proxy_has_plain_source() {
    proxy_config > "$WORK/proxy.json" && grep -q '"source":"a"' "$WORK/proxy.json" && ! grep -q '"source":"shipwick"' "$WORK/proxy.json"
}

stored_value() {
    on_server curl -sk -m 10 https://db.localhost/v3/kv/range -d "{\"key\":\"$DB_KEY\"}" | grep -q "\"value\":\"$DB_VALUE\""
}
secret_in_replica() {
    on_server docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$(replica_of web)" | grep -qx "GREETING=$1"
}
web_runs() {
    answers https://web.localhost/ \
        && on_server docker inspect --format '{{.Config.Image}}' "$(replica_of web)" | grep -qx "$WEB_IMAGE:$1"
}
job_runs() { new jobs db | grep -q version && new jobs run db version; }
backup_listed() { new backups db | grep -q succeeded; }
refused() { ! "$@"; }

# doctor asks every hostname for an answer the way a browser would. The
# proxy's own certificate authority signs *.localhost; the CLI is told to
# trust it, as it trusts Let's Encrypt on a real name.
doctor() {
    on_server sh -c 'cd /opt/shipwick && docker compose exec -T caddy cat /data/caddy/pki/authorities/local/root.crt' \
        | put /root/proxy-root.crt
    CA_FILE=/root/proxy-root.crt
    result=0
    new doctor || result=$?
    CA_FILE=""
    return "$result"
}

# --- the server ---------------------------------------------------------------

start_server() {
    say "A server"
    docker rm -fv "$SERVER" >/dev/null 2>&1 || true
    docker run -d --privileged --name "$SERVER" -e DOCKER_TLS_CERTDIR= docker:dind >/dev/null
    eventually on_server docker info >/dev/null 2>&1 || die "The Docker daemon in $SERVER did not start."
    on_server apk add --quiet --no-cache curl
    # What DNS records are on a real server.
    echo "127.0.0.1 web.localhost db.localhost" | on_server sh -c 'cat >> /etc/hosts'
    on_server mkdir -p /root/apps /opt/shipwick
    # The installer leaves this file alone. The API on the server's loopback is
    # the handbook's way to reach it without a hostname. The two names are for
    # 0.3.0 alone, whose agent asks its own resolver whether a hostname has a
    # record before it serves it; the releases after it ask public resolvers,
    # which answer for *.localhost.
    put /opt/shipwick/compose.override.yml <<'EOF'
services:
  agent:
    ports: ["127.0.0.1:9000:9000"]
    extra_hosts: ["web.localhost:127.0.0.1", "db.localhost:127.0.0.1"]
EOF
    step "Docker $(on_server docker version --format '{{.Server.Version}}') in the container $SERVER"
}

# The installer of the release itself, as its tag has it.
install_release() {
    say "Shipwick $RELEASE, installed as released"
    on_server curl -fsSL -o /root/install-release.sh "https://raw.githubusercontent.com/$REPO/$RELEASE/scripts/install.sh"
    on_server env "SHIPWICK_VERSION=$RELEASE" sh /root/install-release.sh > "$WORK/install.out" 2>&1 \
        || { cat "$WORK/install.out"; die "The installer of $RELEASE failed."; }
    grep '✓' "$WORK/install.out" | sed 's/^/  /'
    TOKEN="$(on_server sed -n 's/^SHIPWICK_AGENT_TOKEN=//p' /opt/shipwick/.env)"
    ADMIN_TOKEN="$TOKEN"
    on_server cp /usr/local/bin/shipwick /root/shipwick-old
    eventually on_server curl -fsS -o /dev/null "$AGENT_URL/api/v1/health" || die "The agent of $RELEASE does not answer on $AGENT_URL."
    agent_is "$RELEASE" || die "The agent says it is $(agent_version), not $RELEASE."
    step "The agent and the CLI are $RELEASE: $(on_server sh -c 'cd /opt/shipwick && docker compose config --images' | tr '\n' ' ')"
}

web_yaml() { # web_yaml TAG
    cat <<EOF
name: web
image: $WEB_IMAGE:$1
port: 80
domain: web.localhost
replicas: 2
health:
  path: /health
EOF
    if since v0.4.0; then
        printf '%s\n' 'env:' "  GREETING: \${GREETING}"
    fi
}

db_yaml() {
    cat <<EOF
name: db
image: $DB_IMAGE
port: 2379
domain: db.localhost
health:
  path: /health
env:
  ETCD_DATA_DIR: /data
  ETCD_LISTEN_CLIENT_URLS: http://0.0.0.0:2379
  ETCD_ADVERTISE_CLIENT_URLS: http://db:2379
volumes:
  - name: data
    path: /data
deploy:
  strategy: recreate
EOF
    if since v0.3.0; then
        cat <<EOF
jobs:
  - name: version
    schedule: "0 3 1 1 *"
    command: ["etcdctl", "version"]
EOF
    fi
}

# What a server carries after a while, made with the release's own CLI.
use_release() {
    say "An installation on $RELEASE"
    old server status > "$WORK/status.out" 2>&1 || { cat "$WORK/status.out"; die "The CLI of $RELEASE cannot reach its agent."; }

    if since v0.4.0; then
        printf '%s' "$SECRET_VALUE" | old secret set GREETING >/dev/null
        step "A secret, GREETING, stored on the server"
    fi

    web_yaml "$WEB_FIRST" | put /root/apps/web.yaml
    old deploy -f web.yaml >/dev/null
    web_yaml "$WEB_SECOND" | put /root/apps/web.yaml
    old deploy -f web.yaml >/dev/null
    old rollback web >/dev/null
    eventually answers https://web.localhost/ || die "web.localhost does not answer on $RELEASE."
    step "web: two replicas behind https://web.localhost, deployed twice and rolled back to $WEB_FIRST"

    if since v0.2.0; then
        db_yaml | put /root/apps/db.yaml
        old deploy -f db.yaml >/dev/null
        eventually answers https://db.localhost/health || die "db.localhost does not answer on $RELEASE."
        on_server curl -fsSk -o /dev/null https://db.localhost/v3/kv/put -d "{\"key\":\"$DB_KEY\",\"value\":\"$DB_VALUE\"}"
        stored_value || die "etcd did not keep the key."
        step "db: etcd on a volume, strategy recreate, with a key written through https://db.localhost"
    fi
    if since v0.3.0; then
        old jobs run db version >/dev/null
        step "db: the scheduled job \"version\" ran once"
        CI_TOKEN="$(old token create ci --role deploy | awk 'NF == 1 && length($1) >= 32 { print $1 }')"
        [ -n "$CI_TOKEN" ] || die "shipwick token create printed no token."
        step "A token, ci, with the deploy role"
    fi
    if since v0.5.0; then
        old backups run db >/dev/null
        step "db: a backup the server keeps"
    fi

    REPLICAS_BEFORE="$(replicas)"
    WEB_DEPLOYMENTS_BEFORE="$(deployments_of web)"
}

# --- the checkout -------------------------------------------------------------

# The checkout becomes what a release is to the installer: three images, a
# compose file that pins them (as build-release.sh does), and the CLI.
build_checkout() {
    say "The checkout"
    CHECKOUT_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    docker build --quiet --build-arg "VERSION=$CHECKOUT_VERSION" -t "ghcr.io/shipwick/agent:$TAG" . >/dev/null
    docker build --quiet -t "ghcr.io/shipwick/dashboard:$TAG" dashboard/ >/dev/null
    docker build --quiet -t "ghcr.io/shipwick/caddy:$TAG" -f Dockerfile.caddy . >/dev/null
    docker save "ghcr.io/shipwick/agent:$TAG" "ghcr.io/shipwick/dashboard:$TAG" "ghcr.io/shipwick/caddy:$TAG" \
        | on_server docker load --quiet >/dev/null
    sed "s|\(ghcr\.io/shipwick/[a-z]*\):latest|\1:$TAG|" configs/compose.production.yml > "$WORK/compose.production.yml"
    [ "$(grep -c "ghcr.io/shipwick/[a-z]*:$TAG" "$WORK/compose.production.yml")" -eq 3 ] \
        || die "Could not pin the three images in compose.production.yml."

    case "$(on_server uname -m)" in
        aarch64|arm64) arch=arm64 ;;
        *) arch=amd64 ;;
    esac
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags "-X github.com/shipwick/shipwick/pkg/version.Version=$CHECKOUT_VERSION" -o "$WORK/shipwick" ./cli/cmd/shipwick

    on_server mkdir -p /root/checkout
    docker cp --quiet "$WORK/shipwick" "$SERVER:/root/shipwick-new"
    docker cp --quiet "$WORK/compose.production.yml" "$SERVER:/root/checkout/compose.production.yml"
    docker cp --quiet scripts/install.sh "$SERVER:/root/checkout/install.sh"
    step "Built $CHECKOUT_VERSION: three images, loaded into the server's Docker, and the CLI"
}

start_requests() {
    put /root/requests.sh <<'EOF'
# One line per request: when it was sent, the status, the seconds it took.
# Every request is a connection of its own.
( while [ ! -e /root/requests.stop ]; do
      curl -sk -o /dev/null -m 10 -w "$(date +%s) %{http_code} %{time_total}\n" https://web.localhost/ &
      sleep 0.1
  done; wait ) > /root/requests.txt
touch /root/requests.done
EOF
    on_server rm -f /root/requests.stop /root/requests.done
    docker exec -d "$SERVER" sh /root/requests.sh
    sleep 3
}

# stop_requests LABEL — what the requests sent meanwhile were answered with.
stop_requests() {
    sleep 3
    on_server touch /root/requests.stop
    eventually on_server test -e /root/requests.done
    on_server cat /root/requests.txt | awk -v label="$1" '
        { total++; codes[$2]++ }
        $2 != 200 { failed++; if (!first) first = $1; last = $1 }
        $3 + 0 > slowest { slowest = $3 + 0 }
        END {
            printf "  %s: %d requests to https://web.localhost,", label, total
            for (code in codes) printf " %d×%s", codes[code], code
            if (failed) printf "; %d not answered with 200, within %d s", failed, last - first + 1
            printf "; the slowest took %.2f s\n", slowest
        }'
}

# The installer, given the checkout's compose file where it would download a
# release's.
upgrade() {
    say "The upgrade to the checkout"
    start_requests
    on_server env SHIPWICK_COMPOSE_FILE=/root/checkout/compose.production.yml sh /root/checkout/install.sh > "$WORK/upgrade.out" 2>&1 \
        || { cat "$WORK/upgrade.out"; die "The checkout's installer failed on an installation of $RELEASE."; }
    eventually answers https://web.localhost/ || true
    stop_requests "During the upgrade"
    grep -E '✓|!' "$WORK/upgrade.out" | sed 's/^/  /'
}

check_upgraded() {
    say "After the upgrade"
    TOKEN="$ADMIN_TOKEN"
    check "The agent is $CHECKOUT_VERSION and applied its migrations" agent_is "$CHECKOUT_VERSION"
    check "The token from the installer is still the admin's" new server status
    check "No replica was replaced: the containers are the ones $RELEASE started" test "$(replicas)" = "$REPLICAS_BEFORE"
    check "https://web.localhost answers 200" eventually answers https://web.localhost/
    check "The history of web is intact ($WEB_DEPLOYMENTS_BEFORE deployments)" test "$(deployments_of web)" = "$WEB_DEPLOYMENTS_BEFORE"
    check "The proxy runs the configuration of this version (\"source\":\"shipwick\")" eventually proxy_has_kept_source
    if since v0.2.0; then
        check "https://db.localhost answers 200" eventually answers https://db.localhost/health
        check "The key written before the upgrade is in the volume" stored_value
    fi

    check "The CLI of $RELEASE talks to the new agent: ps" old ps
    check "The CLI of $RELEASE redeploys web" old redeploy web
    if since v0.4.0; then
        check "The secret still resolves in the redeployed replicas" secret_in_replica "$SECRET_VALUE"
    fi
    web_yaml "$WEB_SECOND" | put /root/apps/web.yaml
    check "The new CLI deploys web $WEB_SECOND from the same deploy.yaml" new deploy -f web.yaml
    check "The new CLI rolls web back" new rollback web
    check "web runs $WEB_FIRST again and answers" web_runs "$WEB_FIRST"
    check "The history of web grew by the three deployments" test "$(deployments_of web)" = "$((WEB_DEPLOYMENTS_BEFORE + 3))"

    if since v0.2.0; then
        check "The new CLI redeploys db, which is stopped and started on its volume" new redeploy db
        check "The key is still there" eventually stored_value
    fi
    if since v0.3.0; then
        check "The job of db is listed and runs" job_runs
        check "A one-off command runs in db's image" new run db -- etcdctl version
        TOKEN="$CI_TOKEN"
        check "The deploy token made on $RELEASE is accepted, from the new CLI" new ps
        check "and from the CLI of $RELEASE" old ps
        check "It still may not do what an admin does" refused new token ls
        TOKEN="$ADMIN_TOKEN"
    fi
    if since v0.5.0; then
        check "The backup taken on $RELEASE is listed" backup_listed
        check "It restores: shipwick backups verify" new backups verify db
    fi
    check "shipwick doctor finds no problems" doctor
}

# --- and back -----------------------------------------------------------------

# Back to the release, with the installer of the checkout: the one
# get.shipwick.com serves once the checkout is released. How it ends depends
# on the database. A checkout that added nothing to it can be left, and the
# release is checked again; one that added a migration cannot, because the
# release's agent refuses the database, and the installer has to say so and
# start the checkout again.
go_back() {
    say "Back to $RELEASE"
    check "The proxy's saved configuration names what the proxy of $RELEASE does not have" proxy_has_kept_source
    start_requests
    went_back=1
    on_server env "SHIPWICK_VERSION=$RELEASE" sh /root/checkout/install.sh > "$WORK/back.out" 2>&1 || went_back=""
    eventually answers https://web.localhost/ || true
    stop_requests "On the way back"
    grep -E '✓|!|✗' "$WORK/back.out" | sed 's/^/  /'

    TOKEN="$ADMIN_TOKEN"
    if [ -z "$went_back" ]; then
        check "The installer said that the agent of $RELEASE cannot read the database" grep -q "cannot read the database" "$WORK/back.out"
        check "and started the checkout again: the agent is $CHECKOUT_VERSION" eventually agent_is "$CHECKOUT_VERSION"
        check "The proxy runs the configuration of this version again" eventually proxy_has_kept_source
    else
        check "The installer removed the saved configuration the older proxy cannot read" grep -q "saved configuration was written by a newer release" "$WORK/back.out"
        check "The agent is $RELEASE again" eventually agent_is "$RELEASE"
        check "The proxy of $RELEASE runs, with the configuration of $RELEASE" eventually proxy_has_plain_source
    fi
    check "https://web.localhost answers 200" eventually answers https://web.localhost/
    check "The CLI of $RELEASE redeploys web" old redeploy web
    if since v0.4.0; then
        check "The secret resolves" secret_in_replica "$SECRET_VALUE"
    fi
    if since v0.2.0; then
        check "https://db.localhost answers 200" eventually answers https://db.localhost/health
        check "The key is in the volume" stored_value
    fi
}

# --- entry point ----------------------------------------------------------------

start_server
install_release
use_release
build_checkout
upgrade
check_upgraded
if [ -n "$BACK" ]; then
    go_back
fi

printf '\n'
if [ "$FAILED" -gt 0 ]; then
    die "$RELEASE → $CHECKOUT_VERSION: $FAILED of the checks failed."
fi
step "$RELEASE → $CHECKOUT_VERSION: every check passed."
