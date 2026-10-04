# Releasing

A release is a tag. Pushing it runs [release.yml](../.github/workflows/release.yml),
which — only after the whole CI suite passed on that commit — publishes:

| What | Where |
|---|---|
| `shipwick_<os>_<arch>` for Linux, macOS and Windows (amd64, arm64) | the GitHub release |
| `shipwick-agent_<arch>.deb` and `shipwick-agent_<arch>.rpm` (amd64, arm64): the agent as a plain binary with a systemd unit | the GitHub release |
| `<binary>.spdx.json` for each of those CLI binaries, and `shipwick-agent_<arch>.spdx.json` for the agent in the packages: what each was built from | the GitHub release |
| `compose.production.yml`, **all three images pinned to this version** | the GitHub release |
| `install.sh`, the installer as it is at the tag, for `shipwick server bundle` | the GitHub release |
| `ghcr.io/shipwick/agent`, `ghcr.io/shipwick/dashboard`, `ghcr.io/shipwick/caddy` for `linux/amd64` and `linux/arm64` | GitHub Container Registry |
| `image-digests.txt`: the digests of those images, read back from the registry | the GitHub release |
| `checksums.txt` (SHA-256 of every file above) | the GitHub release |
| `checksums.txt.sigstore.json`: the signature of `checksums.txt`, made by the workflow run | the GitHub release |
| A signature of each image, by digest, and a provenance attestation for each image and for every file `checksums.txt` lists | next to the image in the registry; GitHub's attestation store |

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

## What is signed, and by whom

Nothing is signed with a key, and none is stored. A workflow run asks GitHub
for a token that says which workflow it is and for which ref it runs;
Sigstore issues a certificate for that identity, valid for ten minutes, and
the signature is recorded in its public log. The identity of a release is the
workflow file at the tag:

```text
https://github.com/shipwick/shipwick/.github/workflows/release.yml@refs/tags/v0.8.0
issued by https://token.actions.githubusercontent.com
```

Whoever verifies names both, and a signature made anywhere else — another
repository, another workflow, a branch, a dry run — does not pass.

| What | How | With |
|---|---|---|
| Every file of the release | `checksums.txt` lists them and is signed: `checksums.txt.sigstore.json`, a file of the release (and the only one `checksums.txt` does not list) | `cosign sign-blob` |
| Each image | The manifest list its tag points at, the `index` line of `image-digests.txt`, is signed by digest; the signature lies next to the image in the registry | `cosign sign` |
| Provenance | One attestation for the files `checksums.txt` lists and one for each image's manifest list: repository, commit, workflow and run that produced them | `actions/attest` |
| Bills of materials | For each binary a `.spdx.json` among the release's files, written from the binary by syft (`scripts/build-sbom.sh`). For each image and platform one written by BuildKit and attached to the manifest list, with BuildKit's record of the build | syft; `sbom` and `provenance` of `docker/build-push-action` |

The bills of materials are SPDX 2.3: it is what BuildKit writes for an image,
so the binaries and the images are described in one format, and syft reads a
Go binary's own record of its modules into it. They are covered by the
signatures rather than signed again: a binary's is listed in `checksums.txt`,
an image's is a manifest of the signed list.

The workflow verifies each signature against its own identity right after
making it, and publishes nothing if that fails. The installer and `shipwick
upgrade` verify `checksums.txt` when cosign is installed (handbook §4 and
§12, which has the commands to do it by hand).

