#!/bin/sh
# Shipwick installer.
#
#   curl -fsSL https://get.shipwick.com | sh                 # server: agent + Caddy + dashboard, and the CLI
#   curl -fsSL https://get.shipwick.com | sh -s -- --cli     # the CLI only (your laptop, CI)
#   sh install.sh --bundle shipwick-v0.6.0-linux-amd64.tar.gz  # a server with no way out: from files
#
# Safe to run again: that is how you upgrade. An existing .env — and with it
# your API token — is never touched.
#
# Everything lives in functions and nothing runs until the last line, so a
# download that is cut off half-way executes nothing at all.

set -eu

# --- what to install, from where -------------------------------------------

REPO="${SHIPWICK_REPO:-shipwick/shipwick}"
VERSION="${SHIPWICK_VERSION:-latest}"
INSTALL_DIR="${SHIPWICK_INSTALL_DIR:-/opt/shipwick}"
BIN_DIR="${SHIPWICK_BIN_DIR:-/usr/local/bin}"
# For testing and air-gapped servers: use this compose file instead of downloading one.
COMPOSE_SOURCE="${SHIPWICK_COMPOSE_FILE:-}"
# Run from a checkout (sh scripts/install.sh), the compose file next door is the one to use.
if [ -z "$COMPOSE_SOURCE" ] && [ -f "$(dirname "$0")/../configs/compose.production.yml" ]; then
    COMPOSE_SOURCE="$(dirname "$0")/../configs/compose.production.yml"
fi

COMPOSE_FILE="compose.yml"

# A bundle made by `shipwick server bundle` and copied here: a directory, or
# its .tar.gz. With one, nothing is downloaded and nothing is pulled. Run from
# inside an unpacked bundle, the files next door are the bundle.
BUNDLE="${SHIPWICK_BUNDLE:-}"
if [ -z "$BUNDLE" ] && [ -f "$(dirname "$0")/images.tar" ] && [ -f "$(dirname "$0")/compose.production.yml" ]; then
    BUNDLE="$(dirname "$0")"
fi

# Behind a proxy. curl reads HTTPS_PROXY in either case, wget in lower case
# only; .env carries one spelling, the one the compose file passes on.
HTTPS_PROXY="${HTTPS_PROXY:-${https_proxy:-}}"
HTTP_PROXY="${HTTP_PROXY:-${http_proxy:-}}"
NO_PROXY="${NO_PROXY:-${no_proxy:-}}"
if [ -n "$HTTPS_PROXY" ]; then https_proxy="$HTTPS_PROXY"; export HTTPS_PROXY https_proxy; fi
if [ -n "$HTTP_PROXY" ]; then http_proxy="$HTTP_PROXY"; export HTTP_PROXY http_proxy; fi
if [ -n "$NO_PROXY" ]; then no_proxy="$NO_PROXY"; export NO_PROXY no_proxy; fi

# --- output -----------------------------------------------------------------

if [ -t 1 ]; then
    BOLD="$(printf '\033[1m')"; DIM="$(printf '\033[2m')"; RED="$(printf '\033[31m')"
    GREEN="$(printf '\033[32m')"; YELLOW="$(printf '\033[33m')"; RESET="$(printf '\033[0m')"
else
    BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; RESET=""
fi

