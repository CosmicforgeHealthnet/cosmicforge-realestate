param([string]$StaticcheckPath = 'staticcheck', [string]$SqlcPath = 'sqlc', [string]$BashPath = 'bash', [string]$PythonPath = 'python')
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
    go test -count=1 ./services/payment-service/... ./gateway/api-gateway/... ./services/individual-owner-service/...
    if ($LASTEXITCODE -ne 0) { throw 'Ordinary financial/regression suite failed' }
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
            go build ./...
            if ($LASTEXITCODE -ne 0) { throw "build failed: $module" }
            if ($module -eq 'services/individual-owner-service') {
                go vet -tags financial_cross_service ./tests/integration
                if ($LASTEXITCODE -ne 0) { throw 'Tagged Owner cross-service vet failed' }
                & $StaticcheckPath -tags financial_cross_service ./tests/integration
                if ($LASTEXITCODE -ne 0) { throw 'Tagged Owner cross-service staticcheck failed' }
            }
        } finally {
            $env:GOWORK = $taskPriorWork
            Pop-Location
        }
    }
    & $SqlcPath diff -f services/payment-service/sql/sqlc.yaml
    if ($LASTEXITCODE -ne 0) { throw 'SQLC contract drift' }
    & $SqlcPath diff -f services/individual-owner-service/sql/sqlc.yaml
    if ($LASTEXITCODE -ne 0) { throw 'Owner SQLC contract drift' }
    $taskContractPaths = @(rg --files contracts postman | Sort-Object)
    $taskContractBefore = @{}
    foreach ($taskPath in $taskContractPaths) { $taskContractBefore[$taskPath] = (Get-FileHash -LiteralPath $taskPath).Hash }
    & $PythonPath scripts/payments/generate_contracts.py
    if ($LASTEXITCODE -ne 0) { throw 'Payment contract generator failed' }
    go run ./services/individual-owner-service/cmd/apidocgen -root .
    if ($LASTEXITCODE -ne 0) { throw 'Owner contract generator failed' }
    $taskContractAfterPaths = @(rg --files contracts postman | Sort-Object)
    if (@(Compare-Object $taskContractPaths $taskContractAfterPaths).Count -ne 0) { throw 'Contract file inventory drift' }
    foreach ($taskPath in $taskContractPaths) {
        if ($taskContractBefore[$taskPath] -ne (Get-FileHash -LiteralPath $taskPath).Hash) { throw "Contract regeneration drift: $taskPath" }
    }
    & $PythonPath scripts/payments/check_secrets.py
    if ($LASTEXITCODE -ne 0) { throw 'Financial secret/evidence scan failed' }
    git diff --check
    if ($LASTEXITCODE -ne 0) { throw 'Whitespace gate failed' }
} finally { Pop-Location }
