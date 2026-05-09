# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**priceScount** — a microservices-based price monitoring system using AI agents for discovery and extraction. Users submit a product name; the system discovers relevant product URLs via Google Shopping, periodically scrapes prices, and alerts users when the price drops below their target.

## Tech Stack

- **Language**: Go 1.22+
- **Database**: PostgreSQL (`pgx/v5`, raw SQL — no ORM)
- **Messaging**: RabbitMQ (`amqp091-go`)
- **Key-Value**: Redis (Sorted Sets for scheduling)
- **Deployment**: Docker & Docker Compose

## Architecture

Four Go services communicate over RabbitMQ. Redis handles URL scheduling and deduplication. PostgreSQL stores price history and subscriptions.

```
User → POST /discover
         ↓
  [Discovery Service]  ──discovery.urls──▶  [Scheduler Service]
                                                    │
                                          scraper.tasks (hourly tick)
                                                    ↓
                                          [Extractor Service]
                                                    │
                                            price.results
                                                    ↓
                                          [Notifier & Engine]
                                                    │
                                              PostgreSQL + Alert
```

### Services

| Service | Port | Consumes | Produces | Status |
|---------|------|----------|----------|--------|
| `services/discovery` | 8081 | — | `discovery.urls` | ✅ done |
| `services/scheduler` | 8082 | `discovery.urls` | `scraper.tasks` | ✅ done |
| `services/extractor` | 8083 | `scraper.tasks` | `price.results` | 🔲 stub |
| `services/notifier`  | 8084 | `price.results` | — | 🔲 stub |

### Shared module (`shared/`)

Used by all services. Module path: `github.com/Gergov00/pricescount/shared`.

- `pkg/broker` — `Connection` wraps amqp091-go: `ConnectWithRetry`, `DeclareQueue`, `Publish`, `Consume`. QoS prefetch = 10; all consumers ack/nack manually.
- `pkg/contracts` — typed message structs for all queues. Always use these for RabbitMQ messages, never raw maps.

### Discovery Service internals

- `internal/agent` — primary: Serper `/shopping` endpoint (Google Shopping, no whitelist needed); fallback: `/search` with blocklist of non-shop domains. Supports `locale`: `"ru"`, `"us"`, `"all"` (parallel, default).
- `internal/handler` — `POST /discover` assigns `product_id` (UUID), calls agent, publishes to `discovery.urls`, returns `items[]` with url/source/title/price.

### Scheduler Service internals

- `internal/consumer` — reads `discovery.urls`, calls `ZADD NX` to add URLs to Redis pool (deduplication: same URL from multiple requests is stored once).
- `internal/store` — two Redis keys: sorted set `pricescount:urls` (score = next check unix timestamp), hash `pricescount:url_meta` (url → product_id).
- `internal/scheduler` — tick loop (default 1h, configurable via `CHECK_INTERVAL_MINUTES`): `ZRANGEBYSCORE 0 now` → publish `ScraperTask` → reschedule score to `now + interval`.

## Commands

### Setup

```bash
cp .env.example .env    # add SERPER_API_KEY
```

### Run

```bash
docker compose up --build       # full stack
docker compose up --build -d    # detached

# single service locally (infra must be running)
cd services/discovery && go run ./cmd/
```

### Build

```bash
# single service
cd services/discovery && go build ./cmd/

# all services from workspace root
go build github.com/Gergov00/pricescount/services/discovery/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...
# note: ./... does not work across workspace module boundaries
```

### Test & Lint

```bash
go test ./...
golangci-lint run
```

### Smoke-test

```bash
curl -X POST http://localhost:8081/discover \
  -H 'Content-Type: application/json' \
  -d '{"product_name": "iPhone 15 Pro", "locale": "all"}'
```

## Git Workflow

After the user approves any change, commit immediately with a concise message:

```bash
git add <changed files>
git commit -m "feat|fix|refactor: short description"
```

- Do not mention Claude in commit messages.
- One commit per logical change, not per file.
- Never commit `.env`.

## Coding Rules

- Always check `err` immediately after the call that returns it.
- Use `log/slog` with `NewJSONHandler` everywhere; never `fmt.Println` for operational output.
- Load all config from env vars in `internal/config/config.go`.
- Use `shared/pkg/contracts` for all RabbitMQ message schemas — never define message types inside a service.
- RabbitMQ consumers: ack on success, nack+requeue on transient errors (Redis down, network), nack+drop on permanent failures (malformed JSON).
- Redis scheduling: `ZADD NX` to add, `ZRANGEBYSCORE 0 now` to query due items, `ZADD` (without NX) to reschedule.
- Docker builds use repo root as context; all service Dockerfiles copy all `go.mod` files before `go work sync`.

## Go Workspace Notes

Each service `go.mod` has both a `require` and a `replace` directive for `shared`:

```go
require github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
replace github.com/Gergov00/pricescount/shared => ../../shared
```

`go.work` handles multi-module builds; `replace` prevents Go from trying to resolve the fake version from GitHub. Both are needed. Do not remove `replace` directives.

When adding a new service that depends on `shared`: add both directives, then run `go mod tidy` from the service directory.