step() { printf '%s\n' "${GREEN}✓${RESET} $*"; }
info() { printf '%s\n' "  $*"; }
warn() { printf '%s\n' "${YELLOW}!${RESET} $*" >&2; }
die()  { printf '\n%s\n' "${RED}✗${RESET} $*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# --- helpers ----------------------------------------------------------------

# download URL DEST — fails on HTTP errors, never leaves a partial DEST behind.
download() {
    tmp="$2.download.$$"
    if have curl; then
        curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp" "$1" || { rm -f "$tmp"; return 1; }
    elif have wget; then
        # --https-only keeps redirects on HTTPS too. BusyBox wget (Alpine) does
        # not have it; what it downloads is still checked against the release's
        # checksums before anything is installed.
        https_only=""
        if wget --help 2>&1 | grep -q -- --https-only; then https_only="--https-only"; fi
        # shellcheck disable=SC2086  # one optional flag, deliberately unquoted
        wget -q $https_only -O "$tmp" "$1" || { rm -f "$tmp"; return 1; }
    else
        die "Neither curl nor wget is installed."
    fi
    mv "$tmp" "$2"
}

release_url() { # release_url ASSET
    if [ "$VERSION" = "latest" ]; then
        echo "https://github.com/$REPO/releases/latest/download/$1"
    else
        echo "https://github.com/$REPO/releases/download/$VERSION/$1"
    fi
}

# fetch_release_asset ASSET DEST — downloads a file of the release and refuses
# it unless it matches the release's published checksum. Returns 1 only when
# the asset cannot be downloaded at all; anything suspicious is fatal.
fetch_release_asset() {
    if [ -n "$BUNDLE" ]; then
        # The bundle carries the release's files and its checksums.txt; a file
        # damaged on the way here is refused like a bad download.
        [ -f "$BUNDLE/$1" ] || return 1
        cp "$BUNDLE/$1" "$2"
        [ -f "$WORK_DIR/checksums.txt" ] || cp "$BUNDLE/checksums.txt" "$WORK_DIR/checksums.txt" 2>/dev/null \
            || { rm -f "$2"; die "The bundle has no checksums.txt; refusing to install unverified files."; }
    else
        download "$(release_url "$1")" "$2" 2>/dev/null || return 1
    fi

    if [ ! -f "$WORK_DIR/checksums.txt" ]; then
        download "$(release_url checksums.txt)" "$WORK_DIR/checksums.txt" \
            || { rm -f "$2"; die "Could not download checksums.txt; refusing to install unverified files."; }
    fi
    expected="$(awk -v f="$1" '$2 == f || $2 == "*"f { print $1 }' "$WORK_DIR/checksums.txt")"
    [ -n "$expected" ] || { rm -f "$2"; die "checksums.txt has no entry for $1."; }
    actual="$(sha256_of "$2")"
    [ "$expected" = "$actual" ] \
        || { rm -f "$2"; die "Checksum mismatch for $1 (expected $expected, got $actual). Nothing was installed."; }
}

sha256_of() {
    if have sha256sum; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# --- a bundle ---------------------------------------------------------------

# open_bundle makes BUNDLE a directory that holds the bundle's files, and
# refuses one made for another kind of machine before anything is changed.
open_bundle() {
    [ -e "$BUNDLE" ] || die "$BUNDLE does not exist. Give the bundle made by:  shipwick server bundle"
    if [ -f "$BUNDLE" ]; then
        have tar || die "tar is not installed, and $BUNDLE is an archive. Unpack it elsewhere and copy the directory instead."
        mkdir "$WORK_DIR/bundle"
        tar -xzf "$BUNDLE" -C "$WORK_DIR/bundle" \
            || die "$BUNDLE could not be unpacked: it is not a .tar.gz, or the copy is incomplete (it needs as much free space in $(dirname "$WORK_DIR") as it is large)."
        BUNDLE="$WORK_DIR/bundle"
        # The archive holds one directory, named after the release.
        for dir in "$BUNDLE"/*/; do
            if [ -f "${dir}compose.production.yml" ]; then BUNDLE="${dir%/}"; fi
        done
    fi
    # The installer changes directory before it is done with the bundle.
    BUNDLE="$(cd "$BUNDLE" && pwd)"
    for file in compose.production.yml checksums.txt images.tar images.tar.sha256; do
        [ -f "$BUNDLE/$file" ] || die "$BUNDLE is not a Shipwick bundle: it has no $file. Make one with:  shipwick server bundle"
    done
    detect_platform
    if [ ! -f "$BUNDLE/shipwick_${PLATFORM}" ]; then
        made_for="another platform"
        for file in "$BUNDLE"/shipwick_*; do
            if [ -f "$file" ]; then made_for="${file##*/shipwick_}"; fi
        done
        die "This bundle was made for $made_for, and this server is $PLATFORM. Make one with:  shipwick server bundle --arch ${PLATFORM#*_}"
    fi
    # The archive of images is not a file of the release, so the bundle brings
    # its checksum with it: what is checked is that the copy arrived whole.
    expected="$(awk '{ print $1; exit }' "$BUNDLE/images.tar.sha256")"
    actual="$(sha256_of "$BUNDLE/images.tar")"
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
        die "images.tar in the bundle is damaged (expected $expected, got $actual): copy the bundle again. Nothing was changed."
    fi
    step "The bundle is complete ($BUNDLE)"
}

# load_bundle_images hands the images to Docker, before the compose file that
# names them replaces the one in use.
load_bundle_images() {
    docker load --quiet --input "$BUNDLE/images.tar" >/dev/null \
        || die "Docker could not load images.tar from the bundle. Nothing was changed."
    step "Loaded the images from the bundle"
}

detect_platform() {
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$os" in
        linux|darwin) ;;
        *) die "Unsupported operating system: $os. On Windows, download shipwick_windows_amd64.exe from https://github.com/$REPO/releases" ;;
    esac
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) die "Unsupported architecture: $arch" ;;
    esac
    PLATFORM="${os}_${arch}"
}

