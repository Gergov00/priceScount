# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**priceScount** — a microservices-based price monitoring system. Users submit a product name; the system discovers relevant product URLs, periodically scrapes prices, and alerts users when the price drops below their target.

## Architecture

Four Go services communicate over RabbitMQ. Redis handles URL scheduling and scrape deduplication. PostgreSQL stores price history and user subscriptions.

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

| Service | Port | Consumes | Produces |
|---------|------|----------|----------|
| `services/discovery` | 8081 | — | `discovery.urls` |
| `services/scheduler` | 8082 | `discovery.urls` | `scraper.tasks` |
| `services/extractor` | 8083 | `scraper.tasks` | `price.results` |
| `services/notifier`  | 8084 | `price.results` | — |

### Shared module (`shared/`)

`pricescount/shared` is a workspace module used by all services.

- `pkg/broker` — `Connection` wraps amqp091-go: `ConnectWithRetry`, `DeclareQueue`, `Publish`, `Consume`. QoS prefetch is set to 10; all consumers ack/nack manually.
- `pkg/contracts` — typed message structs for all three queues (`DiscoveredURL`, `ScraperTask`, `PriceResult`).

### Discovery Service internals

- `internal/config` — loads all config from env vars.
- `internal/agent` — calls the Serper Google Search API, filters results to known e-commerce domains + product-path heuristics, deduplicates within a session.
- `internal/publisher` — wraps `broker.Connection.Publish` for `discovery.urls`.
- `internal/handler` — HTTP handler: assigns `product_id` (UUID), runs the agent, publishes messages, returns JSON.

## Setup

### Prerequisites

- Go 1.22+, Docker & Docker Compose
- [Serper](https://serper.dev) API key (free tier is sufficient for dev)

### First-time

```bash
cp .env.example .env    # add SERPER_API_KEY
go work sync            # download all workspace dependencies
```

### Run with Docker

```bash
docker compose up --build
```

Starts RabbitMQ (`:5672`, management UI `:15672`), Redis (`:6379`), Postgres (`:5432`), and the Discovery Service (`:8081`). Uncomment the other services in `docker-compose.yaml` as they are implemented.

### Run a service locally (no Docker)

```bash
# infrastructure must already be running (e.g. via docker compose up rabbitmq redis postgres)
cd services/discovery
go run ./cmd/
```

### Build

```bash
# all services (from repo root — ./... doesn't work across workspace modules)
go build github.com/Gergov00/pricescount/services/discovery/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...

# single service
cd services/discovery && go build ./cmd/
```

### Test

```bash
go test ./...
```

### Smoke-test the Discovery Service

```bash
curl -X POST http://localhost:8081/discover \
  -H 'Content-Type: application/json' \
  -d '{"product_name": "iPhone 15 Pro"}'
```

## Key conventions

- **No ORM**: use `pgx/v5` with raw SQL for all database work.
- **Structured logging**: `log/slog` with `NewJSONHandler` in every service; never use `fmt.Println` for operational output.
- **Config**: always load from env vars in `internal/config/config.go`; no hard-coded defaults beyond safe local-dev values.
- **RabbitMQ consumers**: set QoS prefetch, ack only on success, nack+requeue on transient errors, nack+drop (dead-letter) on permanent failures.
- **Redis deduplication (Extractor)**: use `ZADD NX` with score = next-allowed-scrape Unix timestamp to prevent re-scraping a URL within its check interval.
- **Docker build context**: always the repo root (`docker build -f services/<svc>/Dockerfile .`) because `go.work` requires the shared module to be in scope.
- **Stub services**: `services/scheduler`, `services/extractor`, and `services/notifier` are stubs with doc-comments describing their full responsibilities. Implement them one at a time; uncomment the corresponding block in `docker-compose.yaml` when ready.
