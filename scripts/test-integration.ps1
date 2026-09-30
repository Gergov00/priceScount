param(
    [switch]$Regression,
    [switch]$Coverage
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $projectRoot 'docker-compose.test.yaml'
$projectName = 'pricescount-tests-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$oldPostgres = [Environment]::GetEnvironmentVariable('TEST_POSTGRES_DSN', 'Process')
$oldRabbit = [Environment]::GetEnvironmentVariable('TEST_RABBITMQ_URL', 'Process')
$modules = @('./services/bot/...', './services/gateway/...', './services/scheduler/...', './services/extractor/...', './services/notifier/...', './shared/...')
$testExitCode = 1
$cleanupExitCode = 0

Push-Location $projectRoot
try {
    & docker compose -p $projectName -f $composeFile up -d --wait --wait-timeout 120 --quiet-pull
    if ($LASTEXITCODE -ne 0) { throw 'Test infrastructure failed to start.' }
    $postgresAddress = & docker compose -p $projectName -f $composeFile port postgres 5432
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve the test PostgreSQL port.' }
    $rabbitAddress = & docker compose -p $projectName -f $composeFile port rabbitmq 5672
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve the test RabbitMQ port.' }
    $env:TEST_POSTGRES_DSN = 'postgres://pricescount_test:pricescount_test@' + $postgresAddress.Trim() + '/pricescount_test?sslmode=disable'
    $env:TEST_RABBITMQ_URL = 'amqp://pricescount_test:pricescount_test@' + $rabbitAddress.Trim() + '/'

    $tags = if ($Regression) { 'integration,regression' } else { 'integration' }
    $goArguments = @('test', '-race', '-count=1', '-timeout=120s', ('-tags=' + $tags))
    if ($Coverage) {
        $resultsDirectory = Join-Path $projectRoot 'tests/results'
        New-Item -ItemType Directory -Path $resultsDirectory -Force | Out-Null
        $goArguments += '-coverprofile=' + (Join-Path $resultsDirectory 'coverage.out')
    }
    & go @goArguments @modules
    $testExitCode = $LASTEXITCODE
}
finally {
    # A unique project name bounds cleanup to resources created by this run.
    & docker compose -p $projectName -f $composeFile down --volumes --remove-orphans
    $cleanupExitCode = $LASTEXITCODE
    [Environment]::SetEnvironmentVariable('TEST_POSTGRES_DSN', $oldPostgres, 'Process')
    [Environment]::SetEnvironmentVariable('TEST_RABBITMQ_URL', $oldRabbit, 'Process')
    Pop-Location
}
if ($testExitCode -ne 0) { exit $testExitCode }
if ($cleanupExitCode -ne 0) { exit $cleanupExitCode }
