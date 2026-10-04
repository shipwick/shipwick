#!/bin/sh
# Measures what one server carries: the numbers of the handbook's "What one
# server carries" (§10), produced the way they were produced for it.
#
#   sh scripts/measure-limits.sh                          # 2 CPUs, 4 GB, everything
#   sh scripts/measure-limits.sh --cpus 4 --memory 8g     # a larger server
#   sh scripts/measure-limits.sh --keep apps database     # some of it, and keep the server
#   sh scripts/measure-limits.sh --reuse requests         # on the server a run kept
#
# The server is a container with a Docker daemon of its own (docker:dind),
# given the first --cpus processors of this machine and no others (a cpuset,
# not a quota: a quota lets a container run on every processor for a slice of
# each tenth of a second and stops it for the rest, which a machine with two
# processors does not do) and --memory without swap, with Shipwick installed
# in it from this checkout by scripts/install.sh. It is not a rented machine:
# the processor is this machine's, the disk is this machine's, and nothing
# crosses a network. The load generator (oha) is a container next to the
# server, on a network the two share, on the processors the server does not
# have.
#
# The applications are served at *.localhost, which the public resolvers the
# agent asks by default do not all answer. A resolver on the server answers
# for them (dnsmasq, SHIPWICK_DNS_RESOLVERS), so that asking whether a
# hostname points at the server costs one question, as it does for a real
# name; it is answered faster than a resolver across a network answers.
#
# What is measured; each part prints what it did. Without arguments the order
# is requests, restore, apps, database: the first two on a server that
# carries nothing else, the last on the applications of the third.
#
#   apps      Applications of two replicas each (traefik/whoami), in growing
#             number. At every step: the memory of the agent, the proxy and
#             the dashboard, and of the whole server; the CPU the server and
#             the agent use while nothing is asked of them; how long
#             `shipwick ps`, `shipwick status <app>`, GET /applications and
#             GET /server take; one rolling deployment of one application;
#             and how long a killed replica stays away. Stops growing when a
#             deployment fails or nine tenths of the memory are in use.
#   database  What a row of each kind takes in shipwick.db, read from SQLite's
#             own account of its pages (dbstat) after a known number of
#             deployments and minutes of traffic, and from that a year of a
#             stated workload.
#   requests  Requests per second and latency through the proxy over HTTPS,
#             to one application of one and of four replicas, on kept
#             connections and on a new connection per request; then the same
#             at a fixed rate while the application is deployed.
#   restore   A volume of each size of SHIPWICK_LIMITS_VOLUMES: backup,
#             verification and restore, without and with encryption; and a
#             backup and restore of the agent's own state.
#
# A whole run takes about three quarters of an hour at 2 CPUs and 4 GB, most
# of it "apps" (deployments, and a minute and a half of watching at every
# step). It needs Docker with privileged
# containers, Go, and a way to ghcr.io; about 25 GB of disk for the largest
# volume and its copies. Nothing else on this machine's Docker is touched
# apart from three images tagged with the server's name; the server and its
# network are removed at the end unless --keep is given.
#
#   SHIPWICK_LIMITS_SERVER    the container's name (shipwick-limits)
#   SHIPWICK_LIMITS_FIRST_CPU the first processor the server is given (0)
#   SHIPWICK_LIMITS_APPS      the steps of "apps" ("10 25 50 100 150 200 250 300")
#   SHIPWICK_LIMITS_VOLUMES   the volume sizes of "restore", in MB ("100 1000 5000")

set -eu

SERVER="${SHIPWICK_LIMITS_SERVER:-shipwick-limits}"
STEPS="${SHIPWICK_LIMITS_APPS:-10 25 50 100 150 200 250 300}"
VOLUMES="${SHIPWICK_LIMITS_VOLUMES:-100 1000 5000}"
FIRST_CPU="${SHIPWICK_LIMITS_FIRST_CPU:-0}"
NETWORK="$SERVER-net"
# The tag of the images built from the checkout: no release has it.
TAG="$SERVER"
LOAD_IMAGE="ghcr.io/hatoo/oha:1.16.0"
APP_IMAGE="ghcr.io/traefik/whoami:v1.10.2"
FILL_IMAGE="busybox:1.37"
PASSPHRASE="measured-not-kept-anywhere"
DATABASE="/var/lib/docker/volumes/shipwick_agent-data/_data/shipwick.db"
# How many deployments "database" makes, and for how many minutes it sends
# every application a request.
DEPLOYMENTS=20
MINUTES=5

