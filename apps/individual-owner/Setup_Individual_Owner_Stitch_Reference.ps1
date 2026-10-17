# Preserves source bytes; refuses unsafe paths and changed originals.
param([string]$Repo = (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent), [string]$Zip = '')
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem
$owner = Join-Path $Repo 'apps/individual-owner'
if (-not $Zip) { $Zip = Join-Path $owner 'stitch_individual_owner_design_system.zip' }
if (-not (Test-Path -LiteralPath $Zip -PathType Leaf)) { throw "Missing archive: $Zip" }
$design = Join-Path $owner 'design/stitch'
$original = Join-Path $design 'original'
$rootName = 'stitch_individual_owner_design_system'
$archiveHash = (Get-FileHash -LiteralPath $Zip -Algorithm SHA256).Hash.ToLowerInvariant()
$manifest = Join-Path $design 'SHA256SUMS.txt'
if (Test-Path -LiteralPath $manifest) {
    if ((Get-Content -LiteralPath $manifest -First 1).Split(' ')[0] -ne $archiveHash) { throw 'Archive differs from baseline; no files overwritten.' }
}
$pairs = @()
foreach ($line in Get-Content -LiteralPath (Join-Path $owner 'docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md')) {
    if ($line -match '^\| (\d{2}) \| `([^`]+)` \| `([^`]+)` \|$') { $pairs += [pscustomobject]@{ id = "pair-$($Matches[1])"; desktop = $Matches[2]; mobile = $Matches[3] } }
}
if ($pairs.Count -ne 15) { throw 'Expected exactly 15 pairs in master catalogue.' }
$catalogue = Get-Content -Raw -LiteralPath (Join-Path $design 'pairs.json') | ConvertFrom-Json
$archive = [IO.Compression.ZipFile]::OpenRead((Resolve-Path -LiteralPath $Zip))
try {
    $entries = @($archive.Entries | Where-Object { $_.Name })
    $names = @{}
    foreach ($entry in $archive.Entries) {
        $path = $entry.FullName
        if ($path -match '(^/|^[A-Za-z]:|(^|/)\.\.(/|$)|\\)' -or -not $path.StartsWith("$rootName/")) { throw "Unsafe archive path: $path" }
        if ($names.ContainsKey($path.ToLowerInvariant())) { throw "Duplicate archive path: $path" }
        $names[$path.ToLowerInvariant()] = $true
    }
    $expected = @("$rootName/individual_owner_design.md", "$rootName/cosmicforge_individual_owner/DESIGN.md")
    foreach ($pair in $pairs) { foreach ($folder in @($pair.desktop, $pair.mobile)) { $expected += "$rootName/$folder/code.html", "$rootName/$folder/screen.png" } }
    $difference = @(Compare-Object ($expected | Sort-Object) ($entries.FullName | Sort-Object))
    if ($difference.Count) { throw "Archive catalogue mismatch: $($difference | Out-String)" }
    $legacy = Join-Path $design 'source'
    $existing = if (Test-Path -LiteralPath $original) { $original } elseif (Test-Path -LiteralPath $legacy) { $legacy } else { $original }
    $hashes = @{}
    foreach ($entry in $entries) {
        $stream = $entry.Open(); $sha = [Security.Cryptography.SHA256]::Create()
        try { $hash = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant() }
        finally { $stream.Dispose(); $sha.Dispose() }
        $hashes[$entry.FullName] = $hash
        $destination = Join-Path $existing $entry.FullName
        if ((Test-Path -LiteralPath $destination) -and (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant() -ne $hash) { throw "Original source changed: $destination. Setup will not overwrite it." }
    }
    if ($existing -eq $legacy) {
        $boundary = [IO.Path]::GetFullPath($design) + [IO.Path]::DirectorySeparatorChar
        foreach ($target in @($legacy, $original)) { if (-not [IO.Path]::GetFullPath($target).StartsWith($boundary, [StringComparison]::OrdinalIgnoreCase)) { throw "Unsafe migration target: $target" } }
        Move-Item -LiteralPath $legacy -Destination $original
    }
    foreach ($entry in $entries) {
        $destination = Join-Path $original $entry.FullName
        if (-not (Test-Path -LiteralPath $destination)) {
            New-Item -ItemType Directory -Force -Path (Split-Path $destination -Parent) | Out-Null
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $destination, $false)
        }
    }
    $actual = @(Get-ChildItem -LiteralPath (Join-Path $original $rootName) -Recurse -File)
    if ($actual.Count -ne 62) { throw "Expected 62 extracted files, found $($actual.Count): $($actual.FullName -join ', ')" }
    $screens = @()
    foreach ($pair in $pairs) {
        $meta = @($catalogue | Where-Object { $_.pairId -eq $pair.id })
        if ($meta.Count -ne 1) { throw "Missing/duplicate pair metadata: $($pair.id)" }
        foreach ($platform in @('DESKTOP', 'MOBILE')) {
            $folder = if ($platform -eq 'DESKTOP') { $pair.desktop } else { $pair.mobile }
            $rel = "original/$rootName/$folder"
            $screens += [ordered]@{ id = "$($pair.id)-$($platform.ToLowerInvariant())"; originalFolder = $folder; title = $meta[0].title; platform = $platform; pairId = $pair.id; html = "$rel/code.html"; screenshot = "$rel/screen.png"; sourcePresence = @{ html = $true; screenshot = $true }; modules = @($meta[0].modules) }
        }
    }
    $inventory = [ordered]@{ schemaVersion = 1; archive = @{ file = 'stitch_individual_owner_design_system.zip'; bytes = (Get-Item -LiteralPath $Zip).Length; sha256 = $archiveHash }; counts = @{ html = 30; png = 30; markdown = 2; desktop = 15; mobile = 15 }; screens = $screens }
    $inventory | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $design 'inventory.json') -Encoding UTF8
    $sums = @("$archiveHash  stitch_individual_owner_design_system.zip")
    foreach ($name in ($hashes.Keys | Sort-Object)) { $sums += "$($hashes[$name])  design/stitch/original/$name" }
    $sums | Set-Content -LiteralPath $manifest -Encoding UTF8
    $cards = foreach ($screen in $screens) {
        $title = [Net.WebUtility]::HtmlEncode($screen.title)
        $other = if ($screen.platform -eq 'DESKTOP') { 'mobile' } else { 'desktop' }
        @"
<article class="card" id="$($screen.id)" data-platform="$($screen.platform)" data-search="$title $($screen.originalFolder) $($screen.modules -join ' ')"><a class="preview" href="$($screen.screenshot)"><img src="$($screen.screenshot)" alt="$title - $($screen.platform) reference" loading="lazy"></a><div class="body"><span class="tag">$($screen.pairId) / $($screen.platform)</span><h2>$title</h2><p>Modules: $($screen.modules -join ', ')</p><nav aria-label="$title $($screen.platform) references"><a href="$($screen.html)">Original HTML</a><a href="$($screen.screenshot)">Full PNG</a><a class="pair" href="#$($screen.pairId)-$other" data-target="$($screen.pairId)-$other">View $other pair</a></nav></div></article>
"@
    }
    $template = Get-Content -Raw -LiteralPath (Join-Path $design 'gallery-template.html')
    ($template.Replace('<!-- SCREEN_CARDS -->', ($cards -join "`n")).TrimEnd() + "`n") | Set-Content -LiteralPath (Join-Path $design 'index.html') -Encoding UTF8 -NoNewline
    Write-Output "Verified ZIP $archiveHash ($($inventory.archive.bytes) bytes); 30 HTML, 30 PNG, 2 Markdown, 15 pairs. Gallery: $design/index.html"
} finally { $archive.Dispose() }