random_token() {
    if have openssl; then
        openssl rand -hex 32
    else
        # 32 bytes from the kernel, as hex.
        od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
    fi
}

# ask VAR "Question" — reads from the terminal even when the script itself
# arrives on stdin (curl | sh). Without a terminal the answer stays empty.
ask() {
    eval "current=\${$1:-}"
    if [ -n "$current" ] || [ ! -r /dev/tty ] || [ ! -t 1 ]; then
        return 0
    fi
    printf '%s' "  $2: " > /dev/tty
    # shellcheck disable=SC2034  # used by the eval below
    IFS= read -r answer < /dev/tty || answer=""
    eval "$1=\$answer"
}

# A hostname and nothing else: it ends up in .env and in the proxy config.
valid_hostname() {
    case "$1" in
        ""|*[!a-zA-Z0-9.-]*|.*|*.|-*|*..*) return 1 ;;
        *.*) return 0 ;;
        *) return 1 ;;
    esac
}

# --- the CLI ------------------------------------------------------------------

install_cli() {
    detect_platform
    asset="shipwick_${PLATFORM}"
    if ! fetch_release_asset "$asset" "$WORK_DIR/shipwick"; then
        warn "Could not download the shipwick CLI ($asset) from https://github.com/$REPO/releases."
        info "Build it from source instead:  go build -o shipwick ./cli/cmd/shipwick"
        return 1
    fi

    chmod 0755 "$WORK_DIR/shipwick"
    if [ -w "$BIN_DIR" ]; then
        mv "$WORK_DIR/shipwick" "$BIN_DIR/shipwick"
    elif have sudo; then
        sudo mv "$WORK_DIR/shipwick" "$BIN_DIR/shipwick"
    else
        die "$BIN_DIR is not writable and sudo is not available. Set SHIPWICK_BIN_DIR to a directory on your PATH."
    fi
    step "Installed the shipwick CLI to $BIN_DIR/shipwick"
}

# --- server -----------------------------------------------------------------

