# Why: a Windows user needs one command that installs a released Mk
# toolchain, signed by the project, without admin rights, Go or LLVM.
#
# What: installs the Mk toolchain on Windows. Windows PowerShell 5.1 or
# later; never asks for elevation.
#   irm https://github.com/zerodayokay/Mk-Lang/releases/latest/download/install.ps1 | iex
#   powershell -File install.ps1 [-Version 0.1.0] [-Root <dir>] [-Releases <address>] [-NoModifyPath]
#
# How: it downloads the newest release (or -Version): SHA256SUMS and
# SHA256SUMS.sig over https. It checks the Ed25519 signature against the
# release keys written below (the keys mk itself trusts,
# internal/release/keys.go) before it reads the list, then the archive's
# SHA-256 against the list. It unpacks the archive to
# <Root>\toolchains\<version>, points the junction <Root>\current at it,
# and adds <Root>\current\bin to the user's PATH (HKCU\Environment) unless
# -NoModifyPath. Root defaults to %LOCALAPPDATA%\mk. Every later
# `mk upgrade` checks signatures the same way. The parameters also come
# from MK_VERSION, MK_HOME, MK_RELEASES and MK_NO_MODIFY_PATH=1, for
# `irm ... | iex`. MK_INSTALL_TEST_KEY ("<key id> <base64 public key>")
# adds a trusted key, and only with a loopback release address (tests).
#
# The signature protects against a changed archive or list. It cannot
# protect against a changed copy of this script: read the script first, or
# take it from the repository at a release tag. .NET Framework has no
# Ed25519, so Test-MkEd25519 checks it in PowerShell.
#
# The whole script is one script block run in its own scope, so `irm | iex`
# leaves no preference, strict mode, function or variable in the caller's
# session. -JoinPathOnly <value>,<entry> prints the user PATH value the
# script would write and does nothing else (tests).

