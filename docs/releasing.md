# Releasing

A release is a tag. Pushing it runs [release.yml](../.github/workflows/release.yml),
which — only after the whole CI suite passed on that commit — publishes:

| What | Where |
|---|---|
| `shipwick_<os>_<arch>` for Linux, macOS and Windows (amd64, arm64) | the GitHub release |
| `shipwick-agent_<arch>.deb` and `shipwick-agent_<arch>.rpm` (amd64, arm64): the agent as a plain binary with a systemd unit | the GitHub release |
| `compose.production.yml`, **all three images pinned to this version** | the GitHub release |
| `install.sh`, the installer as it is at the tag, for `shipwick server bundle` | the GitHub release |
| `ghcr.io/shipwick/agent`, `ghcr.io/shipwick/dashboard`, `ghcr.io/shipwick/caddy` for `linux/amd64` and `linux/arm64` | GitHub Container Registry |
| `image-digests.txt`: the digests of those images, read back from the registry | the GitHub release |
| `checksums.txt` (SHA-256 of every file above) | the GitHub release |

The installer takes everything from the release, never from a branch, and
verifies each file against `checksums.txt`. Because the compose file is pinned,
a server runs the version it installed until the installer is run again —
`latest` exists for people who write their own compose file.

`image-digests.txt` has one line for the manifest list of each image and two
for each platform, the manifest and the image's configuration:

```text
ghcr.io/shipwick/agent:0.7.0 index sha256:…
ghcr.io/shipwick/agent:0.7.0 manifest sha256:… linux/amd64
ghcr.io/shipwick/agent:0.7.0 config sha256:… linux/amd64
```

It is the one file that cannot be built before the images are pushed: each
image's job writes its lines with `scripts/image-digests.sh` right after the
push, and the last job puts them together, adds the file to `checksums.txt`
and publishes. `shipwick server bundle` and the installer hold images against
it (handbook §4, "A server with no way out").

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
   The first needs Docker as well as Go: the packages are put together by
   `dpkg-deb` and `rpmbuild` in containers (`scripts/build-packages.sh`).
4. Tag and push:
   ```bash
   git tag -a v0.2.0 -m v0.2.0
   git push origin v0.2.0
   ```

Image tags carry no `v`: the tag `v0.2.0` publishes `0.2.0`, `0.2` and `latest`.

The proxy's image is built from [Dockerfile.caddy](../Dockerfile.caddy), which
pins Caddy and its Cloudflare DNS module. It is tagged with Shipwick's version
like the other two, also when its content did not change. To move to a newer
Caddy, change `CADDY_VERSION` (and `CLOUDFLARE_DNS_VERSION` when the module
has a release for it) there, build it — `docker build -f Dockerfile.caddy .`
— check that `caddy list-modules` lists `dns.providers.cloudflare`, and deploy
something through the development stack before releasing.

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

## Dry run

**Actions → Release → Run workflow** runs the whole pipeline without publishing
anything: the checks, the binaries, the packages, every image for both
platforms, their digests, the trip of the files from one job to the next, the
notes. Nothing is pushed to ghcr.io and no release is created. The images are
pushed to a registry that runs inside each image's job and ends with it, so
that `image-digests.txt` is made the way a release makes it; its digests are
those of the dry run's images, under the names a release would give them.

Do this after changing `release.yml`, a Dockerfile or the release scripts — and
after merging an update of the actions the workflow uses. The CI of such a pull
request says nothing about it: the release workflow only runs on tags.

## If a release goes wrong

- **The workflow failed before "GitHub release"**: nothing users see has
  changed, except that images may exist under the new version's tag. Fix, delete
  the tag (`git push origin :v0.2.0`), tag again.
- **A bad release is out**: do not delete or re-tag it — servers and checksums
  out there refer to it. Release `v0.2.1`. If it is dangerous to install, mark
  it as a pre-release in the GitHub UI so the installer stops picking it.

## The packages

`shipwick-agent_<arch>.deb` and `.rpm` are built from
[packaging/linux](../packaging/linux): the unit, the settings file's template,
the compose file for the proxy and the dashboard (pinned to the release's
version like the release's own), and the scripts both formats run on
installation and removal. The file names carry no version, so that
`releases/latest/download/shipwick-agent_amd64.deb` is always the latest;
the version is inside, with a pre-release as `0.7.0~rc.1`, which sorts before
`0.7.0` for both package managers. The `.rpm` is built in a Rocky Linux 9
container and installs on 8 and later, and on Fedora.

They are not signed and there is no repository: they are files of the
release, verified against `checksums.txt` like the others. CI builds them on
every pull request and installs each in a container of its distribution.
After changing anything under `packaging/linux`, install the package on a
machine with systemd and Docker and start the agent: the unit's restrictions
are only tested by running under them.

## One-time setup

Things the workflow cannot do for itself:

- **Allow public packages in the organization**, or the next step is greyed
  out: Organization settings → Packages → Package creation → Public.
- **Make the packages public.** GHCR creates a package as private on its
  first push, and a server cannot pull a private image:
  github.com/orgs/shipwick/packages → each package → *Package settings* →
  *Change visibility* → Public. Do this right after the first (pre-)release
  that pushes a package — and again whenever a release adds one: `caddy`, the
  proxy's image, is new in 0.5 and starts private like the first two did.
  Until it is public, the installer of that release fails at "Could not
  pull". Releasing a candidate first (`v0.5.0-rc.1`) creates the package
  without moving anything users get by default.
- **`get.shipwick.com`** must answer with `scripts/install.sh` from `main` —
  a redirect to
  `https://raw.githubusercontent.com/shipwick/shipwick/main/scripts/install.sh`
  is enough (`curl -fsSL` follows it). The installer on `main` must therefore
  always be able to install the latest *release*.
- Repository settings: enable *Private vulnerability reporting* (SECURITY.md
  links to it), and protect `v*` tags so only maintainers can release.

## After the release

Two package managers carry the CLI, and neither reads the GitHub release on
its own:

- **Homebrew**: `shipwick/homebrew-tap` checks for a new release once a day and
  updates the formula itself. To do it now, run its *Update* workflow
  (`gh workflow run update.yml -R shipwick/homebrew-tap`).
- **winget**: generate the manifests with `packaging/winget/update-manifests.sh`
  — one installer entry for each Windows build, x64 and arm64 —
  and open the pull request to microsoft/winget-pkgs described in
  [packaging/winget/README.md](../packaging/winget/README.md).