Two versions are pinned by hand, because nothing updates them:
`SYFT_IMAGE` in `scripts/build-sbom.sh` and `COSIGN_IMAGE` in
`scripts/test-install.sh`. The cosign that signs is the one
`sigstore/cosign-installer` installs by default, and moves with that action.

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
   `dpkg-deb` and `rpmbuild` in containers (`scripts/build-packages.sh`), and
   the bills of materials are written by syft in one. What it cannot make is
   the signature: only the workflow has the identity.
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
upgrade path: those are only really tested on a real server. And whenever it
touches the signing steps: the provenance attestations run on a tag only, and
a candidate's signatures are verified the same way as a release's, with its
own tag in the identity ([After the release](#after-the-release)).

## Dry run

**Actions → Release → Run workflow** runs the whole pipeline without publishing
anything: the checks, the binaries, the packages, every image for both
platforms, their digests, the trip of the files from one job to the next, the
notes. Nothing is pushed to ghcr.io and no release is created. The images are
pushed to a registry that runs inside each image's job and ends with it, so
that `image-digests.txt` is made the way a release makes it; its digests are
those of the dry run's images, under the names a release would give them.

A dry run signs as well — `checksums.txt` and the images in its own
registries — and verifies what it signed, so that a change to the signing
steps is tried before a tag depends on it. Its identity is the workflow at
the branch it was started from (`…/release.yml@refs/heads/main`), which no
verification of a release accepts; the entries it leaves in Sigstore's log
say that this repository's workflow signed some digest on a branch, and
nothing else. The provenance attestations are skipped: GitHub keeps an
attestation for good, and these would describe files nobody was given. They
run for the first time on a tag, which is one more reason for a release
candidate.

Do this after changing `release.yml`, a Dockerfile or the release scripts — and
after merging an update of the actions the workflow uses. The CI of such a pull
request says nothing about it: the release workflow only runs on tags.

## If a release goes wrong

- **The workflow failed before "GitHub release"**: nothing users see has
  changed, except that images may exist under the new version's tag. Fix, delete
  the tag (`git push origin :v0.2.0`), tag again. Signatures and attestations
  of the failed run stay where they are, in Sigstore's log and at GitHub:
  they are about digests, and the second run's images and files have others.
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

They carry no signature of their own (`dpkg-sig`, `rpm --addsign`: both need
a key to keep) and there is no repository: they are files of the release,
verified against `checksums.txt` like the others, and signed with it. CI builds them on
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

**Verify what was published**, from a machine that is not the runner, with
cosign 3 and the tag in place of `v0.8.0`:

```bash
tag=v0.8.0
signer=https://github.com/shipwick/shipwick/.github/workflows/release.yml@refs/tags/$tag
issuer=https://token.actions.githubusercontent.com

curl -fsSLO https://github.com/shipwick/shipwick/releases/download/$tag/checksums.txt
curl -fsSLO https://github.com/shipwick/shipwick/releases/download/$tag/checksums.txt.sigstore.json
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity "$signer" --certificate-oidc-issuer "$issuer"

for image in agent dashboard caddy; do
  cosign verify "ghcr.io/shipwick/$image:${tag#v}" \
    --certificate-identity "$signer" --certificate-oidc-issuer "$issuer" > /dev/null
done

curl -fsSLO https://github.com/shipwick/shipwick/releases/download/$tag/shipwick_linux_amd64
gh attestation verify shipwick_linux_amd64 --repo shipwick/shipwick --cert-identity "$signer"
gh attestation verify "oci://ghcr.io/shipwick/agent:${tag#v}" --repo shipwick/shipwick --cert-identity "$signer"
```

Each command exits with 0 only if the signature is that workflow's at that
tag; `gh` has to be signed in (`gh auth login`). Then let the installer do
it, as a server with cosign would, and refuse anything less than a verified
signature:

```bash
sh scripts/test-install.sh --cosign --version v0.8.0 debian:13
```

Two package managers carry the CLI, and neither reads the GitHub release on
its own:

- **Homebrew**: `shipwick/homebrew-tap` checks for a new release once a day and
  updates the formula itself. To do it now, run its *Update* workflow
  (`gh workflow run update.yml -R shipwick/homebrew-tap`).
- **winget**: generate the manifests with `packaging/winget/update-manifests.sh`
  — one installer entry for each Windows build, x64 and arm64 —
  and open the pull request to microsoft/winget-pkgs described in
  [packaging/winget/README.md](../packaging/winget/README.md).