try {
& {
[CmdletBinding()]
param(
    [string]$Version = $env:MK_VERSION,
    [string]$Root = $env:MK_HOME,
    [string]$Releases = $env:MK_RELEASES,
    [switch]$NoModifyPath,
    [string[]]$JoinPathOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
# BigInteger, for Test-MkEd25519: Windows PowerShell 5.1 does not load
# System.Numerics by itself.
Add-Type -AssemblyName System.Numerics

$MkDefaultReleases = 'https://github.com/zerodayokay/Mk-Lang/releases'
$MkHost = 'x86_64-windows'
$MkVersionToken = '^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$'

# The release keys this script trusts: one '<key id> <base64 public key>'
# per entry, the same keys as internal/release/keys.go (TestInstallKeys
# checks they agree). With none, nothing can be installed.
$MkTrustedKeys = @(
    '7f12cb260ba04bea ykIH33Kzl3Uno3l9vL2v5oOUy/H0toVHc5giQVn4BC8='
)

# Join-MkPath is the user PATH value with entry added at the front, unless
# it is already there (compared without case or a trailing backslash).
function Join-MkPath([string]$PathValue, [string]$Entry) {
    $norm = { param($p) $p.Trim().TrimEnd('\').ToLowerInvariant() }
    $parts = @()
    if ($PathValue) { $parts = @($PathValue.Split(';') | Where-Object { $_ -ne '' }) }
    foreach ($p in $parts) {
        if ((& $norm $p) -eq (& $norm $Entry)) { return $PathValue }
    }
    return (@($Entry) + $parts) -join ';'
}

# Test-MkAddress accepts https, and http only to a loopback host (tests).
function Test-MkAddress([string]$Address) {
    if ($Address -match '^https://') { return $true }
    return $Address -match '^http://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?(/|$)'
}

function Get-MkFileUrl([string]$Base, [string]$Ver, [string]$File) {
    $b = $Base.TrimEnd('/')
    if ($Ver) { return "$b/download/v$Ver/$File" }
    return "$b/latest/download/$File"
}

# Save-MkFile downloads Url to OutFile, following at most ten redirects
# itself so that each target is checked: https, or http only on a loopback
# host, so a redirect cannot downgrade the download. HttpWebRequest works the same in Windows
# PowerShell 5.1 and PowerShell 7.
function Save-MkFile([string]$Url, [string]$OutFile) {
    for ($i = 0; $i -le 10; $i++) {
        if (-not (Test-MkAddress $Url)) { throw "redirected to $Url, which is not https; nothing was installed" }
        $req = [Net.HttpWebRequest]::Create($Url)
        $req.AllowAutoRedirect = $false
        $req.UserAgent = 'mk-install'
        try {
            $resp = $req.GetResponse()
        } catch [Net.WebException] {
            if ($null -eq $_.Exception.Response) { throw }
            $resp = $_.Exception.Response
        }
        try {
            $code = [int]$resp.StatusCode
            if ($code -ge 300 -and $code -lt 400) {
                $loc = $resp.Headers['Location']
                if (-not $loc) { throw "$Url answered $code with no Location" }
                $Url = ([Uri]::new([Uri]$Url, $loc)).AbsoluteUri
                continue
            }
            if ($code -ne 200) { throw "$Url answered $code" }
            $in = $resp.GetResponseStream()
            $out = [IO.File]::Create($OutFile)
            try { $in.CopyTo($out) } finally { $out.Dispose(); $in.Dispose() }
            return
        } finally {
            $resp.Dispose()
        }
    }
    throw "more than 10 redirects from $Url"
}

# Compare-MkVersion is -1, 0 or 1 for major.minor.patch[-pre].
function Compare-MkVersion([string]$A, [string]$B) {
    $pa = $A.Split('-', 2); $pb = $B.Split('-', 2)
    $na = @($pa[0].Split('.') | ForEach-Object { [int]$_ })
    $nb = @($pb[0].Split('.') | ForEach-Object { [int]$_ })
    for ($i = 0; $i -lt 3; $i++) {
        if ($na[$i] -lt $nb[$i]) { return -1 }
        if ($na[$i] -gt $nb[$i]) { return 1 }
    }
    $ra = if ($pa.Count -gt 1) { $pa[1] } else { '' }
    $rb = if ($pb.Count -gt 1) { $pb[1] } else { '' }
    if ($ra -eq $rb) { return 0 }
    if ($ra -eq '') { return 1 }
    if ($rb -eq '') { return -1 }
    return [string]::CompareOrdinal($ra, $rb)
}

# Get-MkSha256 is the file's SHA-256 in lower-case hex, from .NET rather
# than Get-FileHash: in Windows PowerShell 5.1 Get-FileHash is a function of
# Microsoft.PowerShell.Utility's script module, which does not load when
# PSModulePath is PowerShell 7's (powershell.exe started from pwsh).
function Get-MkSha256([string]$File) {
    $sha = [Security.Cryptography.SHA256]::Create()
    $f = [IO.File]::OpenRead($File)
    try {
        $h = $sha.ComputeHash($f)
    } finally {
        $f.Dispose()
        $sha.Dispose()
    }
    return ([BitConverter]::ToString($h) -replace '-', '').ToLowerInvariant()
}

# Add-MkEdPoint adds two points of edwards25519 in extended coordinates
# (X, Y, Z, T), with the unified formula of RFC 8032 section 5.1.4, which
# also doubles. $EdQ and $EdD2 are the caller's.
function Add-MkEdPoint([object[]]$P1, [object[]]$P2) {
    $bi = [Numerics.BigInteger]
    $a = $bi::Remainder(($P1[1] - $P1[0] + $EdQ) * ($P2[1] - $P2[0] + $EdQ), $EdQ)
    $b = $bi::Remainder(($P1[1] + $P1[0]) * ($P2[1] + $P2[0]), $EdQ)
    $c = $bi::Remainder($bi::Remainder($P1[3] * $EdD2, $EdQ) * $P2[3], $EdQ)
    $d = $bi::Remainder(($P1[2] + $P1[2]) * $P2[2], $EdQ)
    $e = $b - $a + $EdQ
    $f = $d - $c + $EdQ
    $g = $d + $c
    $h = $b + $a
    return , @($bi::Remainder($e * $f, $EdQ), $bi::Remainder($g * $h, $EdQ), $bi::Remainder($f * $g, $EdQ), $bi::Remainder($e * $h, $EdQ))
}

# Test-MkEd25519 checks an Ed25519 signature (RFC 8032, as Go's
# crypto/ed25519 checks it: S below the group order, [S]B - [k]A encoded
# equal to R).
function Test-MkEd25519([byte[]]$Public, [byte[]]$Message, [byte[]]$Signature) {
    if ($Public.Length -ne 32 -or $Signature.Length -ne 64) { return $false }
    $bi = [Numerics.BigInteger]
    $EdQ = $bi::Pow(2, 255) - 19
    $order = $bi::Parse('7237005577332262213973186563042994240857116359379907606001950938285454250989')
    $dd = $bi::Parse('37095705934669439343138083508754565189542113879843219016388785533085940283555')
    $EdD2 = $bi::Remainder($dd + $dd, $EdQ)
    $sqrtm1 = $bi::Parse('19681161376707505956807079304988542015446066515923890162744021073123829784752')
    $one = $bi::One
    # A little-endian unsigned integer: a zero byte on top keeps it positive.
    $num = { param([byte[]]$b, [int]$at, [int]$n) $t = [byte[]]::new($n + 1); [Array]::Copy($b, $at, $t, 0, $n); $bi::new($t) }

    # A = (x, y) from the public key: y and the sign of x.
    $yb = [byte[]]::new(32); [Array]::Copy($Public, $yb, 32)
    $sign = ($yb[31] -shr 7) -band 1
    $yb[31] = $yb[31] -band 0x7f
    $y = & $num $yb 0 32
    if ($y -ge $EdQ) { return $false }
    $y2 = $bi::Remainder($y * $y, $EdQ)
    $u = $bi::Remainder($y2 - $one + $EdQ, $EdQ)
    $v = $bi::Remainder($dd * $y2 + $one, $EdQ)
    $v3 = $bi::Remainder($v * $v * $v, $EdQ)
    $v7 = $bi::Remainder($v3 * $v3 * $v, $EdQ)
    $x = $bi::Remainder($u * $v3 * $bi::ModPow($bi::Remainder($u * $v7, $EdQ), ($EdQ - 5) / 8, $EdQ), $EdQ)
    $vx2 = $bi::Remainder($v * $x * $x, $EdQ)
    if ($vx2 -ne $u) {
        if ($bi::Remainder($vx2 + $u, $EdQ) -ne $bi::Zero) { return $false }
        $x = $bi::Remainder($x * $sqrtm1, $EdQ)
    }
    if ($x.IsZero -and $sign -eq 1) { return $false }
    if ([int]($x % 2) -ne $sign) { $x = $EdQ - $x }

    $s = & $num $Signature 32 32
    if ($s -ge $order) { return $false }
    $sha = [Security.Cryptography.SHA512]::Create()
    $buf = [byte[]]::new(64 + $Message.Length)
    [Array]::Copy($Signature, 0, $buf, 0, 32)
    [Array]::Copy($Public, 0, $buf, 32, 32)
    [Array]::Copy($Message, 0, $buf, 64, $Message.Length)
    $k = $bi::Remainder((& $num $sha.ComputeHash($buf) 0 64), $order)
    $sha.Dispose()

    # [S]B + [k](-A), one double per bit (Straus).
    $bx = $bi::Parse('15112221349535400772501151409588531511454012693041857206046113283949847762202')
    $by = $bi::Parse('46316835694926478169428394003475163141307993866256225615783033603165251855960')
    $pb = @($bx, $by, $one, $bi::Remainder($bx * $by, $EdQ))
    $nx = $EdQ - $x
    if ($x.IsZero) { $nx = $bi::Zero }
    $pa = @($nx, $y, $one, $bi::Remainder($nx * $y, $EdQ))
    $pba = Add-MkEdPoint $pb $pa
    $sb = $s.ToByteArray(); $kb = $k.ToByteArray()
    $r = @($bi::Zero, $one, $one, $bi::Zero)
    for ($i = 255; $i -ge 0; $i--) {
        $r = Add-MkEdPoint $r $r
        $j = $i -shr 3
        $sbit = ($j -lt $sb.Length) -and ((($sb[$j] -shr ($i -band 7)) -band 1) -eq 1)
        $kbit = ($j -lt $kb.Length) -and ((($kb[$j] -shr ($i -band 7)) -band 1) -eq 1)
        if ($sbit -and $kbit) { $r = Add-MkEdPoint $r $pba }
        elseif ($sbit) { $r = Add-MkEdPoint $r $pb }
        elseif ($kbit) { $r = Add-MkEdPoint $r $pa }
    }
    $zi = $bi::ModPow($r[2], $EdQ - 2, $EdQ)
    $rx = $bi::Remainder($r[0] * $zi, $EdQ)
    $ry = $bi::Remainder($r[1] * $zi, $EdQ)
    $enc = [byte[]]::new(32)
    $rb = $ry.ToByteArray()
    [Array]::Copy($rb, $enc, [Math]::Min($rb.Length, 32))
    if (-not $rx.IsEven) { $enc[31] = $enc[31] -bor 0x80 }
    for ($i = 0; $i -lt 32; $i++) {
        if ($enc[$i] -ne $Signature[$i]) { return $false }
    }
    return $true
}

# Test-MkSums checks SHA256SUMS.sig, one line '<key id> <base64 Ed25519
# signature>', over the SHA256SUMS bytes against the trusted keys, and
# throws unless it holds.
function Test-MkSums([string]$SumsFile, [string]$SigFile, [string]$Base) {
    $keys = @($MkTrustedKeys)
    if ($env:MK_INSTALL_TEST_KEY -and $Base -match '^http://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?(/|$)') {
        $keys += $env:MK_INSTALL_TEST_KEY
    }
    $keys = @($keys | Where-Object { $_ -cmatch '^[0-9a-f]{16} [A-Za-z0-9+/]{43}=$' })
    if ($keys.Count -eq 0) { throw 'this script trusts no release key yet, so it cannot check a release; nothing was installed' }
    $text = [IO.File]::ReadAllText($SigFile)
    if ($text -cnotmatch '\A([0-9a-f]{16}) ([A-Za-z0-9+/]{86}==)\n\z') { throw 'SHA256SUMS.sig is not "<key id> <signature>" on one line; nothing was installed' }
    $id = $Matches[1]
    $sig = [Convert]::FromBase64String($Matches[2])
    $key = @($keys | Where-Object { $_.Split(' ')[0] -ceq $id })
    if ($key.Count -eq 0) { throw "the release is signed by key $id, which this script does not trust; nothing was installed" }
    $pub = [Convert]::FromBase64String($key[0].Split(' ')[1])
    if (-not (Test-MkEd25519 $pub ([IO.File]::ReadAllBytes($SumsFile)) $sig)) { throw 'the signature does not match SHA256SUMS; nothing was installed' }
    Write-Output "SHA256SUMS is signed by release key $id"
}

# Select-MkArchive reads SHA256SUMS and returns the host's archive (the
# version asked for, or the newest listed) as name, version and hash. The
# version is a version token only (no '/' or '..').
function Select-MkArchive([string]$Sums, [string]$Ver) {
    $best = $null
    foreach ($line in $Sums.Split("`n")) {
        $line = $line.TrimEnd("`r")
        if ($line -cnotmatch '^([0-9a-f]{64})  (mk-(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)-x86_64-windows\.zip)$') { continue }
        $c = @{ Hash = $Matches[1]; Name = $Matches[2]; Version = $Matches[3] }
        if ($Ver) {
            if ($c.Version -ceq $Ver) { return $c }
            continue
        }
        if ($null -eq $best -or (Compare-MkVersion $c.Version $best.Version) -gt 0) { $best = $c }
    }
    if ($null -eq $best) {
        $what = if ($Ver) { "mk $Ver" } else { 'the release' }
        throw "$what has no $MkHost archive in its SHA256SUMS"
    }
    return $best
}

# Set-MkJunction points the junction Link at Target: a new junction beside
# it, the old one moved aside, the new one renamed into place, the old one
# removed (the junction only, never what it points at).
function Set-MkJunction([string]$Link, [string]$Target) {
    $dir = Split-Path -Parent $Link
    $new = Join-Path $dir ('.current-new-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Junction -Path $new -Target $Target | Out-Null
    $old = $null
    if (Test-Path -LiteralPath $Link) {
        $old = Join-Path $dir ('.current-old-' + [guid]::NewGuid().ToString('N'))
        [IO.Directory]::Move($Link, $old)
    }
    try {
        [IO.Directory]::Move($new, $Link)
    } catch {
        if ($old) { [IO.Directory]::Move($old, $Link) }
        [IO.Directory]::Delete($new, $false)
        throw
    }
    # Directory.Delete with recursive false removes a junction itself.
    if ($old) { [IO.Directory]::Delete($old, $false) }
}

# Add-MkUserPath puts Entry at the front of the user PATH. It reads the
# value unexpanded and writes it back as REG_EXPAND_SZ, so entries such as
# %USERPROFILE%\bin stay as written, then announces
# the change to running programs: setting and clearing a user variable
# through [Environment] broadcasts WM_SETTINGCHANGE. Untested by design:
# no test touches the real registry.
function Add-MkUserPath([string]$Entry) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $old = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $new = Join-MkPath $old $Entry
        if ($new -eq $old) { return $false }
        $key.SetValue('Path', $new, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    } finally {
        $key.Dispose()
    }
    [Environment]::SetEnvironmentVariable('MK_INSTALL_PATH_CHANGED', '1', 'User')
    [Environment]::SetEnvironmentVariable('MK_INSTALL_PATH_CHANGED', $null, 'User')
    return $true
}

# Use extended paths for .NET ZIP and directory operations. The final
# install directory can be short while an entry's staging path exceeds
# the Windows PowerShell 5.1 MAX_PATH limit.
function Get-MkLongPath([string]$Path) {
    if ($Path.StartsWith('\\?\')) { return $Path }
    if ($Path.StartsWith('\\')) { return '\\?\UNC\' + $Path.Substring(2) }
    return '\\?\' + $Path
}

# The signed ZIP uses slash-separated names. Check each relative name
# before switching to Win32 extended paths, which bypass normalization.
function Expand-MkArchive([string]$Zip, [string]$Tree) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($Zip)
    try {
        foreach ($entry in $archive.Entries) {
            $directory = $entry.FullName.EndsWith('/')
            $name = $entry.FullName
            if ($directory) { $name = $name.Substring(0, $name.Length - 1) }
            foreach ($part in $name.Split('/')) {
                if (-not $part -or $part -match '[<>:"\\|?*\x00-\x1f\x7f]|[. ]$' -or
                    $part -match '^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9]) *(\.|$)') {
                    throw "unsafe archive entry '$($entry.FullName)'"
                }
            }
            $path = "$Tree\" + $name.Replace('/', '\')
            if ($directory) {
                if ($entry.Length -ne 0) { throw "archive directory '$name' contains data" }
                [IO.Directory]::CreateDirectory($path) | Out-Null
            } else {
                [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path)) | Out-Null
                [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $path)
            }
        }
    } finally {
        $archive.Dispose()
    }
}

function Install-Mk {
    if (-not $Releases) { $Releases = $MkDefaultReleases }
    if (-not $Root) { $Root = Join-Path $env:LOCALAPPDATA 'mk' }
    # A relative root is the working directory's, made absolute now.
    $Root = [IO.Path]::GetFullPath([IO.Path]::Combine((Get-Location -PSProvider FileSystem).ProviderPath, $Root))
    if ($env:MK_NO_MODIFY_PATH -eq '1') { $NoModifyPath = $true }
    if (-not (Test-MkAddress $Releases)) { throw "the release address $Releases is not https" }
    if ($Version) {
        $Version = $Version -replace '^v', ''
        if ($Version -cnotmatch $MkVersionToken) { throw "-Version $Version is not a version (major.minor.patch[-pre])" }
    }
    $ProgressPreference = 'SilentlyContinue'
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

    $toolchains = Join-Path $Root 'toolchains'
    New-Item -ItemType Directory -Force -Path $toolchains | Out-Null
    $work = Join-Path $toolchains ('.install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $sumsFile = Join-Path $work 'SHA256SUMS'
        Save-MkFile (Get-MkFileUrl $Releases $Version 'SHA256SUMS') $sumsFile
        $sigFile = Join-Path $work 'SHA256SUMS.sig'
        Save-MkFile (Get-MkFileUrl $Releases $Version 'SHA256SUMS.sig') $sigFile
        Test-MkSums $sumsFile $sigFile $Releases
        $a = Select-MkArchive ([IO.File]::ReadAllText($sumsFile)) $Version
        $dest = Join-Path $toolchains $a.Version
        if (Test-Path -LiteralPath $dest) {
            Write-Output "mk $($a.Version) is already in $dest"
        } else {
            $zip = Join-Path $work $a.Name
            Save-MkFile (Get-MkFileUrl $Releases $a.Version $a.Name) $zip
            $got = Get-MkSha256 $zip
            if ($got -ne $a.Hash) { throw "$($a.Name): SHA-256 $got, but SHA256SUMS says $($a.Hash); nothing was installed" }
            $tree = Get-MkLongPath (Join-Path $work 'tree')
            Expand-MkArchive $zip $tree
            $top = "$tree\mk-$($a.Version)-$MkHost"
            if (-not [IO.File]::Exists("$top\bin\mk.exe")) { throw "$($a.Name) holds no bin\mk.exe; nothing was installed" }
            [IO.Directory]::Move($top, (Get-MkLongPath $dest))
            Write-Output "unpacked mk $($a.Version) to $dest"
        }
        Set-MkJunction (Join-Path $Root 'current') $dest
        Write-Output "current is mk $($a.Version)"
    } finally {
        try { [IO.Directory]::Delete((Get-MkLongPath $work), $true) } catch { }
    }

    $entry = Join-Path $Root 'current\bin'
    if ($NoModifyPath) {
        Write-Output "PATH was not changed; add this directory to your PATH: $entry"
    } elseif (Add-MkUserPath $entry) {
        Write-Output "added $entry to your user PATH; open a new terminal to use mk"
    } else {
        Write-Output "$entry is already on your user PATH"
    }
}

if ($JoinPathOnly) {
    $v = if ($JoinPathOnly.Count -gt 1) { $JoinPathOnly[0] } else { '' }
    Join-MkPath $v $JoinPathOnly[-1]
    return
}
Install-Mk
} @args
} catch {
    # A terminating error in the invoked scope alone can leave -File at
    # exit code 0. Rethrow in script scope so the host sees the failure.
    throw
}