check_server_requirements() {
    [ "$(uname -s)" = "Linux" ] || die "The Shipwick server runs on Linux. To install only the CLI here, use --cli."
    [ "$(id -u)" -eq 0 ] || die "Installing the server needs root (it writes to $INSTALL_DIR and talks to Docker). Re-run with sudo."

    if ! have docker && [ -n "$BUNDLE" ]; then
        die "Docker is not installed, and the bundle does not bring it. On a server with no way out, install Docker Engine
  and the Compose plugin from your distribution's packages or from Docker's static binaries, copied here like the
  bundle (docs/handbook.md, \"A server with no way out\"), then run this installer again."
    fi
    if ! have docker; then
        die "Docker is not installed. Shipwick does not install it for you — that is your server's decision to make.
  Install it with:   curl -fsSL https://get.docker.com | sh
  then run this installer again."
    fi
    docker info >/dev/null 2>&1 || die "Docker is installed but not running (or this user cannot reach it)."
    docker compose version >/dev/null 2>&1 || die "The Docker Compose plugin is missing. Install 'docker-compose-plugin' and run this installer again."
    step "Docker $(docker version --format '{{.Server.Version}}') with Compose $(docker compose version --short)"
}

# setting NAME DEFAULT — NAME from the environment, else from an existing .env.
setting() {
    eval "value=\${$1:-}"
    if [ -z "$value" ] && [ -f "$INSTALL_DIR/.env" ]; then
        value="$(sed -n "s/^$1=//p" "$INSTALL_DIR/.env" | tail -n 1)"
    fi
    printf '%s' "${value:-$2}"
}

# Caddy needs the HTTP and HTTPS ports to itself. Found out now, that is one
# clear sentence; found out by Docker, it is half a started stack.
check_ports() {
    # On an upgrade the ports are held by our own Caddy.
    if [ -n "$(docker ps -q --filter label=com.docker.compose.project=shipwick --filter label=com.docker.compose.service=caddy)" ]; then
        return 0
    fi
    for port in "$(setting SHIPWICK_HTTP_PORT 80)" "$(setting SHIPWICK_HTTPS_PORT 443)"; do
        holder="$(docker ps --filter "publish=$port" --format '{{.Names}} ({{.Image}})' | head -n 1)"
        if [ -n "$holder" ]; then
            holder="the container $holder"
        elif have ss && [ -n "$(ss -H -ltn "sport = :$port" 2>/dev/null)" ]; then
            holder="a process on this server (find it with:  ss -ltnp 'sport = :$port')"
        else
            continue
        fi
        die "Port $port is already in use by $holder.
  Shipwick's reverse proxy needs ports 80 and 443: certificates are issued and renewed through them.
  Stop what is using the port, or install Shipwick on a server where both are free. Nothing was changed."
    done
}

write_env() {
    env_file="$INSTALL_DIR/.env"
    if [ -f "$env_file" ]; then
        step "Keeping the existing $env_file (your API token is unchanged)"
        GENERATED_TOKEN=""
        return 0
    fi

    SHIPWICK_AGENT_DOMAIN="${SHIPWICK_AGENT_DOMAIN:-}"
    SHIPWICK_DASHBOARD_DOMAIN="${SHIPWICK_DASHBOARD_DOMAIN:-}"
    if [ -r /dev/tty ] && [ -t 1 ]; then
        printf '\n%s\n' "${BOLD}Two hostnames, both optional${RESET} ${DIM}(press Enter to skip; their DNS must point at this server)${RESET}"
    fi
    ask SHIPWICK_AGENT_DOMAIN "Hostname for the API, used by the shipwick CLI (e.g. agent.example.com)"
    ask SHIPWICK_DASHBOARD_DOMAIN "Hostname for the dashboard (e.g. dashboard.example.com)"
    for name in SHIPWICK_AGENT_DOMAIN SHIPWICK_DASHBOARD_DOMAIN; do
        eval "value=\${$name}"
        if [ -n "$value" ] && ! valid_hostname "$value"; then
            die "$name: \"$value\" is not a hostname. Give a bare name such as agent.example.com — no https://, port or path."
        fi
    done
    if [ -n "$SHIPWICK_AGENT_DOMAIN" ] && [ "$SHIPWICK_AGENT_DOMAIN" = "$SHIPWICK_DASHBOARD_DOMAIN" ]; then
        die "The API and the dashboard need two different hostnames."
    fi
    # The agent refuses to start with a token of another form; said here, it
    # is one sentence instead of a stack that never becomes healthy.
    case "${SHIPWICK_CLOUDFLARE_API_TOKEN:-}" in
        *[!A-Za-z0-9_-]*) die "SHIPWICK_CLOUDFLARE_API_TOKEN is not a Cloudflare API token: letters, digits, - and _ only, without quotes." ;;
    esac

    GENERATED_TOKEN="$(random_token)"
    [ "${#GENERATED_TOKEN}" -ge 32 ] || die "Could not generate a random token."

    # The token is root on this server: the file is created unreadable to
    # anyone else, not made so afterwards.
    ( umask 077
      cat > "$env_file" <<EOF
# Shipwick configuration. Keep this file private: the token is root on this server.
SHIPWICK_AGENT_TOKEN=$GENERATED_TOKEN
SHIPWICK_AGENT_DOMAIN=$SHIPWICK_AGENT_DOMAIN
SHIPWICK_DASHBOARD_DOMAIN=$SHIPWICK_DASHBOARD_DOMAIN
EOF
      # Settings given for this run have to outlive it: the next run, an
      # upgrade, must not quietly move the proxy back to the default ports.
      for name in SHIPWICK_HTTP_PORT SHIPWICK_HTTPS_PORT SHIPWICK_AGENT_IMAGE SHIPWICK_DASHBOARD_IMAGE SHIPWICK_CADDY_IMAGE SHIPWICK_WEBHOOK_URL SHIPWICK_WEBHOOK_SECRET SHIPWICK_CLOUDFLARE_API_TOKEN SHIPWICK_ALERT_MEMORY_PERCENT SHIPWICK_ALERT_DISK_PERCENT SHIPWICK_BACKUP_PASSPHRASE SHIPWICK_BACKUP_S3_ENDPOINT SHIPWICK_BACKUP_S3_BUCKET SHIPWICK_BACKUP_S3_ACCESS_KEY_ID SHIPWICK_BACKUP_S3_SECRET_ACCESS_KEY SHIPWICK_BACKUP_S3_REGION SHIPWICK_BACKUP_S3_PREFIX SHIPWICK_EXPORT_SCHEDULE SHIPWICK_EXPORT_KEEP SHIPWICK_STANDBY_SCHEDULE HTTPS_PROXY HTTP_PROXY NO_PROXY SHIPWICK_CA_FILE SHIPWICK_DNS_RESOLVERS SHIPWICK_ACME_DIRECTORY SHIPWICK_OIDC_ISSUER SHIPWICK_OIDC_CLIENT_ID SHIPWICK_OIDC_CLIENT_SECRET SHIPWICK_OIDC_SCOPES SHIPWICK_OIDC_GROUPS_CLAIM SHIPWICK_AGENTS; do
          eval "value=\${$name:-}"
          [ -z "$value" ] || printf '%s=%s\n' "$name" "$value" >> "$env_file"
      done
    )
    step "Wrote $env_file"
}

