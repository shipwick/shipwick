#!/bin/sh
# Builds shipwick-agent_<arch>.deb for amd64 and arm64 from the files in /src,
# into /out. Run by scripts/build-packages.sh in a debian:stable-slim
# container: it needs dpkg-deb and nothing else, and the binaries arrive
# built.
#
#   sh build-deb.sh 0.7.0

set -eu

VERSION="${1:?usage: build-deb.sh <version without v>}"
# 0.7.0-rc.1 sorts after 0.7.0 for dpkg; 0.7.0~rc.1 before it.
DEB_VERSION="$(printf '%s' "$VERSION" | sed 's/-/~/')"
SRC="${SRC:-/src}"
OUT="${OUT:-/out}"

# maintainer_script NAME PREAMBLE — writes DEBIAN/NAME: the script the .rpm
# shares, after the lines that turn dpkg's arguments into its own.
maintainer_script() {
    { printf '%s\n' '#!/bin/sh' 'set -e' "$2"; sed '1d' "$SRC/$1.sh"; } > "$root/DEBIAN/$1"
    chmod 0755 "$root/DEBIAN/$1"
}

mkdir -p "$OUT"
for arch in amd64 arm64; do
    root="$(mktemp -d)"
    chmod 0755 "$root"
    install -D -m 0755 "$SRC/shipwick-agent_$arch" "$root/usr/bin/shipwick-agent"
    install -D -m 0644 "$SRC/shipwick-agent.service" "$root/usr/lib/systemd/system/shipwick-agent.service"
    install -D -m 0644 "$SRC/agent.env" "$root/usr/share/shipwick/agent.env"
    install -D -m 0644 "$SRC/compose.yml" "$root/usr/share/shipwick/compose.yml"
    install -D -m 0644 "$SRC/NOTICE" "$root/usr/share/doc/shipwick-agent/NOTICE"
    # The license itself is in every Debian system; the file says whose work
    # this is and points there.
    cat > "$root/usr/share/doc/shipwick-agent/copyright" <<EOF
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: Shipwick
Source: https://github.com/shipwick/shipwick

Files: *
Copyright: $(sed -n 's/^Copyright //p' "$SRC/NOTICE")
License: Apache-2.0
 Licensed under the Apache License, Version 2.0 (the "License"); you may not
 use this software except in compliance with the License.
 .
 On Debian systems, the complete text of the Apache License, Version 2.0 can
 be found in /usr/share/common-licenses/Apache-2.0.
EOF
    printf '%s\n' "shipwick-agent ($DEB_VERSION) stable; urgency=medium" "" \
        "  * Release $VERSION; its notes are at" \
        "    https://github.com/shipwick/shipwick/releases/tag/v$VERSION" "" \
        " -- The Shipwick Authors <security@shipwick.com>  $(date -R)" \
        | gzip -9n > "$root/usr/share/doc/shipwick-agent/changelog.gz"
    chmod 0644 "$root/usr/share/doc/shipwick-agent/copyright" "$root/usr/share/doc/shipwick-agent/changelog.gz"

    mkdir -p "$root/DEBIAN"
    # shellcheck disable=SC2016  # the preambles are script text, expanded when dpkg runs them
    {
        maintainer_script postinst '[ "$1" = "configure" ] || exit 0
if [ -n "${2:-}" ]; then set -- upgrade; else set -- install; fi'
        maintainer_script prerm '[ "$1" = "remove" ] || exit 0'
        maintainer_script postrm ''
    }
    size="$(du -sk "$root/usr" | cut -f1)"
    cat > "$root/DEBIAN/control" <<EOF
Package: shipwick-agent
Version: $DEB_VERSION
Architecture: $arch
Maintainer: The Shipwick Authors <security@shipwick.com>
Installed-Size: $size
Recommends: systemd, docker-ce | docker.io | moby-engine, docker-compose-plugin | docker-compose-v2
Section: admin
Priority: optional
Homepage: https://shipwick.com
Description: Production deployments on your own server (the agent)
 The Shipwick agent as a service of the host: it runs Docker applications on
 this server with health checks, rolling deployments and rollbacks, and
 answers the shipwick command-line client and the dashboard.
 .
 The package holds the agent, its systemd unit and its settings file. The
 reverse proxy and the dashboard run as containers, from the compose file in
 /usr/share/shipwick. Docker Engine is needed and is not installed by this
 package.
EOF
    # Owned by root whoever builds; xz as every dpkg since Debian 7 reads it.
    dpkg-deb --root-owner-group -Zxz --build "$root" "$OUT/shipwick-agent_$arch.deb" >/dev/null
    rm -rf "$root"
    echo "built shipwick-agent_$arch.deb ($DEB_VERSION)"
done
