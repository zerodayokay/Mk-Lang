# Why: sketch embeds SPIR-V and DXIL for every shader, so a checkout runs on
# both backends without DXC; the blobs must be rebuilt the same way each time.
#
# What: compiles each HLSL entry point in ../shaders to shaders/bin/*.spv and
# *.dxil with the pinned DXC, runs gpu.CheckShader on every pair through
# mk run, and writes src/shaders/manifest.mk with the source and blob hashes.
#
# How: refuses any DXC whose --version differs from the pinned one; hashes
# sources with CRLF normalised to LF so a checkout's line endings do not matter.
#   pwsh -NoProfile -File tools/shaders.ps1 -dxc <dxc.exe> [-mk <mk.exe>]
param(
    [Parameter(Mandatory = $true)]
    [string]$dxc,
    [string]$mk = "mk"
)

$ErrorActionPreference = "Stop"

$wantVersion = "dxcompiler.dll: 1.9(5512-01b62ad4)(1.9.2609.5) - 1.9.2609.5 (01b62ad47)"
$gotVersion = (& $dxc --version 2>&1 | Out-String).Trim()
if ($gotVersion -ne $wantVersion) {
    throw "dxc --version mismatch: got '$gotVersion', want '$wantVersion'"
}

$pkgRoot = Split-Path -Parent $PSScriptRoot
$hlslDir = Join-Path $pkgRoot "shaders"
$binDir = Join-Path $hlslDir "bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

$specs = @(
    @{ Name = "BlitVs"; File = "blit.hlsl"; Stage = "vs"; Entry = "VsMain" },
    @{ Name = "BlitPs"; File = "blit.hlsl"; Stage = "ps"; Entry = "PsMain" }
)

function Get-LfSha256([string]$path) {
    $bytes = [System.IO.File]::ReadAllBytes($path)
    $text = [System.Text.Encoding]::UTF8.GetString($bytes).Replace("`r`n", "`n")
    $norm = [System.Text.Encoding]::UTF8.GetBytes($text)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($sha.ComputeHash($norm))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Get-FileSha256([string]$path) {
    return (Get-FileHash -Path $path -Algorithm SHA256).Hash.ToLowerInvariant()
}

$results = @()
foreach ($s in $specs) {
    $srcPath = Join-Path $hlslDir $s.File
    $profile = "$($s.Stage)_6_0"
    $dxilOut = Join-Path $binDir "$($s.Name).dxil"
    $spvOut = Join-Path $binDir "$($s.Name).spv"

    $dxilArgs = @("-T", $profile, "-E", $s.Entry, "-O3", "-Zpr", "-Qstrip_debug", "-Fo", $dxilOut, $srcPath)
    $out = & $dxc @dxilArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "dxc dxil failed for $($s.Name): $out"
    }

    $spvArgs = @(
        "-T", $profile, "-E", $s.Entry, "-O3", "-Zpr",
        "-spirv", "-fspv-target-env=vulkan1.3",
        "-fvk-use-dx-layout", "-fvk-support-nonzero-base-vertex", "-fvk-support-nonzero-base-instance"
    )
    if ($s.Stage -eq "ps") {
        $spvArgs += "-fvk-use-dx-position-w"
    }
    $spvArgs += @("-Fo", $spvOut, $srcPath)
    $out = & $dxc @spvArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "dxc spirv failed for $($s.Name): $out"
    }

    $results += @{
        Name = $s.Name
        File = $s.File
        Stage = $s.Stage
        Entry = $s.Entry
        HlslSha = Get-LfSha256 $srcPath
        SpvSha = Get-FileSha256 $spvOut
        DxilSha = Get-FileSha256 $dxilOut
        SpvPath = $spvOut
        DxilPath = $dxilOut
    }
}

