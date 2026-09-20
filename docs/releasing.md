# Releasing

A release is a tag. Pushing it runs [release.yml](../.github/workflows/release.yml),
which — only after the whole CI suite passed on that commit — publishes:

| What | Where |
|---|---|
| `deployctl_<os>_<arch>` for Linux, macOS (amd64, arm64) and Windows (amd64) | the GitHub release |
| `compose.production.yml`, **both images pinned to this version** | the GitHub release |
| `checksums.txt` (SHA-256 of the files above) | the GitHub release |
| `ghcr.io/shipwick/agent`, `ghcr.io/shipwick/dashboard` for `linux/amd64` and `linux/arm64` | GitHub Container Registry |

The installer takes everything from the release, never from a branch, and
verifies each file against `checksums.txt`. Because the compose file is pinned,
a server runs the version it installed until the installer is run again —
`latest` exists for people who write their own compose file.

## Cutting a release

1. `main` is green.
2. In `CHANGELOG.md`, rename *Unreleased* to the version with today's date,
   start a new empty *Unreleased*, and update the two links at the bottom.
   The section becomes the release notes; the workflow refuses to release a
   version that has none.
3. Optional but cheap — see what would be published:
   ```bash
   sh scripts/build-release.sh v0.2.0    # → ./dist
   sh scripts/release-notes.sh v0.2.0
   ```
4. Tag and push:
   ```bash
   git tag -a v0.2.0 -m v0.2.0
   git push origin v0.2.0
   ```

Image tags carry no `v`: the tag `v0.2.0` publishes `0.2.0`, `0.2` and `latest`.

### Release candidates

A tag with a suffix — `v0.2.0-rc.1` — is published as a GitHub *pre-release*,
and its images only as `0.2.0-rc.1`. Nothing a user gets by default changes:
the installer follows GitHub's "latest release", which skips pre-releases, and
`latest` does not move. Install one deliberately:

```bash
curl -fsSL https://get.shipwick.com | SHIPWICK_VERSION=v0.2.0-rc.1 sh
```

Use one whenever a release touches the installer, the compose file or the
upgrade path: those are only really tested on a real server.

## If a release goes wrong

- **The workflow failed before "GitHub release"**: nothing users see has
  changed, except that images may exist under the new version's tag. Fix, delete
  the tag (`git push origin :v0.2.0`), tag again.
- **A bad release is out**: do not delete or re-tag it — servers and checksums
  out there refer to it. Release `v0.2.1`. If it is dangerous to install, mark
  it as a pre-release in the GitHub UI so the installer stops picking it.

## One-time setup

Things the workflow cannot do for itself:

- **Make the two packages public.** GHCR creates packages as private on the
  first push, and a server cannot pull a private image:
  github.com/orgs/shipwick/packages → each package → *Package settings* →
  *Change visibility* → Public. Do this right after the first (pre-)release.
- **`get.shipwick.com`** must answer with `scripts/install.sh` from `main` —
  a redirect to
  `https://raw.githubusercontent.com/shipwick/shipwick/main/scripts/install.sh`
  is enough (`curl -fsSL` follows it). The installer on `main` must therefore
  always be able to install the latest *release*.
- Repository settings: enable *Private vulnerability reporting* (SECURITY.md
  links to it), and protect `v*` tags so only maintainers can release.