install_server() {
    check_server_requirements
    check_ports

    ( umask 077; mkdir -p "$INSTALL_DIR" )
    chmod 0700 "$INSTALL_DIR"
    [ -z "$BUNDLE" ] || load_bundle_images
    if [ -n "$COMPOSE_SOURCE" ] && [ -z "$BUNDLE" ]; then
        cp "$COMPOSE_SOURCE" "$INSTALL_DIR/$COMPOSE_FILE"
    else
        # The release's compose file pins the images to the release's version.
        # Verified in the work directory first: a bad download must not replace
        # the compose file of a running installation.
        fetch_release_asset compose.production.yml "$WORK_DIR/compose.yml" \
            || die "Could not download $(release_url compose.production.yml)
  Behind a proxy, set HTTPS_PROXY for this installer (with sudo:  sudo -E sh). On a server with no way out,
  install from a bundle made elsewhere with:  shipwick server bundle"
        mv "$WORK_DIR/compose.yml" "$INSTALL_DIR/$COMPOSE_FILE"
    fi
    step "Installed $INSTALL_DIR/$COMPOSE_FILE"

    write_env
    cd "$INSTALL_DIR"

    if [ -n "$BUNDLE" ]; then
        # Nothing is pulled: what the compose file names must be what the
        # bundle brought, or what .env names instead and the server has.
        missing=""
        for image in $(docker compose config --images); do
            docker image inspect "$image" >/dev/null 2>&1 || missing="$missing $image"
        done
        [ -z "$missing" ] || die "Not on this server and not in the bundle:$missing
  If $INSTALL_DIR/.env names images of your own (SHIPWICK_AGENT_IMAGE, SHIPWICK_DASHBOARD_IMAGE,
  SHIPWICK_CADDY_IMAGE), load them with docker load, or remove those lines."
    elif ! docker compose pull --quiet 2>/dev/null; then
        # Not fatal if every image the compose file names is here anyway: built
        # locally, or pulled earlier.
        missing=""
        for image in $(docker compose config --images); do
            docker image inspect "$image" >/dev/null 2>&1 \
                || docker pull --quiet "$image" >/dev/null 2>&1 \
                || missing="$missing $image"
        done
        if [ -n "$missing" ]; then
            die "Could not pull:$missing
  Check this server's connection to the registry and run the installer again.$(daemon_proxy_advice)
  To run images you built yourself, set SHIPWICK_AGENT_IMAGE,
  SHIPWICK_DASHBOARD_IMAGE and SHIPWICK_CADDY_IMAGE in $INSTALL_DIR/.env.
  On a server with no way out, install from a bundle made elsewhere with:  shipwick server bundle"
        fi
        warn "Could not pull images; using the ones already on this server."
    fi

    docker compose up -d --remove-orphans
    step "Started the Shipwick services"

    printf '%s' "  Waiting for the agent"
    i=0
    until docker compose exec -T agent shipwick-agent healthcheck >/dev/null 2>&1; do
        i=$((i + 1))
        if [ "$i" -ge 60 ]; then
            printf '\n'
            die "The agent did not become healthy. Look at:  cd $INSTALL_DIR && docker compose logs agent"
        fi
        printf '.'; sleep 1
    done
    printf '\n'
    step "The agent is healthy"

    prune_old_images
    install_cli || true
    sign_in_cli
    print_summary
}

