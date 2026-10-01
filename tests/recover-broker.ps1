[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$projectName = 'pricescount-recovery-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$queueName = 'recovery-' + $projectName
$workDir = Join-Path ([IO.Path]::GetTempPath()) $projectName
$composeFile = Join-Path $workDir 'compose.yaml'
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$verified = $false
$compose = @'
services:
  broker:
    image: rabbitmq:3.13-management-alpine
    hostname: pricescount-recovery-rabbitmq
    environment:
      RABBITMQ_DEFAULT_USER: recovery
      RABBITMQ_DEFAULT_PASS: recovery
      RABBITMQ_NODENAME: rabbit@pricescount-recovery-rabbitmq
    ports:
      - "127.0.0.1::5672"
      - "127.0.0.1::15672"
    volumes:
      - rabbitmq_data:/var/lib/rabbitmq
    healthcheck:
      test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
      interval: 2s
      timeout: 5s
      retries: 30
volumes:
  rabbitmq_data:
'@

function Invoke-Compose {
    param([string[]]$Arguments)
    & docker compose -p $projectName -f $composeFile @Arguments
    if ($LASTEXITCODE -ne 0) { throw "docker compose failed: $($Arguments -join ' ')" }
}

function Get-ManagementUri {
    $mapping = & docker compose -p $projectName -f $composeFile port broker 15672
    if ($LASTEXITCODE -ne 0) { throw 'Could not resolve isolated broker management port.' }
    if ($mapping -notmatch ':(\d+)$') { throw "Unexpected management port mapping: $mapping" }
    return "http://127.0.0.1:$($Matches[1])"
}

function Wait-Management([string]$BaseUri) {
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            $null = Invoke-Management 'Get' "$BaseUri/api/overview"
            return $BaseUri
        } catch {
            Start-Sleep -Seconds 1
        }
    }
    throw 'Isolated RabbitMQ management API did not become ready.'
}

function Invoke-TestHelper {
    param([string[]]$Arguments)
    Push-Location (Join-Path $repositoryRoot 'shared')
    try {
        $output = & go run ./cmd/testhelper @Arguments
        if ($LASTEXITCODE -ne 0) { throw "Go recovery helper failed: $($Arguments[0])" }
        return ($output -join "`n")
    } finally {
        Pop-Location
    }
}

function Wait-QueuedMessage([string]$QueueUri) {
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        $state = Invoke-Management 'Get' $QueueUri
        $messageCount = $state.PSObject.Properties['messages']
        if ($null -ne $messageCount -and $messageCount.Value -eq 1) { return }
        Start-Sleep -Milliseconds 250
    }
    throw 'Expected the confirmed persistent test message to appear in the durable queue.'
}

function Remove-TestWorkDirectory {
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $resolvedWorkDir = [IO.Path]::GetFullPath($workDir).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    if ([IO.Path]::GetDirectoryName($resolvedWorkDir) -ne $tempRoot -or [IO.Path]::GetFileName($resolvedWorkDir) -ne $projectName) {
        throw "Refusing to remove an unexpected temporary path: $resolvedWorkDir"
    }
    Remove-Item -LiteralPath $resolvedWorkDir -Recurse -Force
}

function Invoke-Management([string]$Method, [string]$Uri, [object]$Body = $null) {
    $credentialBytes = [Text.Encoding]::ASCII.GetBytes('recovery:recovery')
    $headers = @{ Authorization = 'Basic ' + [Convert]::ToBase64String($credentialBytes) }
    $parameters = @{ Method = $Method; Uri = $Uri; Headers = $headers; TimeoutSec = 10 }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = ConvertTo-Json -InputObject $Body -Depth 8 -Compress
    }
    return Invoke-RestMethod @parameters
}

New-Item -ItemType Directory -Path $workDir -Force | Out-Null
Set-Content -LiteralPath $composeFile -Value $compose -NoNewline
try {
    Invoke-Compose -Arguments @('up', '-d', '--wait', '--wait-timeout', '90')
    $baseUri = Get-ManagementUri
    $managementUri = Wait-Management $baseUri

    $encodedQueue = [Uri]::EscapeDataString($queueName)
    $queueUri = "$managementUri/api/queues/%2F/$encodedQueue"
    $task = '{"task_id":"' + [guid]::NewGuid().ToString() + '","kind":"persistent-recovery-check"}'
    $rabbitAddress = (& docker compose -p $projectName -f $composeFile port broker 5672).Trim()
    if ($LASTEXITCODE -ne 0 -or $rabbitAddress -notmatch ':(\d+)$') { throw 'Could not resolve isolated RabbitMQ AMQP port.' }
    $rabbitUrl = "amqp://recovery:recovery@127.0.0.1:$($Matches[1])/"
    $publishOutput = Invoke-TestHelper -Arguments @('broker-publish', '-url', $rabbitUrl, '-queue', $queueName, '-body', $task)
    if (-not $publishOutput.Contains('publish confirmed')) { throw 'Go helper did not confirm the persistent publish.' }
    Wait-QueuedMessage $queueUri

    Invoke-Compose -Arguments @('up', '-d', '--force-recreate', '--wait', '--wait-timeout', '90', 'broker')
    $managementUri = Wait-Management (Get-ManagementUri)
    $queueUri = "$managementUri/api/queues/%2F/$encodedQueue"
    Wait-QueuedMessage $queueUri
    $rabbitAddress = (& docker compose -p $projectName -f $composeFile port broker 5672).Trim()
    if ($LASTEXITCODE -ne 0 -or $rabbitAddress -notmatch ':(\d+)$') { throw 'Could not resolve recreated RabbitMQ AMQP port.' }
    $rabbitUrl = "amqp://recovery:recovery@127.0.0.1:$($Matches[1])/"
    $recovered = Invoke-TestHelper -Arguments @('broker-consume', '-url', $rabbitUrl, '-queue', $queueName)
    if ($recovered -ne $task) {
        throw 'Broker recreation did not preserve the original queued task body.'
    }
    $verified = $true
    Write-Output "PASS: publisher-confirmed persistent task recovered from the recreated broker in isolated Compose project $projectName."
} finally {
    if ($verified) {
        Invoke-Compose -Arguments @('down', '--volumes', '--remove-orphans')
        Remove-TestWorkDirectory
        Write-Output "Cleaned up only isolated project $projectName and its named volume after recovery verification."
    } else {
        Write-Warning "Recovery was not verified. Isolated project $projectName was preserved for inspection; remove only it after review."
    }
}
