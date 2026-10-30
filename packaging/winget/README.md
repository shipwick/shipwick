# winget

Manifests for installing the CLI with the Windows Package Manager:

```powershell
winget install Shipwick.Shipwick
```

`update-manifests.sh` writes the manifests for a release into
`manifests/s/Shipwick/Shipwick/<version>/`, the layout of
[microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs), in the
schema version that repository accepts (1.28.0; it deprecates older ones, and
a pull request in a deprecated version is not merged). The checksums come
from the release's `checksums.txt` and the date from the release's
publication; nothing is typed by hand. On Windows the script ends with
`winget validate`, the check the repository's automation runs first;
elsewhere it says so and leaves the check to a Windows machine.

```sh
sh packaging/winget/update-manifests.sh            # the latest release
sh packaging/winget/update-manifests.sh v0.2.0     # a specific one
winget validate --manifest packaging/winget/manifests/s/Shipwick/Shipwick/0.2.0
```

The installer is `portable`: winget copies the build for the machine —
`shipwick_windows_amd64.exe` or `shipwick_windows_arm64.exe` — into its links
directory as `shipwick.exe` and puts it on `PATH`, which is what `Commands` in
the installer manifest declares. The manifest has one installer entry for each
Windows build the release's `checksums.txt` lists: `x64` and, from 0.7.0 on,
`arm64`. For a release before that it lists `x64` alone, which an arm64
Windows runs as well.

## Publishing a version

winget has no equivalent of a Homebrew tap: every version is a pull request to
microsoft/winget-pkgs, from a fork, checked by Microsoft's automation and
merged by a moderator. The release workflow does this:

1. The *GitHub release* job writes the manifests from the release's
   `checksums.txt` with `update-manifests.sh` and publishes them as
   `winget-manifests.zip`, a file of the release listed in `checksums.txt`
   and signed with the rest.
2. The *winget* job, on a Windows runner, validates them with `winget
   validate` and, when the `WINGET_TOKEN` secret is set, submits them with
   [wingetcreate](https://github.com/microsoft/winget-create): it forks
   winget-pkgs under the token's account if that account has no fork yet,
   pushes the version directory to a branch of the fork and opens the pull
   request, titled `New package: Shipwick.Shipwick version 1.0.0` while the
   package has no directory in winget-pkgs and `Update: …` afterwards. A
   pre-release is not submitted; a dry run of the workflow stops after the
   validation.
3. The automation validates the manifest and installs the package in a
   sandbox; a moderator merges. Expect a day or two the first time.

The manifests of a release are therefore no longer committed here. The
directories under `manifests/` are the versions that were, up to 1.0.0; they
stay as they are.

### What the maintainer sets up once

`WINGET_TOKEN` is a GitHub personal access token that wingetcreate uses to
fork, push and open the pull request. Its
[documentation](https://github.com/microsoft/winget-create/blob/main/doc/token.md)
says which: a **classic** token with the **`public_repo`** scope — fine-grained
tokens are not supported ([winget-create#595](https://github.com/microsoft/winget-create/issues/595))
— and, optionally, `delete_repo`, so that the tool can remove a fork it
created itself when the submission fails. A classic token is not limited to one
repository: `public_repo` writes to every public repository the account can
write to. Give it to an account that has nothing else — a second GitHub
account whose only repository is the fork of winget-pkgs — rather than to the
maintainer's own, and sign Microsoft's Contributor License Agreement from that
account once, by answering the bot's comment on its first pull request with
`@microsoft-github-policy-service agree`. Store the token as the `WINGET_TOKEN`
secret of this repository (Settings → Secrets and variables → Actions).
Without the secret the job validates and says that nothing was submitted.

### By hand

When the secret is not set, the job failed, or a pull request has to be
redone: download `winget-manifests.zip` from the release, or generate the
manifests as above, and either

```powershell
wingetcreate submit --prtitle "Update: Shipwick.Shipwick version 1.0.0" <directory with the three files>
```

with a token stored by `wingetcreate token --store`, or copy the files to
`manifests/s/Shipwick/Shipwick/<version>/` in a fork of microsoft/winget-pkgs
and open the pull request yourself. One version per pull request; a pull
request for a version that a newer release has overtaken is closed, not
updated.

To look at the manifests of a release before it is published, give the script
the checksums that `scripts/build-release.sh` wrote:

```sh
SHIPWICK_CHECKSUMS=$PWD/dist/checksums.txt sh packaging/winget/update-manifests.sh v0.7.0
```

The path is read after the script has changed into its own directory: give
it in full. The date is then today's, as the release's will be, and the files
named are ones the release does not have yet.