# daemon_proxy_advice is for a pull that failed while this installer has a
# proxy and the Docker daemon has none: images are pulled by the daemon, and
# what the shell is told about proxies does not reach it.
daemon_proxy_advice() {
    [ -n "$HTTPS_PROXY$HTTP_PROXY" ] || return 0
    [ -z "$(docker info --format '{{.HTTPSProxy}}{{.HTTPProxy}}' 2>/dev/null)" ] || return 0
    printf '\n%s\n%s\n%s' \
        "  This installer goes through a proxy, and the Docker daemon has none configured: images are" \
        "  pulled by the daemon. Add  \"proxies\": {\"http-proxy\": \"...\", \"https-proxy\": \"...\"}  to" \
        "  /etc/docker/daemon.json, restart Docker, and run the installer again."
}

# shipwick_here ARGS — the installed CLI, deaf to variables that point it at
# some other agent: what is saved here must describe this server.
shipwick_here() {
    ( unset SHIPWICK_AGENT_URL SHIPWICK_AGENT_TOKEN SHIPWICK_CONTEXT
      "$BIN_DIR/shipwick" "$@" )
}

# sign_in_cli saves this server as a context of the user running the
# installer, so that `shipwick ps` works here as it does on a laptop. The
# agent publishes no port: its hostname is the one address there is, and
# without one nothing is saved.
#
# The CLI writes its own file. It does not ask the agent first (--no-check):
# the token is the one the agent was just started with, and on a new server
# the hostname may have neither a DNS record nor a certificate yet.
sign_in_cli() {
    SIGNED_IN=""
    agent_domain="$(sed -n 's/^SHIPWICK_AGENT_DOMAIN=//p' "$INSTALL_DIR/.env" | tail -n 1)"
    token="$(sed -n 's/^SHIPWICK_AGENT_TOKEN=//p' "$INSTALL_DIR/.env" | tail -n 1)"
    if [ -z "$agent_domain" ] || [ -z "$token" ] || [ ! -x "$BIN_DIR/shipwick" ]; then
        return 0
    fi
    url="https://$agent_domain"

    # A CLI from before contexts fails here, and is left as it is.
    saved="$(shipwick_here context ls 2>/dev/null)" || return 0
    # Rows are "* name url" for the current context and "name url" for the others.
    name="$(printf '%s\n' "$saved" | awk -v url="$url" '$NF == url { if ($1 == "*") print $2; else print $1; exit }')"
    if [ -z "$name" ]; then
        # Servers saved here by hand stay as they are, the current one too.
        case "$saved" in
            "No saved servers."*) name="default" ;;
            *) SIGNED_IN="elsewhere"; return 0 ;;
        esac
    fi

    # Saving makes a context the current one; an upgrade must not change
    # which server the commands typed here talk to.
    current="$(shipwick_here context current 2>/dev/null || true)"
    # The token goes in on standard input, never as an argument.
    printf '%s' "$token" | shipwick_here login --context "$name" --url "$url" --token-stdin --no-check >/dev/null 2>&1 \
        || return 0
    SIGNED_IN="shipwick"
    if [ -n "$current" ] && [ "$current" != "$name" ]; then
        shipwick_here context use "$current" >/dev/null 2>&1 || true
        SIGNED_IN="shipwick --context $name"
    fi
    step "shipwick on this server is signed in"
}

# prune_old_images removes the Shipwick images of earlier releases. Every
# upgrade leaves the previous agent, dashboard and proxy images behind, up to
# a few hundred megabytes each; the running ones and anything else on the
# server are kept.
prune_old_images() {
    keep="$(docker compose config --images 2>/dev/null)"
    removed=0
    # `docker images` takes one repository at a time.
    for repo in ghcr.io/shipwick/agent ghcr.io/shipwick/dashboard ghcr.io/shipwick/caddy; do
        for image in $(docker images --format '{{.Repository}}:{{.Tag}}' "$repo" 2>/dev/null); do
            case " $keep " in *" $image "*) continue ;; esac
            docker image rm "$image" >/dev/null 2>&1 && removed=$((removed + 1))
        done
    done
    [ "$removed" -eq 0 ] || step "Removed $removed image(s) of earlier Shipwick releases"
}

