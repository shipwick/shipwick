#!/bin/sh
# Builds the files attached to a GitHub release into ./dist:
#
#   deployctl_<os>_<arch>[.exe]   the CLI, for every supported platform
#   compose.production.yml        with both images pinned to this version
#   checksums.txt                 SHA-256 of all of the above
#
#   sh scripts/build-release.sh v0.1.0
#
# The release workflow runs exactly this; run it yourself to see what a release
# would contain. The names are a contract with scripts/install.sh.

set -eu

VERSION="${1:-}"
case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "usage: $0 vMAJOR.MINOR.PATCH[-prerelease]" >&2; exit 2 ;;
esac
# Image tags carry no "v": ghcr.io/shipwick/agent:0.1.0
IMAGE_TAG="${VERSION#v}"

cd "$(dirname "$0")/.."
rm -rf dist
mkdir dist

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    os="${target%/*}"
    arch="${target#*/}"
    out="dist/deployctl_${os}_${arch}"
    [ "$os" = "windows" ] && out="$out.exe"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X github.com/shipwick/shipwick/pkg/version.Version=$VERSION" \
        -o "$out" ./cli/cmd/deployctl
    echo "built $out"
done

# A server installed from this release runs this release, not whatever
# "latest" means on the day its containers are recreated.
sed -e "s|\(ghcr\.io/shipwick/agent\):latest|\1:$IMAGE_TAG|" \
    -e "s|\(ghcr\.io/shipwick/dashboard\):latest|\1:$IMAGE_TAG|" \
    configs/compose.production.yml > dist/compose.production.yml
for image in agent dashboard; do
    grep -q "ghcr.io/shipwick/$image:$IMAGE_TAG" dist/compose.production.yml || {
        echo "could not pin the $image image in compose.production.yml" >&2
        exit 1
    }
done
echo "pinned images to $IMAGE_TAG in dist/compose.production.yml"

cd dist
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- * > ../checksums.txt
else
    shasum -a 256 -- * > ../checksums.txt
fi
mv ../checksums.txt checksums.txt
echo "wrote dist/checksums.txt"