CPUS=2
MEMORY=4g
KEEP=""
REUSE=""
PARTS=""

usage() {
    echo "usage: sh scripts/measure-limits.sh [--cpus N] [--memory SIZE] [--keep] [--reuse] [apps] [database] [requests] [restore]" >&2
    exit 2
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --cpus) [ "$#" -ge 2 ] || usage; CPUS="$2"; shift ;;
        --memory) [ "$#" -ge 2 ] || usage; MEMORY="$2"; shift ;;
        --keep) KEEP=1 ;;
        --reuse) REUSE=1; KEEP=1 ;;
        apps|database|requests|restore) PARTS="$PARTS $1" ;;
        *) usage ;;
    esac
    shift
done
# The proxy and the backups first, on a server that carries nothing else.
[ -n "$PARTS" ] || PARTS="requests restore apps database"

# Git Bash would turn /root into a Windows path on its way to docker.
export MSYS_NO_PATHCONV=1

cd "$(dirname "$0")/.."
WORK="$(mktemp -d)"
# Under Git Bash docker is a Windows program and takes the directory by its
# Windows name.
if command -v cygpath >/dev/null 2>&1; then
    WORK="$(cygpath -m "$WORK")"
fi

say()  { printf '\n== %s\n' "$*"; }
note() { printf '%s\n' "$*"; }
die()  { printf 'FAILED: %s\n' "$*" >&2; exit 1; }

cleanup() {
    rm -f "$WORK/shipwick" "$WORK/compose.production.yml" "$WORK/load.json"
    rmdir "$WORK" 2>/dev/null || true
    if [ -n "$KEEP" ]; then
        printf '\nThe server was kept: docker exec -it %s sh; remove it with: docker rm -fv %s && docker network rm %s\n' "$SERVER" "$SERVER" "$NETWORK"
    else
        docker rm -fv "$SERVER" >/dev/null 2>&1 || true
        docker network rm "$NETWORK" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# --- helpers ------------------------------------------------------------------

# on_server COMMAND... — as root on the server.
on_server() { docker exec -i "$SERVER" "$@"; }

# put FILE — standard input becomes FILE on the server, executable.
# shellcheck disable=SC2016 # expanded by the shell on the server
put() { on_server sh -c 'cat > "$1" && chmod 0755 "$1"' sh "$1"; }

# sw ARGS — the checkout's CLI on the server, against the agent there.
sw() { on_server /root/sw "$@"; }

# timed N COMMAND... — the median of N runs on the server, in milliseconds.
timed() { on_server /root/timed "$@"; }

# seconds MILLISECONDS — for a table.
seconds() { awk -v ms="$1" 'BEGIN { printf "%.1f", ms / 1000 }'; }

eventually() {
    tries=0
    until "$@"; do
        tries=$((tries + 1))
        [ "$tries" -lt 120 ] || return 1
        sleep 1
    done
}

# The server's address on the network it shares with the load generator.
server_address() {
    SERVER_ADDRESS="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NETWORK\").IPAddress}}" "$SERVER")"
}

