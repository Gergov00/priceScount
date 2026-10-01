# Test procedures

The workspace has six Go modules: five services and `shared`. Run unit tests, the integration suite, builds, and vet across all six modules; `go test ./...` from the workspace root does not cross module boundaries.

## Unit tests

```powershell
go test -race -count=1 ./services/bot/... ./services/gateway/... ./services/scheduler/... ./services/extractor/... ./services/notifier/... ./shared/...
```

## PostgreSQL and RabbitMQ integration tests

Requires Go 1.26, a working race detector (CGO and a C compiler), Docker Desktop with Linux containers, and PowerShell 7. The runner creates a new Compose project for each invocation, publishes PostgreSQL 16 and RabbitMQ 3.13 on dynamic localhost ports, sets process-local fake test credentials, and removes only that project and its volumes in `finally`:

```powershell
./scripts/test-integration.ps1
./scripts/test-integration.ps1 -Coverage
```

The coverage profile is written to the gitignored `tests/results/coverage.out`:

```powershell
go tool cover -func=tests/results/coverage.out
```

`-Regression` remains as a compatibility alias for `-tags=integration,regression`. Closed defect regressions now run in the ordinary unit or integration set; the alias does not hide or add the only check for any repaired behavior.

The integration suite creates an isolated PostgreSQL schema per DB test and runs the schema migration twice. Upgrade-fixture tests seed the pre-repair `users`, `subscriptions`, `price_history`, and `scheduled_urls` schema, check row preservation and backfill, and verify that duplicate `scheduled_urls.product_id` values fail with an explicit manual-reconciliation error without deleting rows. RabbitMQ tests use unique queues. The Gateway pipeline exercises HTTP → PostgreSQL → RabbitMQ → price consumer → notification outbox. No real Telegram or Wildberries requests are made.

When a test explicitly selects the `integration` tag, a missing `TEST_POSTGRES_DSN` or `TEST_RABBITMQ_URL` fails. Example process-local test values for an already-isolated service are:

```powershell
$env:TEST_POSTGRES_DSN='postgres://pricescount_test:pricescount_test@127.0.0.1:5432/pricescount_test?sslmode=disable'
$env:TEST_RABBITMQ_URL='amqp://pricescount_test:pricescount_test@127.0.0.1:5672/'
go test -race -count=1 -tags=integration ./services/gateway/... ./services/scheduler/... ./shared/...
```

Do not point these variables at production or shared development data. Each database test owns and drops its generated schema; queue tests own their generated queue names.

## Controlled browser and broker recovery

The controlled Chrome cancellation test is opt-in and targets a local `httptest` page, not Wildberries. On Windows, provide the local Chrome executable path:

```powershell
$env:TEST_CHROME_PATH='C:\Program Files\Google\Chrome\Application\chrome.exe'
go test -race -count=1 '-tags=integration,browser' ./shared/pkg/marketplace
```

The result is meaningful only if that command actually runs rather than reports the test skipped. The Linux CI job does not have Chrome configured and records browser cancellation as outside that job.

Broker data recovery uses the separate, isolated procedure in [BROKER_RECOVERY.md](BROKER_RECOVERY.md): a unique Compose project, named test volume, durable queue, publisher-confirmed persistent message, forced broker recreation with the same nodename, and consumption of the original body before project-only cleanup.

## CI checks

`.github/workflows/test.yml` runs unit/race and PostgreSQL 16/RabbitMQ 3.13 integration/race tests over all six module paths, then build and vet over all six modules. It also builds all five root-context Docker images with fake credentials and runs the isolated broker recovery procedure. The workflow has no production secrets and does not use the repository `.env` file.