print_summary() {
    agent_domain="$(sed -n 's/^SHIPWICK_AGENT_DOMAIN=//p' "$INSTALL_DIR/.env")"
    dashboard_domain="$(sed -n 's/^SHIPWICK_DASHBOARD_DOMAIN=//p' "$INSTALL_DIR/.env")"

    printf '\n%s\n\n' "${BOLD}Shipwick is running.${RESET}"
    if [ -n "${GENERATED_TOKEN:-}" ]; then
        info "${BOLD}API token${RESET} (also in $INSTALL_DIR/.env — it is root on this server, treat it so):"
        printf '\n      %s\n\n' "$GENERATED_TOKEN"
    else
        info "Your API token is unchanged: SHIPWICK_AGENT_TOKEN in $INSTALL_DIR/.env"
    fi
    if [ -n "$agent_domain" ]; then
        info "From your laptop or CI:   shipwick login --url https://$agent_domain"
        case "${SIGNED_IN:-}" in
            "") ;;
            elsewhere)
                info "shipwick on this server has other servers saved and was left as it is. To add this one:"
                info "                          shipwick login --context here --url https://$agent_domain" ;;
            *) info "On this server:           $SIGNED_IN ps" ;;
        esac
    else
        info "No API hostname was set, so the API is not exposed. Reach it through a tunnel:"
        info "  1. create $INSTALL_DIR/compose.override.yml:"
        info "         services:"
        info "           agent:"
        info "             ports: [\"127.0.0.1:9000:9000\"]"
        info "     and run:   cd $INSTALL_DIR && docker compose up -d"
        info "  2. on your laptop:   ssh -N -L 9000:127.0.0.1:9000 root@<this-server> &   shipwick login"
    fi
    [ -z "$dashboard_domain" ] || info "Dashboard:                https://$dashboard_domain"
    info "Open ports 80 and 443 (and 443/udp) — and nothing else — in your firewall."
    if grep -q '^SHIPWICK_ACME_DIRECTORY=.' "$INSTALL_DIR/.env"; then
        info "Certificates are obtained from the ACME server in SHIPWICK_ACME_DIRECTORY."
    elif [ -n "$BUNDLE" ]; then
        info "Certificates: a server with no way out cannot reach Let's Encrypt. Supply them with"
        info "  shipwick cert set <hostname> --cert <file> --key <file>"
        info "or set SHIPWICK_ACME_DIRECTORY in $INSTALL_DIR/.env to an ACME server of your own."
    elif grep -q '^SHIPWICK_CLOUDFLARE_API_TOKEN=.' "$INSTALL_DIR/.env"; then
        info "Certificates are obtained through Cloudflare DNS: hostnames may be proxied by"
        info "Cloudflare. Set the zone's SSL/TLS mode to Full (strict)."
    else
        info "DNS records must point straight at this server (at Cloudflare: DNS only). To keep"
        info "Cloudflare's proxy on, add SHIPWICK_CLOUDFLARE_API_TOKEN to $INSTALL_DIR/.env"
        info "and run:   cd $INSTALL_DIR && docker compose up -d"
    fi
    if [ -n "$BUNDLE" ]; then
        info "Upgrade later by making a bundle of the newer release and running its installer the same way."
    else
        info "Upgrade later by running this installer again."
    fi
    printf '\n'
}

# --- entry point --------------------------------------------------------------

