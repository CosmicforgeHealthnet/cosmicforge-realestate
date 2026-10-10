param([string]$StaticcheckPath = 'staticcheck', [string]$SqlcPath = 'sqlc', [string]$BashPath = 'bash')
$ErrorActionPreference = 'Stop'
$taskRepoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $env:CF_TEST_POSTGRES_URL -or -not $env:CF_TEST_REDIS_ADDR) {
    throw 'Set CF_TEST_POSTGRES_URL and CF_TEST_REDIS_ADDR to real test infrastructure.'
}
$env:CF_REQUIRE_INTEGRATION = '1'
Push-Location -LiteralPath $taskRepoRoot
try {
    & $BashPath scripts/ci/check-architecture.sh --self-test
    if ($LASTEXITCODE -ne 0) { throw 'Architecture self-test failed' }
    & $BashPath scripts/ci/check-architecture.sh
    if ($LASTEXITCODE -ne 0) { throw 'Architecture boundary gate failed' }
    go test -race -count=1 ./services/payment-service/... ./gateway/api-gateway/... ./services/individual-owner-service/...
    if ($LASTEXITCODE -ne 0) { throw 'Financial/regression race suite failed' }
    foreach ($module in @('services/payment-service', 'services/individual-owner-service', 'gateway/api-gateway')) {
        Push-Location -LiteralPath (Join-Path $taskRepoRoot $module)
        try {
            $taskPriorWork = $env:GOWORK
            $env:GOWORK = 'off'
            go vet ./...
            if ($LASTEXITCODE -ne 0) { throw "go vet failed: $module" }
            & $StaticcheckPath ./...
            if ($LASTEXITCODE -ne 0) { throw "staticcheck failed: $module" }
        } finally {
            $env:GOWORK = $taskPriorWork
            Pop-Location
        }
    }
    & $SqlcPath diff -f services/payment-service/sql/sqlc.yaml
    if ($LASTEXITCODE -ne 0) { throw 'SQLC contract drift' }
    & $SqlcPath diff -f services/individual-owner-service/sql/sqlc.yaml
    if ($LASTEXITCODE -ne 0) { throw 'Owner SQLC contract drift' }
    git diff --check
    if ($LASTEXITCODE -ne 0) { throw 'Whitespace gate failed' }
} finally { Pop-Location }
