#!/bin/sh
# Builds the packages of the agent as a plain binary into ./dist:
#
#   shipwick-agent_<arch>.deb    for Debian and Ubuntu
#   shipwick-agent_<arch>.rpm    for Fedora, RHEL and their relatives
#   shipwick-agent_<arch>.spdx.json   what the agent in both was built from
#
#   sh scripts/build-packages.sh v0.7.0
#
# The agent is compiled here, for amd64 and arm64; the packages are put
# together by dpkg-deb and rpmbuild in containers (packaging/linux/build-deb.sh
# and build-rpm.sh), so the machine needs Go and Docker and neither tool. The
# files go in and the packages come out as a tar stream: nothing is mounted.
#
# Run by scripts/build-release.sh, before it writes the checksums.

set -eu

VERSION="${1:-}"
case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "usage: $0 vMAJOR.MINOR.PATCH[-prerelease]" >&2; exit 2 ;;
esac
case "$VERSION" in
    *[!A-Za-z0-9.-]*) echo "$VERSION: a version is letters, digits, dots and hyphens" >&2; exit 2 ;;
esac
IMAGE_TAG="${VERSION#v}"

DEB_IMAGE="${SHIPWICK_DEB_IMAGE:-debian:stable-slim}"
RPM_IMAGE="${SHIPWICK_RPM_IMAGE:-rockylinux/rockylinux:9}"

cd "$(dirname "$0")/.."
command -v docker >/dev/null 2>&1 || { echo "docker is needed to build the packages: dpkg-deb and rpmbuild run in containers" >&2; exit 1; }
mkdir -p dist

src="$(mktemp -d)"
trap 'rm -rf "$src"' EXIT

for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X github.com/shipwick/shipwick/pkg/version.Version=$VERSION" \
        -o "$src/shipwick-agent_$arch" ./agent/cmd/shipwick-agent
    # The .deb and the .rpm of an architecture hold this one binary.
    sh scripts/build-sbom.sh "$VERSION" "$src/shipwick-agent_$arch" "dist/shipwick-agent_$arch.spdx.json"
done
cp packaging/linux/shipwick-agent.service packaging/linux/agent.env \
    packaging/linux/postinst.sh packaging/linux/prerm.sh packaging/linux/postrm.sh \
    packaging/linux/build-deb.sh packaging/linux/build-rpm.sh "$src/"
cp LICENSE NOTICE "$src/"
# As in the release's compose file: a package runs the proxy and the dashboard
# of its own version.
sed -e "s|\(ghcr\.io/shipwick/dashboard\):latest|\1:$IMAGE_TAG|" \
    -e "s|\(ghcr\.io/shipwick/caddy\):latest|\1:$IMAGE_TAG|" \
    packaging/linux/compose.yml > "$src/compose.yml"
for image in dashboard caddy; do
    grep -q "ghcr.io/shipwick/$image:$IMAGE_TAG" "$src/compose.yml" || {
        echo "could not pin the $image image in the package's compose.yml" >&2
        exit 1
    }
done

# package IMAGE SCRIPT — runs SCRIPT in a container of IMAGE over the files
# in $src and unpacks what it built into dist. What the script prints goes to
# standard error; standard output is the packages.
package() {
    tar -C "$src" -cf - . | docker run --rm -i "$1" sh -c '
        set -eu
        mkdir -p /src /out
        tar -xf - -C /src
        sh "/src/$1" "$2" >&2
        tar -C /out -cf - .' sh "$2" "$IMAGE_TAG" | tar -C dist -xf -
}
package "$DEB_IMAGE" build-deb.sh
package "$RPM_IMAGE" build-rpm.sh

for file in shipwick-agent_amd64.deb shipwick-agent_arm64.deb shipwick-agent_amd64.rpm shipwick-agent_arm64.rpm; do
    [ -s "dist/$file" ] || { echo "dist/$file was not built" >&2; exit 1; }
done
echo "built the packages in dist/"
