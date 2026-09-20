#!/bin/sh
# Shipwick installer.
#
#   curl -fsSL https://get.shipwick.com | sh                 # server: agent + Caddy + dashboard, and the CLI
#   curl -fsSL https://get.shipwick.com | sh -s -- --cli     # the CLI only (your laptop, CI)
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
    download "$(release_url "$1")" "$2" 2>/dev/null || return 1

    if [ ! -f "$WORK_DIR/checksums.txt" ]; then
        download "$(release_url checksums.txt)" "$WORK_DIR/checksums.txt" \
            || { rm -f "$2"; die "Could not download checksums.txt; refusing to install unverified files."; }
    fi
    expected="$(awk -v f="$1" '$2 == f || $2 == "*"f { print $1 }' "$WORK_DIR/checksums.txt")"
    [ -n "$expected" ] || { rm -f "$2"; die "checksums.txt has no entry for $1."; }
    if have sha256sum; then
        actual="$(sha256sum "$2" | awk '{print $1}')"
    else
        actual="$(shasum -a 256 "$2" | awk '{print $1}')"
    fi
    [ "$expected" = "$actual" ] \
        || { rm -f "$2"; die "Checksum mismatch for $1 (expected $expected, got $actual). Nothing was installed."; }
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
      for name in SHIPWICK_HTTP_PORT SHIPWICK_HTTPS_PORT SHIPWICK_AGENT_IMAGE SHIPWICK_DASHBOARD_IMAGE; do
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
    if [ -n "$COMPOSE_SOURCE" ]; then
        cp "$COMPOSE_SOURCE" "$INSTALL_DIR/$COMPOSE_FILE"
    else
        # The release's compose file pins both images to the release's version.
        # Verified in the work directory first: a bad download must not replace
        # the compose file of a running installation.
        fetch_release_asset compose.production.yml "$WORK_DIR/compose.yml" \
            || die "Could not download $(release_url compose.production.yml)"
        mv "$WORK_DIR/compose.yml" "$INSTALL_DIR/$COMPOSE_FILE"
    fi
    step "Installed $INSTALL_DIR/$COMPOSE_FILE"

    write_env
    cd "$INSTALL_DIR"

    if ! docker compose pull --quiet 2>/dev/null; then
        # Not fatal if every image the compose file names is here anyway: built
        # locally (then only Caddy's needs pulling), or pulled earlier.
        missing=""
        for image in $(docker compose config --images); do
            docker image inspect "$image" >/dev/null 2>&1 \
                || docker pull --quiet "$image" >/dev/null 2>&1 \
                || missing="$missing $image"
        done
        if [ -n "$missing" ]; then
            die "Could not pull:$missing
  Check this server's connection to the registry and run the installer again.
  To run images you built yourself, set SHIPWICK_AGENT_IMAGE and
  SHIPWICK_DASHBOARD_IMAGE in $INSTALL_DIR/.env."
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

    install_cli || true
    print_summary
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
    info "Upgrade later by running this installer again."
    printf '\n'
}

# --- entry point --------------------------------------------------------------

# Spelled out rather than read from this file: piped into sh, there is no file.
usage() {
    cat <<EOF
Shipwick installer

  install.sh          install or upgrade the server (agent, Caddy, dashboard) and the CLI
  install.sh --cli    install the CLI only

Environment:
  SHIPWICK_VERSION            release to install (default: latest)
  SHIPWICK_AGENT_DOMAIN       hostname for the API; asked for when run in a terminal
  SHIPWICK_DASHBOARD_DOMAIN   hostname for the dashboard; likewise
  SHIPWICK_INSTALL_DIR        default /opt/shipwick
  SHIPWICK_BIN_DIR            default /usr/local/bin
EOF
}

main() {
    mode="server"
    for arg in "$@"; do
        case "$arg" in
            --cli) mode="cli" ;;
            -h|--help) usage; exit 0 ;;
            *) die "Unknown option: $arg (try --help)" ;;
        esac
    done

    # Downloads land here and move into place only once verified.
    WORK_DIR="$(mktemp -d)"
    trap 'rm -rf "$WORK_DIR"' EXIT

    printf '%s\n\n' "${BOLD}Shipwick installer${RESET} ${DIM}($REPO@$VERSION)${RESET}"
    if [ "$mode" = "cli" ]; then
        install_cli || exit 1
        printf '\n'; info "Next:   shipwick login"
    else
        install_server
    fi
}

main "$@"
