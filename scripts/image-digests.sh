#!/bin/sh
# Prints the digests of one image of a release as the registry holds it, in
# the lines of the release's image-digests.txt:
#
#   <name> index    sha256:…                  the manifest list the tag points at
#   <name> manifest sha256:… linux/amd64      one platform's manifest
#   <name> config   sha256:… linux/amd64      and its configuration, the image's ID
#
#   sh scripts/image-digests.sh ghcr.io/shipwick/agent:0.7.0
#   sh scripts/image-digests.sh localhost:5000/shipwick/agent:0.7.0 ghcr.io/shipwick/agent:0.7.0
#
# The first argument is where to read, the second the name to write when that
# differs: a dry run of the release pushes to a registry of its own, and a
# digest does not depend on where the image lies. Everything is read back
# from the registry, after the push: the file says what a server will be
# given, not what the build meant to produce.
#
# `shipwick server bundle` checks the archive it saves against these lines,
# and the installer checks what `docker load` made of it (the ID is the
# manifest's digest or the configuration's, depending on how the Docker there
# stores images). Needs docker buildx and jq.

set -eu

ref="${1:-}"
name="${2:-$ref}"
[ -n "$ref" ] || { echo "usage: $0 <image>:<tag> [name to write]" >&2; exit 2; }
repo="${ref%:*}"

index="$(docker buildx imagetools inspect --raw "$ref")"
case "$(printf '%s' "$index" | jq -r '.mediaType // ""')" in
    application/vnd.oci.image.index.v1+json|application/vnd.docker.distribution.manifest.list.v2+json) ;;
    *) echo "$ref is not a multi-platform image: the release publishes one manifest list per image" >&2; exit 1 ;;
esac
# The digest of a manifest is the SHA-256 of its bytes, which a shell variable
# does not keep: the registry is asked for it.
digest="$(docker buildx imagetools inspect "$ref" --format '{{json .Manifest}}' | jq -r '.digest')"
case "$digest" in
    sha256:*) ;;
    *) echo "could not read the digest of $ref" >&2; exit 1 ;;
esac
echo "$name index $digest"

# Attestations are manifests of the list too, for the platform unknown/unknown.
platforms="$(printf '%s' "$index" | jq -r '.manifests[] | select(.platform.os != "unknown") | "\(.digest) \(.platform.os)/\(.platform.architecture)"')"
[ -n "$platforms" ] || { echo "$ref lists no platform" >&2; exit 1; }
printf '%s\n' "$platforms" | while read -r manifest platform; do
    config="$(docker buildx imagetools inspect --raw "$repo@$manifest" | jq -r '.config.digest // ""')"
    case "$config" in
        sha256:*) ;;
        *) echo "could not read the configuration of $ref for $platform" >&2; exit 1 ;;
    esac
    echo "$name manifest $manifest $platform"
    echo "$name config $config $platform"
done