# Verify compiled blobs via gpu.CheckShader through mk run.
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("sketch-shaders-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
try {
    $chkLines = @(
        "import std.fs",
        "import std.gpu",
        "",
        "export Main(sys: &System): fs.Error|gpu.Error|OutOfMemory!void {"
    )
    $idx = 0
    foreach ($r in $results) {
        $stageExpr = switch ($r.Stage) {
            "vs" { "gpu.Stage.Vertex" }
            "ps" { "gpu.Stage.Fragment" }
            "cs" { "gpu.Stage.Compute" }
        }
        $spvLit = $r.SpvPath.Replace("\", "\\")
        $dxilLit = $r.DxilPath.Replace("\", "\\")
        $chkLines += "    spv_$idx := try fs.ReadBytes(sys.arena, sys.io, `"$spvLit`")"
        $chkLines += "    dxil_$idx := try fs.ReadBytes(sys.arena, sys.io, `"$dxilLit`")"
        $chkLines += "    try gpu.CheckShader(gpu.ShaderCode(spirv: spv_$idx, dxil: dxil_$idx), $stageExpr, `"$($r.Entry)`")"
        $idx++
    }
    $chkLines += "}"
    $chkFile = Join-Path $tmpDir "check_shaders.mk"
    [System.IO.File]::WriteAllText($chkFile, ($chkLines -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))
    $chkOut = & $mk run $chkFile 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "mk run CheckShader failed: $chkOut"
    }
} finally {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}

# Generate src/shaders/manifest.mk.
$manifestDir = Join-Path $pkgRoot "src\shaders"
New-Item -ItemType Directory -Force -Path $manifestDir | Out-Null
$manifestPath = Join-Path $manifestDir "manifest.mk"

$hlslFiles = ($results | ForEach-Object { $_.File } | Select-Object -Unique | Sort-Object)
$lines = @(
    "// Why: embedded shader blobs and recorded source/blob SHA-256 hashes let",
    "// sketch verify shader integrity on every target without requiring DXC.",
    "//",
    "// What: embedded SPIR-V, DXIL and HLSL bytes, recorded hashes, and",
    "// VerifyAll checking hashes and gpu.CheckShader.",
    "//",
    "// How: generated by tools/shaders.ps1; normalizes CRLF to LF when hashing",
    "// HLSL sources so hashes match across checkouts.",
    "",
    "import std.build",
    "import std.encoding",
    "import std.gpu",
    "import std.hash",
    "",
    "export const DxcVersion: str = `"$wantVersion`"",
    ""
)

foreach ($hf in $hlslFiles) {
    $constBase = ($hf -replace "\.hlsl$", "")
    $pascal = ($constBase.Substring(0, 1).ToUpper() + $constBase.Substring(1))
    $lines += "const ${pascal}Hlsl: []u8 = build.File(`"shaders/$hf`")"
}
$lines += ""

foreach ($r in $results) {
    $lines += "export const $($r.Name)Spv: []u8 = build.File(`"shaders/bin/$($r.Name).spv`")"
    $lines += "export const $($r.Name)Dxil: []u8 = build.File(`"shaders/bin/$($r.Name).dxil`")"
    $lines += "export const $($r.Name)HlslSha256: str = `"$($r.HlslSha)`""
    $lines += "export const $($r.Name)SpvSha256: str = `"$($r.SpvSha)`""
    $lines += "export const $($r.Name)DxilSha256: str = `"$($r.DxilSha)`""
    $lines += ""
}

$lines += @(
    "/// Returns the ShaderCode pair for a precompiled shader.",
    "export CodeOf(spv: []u8, dxil: []u8): gpu.ShaderCode from spv, dxil {",
    "    return gpu.ShaderCode(spirv: spv, dxil: dxil)",
    "}",
    "",
    "Sha256Hex(a: &Arena, data: []u8): encoding.Error|OutOfMemory!str from a {",
    "    sum := hash.Sha256(data)",
    "    return try encoding.HexEncode(a, sum[..])",
    "}",
    "",
    "NormalizeLfSha256(a: &Arena, data: []u8): encoding.Error|OutOfMemory!str from a {",
    "    var buf := List[u8].New(a)",
    "    var i := 0",
    "    for i < data.Len() {",
    "        if data[i] == 13 && i + 1 < data.Len() && data[i + 1] == 10 {",
    "            try buf.Push(10)",
    "            i += 2",
    "        } else {",
    "            try buf.Push(data[i])",
    "            i += 1",
    "        }",
    "    }",
    "    return try Sha256Hex(a, buf[..])",
    "}",
    "",
    "CheckOne(a: &Arena, name: str, hlsl: []u8, want_hlsl: str, spv: []u8, want_spv: str, dxil: []u8, want_dxil: str, stage: gpu.Stage, entry: str): gpu.Error|encoding.Error|OutOfMemory!void {",
    "    got_hlsl := try NormalizeLfSha256(a, hlsl)",
    "    if !got_hlsl.Eq(&want_hlsl) {",
    "        _ = name",
    "        fail gpu.Invalid(misuse: gpu.Misuse.BadShader, at: 0)",
    "    }",
    "    got_spv := try Sha256Hex(a, spv)",
    "    if !got_spv.Eq(&want_spv) {",
    "        fail gpu.Invalid(misuse: gpu.Misuse.BadShader, at: 1)",
    "    }",
    "    got_dxil := try Sha256Hex(a, dxil)",
    "    if !got_dxil.Eq(&want_dxil) {",
    "        fail gpu.Invalid(misuse: gpu.Misuse.BadShader, at: 2)",
    "    }",
    "    try gpu.CheckShader(gpu.ShaderCode(spirv: spv, dxil: dxil), stage, entry)",
    "}",
    "",
    "/// Verifies all embedded HLSL sources, SPIR-V/DXIL blob hashes, and shader interfaces. O(total bytes); allocates scratch in a.",
    "export VerifyAll(a: &Arena): gpu.Error|encoding.Error|OutOfMemory!int {",
    "    var count := 0"
)

foreach ($r in $results) {
    $hlslConst = ($r.File -replace "\.hlsl$", "")
    $hlslConst = $hlslConst.Substring(0, 1).ToUpper() + $hlslConst.Substring(1) + "Hlsl"
    $stageExpr = switch ($r.Stage) {
        "vs" { "gpu.Stage.Vertex" }
        "ps" { "gpu.Stage.Fragment" }
        "cs" { "gpu.Stage.Compute" }
    }
    $lines += "    try CheckOne(a, `"$($r.Name)`", $hlslConst, $($r.Name)HlslSha256, $($r.Name)Spv, $($r.Name)SpvSha256, $($r.Name)Dxil, $($r.Name)DxilSha256, $stageExpr, `"$($r.Entry)`")"
    $lines += "    count += 1"
}
$lines += @(
    "    return count",
    "}"
)

[System.IO.File]::WriteAllText($manifestPath, ($lines -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))
Write-Output "Generated $manifestPath ($($results.Count) shaders verified)"
