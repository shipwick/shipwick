#!/bin/sh
# Writes the winget manifests for a release of shipwick/shipwick into
# manifests/s/Shipwick/Shipwick/<version>/, the layout microsoft/winget-pkgs
# expects. The checksums come from the release's checksums.txt: one installer
# entry for each Windows build the release has, x64 and arm64.
#
#   sh packaging/winget/update-manifests.sh            # the latest release
#   sh packaging/winget/update-manifests.sh v0.2.0     # a specific one
#
# Publishing is a pull request to https://github.com/microsoft/winget-pkgs
# with the generated directory; see README.md next to this script.

set -eu

REPO="shipwick/shipwick"
cd "$(dirname "$0")"

tag="${1:-}"
if [ -z "$tag" ]; then
    tag="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
    tag="${tag##*/}"
fi
case "$tag" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "not a release tag: '$tag'" >&2; exit 1 ;;
esac
case "$tag" in *-*) echo "refusing a pre-release: $tag" >&2; exit 1 ;; esac
version="${tag#v}"

# SHIPWICK_CHECKSUMS names a checksums.txt on disk instead, such as the one
# scripts/build-release.sh wrote to ./dist: the manifests of a release can be
# looked at before it is published.
if [ -n "${SHIPWICK_CHECKSUMS:-}" ]; then
    sums="$(cat "$SHIPWICK_CHECKSUMS")"
else
    sums="$(curl -fsSL "https://github.com/$REPO/releases/download/$tag/checksums.txt")"
fi

# installer WINGET_ARCH GO_ARCH — the entry for one build of the release;
# nothing when the release does not have it.
installer() {
    asset="shipwick_windows_$2.exe"
    sum="$(printf '%s\n' "$sums" | awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }')"
    [ "${#sum}" -eq 64 ] || return 0
    cat <<YAML
- Architecture: $1
  InstallerUrl: https://github.com/$REPO/releases/download/$tag/$asset
  InstallerSha256: $(printf '%s' "$sum" | tr '[:lower:]' '[:upper:]')
YAML
}
# winget picks the entry for the machine: an arm64 Windows gets the arm64
# build from 0.7.0 on, and the x64 one, which it runs too, before that.
installers="$(installer x64 amd64; installer arm64 arm64)"
case "$installers" in
    *"Architecture: x64"*) ;;
    *) echo "checksums.txt of $tag has no entry for shipwick_windows_amd64.exe" >&2; exit 1 ;;
esac

dir="manifests/s/Shipwick/Shipwick/$version"
mkdir -p "$dir"

cat > "$dir/Shipwick.Shipwick.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.1.10.0.schema.json
PackageIdentifier: Shipwick.Shipwick
PackageVersion: $version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.10.0
YAML

cat > "$dir/Shipwick.Shipwick.installer.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.1.10.0.schema.json
PackageIdentifier: Shipwick.Shipwick
PackageVersion: $version
InstallerType: portable
Commands:
- shipwick
ReleaseDate: $(date -u +%Y-%m-%d)
Installers:
$installers
ManifestType: installer
ManifestVersion: 1.10.0
YAML

cat > "$dir/Shipwick.Shipwick.locale.en-US.yaml" <<YAML
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.1.10.0.schema.json
PackageIdentifier: Shipwick.Shipwick
PackageVersion: $version
PackageLocale: en-US
Publisher: The Shipwick Authors
PublisherUrl: https://shipwick.com
PublisherSupportUrl: https://github.com/$REPO/issues
PackageName: Shipwick
PackageUrl: https://shipwick.com
License: Apache-2.0
LicenseUrl: https://github.com/$REPO/blob/main/LICENSE
ShortDescription: Production deployments on your own server
Description: |-
  shipwick is the command-line client of Shipwick, which runs Docker applications on a single server: health checks, rolling deployments, rollbacks, resource limits and HTTPS from one small config file. This package installs the client; the server side is installed on the server itself.
Moniker: shipwick
Tags:
- deploy
- deployment
- docker
- self-hosted
ReleaseNotesUrl: https://github.com/$REPO/releases/tag/$tag
ManifestType: defaultLocale
ManifestVersion: 1.10.0
YAML

echo "wrote $dir"
