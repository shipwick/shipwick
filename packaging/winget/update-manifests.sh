#!/bin/sh
# Writes the winget manifests for a release of shipwick/shipwick into
# manifests/s/Shipwick/Shipwick/<version>/, the layout microsoft/winget-pkgs
# expects. The checksum comes from the release's checksums.txt.
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

sum="$(curl -fsSL "https://github.com/$REPO/releases/download/$tag/checksums.txt" | awk '$2 == "shipwick_windows_amd64.exe" { print $1 }')"
[ "${#sum}" -eq 64 ] || { echo "checksums.txt of $tag has no entry for shipwick_windows_amd64.exe" >&2; exit 1; }

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
- Architecture: x64
  InstallerUrl: https://github.com/$REPO/releases/download/$tag/shipwick_windows_amd64.exe
  InstallerSha256: $(printf '%s' "$sum" | tr '[:lower:]' '[:upper:]')
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
