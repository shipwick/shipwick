#!/bin/sh
# Writes the software bill of materials of one compiled binary: the Go modules
# it was linked from and the Go it was compiled with, as the binary itself
# records them, in SPDX 2.3 JSON.
#
#   sh scripts/build-sbom.sh v0.8.0 dist/shipwick_linux_amd64 dist/shipwick_linux_amd64.spdx.json
#
# The binary is read, not go.mod: a module the linker left out is not listed,
# and the Windows build lists what only it links. Read by syft in a container,
# so the machine needs Docker and no other tool; the binary goes in as a tar
# stream and the document comes out on standard output: nothing is mounted.
#
# Run by scripts/build-release.sh and scripts/build-packages.sh for every
# binary of a release. The images carry theirs with them: the release
# workflow has BuildKit write one per platform (docs/releasing.md).

set -eu

VERSION="${1:-}"
binary="${2:-}"
out="${3:-}"
if [ -z "$VERSION" ] || [ ! -f "$binary" ] || [ -z "$out" ]; then
    echo "usage: $0 <version> <binary> <file to write>" >&2
    exit 2
fi

# Pinned: a newer syft may describe the same binary differently. Moved by
# hand, with a look at what it writes.
SYFT_IMAGE="${SHIPWICK_SYFT_IMAGE:-anchore/syft:v1.54.0}"

command -v docker >/dev/null 2>&1 || { echo "docker is needed to write $out: syft runs in a container" >&2; exit 1; }

name="$(basename "$binary")"
# Git Bash would rewrite the container's paths into Windows ones.
export MSYS_NO_PATHCONV=1

container="$(docker create "$SYFT_IMAGE" scan "file:/$name" --output spdx-json \
    --source-name "$name" --source-version "$VERSION" --quiet)"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

tar -C "$(dirname "$binary")" -cf - "$name" | docker cp - "$container:/"
docker start --attach "$container" > "$out.partial"
# A binary without build information yields a document with no packages:
# the release would then publish a bill of materials that lists nothing.
grep -q '"name": *"stdlib"' "$out.partial" || {
    rm -f "$out.partial"
    echo "$binary: syft found no Go build information in it" >&2
    exit 1
}
mv "$out.partial" "$out"
echo "wrote $out"
