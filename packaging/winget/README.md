# winget

Manifests for installing the CLI with the Windows Package Manager:

```powershell
winget install Shipwick.Shipwick
```

`update-manifests.sh` writes the manifests for a release into
`manifests/s/Shipwick/Shipwick/<version>/`, the layout of
[microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs). The
checksums come from the release's `checksums.txt`; nothing is typed by hand.

```sh
sh packaging/winget/update-manifests.sh            # the latest release
sh packaging/winget/update-manifests.sh v0.2.0     # a specific one
winget validate packaging/winget/manifests/s/Shipwick/Shipwick/0.2.0
```

## Publishing a version

winget has no equivalent of a Homebrew tap: every version is a pull request to
microsoft/winget-pkgs, from a fork, reviewed by Microsoft's automation and a
moderator. After a release:

1. Generate and validate the manifests as above; commit them here first.
2. In a fork of microsoft/winget-pkgs, copy the new version directory to the
   same path and open a pull request with the title
   `New package: Shipwick.Shipwick version 0.2.0` (the first time) or
   `Update: Shipwick.Shipwick version 0.2.1` (afterwards). One version per
   pull request.
3. The automation validates the manifest and installs the package in a
   sandbox; a moderator merges. Expect a day or two the first time.

The installer is `portable`: winget copies the build for the machine —
`shipwick_windows_amd64.exe` or `shipwick_windows_arm64.exe` — into its links
directory as `shipwick.exe` and puts it on `PATH`, which is what `Commands` in
the installer manifest declares. The manifest has one installer entry for each
Windows build the release's `checksums.txt` lists: `x64` and, from 0.7.0 on,
`arm64`. For a release before that it lists `x64` alone, which an arm64
Windows runs as well.

To look at the manifests of a release before it is published, give the script
the checksums that `scripts/build-release.sh` wrote:

```sh
SHIPWICK_CHECKSUMS=dist/checksums.txt sh packaging/winget/update-manifests.sh v0.7.0
```

They name files the release does not have yet: remove the directory again, or
keep it for the pull request once the release is out and the checksums are
the same.
