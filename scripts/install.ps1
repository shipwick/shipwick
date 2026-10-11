<#
.SYNOPSIS
Installs the shipwick CLI on Windows.

.DESCRIPTION
The Windows counterpart of install.sh --cli:

  irm https://get.shipwick.com/windows | iex

It downloads shipwick_windows_<arch>.exe and checksums.txt from the release
on GitHub, verifies the binary's SHA-256 against the one the release lists
(a mismatch installs nothing), puts it at %LOCALAPPDATA%\Programs\shipwick\shipwick.exe
and adds that directory to your PATH, once. With cosign on this machine the
release's signature is verified first; without it, the installer says so.

Safe to run again: that is how you upgrade. So is "shipwick upgrade".

Everything lives in functions and nothing runs until the last line, so a
download that is cut off half-way executes nothing at all.

.PARAMETER Version
The release to install, such as v0.8.1 (default: the latest release, never a
pre-release). Piped into iex, set SHIPWICK_VERSION instead, or pass the
parameter to the script as a block:

  & ([scriptblock]::Create((irm https://get.shipwick.com/windows))) -Version v0.8.1

.PARAMETER Destination
The directory shipwick.exe goes to (default: %LOCALAPPDATA%\Programs\shipwick).

.NOTES
Environment:
  SHIPWICK_VERSION            the release to install (default: latest)
  SHIPWICK_REQUIRE_SIGNATURE  set to 1: install nothing unless cosign is here and
                              verifies the release's signature (default: verified
                              when cosign is installed, said when it is not)
#>
[Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingWriteHost', '', Justification = 'An installer talks to the person running it.')]
[CmdletBinding()]
param(
    [string]$Version = $(if ($env:SHIPWICK_VERSION) { $env:SHIPWICK_VERSION } else { 'latest' }),
    [string]$Destination = (Join-Path $env:LOCALAPPDATA 'Programs\shipwick')
)

# --- output -----------------------------------------------------------------

function Write-Step([string]$Message) {
    Write-Host ([string][char]0x2713) -ForegroundColor Green -NoNewline
    Write-Host " $Message"
}

function Write-Info([string]$Message) { Write-Host "  $Message" }

function Write-Warn([string]$Message) {
    Write-Host '!' -ForegroundColor Yellow -NoNewline
    Write-Host " $Message"
}

function Write-Failure([string]$Message) {
    Write-Host ''
    Write-Host ([string][char]0x2717) -ForegroundColor Red -NoNewline
    Write-Host " $Message"
}

# --- helpers ----------------------------------------------------------------

# Get-MachineArchitecture names the build for this machine. PowerShell 7 asks
# .NET, which names the machine even from an emulated x64 process on Windows
# on Arm. Windows PowerShell sees the environment its process was given,
# which under emulation is the emulated one; the system's own copy in the
# registry is the machine's, whatever the process.
function Get-MachineArchitecture {
    if ($PSVersionTable.PSVersion.Major -ge 7) {
        $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    } else {
        $system = Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -Name PROCESSOR_ARCHITECTURE -ErrorAction SilentlyContinue
        $arch = if ($system) { $system.PROCESSOR_ARCHITECTURE } elseif ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    }
    switch -Regex ($arch) {
        '^(X64|AMD64)$' { return 'amd64' }
        '^ARM64$' { return 'arm64' }
    }
    throw "Unsupported architecture: $arch. shipwick is built for Windows on x64 and on Arm64."
}

# Resolve-Release turns "latest" into the tag GitHub calls so, which is never
# a pre-release. GitHub answers /releases/latest with a redirect to the
# release's page, and the tag is its last path segment; one request, and no
# API with an hourly limit. A named version is checked for being one before
# it goes into URLs and into cosign's identity.
function Resolve-Release([string]$Repo, [string]$Version) {
    if ($Version -ne 'latest') {
        if ($Version -notmatch '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
            throw "'$Version' is not a release version; releases look like v0.8.1 or v0.9.0-rc.1."
        }
        if ($Version.StartsWith('v')) { return $Version } else { return "v$Version" }
    }
    $url = "https://github.com/$Repo/releases/latest"
    $request = [Net.HttpWebRequest]::Create($url)
    $request.AllowAutoRedirect = $false
    $request.UserAgent = 'shipwick-installer'
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch {
        # One .NET reports a redirect that is not followed as an exception
        # that carries the response, the other returns it: the same answer.
        # PowerShell wraps what a .NET method throws.
        $thrown = $_.Exception
        if ($thrown -isnot [Net.WebException] -and $thrown.InnerException) { $thrown = $thrown.InnerException }
        $response = $thrown.Response
        if (-not $response) { throw "Could not reach $url`: $($thrown.Message)" }
    }
    try { $location = [string]$response.Headers['Location'] } finally { $response.Close() }
    if ($location -notmatch '/releases/tag/(v[0-9]+\.[0-9]+\.[0-9]+[^/]*)$') {
        throw "$url did not point at a release (it answered HTTP $([int]$response.StatusCode), Location: '$location')."
    }
    return $Matches[1]
}

# Save-ReleaseFile downloads one file of the release to Path, through a
# temporary name, so that a download cut short never leaves a half file
# under the real one. Returns $false for a file the release does not have
# when the caller allows that; everything else that goes wrong is fatal.
function Save-ReleaseFile([string]$Repo, [string]$Tag, [string]$Name, [string]$Path, [switch]$Optional) {
    $url = "https://github.com/$Repo/releases/download/$Tag/$Name"
    $partial = "$Path.download"
    try {
        Invoke-WebRequest -Uri $url -OutFile $partial -UseBasicParsing -UserAgent 'shipwick-installer'
    } catch {
        Remove-Item -Path $partial -Force -ErrorAction SilentlyContinue
        $status = 0
        if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
        if ($status -eq 404) {
            if ($Optional) { return $false }
            throw "Release $Tag has no $Name (HTTP 404 for $url). The releases are listed at https://github.com/$Repo/releases."
        }
        throw "Could not download $url`: $($_.Exception.Message)"
    }
    Move-Item -Path $partial -Destination $Path -Force
    return $true
}

# Confirm-Checksum holds File against the line for Name in checksums.txt, as
# sha256sum writes it: "<hex>  <name>", sometimes "*<name>". A file that
# does not match is removed before anything else happens.
function Confirm-Checksum([string]$File, [string]$Name, [string]$Checksums) {
    $expected = ''
    foreach ($line in Get-Content -Path $Checksums) {
        $fields = $line.Trim() -split '\s+', 2
        if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $Name) { $expected = $fields[0].ToLower() }
    }
    if (-not $expected) { throw "checksums.txt has no entry for $Name." }
    $actual = (Get-FileHash -Path $File -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) {
        Remove-Item -Path $File -Force
        throw "Checksum mismatch for $Name (expected $expected, got $actual). Nothing was installed."
    }
}

# Confirm-Signature holds checksums.txt against the signature the release
# workflow made of it, checksums.txt.sigstore.json, the way install.sh does:
# the checksums say that the files are the ones the release lists; the
# signature says who wrote the list, the workflow .github/workflows/release.yml
# of this repository, run for this tag. Verifying takes cosign, which this
# machine need not have: without it the installer says that it did not check,
# and goes on. With SHIPWICK_REQUIRE_SIGNATURE set, anything short of a
# verified signature stops the installation.
function Confirm-Signature([string]$Repo, [string]$Tag, [string]$Work, [bool]$Required) {
    $cosign = Get-Command -Name cosign -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $cosign) {
        if ($Required) {
            throw "SHIPWICK_REQUIRE_SIGNATURE is set and cosign is not installed: the release's signature cannot be checked.`n  Install cosign (https://docs.sigstore.dev/cosign/system_config/installation/) and run the installer again. Nothing was installed."
        }
        Write-Info "The release's signature was not checked: cosign is not installed. Every file is checked against the release's checksums."
        return
    }
    $signature = Join-Path $Work 'checksums.txt.sigstore.json'
    if (-not (Save-ReleaseFile -Repo $Repo -Tag $Tag -Name 'checksums.txt.sigstore.json' -Path $signature -Optional)) {
        if ($Required) {
            throw "SHIPWICK_REQUIRE_SIGNATURE is set and this release has no signature (checksums.txt.sigstore.json).`n  Releases before 0.8.0 were not signed. Nothing was installed."
        }
        Write-Warn 'This release has no signature to check: releases before 0.8.0 were not signed. Every file is checked against its checksums.'
        return
    }
    $identity = "https://github.com/$Repo/.github/workflows/release.yml@refs/tags/$Tag"
    # cosign writes to standard error whatever happens; under Windows
    # PowerShell a line there would otherwise become an error of ours.
    $ErrorActionPreference = 'Continue'
    $said = & $cosign.Source verify-blob --bundle $signature --certificate-identity $identity `
        --certificate-oidc-issuer https://token.actions.githubusercontent.com (Join-Path $Work 'checksums.txt') 2>&1 |
        ForEach-Object { "$_" }
    $ErrorActionPreference = 'Stop'
    if ($LASTEXITCODE -ne 0) {
        $last = ($said | Where-Object { $_.Trim() } | Select-Object -Last 1)
        throw "The signature of the release's checksums.txt did not verify as one made by the release workflow of github.com/$Repo.`n  cosign: $last`n  cosign 2.5 or later reads this signature; an older one fails here whatever the file. Nothing was installed."
    }
    Write-Step "The release is signed by the release workflow of github.com/$Repo"
}

# Install-Binary puts the verified Source in Destination as shipwick.exe. A
# shipwick.exe that is running cannot be overwritten, but it can be renamed:
# it moves aside as shipwick.old.exe, which shipwick removes on its next run,
# the way "shipwick upgrade" does.
function Install-Binary([string]$Source, [string]$Destination) {
    $null = New-Item -ItemType Directory -Path $Destination -Force
    $exe = Join-Path $Destination 'shipwick.exe'
    if (Test-Path -Path $exe) {
        $old = Join-Path $Destination 'shipwick.old.exe'
        Remove-Item -Path $old -Force -ErrorAction SilentlyContinue
        try {
            Move-Item -Path $exe -Destination $old -Force
        } catch {
            throw "Could not replace $exe`: $($_.Exception.Message)`n  Close every shipwick that is running and run the installer again. Nothing was changed."
        }
    }
    Move-Item -Path $Source -Destination $exe -Force
    return $exe
}

# Add-PathEntry puts Directory on the user's PATH, once: in the registry, where
# Windows keeps it (HKCU\Environment, unexpanded, so entries such as
# %USERPROFILE%\bin stay as they are), in this process, and told to every
# window that listens. Returns whether it had to.
function Add-PathEntry([string]$Directory) {
    $wanted = $Directory.TrimEnd('\')
    $onPath = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $wanted })
    if ($onPath.Count -gt 0) { return $false }

    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $current = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $saved = @($current -split ';' | Where-Object { $_.TrimEnd('\') -ieq $wanted })
        if ($saved.Count -eq 0) {
            # Appended to the value as it is: what is there, in whatever
            # state, is the user's.
            $joined = if ($current) { $current.TrimEnd(';') + ';' + $wanted } else { $wanted }
            $key.SetValue('Path', $joined, [Microsoft.Win32.RegistryValueKind]::ExpandString)
        }
    } finally {
        $key.Close()
    }
    $env:Path = "$env:Path;$wanted"
    Send-EnvironmentChange
    return $true
}

# Send-EnvironmentChange is WM_SETTINGCHANGE for "Environment", broadcast to
# every top-level window: Explorer rereads the registry, and terminals opened
# from it afterwards carry the new PATH. Windows that are already open keep
# the one they started with. Not being able to say it does not undo the
# installation.
function Send-EnvironmentChange {
    try {
        if (-not ('Shipwick.Installer.User32' -as [type])) {
            Add-Type -Namespace 'Shipwick.Installer' -Name 'User32' -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
        }
        $result = [UIntPtr]::Zero
        $HWND_BROADCAST, $WM_SETTINGCHANGE, $SMTO_ABORTIFHANG = [IntPtr]0xffff, 0x1A, 2
        $null = [Shipwick.Installer.User32]::SendMessageTimeout($HWND_BROADCAST, $WM_SETTINGCHANGE, [UIntPtr]::Zero, 'Environment', $SMTO_ABORTIFHANG, 5000, [ref]$result)
    } catch {
        Write-Info 'Windows could not be told of the new PATH; sign out and in again for every program to see it.'
    }
}

# Find-OtherShipwick names a shipwick.exe that PATH finds before the one just
# installed: one put somewhere by hand, which would go on being the one that
# runs.
function Find-OtherShipwick([string]$Installed) {
    $first = Get-Command -Name shipwick.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($first -and -not ($first.Source -ieq $Installed)) { return $first.Source }
    return ''
}

# --- entry point --------------------------------------------------------------

function Install-Shipwick([string]$Version, [string]$Destination) {
    $ErrorActionPreference = 'Stop'
    # Invoke-WebRequest's progress bar makes a download take minutes under
    # Windows PowerShell.
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell on an older Windows offers TLS 1.0 by default, which
    # GitHub refuses. Added, never taken away.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = if ($env:SHIPWICK_REPO) { $env:SHIPWICK_REPO } else { 'shipwick/shipwick' }
    $required = [bool]$env:SHIPWICK_REQUIRE_SIGNATURE
    # Downloads land here and move into place only once verified.
    $work = Join-Path ([IO.Path]::GetTempPath()) "shipwick-install-$PID-$(Get-Random)"

    Write-Host "Shipwick installer ($repo@$Version)"
    Write-Host ''
    try {
        $null = New-Item -ItemType Directory -Path $work
        $arch = Get-MachineArchitecture
        $tag = Resolve-Release -Repo $repo -Version $Version
        $asset = "shipwick_windows_$arch.exe"

        $checksums = Join-Path $work 'checksums.txt'
        $null = Save-ReleaseFile -Repo $repo -Tag $tag -Name 'checksums.txt' -Path $checksums
        Confirm-Signature -Repo $repo -Tag $tag -Work $work -Required $required

        $binary = Join-Path $work 'shipwick.exe'
        $null = Save-ReleaseFile -Repo $repo -Tag $tag -Name $asset -Path $binary
        Confirm-Checksum -File $binary -Name $asset -Checksums $checksums

        $exe = Install-Binary -Source $binary -Destination $Destination
        Write-Step "Installed shipwick $tag to $exe"
        if (Add-PathEntry -Directory $Destination) {
            Write-Step "Added $Destination to your PATH (open other terminals anew for them to see it)"
        }
        $other = Find-OtherShipwick -Installed $exe
        if ($other) {
            Write-Warn "Another shipwick comes first on your PATH and is the one that runs: $other. Remove it, or move it out of the way."
        }
        Write-Host ''
        Write-Info 'Next:   shipwick login'
    } catch {
        Write-Failure $_.Exception.Message
        # A terminating error, not "exit": piped into iex, exit would close
        # the window this runs in. A script that ran the installer stops here
        # with a non-zero exit code; a prompt gets its prompt back.
        throw 'shipwick was not installed.'
    } finally {
        Remove-Item -Path $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Install-Shipwick -Version $Version -Destination $Destination
