#!/bin/sh
# Prints the release notes for a version: its section of CHANGELOG.md.
#
#   sh scripts/release-notes.sh v0.1.0
#
# A pre-release (v0.1.0-rc.1) has no section of its own; it gets the one of the
# version it leads up to, or "Unreleased". A final release without a section
# fails — releasing is the moment the changelog must be written.

set -eu

version="${1:-}"
version="${version#v}"
[ -n "$version" ] || { echo "usage: $0 vMAJOR.MINOR.PATCH[-prerelease]" >&2; exit 2; }

cd "$(dirname "$0")/.."

section() { # section NAME — the body under "## [NAME]", without the link references
    awk -v heading="## [$1]" '
        index($0, heading) == 1 { printing = 1; next }
        /^## \[/                { printing = 0 }
        /^\[[^]]+\]: /          { next }
        printing
    ' CHANGELOG.md
}

# has_text — true if stdin contains anything but blank lines
has_text() { grep -q '[^[:space:]]'; }

notes="$(section "$version")"
case "$version" in
    *-*)
        printf '%s' "$notes" | has_text || notes="$(section "${version%%-*}")"
        printf '%s' "$notes" | has_text || notes="$(section Unreleased)"
        notes="This is a pre-release of ${version%%-*}, for testing. The installer only picks it when asked to: \`SHIPWICK_VERSION=v$version\`.

$notes"
        ;;
esac

printf '%s' "$notes" | has_text || {
    echo "CHANGELOG.md has no section \"## [$version]\". Write it, then tag again." >&2
    exit 1
}
printf '%s\n' "$notes"