# Spelled out rather than read from this file: piped into sh, there is no file.
usage() {
    cat <<EOF
Shipwick installer

  install.sh          install or upgrade the server (agent, Caddy, dashboard) and the CLI
  install.sh --cli    install the CLI only
  install.sh --bundle <directory or .tar.gz>
                      install or upgrade from files made elsewhere with "shipwick server bundle":
                      nothing is downloaded and nothing is pulled. Run from inside an unpacked
                      bundle, the option is not needed.

With a hostname for the API, the CLI on the server is signed in to it for the
user who runs the installer: the URL and the token are saved as a context, so
"shipwick ps" works there too. A context saved there by hand is not touched.

Environment:
  SHIPWICK_VERSION            release to install (default: latest)
  SHIPWICK_AGENT_DOMAIN       hostname for the API; asked for when run in a terminal
  SHIPWICK_DASHBOARD_DOMAIN   hostname for the dashboard; likewise
  SHIPWICK_WEBHOOK_URL        where the agent posts notifications (optional)
  SHIPWICK_WEBHOOK_SECRET     signs those requests (optional)
  SHIPWICK_CLOUDFLARE_API_TOKEN
                              a Cloudflare API token (Zone:Read, DNS:Edit): certificates
                              through DNS, so hostnames can stay behind Cloudflare's
                              proxy and can be wildcards (optional)
  SHIPWICK_BACKUP_PASSPHRASE  encrypts the backups the agent takes, and lets it back up
                              its own database and key (optional)
  SHIPWICK_BACKUP_S3_ENDPOINT, _BUCKET, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _REGION, _PREFIX
                              an S3-compatible bucket the backups are also sent to (optional)
  SHIPWICK_EXPORT_SCHEDULE, SHIPWICK_EXPORT_KEEP
                              an export of the whole server on a schedule (optional)
  SHIPWICK_STANDBY_SCHEDULE   makes this server a standby that imports those exports (optional)
  SHIPWICK_ALERT_MEMORY_PERCENT, SHIPWICK_ALERT_DISK_PERCENT
                              when a replica's memory and the disk raise an alert (90, 85)
  HTTPS_PROXY, HTTP_PROXY, NO_PROXY
                              a proxy between the server and the internet: used for the downloads
                              and kept for the agent and Caddy. The Docker daemon, which pulls
                              the images, has its own setting in /etc/docker/daemon.json
  SHIPWICK_CA_FILE            a PEM file of certificate authorities the agent trusts as well, as
                              the agent's container sees it; mount it in compose.override.yml
  SHIPWICK_DNS_RESOLVERS      who is asked whether a hostname points here: "system", or name
                              servers by address (default: public ones)
  SHIPWICK_ACME_DIRECTORY     an ACME server of your own to obtain certificates from
  SHIPWICK_AGENTS             several servers in this server's dashboard: name=URL pairs, such as
                              production=http://agent:9000,staging=https://agent.staging.example.com;
                              each has its own sign-in (optional)
  SHIPWICK_BUNDLE             the same as --bundle
  SHIPWICK_OIDC_ISSUER, SHIPWICK_OIDC_CLIENT_ID, SHIPWICK_OIDC_CLIENT_SECRET
                              an OpenID Connect provider people sign in to the dashboard
                              with; needs SHIPWICK_DASHBOARD_DOMAIN (optional)
  SHIPWICK_OIDC_SCOPES, SHIPWICK_OIDC_GROUPS_CLAIM
                              what is asked of the provider, and the claim that lists
                              groups ("openid email profile", groups)
  SHIPWICK_HTTP_PORT, SHIPWICK_HTTPS_PORT
                              the proxy's ports, when something else owns 80 and 443
  SHIPWICK_AGENT_IMAGE, SHIPWICK_DASHBOARD_IMAGE, SHIPWICK_CADDY_IMAGE
                              images of your own instead of the release's
  SHIPWICK_INSTALL_DIR        default /opt/shipwick
  SHIPWICK_BIN_DIR            default /usr/local/bin
EOF
}

main() {
    mode="server"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --cli) mode="cli" ;;
            --bundle)
                [ "$#" -ge 2 ] || die "--bundle needs the bundle's directory or .tar.gz"
                BUNDLE="$2"; shift ;;
            --bundle=*) BUNDLE="${1#--bundle=}" ;;
            -h|--help) usage; exit 0 ;;
            *) die "Unknown option: $1 (try --help)" ;;
        esac
        shift
    done

    # Downloads land here and move into place only once verified.
    WORK_DIR="$(mktemp -d)"
    trap 'rm -rf "$WORK_DIR"' EXIT

    if [ -n "$BUNDLE" ]; then
        printf '%s\n\n' "${BOLD}Shipwick installer${RESET} ${DIM}(from a bundle)${RESET}"
        open_bundle
    else
        printf '%s\n\n' "${BOLD}Shipwick installer${RESET} ${DIM}($REPO@$VERSION)${RESET}"
    fi
    if [ "$mode" = "cli" ]; then
        install_cli || exit 1
        printf '\n'; info "Next:   shipwick login"
    else
        install_server
    fi
}

main "$@"
