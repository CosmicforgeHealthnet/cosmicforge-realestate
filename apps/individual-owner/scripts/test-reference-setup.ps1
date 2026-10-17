$ErrorActionPreference = 'Stop'
$owner = Split-Path $PSScriptRoot -Parent
$repo = Split-Path (Split-Path $owner -Parent) -Parent
$sandbox = Join-Path $repo ('tmp/owner-reference-test-' + [guid]::NewGuid().ToString('N'))
$testOwner = Join-Path $sandbox 'apps/individual-owner'
$testDesign = Join-Path $testOwner 'design/stitch'
New-Item -ItemType Directory -Path $testDesign -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $testOwner 'docs') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $owner 'stitch_individual_owner_design_system.zip') -Destination $testOwner
Copy-Item -LiteralPath (Join-Path $owner 'docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md') -Destination (Join-Path $testOwner 'docs')
foreach ($file in @('pairs.json','gallery-template.html')) { Copy-Item -LiteralPath (Join-Path $owner "design/stitch/$file") -Destination $testDesign }
$setup = Join-Path $owner 'Setup_Individual_Owner_Stitch_Reference.ps1'
& $setup -Repo $sandbox | Out-Null
$before = (Get-FileHash -LiteralPath (Join-Path $testDesign 'SHA256SUMS.txt')).Hash
$beforeInventory = (Get-FileHash -LiteralPath (Join-Path $testDesign 'inventory.json')).Hash
& $setup -Repo $sandbox | Out-Null
if ($before -ne (Get-FileHash -LiteralPath (Join-Path $testDesign 'SHA256SUMS.txt')).Hash) { throw 'Repeated setup changed manifest.' }
if ($beforeInventory -ne (Get-FileHash -LiteralPath (Join-Path $testDesign 'inventory.json')).Hash) { throw 'Repeated setup changed inventory.' }
$sample = Join-Path $testDesign 'original/stitch_individual_owner_design_system/owner_dashboard/code.html'
Add-Content -LiteralPath $sample -Value 'tampered for isolated test'
$rejected = $false
try { & $setup -Repo $sandbox | Out-Null } catch { if ($_.Exception.Message -match 'Original source changed') { $rejected = $true } else { throw } }
if (-not $rejected) { throw 'Setup overwrote a changed original.' }
# Traversal must fail before extraction. Use a separate sandbox, so baseline hashes cannot mask the path check.
$unsafeRepo = Join-Path $sandbox 'unsafe'
$unsafeOwner = Join-Path $unsafeRepo 'apps/individual-owner'
New-Item -ItemType Directory -Path (Join-Path $unsafeOwner 'design/stitch') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $unsafeOwner 'docs') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $owner 'docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md') -Destination (Join-Path $unsafeOwner 'docs')
Copy-Item -LiteralPath (Join-Path $testDesign 'pairs.json') -Destination (Join-Path $unsafeOwner 'design/stitch')
$unsafeZip = Join-Path $unsafeOwner 'unsafe.zip'
$archive = [IO.Compression.ZipFile]::Open($unsafeZip, [IO.Compression.ZipArchiveMode]::Create)
$archive.CreateEntry('stitch_individual_owner_design_system/../../escaped.txt') | Out-Null
$archive.Dispose()
$rejected = $false
try { & $setup -Repo $unsafeRepo -Zip $unsafeZip | Out-Null } catch { if ($_.Exception.Message -match 'Unsafe archive path') { $rejected = $true } else { throw } }
if (-not $rejected) { throw 'Traversal archive was accepted.' }
Write-Output "PASS: clean extraction, repeat stability, tamper refusal and traversal refusal. Isolated evidence retained in $sandbox"