agent_answers() { on_server curl -fsS -o /dev/null http://127.0.0.1:9000/api/v1/health 2>/dev/null; }

# What runs on the server itself: a clock read from outside would time
# `docker exec` as well.
server_tools() {
    put /root/sw <<'EOF'
#!/bin/sh
# The checkout's CLI against this server's agent. The file SHIPWICK_CONFIG
# names does not exist: no saved context is read.
SHIPWICK_AGENT_TOKEN="$(sed -n 's/^SHIPWICK_AGENT_TOKEN=//p' /opt/shipwick/.env)"
export SHIPWICK_AGENT_TOKEN SHIPWICK_CONFIG=/root/no-contexts.yaml SHIPWICK_AGENT_URL=http://127.0.0.1:9000
exec /root/shipwick "$@"
EOF
    put /root/api <<'EOF'
#!/bin/sh
# api PATH — one authenticated GET.
token="$(sed -n 's/^SHIPWICK_AGENT_TOKEN=//p' /opt/shipwick/.env)"
exec curl -fsS -o /dev/null -H "Authorization: Bearer $token" "http://127.0.0.1:9000$1"
EOF
    put /root/timed <<'EOF'
#!/bin/sh
# timed N COMMAND... — runs COMMAND N times and prints the median of the
# times it took, in milliseconds. A run that fails says so on standard error.
runs="$1"; shift
i=0
while [ "$i" -lt "$runs" ]; do
    start="$(date +%s%N)"
    "$@" > /root/timed.out 2>&1 || { echo "failed: $*" >&2; sed 's/^/    /' /root/timed.out >&2; }
    end="$(date +%s%N)"
    echo $(( (end - start) / 1000000 ))
    i=$((i + 1))
done | sort -n | awk '{ run[NR] = $1 } END { print run[int((NR + 1) / 2)] }'
EOF
    put /root/usage <<'EOF'
#!/bin/sh
# usage SECONDS — over that long: the CPU of the whole server and of the
# agent's container, in percent of one core, from the cgroups' own counters.
cgroup_of() { echo "/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$(docker inspect -f '{{.State.Pid}}' "$1")/cgroup")"; }
used() { sed -n 's/^usage_usec //p' "$1/cpu.stat"; }
agent="$(cgroup_of shipwick-agent-1)"
server_before="$(used /sys/fs/cgroup)"; agent_before="$(used "$agent")"
sleep "$1"
server_after="$(used /sys/fs/cgroup)"; agent_after="$(used "$agent")"
awk -v s="$((server_after - server_before))" -v a="$((agent_after - agent_before))" -v t="$1" \
    'BEGIN { printf "%.0f %.0f\n", s / t / 10000, a / t / 10000 }'
EOF
    put /root/memory <<'EOF'
#!/bin/sh
# memory [CONTAINER] — megabytes in use, as `docker stats` counts them: what
# the cgroup holds, less the page cache the kernel would give back. Of the
# whole server without an argument.
dir=/sys/fs/cgroup
if [ -n "${1:-}" ]; then
    dir="/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$(docker inspect -f '{{.State.Pid}}' "$1")/cgroup")"
fi
awk -v current="$(cat "$dir/memory.current")" '$1 == "inactive_file" { printf "%.0f\n", (current - $2) / 1048576 }' "$dir/memory.stat"
EOF
    put /root/deploy-apps <<EOF
#!/bin/sh
# deploy-apps FIRST LAST — the applications a<FIRST> to a<LAST>, four at a
# time, each waited for. Prints the names of those that failed.
i="\$1"
while [ "\$i" -le "\$2" ]; do
    batch=0
    while [ "\$batch" -lt 4 ] && [ "\$i" -le "\$2" ]; do
        name="\$(printf 'a%03d' "\$i")"
        printf '%s\n' "name: \$name" "image: $APP_IMAGE" "port: 80" "domain: \$name.localhost" "replicas: 2" "health:" "  path: /health" > "/root/apps/\$name.yaml"
        ( /root/sw deploy -f "/root/apps/\$name.yaml" > "/root/apps/\$name.out" 2>&1 || echo "\$name" ) &
        batch=\$((batch + 1)); i=\$((i + 1))
    done
    wait
done
EOF
    put /root/list <<'EOF'
#!/bin/sh
# list [APP] — what the supervisor asks Docker every second: the containers
# Shipwick manages, stopped ones included; with APP, those of one application.
filters='%22com.shipwick.managed%3Dtrue%22'
[ -z "${1:-}" ] || filters="$filters%2C%22com.shipwick.app%3D$1%22"
exec curl -fsS -o /dev/null --unix-socket /var/run/docker.sock "http://docker/containers/json?all=1&filters=%7B%22label%22%3A%5B$filters%5D%7D"
EOF
    put /root/overflows <<'EOF'
#!/bin/sh
# overflows SECONDS — how often, since the kernel had been up that long, it
# said that its table of neighbours (the ARP cache) was full. The kernel says
# so at most a few times a second.
dmesg | awk -v since="$1" '/neighbor table overflow/ { gsub(/[][]/, "", $1); if ($1 + 0 >= since) n++ } END { print n + 0 }'
EOF
    put /root/recover <<'EOF'
#!/bin/sh
# recover APP — kills one replica of a two-replica application and prints the
# milliseconds until two are running again.
running() { docker ps -q --filter "label=com.shipwick.app=$1" --filter label=com.shipwick.replica; }
start="$(date +%s%N)"
docker kill "$(running "$1" | head -n 1)" > /dev/null
until [ "$(running "$1" | wc -l)" -ge 2 ]; do
    [ $(( ($(date +%s%N) - start) / 1000000000 )) -lt 120 ] || { echo "the replica of $1 did not come back in two minutes" >&2; exit 1; }
    sleep 0.05
done
echo $(( ($(date +%s%N) - start) / 1000000 ))
EOF
    put /root/visit <<'EOF'
#!/bin/sh
# visit COUNT MINUTES — one request to every application a001…a<COUNT>, once
# a minute: a minute with traffic is a row in the agent's database. Prints
# how many of the requests were answered with 200.
minute=0
answered=0
while [ "$minute" -lt "$2" ]; do
    began="$(date +%s)"
    i=1
    while [ "$i" -le "$1" ]; do
        [ "$(curl -sk -o /dev/null -m 10 -w '%{http_code}' "https://$(printf 'a%03d' "$i").localhost/")" != 200 ] || answered=$((answered + 1))
        i=$((i + 1))
    done
    minute=$((minute + 1))
    left=$((60 - $(date +%s) + began))
    [ "$minute" -ge "$2" ] || [ "$left" -le 0 ] || sleep "$left"
done
echo "$answered"
EOF
    put /root/tables <<EOF
#!/bin/sh
# tables — every table of the agent's database: its rows, and the bytes of
# the pages it and its indexes take, as SQLite's dbstat counts them. The
# events of deployments, which are kept for as long as the deployments, are
# counted again on a line of their own, at the size of an average event.
db() { sqlite3 -readonly "$DATABASE" "\$1"; }
for table in \$(db "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); do
    echo "\$table \$(db "SELECT count(*) FROM \$table") \$(db "SELECT sum(pgsize) FROM dbstat WHERE name IN (SELECT name FROM sqlite_master WHERE tbl_name = '\$table')")"
done
db "SELECT 'deployment_events', count(deployment_id), count(deployment_id) * (SELECT sum(pgsize) FROM dbstat WHERE name IN (SELECT name FROM sqlite_master WHERE tbl_name = 'events')) / count(*) FROM events" | tr '|' ' '
EOF
}

# --- the server ---------------------------------------------------------------

start_server() {
    say "A server of $CPUS CPUs and $MEMORY"
    docker rm -fv "$SERVER" >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
    docker network create "$NETWORK" >/dev/null
    [ "$((FIRST_CPU + CPUS))" -lt "$(docker info --format '{{.NCPU}}')" ] || die "This machine has no processors left for the load generator next to a server of $CPUS."
    # Swap is given none: --memory-swap equal to --memory.
    docker run -d --privileged --cpuset-cpus "$FIRST_CPU-$((FIRST_CPU + CPUS - 1))" --memory "$MEMORY" --memory-swap "$MEMORY" \
        --name "$SERVER" --network "$NETWORK" -e DOCKER_TLS_CERTDIR= docker:dind >/dev/null
    eventually on_server docker info >/dev/null 2>&1 || die "The Docker daemon in $SERVER did not start."
    # GNU date for nanoseconds, sqlite for the database's own account of its
    # size, jq for the load generator's report.
    on_server apk add --quiet --no-cache curl coreutils sqlite jq dnsmasq
    server_address
    on_server dnsmasq --no-resolv --no-hosts --address=/localhost/127.0.0.1 --listen-address="$SERVER_ADDRESS" --bind-interfaces
    on_server mkdir -p /root/apps /root/checkout /opt/shipwick
    # The installer leaves this file alone. The API on the server's loopback
    # is the handbook's way to reach it without a hostname.
    printf '%s\n' 'services:' '  agent:' '    ports: ["127.0.0.1:9000:9000"]' | put /opt/shipwick/compose.override.yml
    note "Docker $(on_server docker version --format '{{.Server.Version}}') in the container $SERVER; this machine: $(docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes of memory, {{.OperatingSystem}}, kernel {{.KernelVersion}}')"
    note "$(on_server sh -c 'grep -m 1 "model name" /proc/cpuinfo' | sed 's/.*: //')"
}

# The checkout becomes what a release is to the installer, as in
# test-upgrade.sh: three images, a compose file that pins them, and the CLI.
install_checkout() {
    say "Shipwick from this checkout"
    version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    docker build --quiet --build-arg "VERSION=$version" -t "ghcr.io/shipwick/agent:$TAG" . >/dev/null
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
        -ldflags "-X github.com/shipwick/shipwick/pkg/version.Version=$version" -o "$WORK/shipwick" ./cli/cmd/shipwick
    docker cp --quiet "$WORK/shipwick" "$SERVER:/root/shipwick"
    docker cp --quiet "$WORK/compose.production.yml" "$SERVER:/root/checkout/compose.production.yml"
    docker cp --quiet scripts/install.sh "$SERVER:/root/checkout/install.sh"
    on_server chmod 0755 /root/shipwick

    on_server env SHIPWICK_COMPOSE_FILE=/root/checkout/compose.production.yml "SHIPWICK_DNS_RESOLVERS=$SERVER_ADDRESS" \
        sh /root/checkout/install.sh > "$WORK/install.out" 2>&1 \
        || { cat "$WORK/install.out"; rm -f "$WORK/install.out"; die "The installer failed."; }
    rm -f "$WORK/install.out"
    eventually agent_answers || die "The agent does not answer on the server's loopback."
    sw server status >/dev/null || die "The CLI cannot reach the agent on the server."
    note "Installed $version: $(on_server sh -c 'cd /opt/shipwick && docker compose config --images' | tr '\n' ' ')"
}

# --- applications and replicas ------------------------------------------------

# neighbours_full UPTIME — says so when the kernel ran out of room for the
# addresses of its neighbours since then.
neighbours_full() {
    overflows="$(on_server /root/overflows "$1")"
    [ "$overflows" = 0 ] || note "During this step the kernel said $overflows times that its table of neighbours was full (net.ipv4.neigh.default.gc_thresh3): addresses on Docker's networks stop being reachable."
}

# The last step that held; "database" visits that many applications.
APPS=0

measure_apps() {
    say "Applications of two replicas each ($APP_IMAGE)"
    limit="$(on_server cat /sys/fs/cgroup/memory.max)"
    note "The server's resolver answers for a hostname in $(timed 9 nslookup -type=a a001.localhost "$SERVER_ADDRESS" 2>/dev/null) ms."
    note "Memory in MB. CPU in percent of one core over 30 s with no request and no deployment. Times are medians: of 5 runs for the commands and requests, of 3 for the killed replica; the rolling deployment is one run."
    note "\"answer\" is how many of the applications answered one request through the proxy with 200."
    note "\"list\" is one question to Docker for every container Shipwick manages, \"list app\" for those of one application: the supervisor asks the first three times a second and the second once for every application."
    printf '%-5s %-10s %-8s %-7s %-6s %-6s %-9s %-7s %-6s %-6s %-7s %-9s %-10s %-8s %-6s %-8s %-8s %-9s %-9s\n' \
        apps containers answer server agent proxy dashboard cpu agent ps status "GET apps" "GET server" metrics list "list app" deploy recovery "added in"
    for step in $STEPS; do
        [ "$step" -gt "$APPS" ] || continue
        since="$(on_server cut -d. -f1 /proc/uptime)"
        added_in="$(timed 1 /root/deploy-apps "$((APPS + 1))" "$step")"
        failed="$(on_server sh -c 'cat /root/apps/*.out | grep -c "^✗" || true')"
        if [ "$failed" != 0 ]; then
            note "At $step applications $failed deployments failed. The first:"
            on_server sh -c 'grep -l "^✗" /root/apps/*.out | head -n 1 | xargs cat' | sed 's/^/    /'
            neighbours_full "$since"
            break
        fi
        APPS="$step"
        # The supervisor and the sampler get a minute to see what is there.
        sleep 30
        cpu="$(on_server /root/usage 30)"
        printf '%-5s %-10s %-8s %-7s %-6s %-6s %-9s %-7s %-6s %-6s %-7s %-9s %-10s %-8s %-6s %-8s %-8s %-9s %-9s\n' \
            "$step" "$(on_server sh -c 'docker ps -q | wc -l')" \
            "$(on_server /root/visit "$step" 1)/$step" \
            "$(on_server /root/memory)" "$(on_server /root/memory shipwick-agent-1)" \
            "$(on_server /root/memory shipwick-caddy-1)" "$(on_server /root/memory shipwick-dashboard-1)" \
            "${cpu% *}%" "${cpu#* }%" \
            "$(timed 5 /root/sw ps)ms" "$(timed 5 /root/sw status a001)ms" \
            "$(timed 5 /root/api /api/v1/applications)ms" "$(timed 5 /root/api /api/v1/server)ms" "$(timed 5 /root/api /metrics)ms" \
            "$(timed 5 /root/list)ms" "$(timed 5 /root/list a001)ms" \
            "$(seconds "$(timed 1 /root/sw redeploy a001)")s" \
            "$(seconds "$(timed 3 /root/recover a002)")s" \
            "$(seconds "$added_in")s"
        neighbours_full "$since"
        used="$(on_server /root/memory)"
        if [ "$((used * 1048576))" -gt "$((limit * 9 / 10))" ]; then
            note "Stopped at $step applications: $used MB of the server's $((limit / 1048576)) MB are in use."
            break
        fi
    done
    note "Docker's own account: $(on_server docker info --format '{{.Containers}} containers, {{.Images}} images'); the shipwick network: $(on_server docker network inspect shipwick --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}')"
}

# --- the database -------------------------------------------------------------

measure_database() {
    say "The database"
    [ "$APPS" -gt 0 ] || APPS="$(on_server sh -c 'ls /root/apps/*.yaml | wc -l')"
    on_server /root/tables > "$WORK/before.txt"
    i=0
    while [ "$i" -lt "$DEPLOYMENTS" ]; do
        sw redeploy a001 >/dev/null
        i=$((i + 1))
    done
    note "$DEPLOYMENTS rolling deployments of a001 (two replicas), then one request a minute to each of $APPS applications for $MINUTES minutes."
    answered="$(on_server /root/visit "$APPS" "$MINUTES")"
    note "$answered of the $((APPS * MINUTES)) requests were answered with 200."
    # The traffic of a minute is written when the minute is over.
    sleep 70
    on_server /root/tables > "$WORK/after.txt"

    note "Every table with rows: rows, bytes of its pages with its indexes, bytes a row."
    awk '$2 > 0 { printf "  %-22s %8d rows %10d bytes %8.0f a row\n", $1, $2, $3, $3 / $2 }' "$WORK/after.txt"
    note "What the $DEPLOYMENTS deployments added, in rows, and in bytes at the size of a row above:"
    awk -v n="$DEPLOYMENTS" '
        NR == FNR { rows[$1] = $2; next }
        $1 ~ /^(deployments|deployment_replicas|deployment_events|audit_log|log_archives)$/ {
            added = ($2 - rows[$1]) * $3 / $2
            printf "  %-22s %+6d rows %9.0f bytes\n", $1, $2 - rows[$1], added
            if ($1 ~ /^deployment/) forever += added; else kept += added
        }
        END { printf "  a deployment: %.0f bytes kept for as long as the application, %.0f bytes that age out\n", forever / n, kept / n }' "$WORK/before.txt" "$WORK/after.txt"
    rm -f "$WORK/before.txt" "$WORK/after.txt"
    note "The files: $(on_server sh -c "ls -l $DATABASE* | awk '{ printf \"%s %d bytes; \", \$NF, \$5 }'" | sed "s|$(dirname "$DATABASE")/||g")"
    note "The log archive next to it: $(on_server sh -c "find $(dirname "$DATABASE")/logs -type f | wc -l") files, $(on_server sh -c "du -sk $(dirname "$DATABASE")/logs | cut -f1") KB"
}

# --- requests through the proxy -------------------------------------------------

bench_yaml() { # bench_yaml REPLICAS
    printf '%s\n' "name: bench" "image: $APP_IMAGE" "port: 80" "domain: bench.localhost" "replicas: $1" "health:" "  path: /health"
}

# load ARGS... — oha, next to the server and on the processors the server
# does not have, against the proxy's HTTPS port; its report is left in
# $WORK/load.json.
load() {
    docker run --rm --network "$NETWORK" --add-host "bench.localhost:$SERVER_ADDRESS" \
        --cpuset-cpus "$((FIRST_CPU + $(on_server nproc)))-$(($(docker info --format '{{.NCPU}}') - 1))" "$LOAD_IMAGE" \
        --no-tui --insecure --output-format json -w "$@" https://bench.localhost/ > "$WORK/load.json"
}

# report — one line of the last load: requests a second, the share answered
# with 200, the latency percentiles in milliseconds, and whatever else came
# back.
report() {
    # shellcheck disable=SC2016 # jq's variables, not the shell's
    on_server jq -r '
        (.statusCodeDistribution // {}) as $codes
        | ([$codes[]] | add // 0) as $answered
        | ([.errorDistribution // {} | .[]] | add // 0) as $errors
        | [ (.summary.requestsPerSec | floor),
            (if $answered + $errors > 0 then (($codes["200"] // 0) * 100 / ($answered + $errors) * 100 | floor) / 100 else 0 end),
            (.latencyPercentiles.p50 * 1000 * 10 | floor) / 10,
            (.latencyPercentiles.p90 * 1000 * 10 | floor) / 10,
            (.latencyPercentiles.p99 * 1000 * 10 | floor) / 10,
            ($codes | to_entries | map("\(.value)×\(.key)") | join(" ")),
            (.errorDistribution // {} | to_entries | map("\(.value)×\(.key)") | join("; "))
          ] | @tsv' < "$WORK/load.json"
}

measure_requests() {
    say "Requests through the proxy ($LOAD_IMAGE, HTTP/1.1 over TLS, GET / of $APP_IMAGE)"
    docker pull --quiet "$LOAD_IMAGE" >/dev/null
    note "As fast as the connections allow, 20 s each. Columns: requests a second, percent answered 200, p50, p90 and p99 in ms, the answers, the errors."
    for replicas in 1 4; do
        bench_yaml "$replicas" | put /root/apps/bench.yml
        sw deploy -f /root/apps/bench.yml >/dev/null
        eventually on_server curl -fsSk -o /dev/null -m 5 https://bench.localhost/ || die "bench.localhost does not answer."
        for connections in 16 64 256; do
            load -z 20s -c "$connections"
            printf '%s replicas  kept connections   %4s  %s\n' "$replicas" "$connections" "$(report)"
        done
        for connections in 16 64 256; do
            load -z 20s -c "$connections" --disable-keepalive
            printf '%s replicas  new connections    %4s  %s\n' "$replicas" "$connections" "$(report)"
        done
    done

    note "During a rolling deployment of the four replicas: 40 s at a fixed rate, the deployment started 5 s in."
    for mode in "kept connections" "new connections"; do
        if [ "$mode" = "kept connections" ]; then
            load -z 40s -c 32 -q 500 &
        else
            load -z 40s -c 32 -q 200 --disable-keepalive &
        fi
        sleep 5
        took="$(timed 1 /root/sw redeploy bench)"
        wait
        printf '4 replicas  %-17s deployed in %ss  %s\n' "$mode" "$(seconds "$took")" "$(report)"
    done
}

# --- a restore ------------------------------------------------------------------

vol_yaml() {
    printf '%s\n' "name: vol" "image: $APP_IMAGE" "port: 80" "health:" "  path: /health" "volumes:" "  - name: data" "    path: /data" "deploy:" "  strategy: recreate"
}

# rate MEGABYTES MILLISECONDS — "12.3s 81MB/s".
rate() { awk -v mb="$1" -v ms="$2" 'BEGIN { printf "%.1fs %.0fMB/s", ms / 1000, mb * 1000 / ms }'; }

# restore_volume SIZE KIND LABEL — fills the volume of vol with SIZE MB of
# KIND (random: nothing in it repeats; text: one line repeated), then times a
# backup, its verification and its restore.
restore_volume() {
    volume="$(on_server docker volume ls -q --filter label=com.shipwick.app=vol | head -n 1)"
    [ -n "$volume" ] || die "The volume of vol was not found."
    if [ "$2" = random ]; then
        on_server docker run --rm -v "$volume:/data" "$FILL_IMAGE" sh -c "rm -f /data/fill && dd if=/dev/urandom of=/data/fill bs=1M count=$1 2>/dev/null && sync"
    else
        on_server docker run --rm -v "$volume:/data" "$FILL_IMAGE" sh -c "rm -f /data/fill && yes 'the same line, again and again, as a log would have it' | dd of=/data/fill bs=1M count=$1 iflag=fullblock 2>/dev/null && sync"
    fi
    backup="$(timed 1 /root/sw backups run vol)"
    id="$(sw backups vol | awk '$NF != "VERIFIED" && $1 + 0 > 0 { print $1; exit }')"
    verify="$(timed 1 /root/sw backups verify vol "$id")"
    sw stop vol >/dev/null
    restore="$(timed 1 /root/sw backups restore vol "$id" --yes)"
    sw start vol >/dev/null
    printf '%5s MB  %-6s  %-13s  backup %-16s  verify %-16s  restore %-16s\n' "$1" "$2" "$3" \
        "$(rate "$1" "$backup")" "$(rate "$1" "$verify")" "$(rate "$1" "$restore")"
    sw backups rm vol "$id" >/dev/null 2>&1 || true
}

restore_volumes() { # restore_volumes LABEL
    for size in $VOLUMES; do
        restore_volume "$size" random "$1"
    done
    # A backup is a tar archive, not compressed: what the data is should not matter.
    restore_volume 1000 text "$1"
}

measure_restore() {
    say "A backup, its verification and its restore (local directory, no bucket)"
    note "The application is one replica of $APP_IMAGE with one volume; the volume holds one file. MB/s is the volume's size over the time of the command, the CLI's waiting included."
    vol_yaml | put /root/apps/vol.yml
    sw deploy -f /root/apps/vol.yml >/dev/null
    on_server docker pull --quiet "$FILL_IMAGE" >/dev/null
    restore_volumes "not encrypted"

    # As the handbook has it: the passphrase in .env, and the agent started again.
    echo "SHIPWICK_BACKUP_PASSPHRASE=$PASSPHRASE" | on_server sh -c 'cat >> /opt/shipwick/.env'
    on_server sh -c 'cd /opt/shipwick && docker compose up -d' >/dev/null 2>&1
    eventually agent_answers || die "The agent did not come back with the passphrase set."
    sleep 5
    restore_volumes "encrypted"

    say "The agent's own state"
    backup="$(timed 1 /root/sw server backup)"
    state="$(on_server sh -c "ls -d $(dirname "$DATABASE")/backups/_agent/*/ | sort -t/ -k10 -n | tail -n 1")"
    on_server mkdir -p /root/state
    on_server sh -c "cp ${state}shipwick.db.enc ${state}encryption.key.enc /root/state/"
    decrypt="$(timed 1 env "SHIPWICK_BACKUP_PASSPHRASE=$PASSPHRASE" /root/shipwick backups decrypt /root/state/shipwick.db.enc)"
    on_server env "SHIPWICK_BACKUP_PASSPHRASE=$PASSPHRASE" /root/shipwick backups decrypt /root/state/encryption.key.enc >/dev/null
    size="$(on_server stat -c %s /root/state/shipwick.db)"
    # The handbook's "Restoring the agent's state", over the same server.
    put /root/restore-state <<EOF
#!/bin/sh
set -e
cd /opt/shipwick
docker compose stop agent
docker run --rm -v shipwick_agent-data:/data -v /root/state:/restore:ro $FILL_IMAGE sh -c '
  rm -f /data/shipwick.db-wal /data/shipwick.db-shm &&
  cp /restore/shipwick.db /restore/encryption.key /data/ &&
  chmod 600 /data/shipwick.db /data/encryption.key'
docker compose start agent
until curl -fsS -o /dev/null http://127.0.0.1:9000/api/v1/health 2>/dev/null; do sleep 0.1; done
EOF
    restore="$(timed 1 /root/restore-state)"
    sw server status >/dev/null || die "The agent does not answer with the restored state."
    note "A database of $size bytes: shipwick server backup $(seconds "$backup") s, decrypting it $(seconds "$decrypt") s, stopping the agent, putting the two files back and the agent answering again $(seconds "$restore") s."
    sw delete vol --yes >/dev/null 2>&1 || true
}

# --- entry point ----------------------------------------------------------------

command -v docker >/dev/null 2>&1 || die "docker is needed: the server is a container."
if [ -n "$REUSE" ]; then
    on_server true 2>/dev/null || die "There is no server named $SERVER to use again."
    server_address
    server_tools
else
    command -v go >/dev/null 2>&1 || die "go is needed, to build the CLI."
    start_server
    server_tools
    install_checkout
fi

for part in $PARTS; do
    case "$part" in
        apps) measure_apps ;;
        database) measure_database ;;
        requests) measure_requests ;;
        restore) measure_restore ;;
    esac
done
