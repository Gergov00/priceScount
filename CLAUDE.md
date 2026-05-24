# CLAUDE.md

# CLAUDE.md — Go Development Guidelines

Этот документ задаёт правила написания Go-кода. Следуй им строго. При конфликте с привычками — побеждают эти правила.

## Базовые принципы

- **Простота важнее умности.** Если решение требует объяснения дольше двух предложений — оно слишком сложное. Не используй абстракции «на вырост».
- **Явное лучше неявного.** Никаких скрытых side-эффектов в конструкторах, `init()` использовать только в крайних случаях и с комментарием почему.
- **Ошибки — это значения.** Их обрабатывают, а не прячут. `_ = err` запрещено без явного комментария причины.
- **Composition over inheritance.** В Go нет наследования — не пытайся его эмулировать через встраивание ради повторного использования. Встраивание только когда оно действительно выражает «is-a».
- **Accept interfaces, return structs.** Функции принимают минимально достаточный интерфейс, а возвращают конкретные типы.
- **Интерфейсы определяет потребитель, а не поставщик.** Интерфейс живёт в пакете, который его использует, а не в пакете, где находится реализация. Это ключевое отличие от Java/C#.

## Структура проекта

Стандартная раскладка:

```
cmd/<binary-name>/main.go     — точки входа, минимум логики
internal/                     — приватный код приложения
  <domain>/                   — бизнес-домены (user, order, payment)
    service.go
    repository.go
    model.go
pkg/                          — переиспользуемые библиотеки (только если действительно нужно публичное API)
api/                          — OpenAPI/proto-схемы
migrations/                   — миграции БД
```

Правила:

- `cmd/<name>/main.go` содержит только: парсинг конфига, инициализацию зависимостей, запуск. Никакой бизнес-логики.
- `internal/` — для всего, что не должно импортироваться извне. По умолчанию весь код идёт сюда.
- `pkg/` создавай только когда действительно нужна публичная библиотека. Не используй `pkg/` как «свалку для всего».
- Один пакет — одна зона ответственности. Если пакет называется `utils`, `helpers`, `common`, `misc` — это плохой пакет, разбей его.

## Именование

- Пакеты: короткие, в lowercase, без подчёркиваний и camelCase. `userrepo` плох, `user` или `repository` — нормально.
- Не дублируй имя пакета в типах: `user.User` плохо, `user.Account` хорошо. Исключение — когда тип действительно представляет сам пакет (`http.Client`).
- Интерфейсы из одного метода: суффикс `-er` (`Reader`, `Closer`, `UserFetcher`).
- Геттеры без префикса `Get`: `user.Name()`, не `user.GetName()`. Сеттеры с `Set`: `user.SetName()`.
- Аббревиатуры пишутся в одном регистре: `userID`, `URL`, `HTTPClient`, не `userId`, `Url`, `HttpClient`.
- Ошибки: переменные с префиксом `Err` (`ErrNotFound`), типы с суффиксом `Error` (`ValidationError`).
- Контекст всегда называется `ctx` и идёт первым аргументом.

## Работа с ошибками

- **Не игнорируй ошибки.** Если действительно нужно — пиши `_ = f() //nolint:errcheck // <причина>`.
- **Оборачивай ошибки контекстом** через `fmt.Errorf("doing X: %w", err)`. Глагол + что делали + `%w`. Без `failed to`, `error while` — это шум.
- **Sentinel-ошибки** для известных случаев: `var ErrNotFound = errors.New("not found")`. Сравнивай через `errors.Is`.
- **Типизированные ошибки** когда нужны поля: реализуй интерфейс `error`, проверяй через `errors.As`.
- **Не логируй и возвращай одновременно.** Логирует только тот, кто принимает финальное решение (обычно — верхний уровень). Иначе одна ошибка попадает в логи 5 раз.
- **Паника — только для невосстановимого состояния** на старте программы (например, не распарсился обязательный конфиг). В библиотечном коде паники запрещены.

```go
// Хорошо
if err := repo.Save(ctx, user); err != nil {
    return fmt.Errorf("saving user %s: %w", user.ID, err)
}

// Плохо
if err := repo.Save(ctx, user); err != nil {
    log.Printf("failed to save user: %v", err)
    return err
}
```

## Контексты

- `context.Context` — первый аргумент любой функции, которая делает I/O, может быть отменена или имеет таймаут.
- Не храни `context.Context` в структурах. Исключение — long-running серверы, где это явная часть жизненного цикла.
- Не передавай `nil` в качестве контекста. Используй `context.TODO()` если временно нет нормального контекста (и оставь TODO-комментарий).
- Не используй `context.Value` для передачи обычных параметров. Только для request-scoped данных типа trace ID, auth-токена.

## Конкурентность

- **Не запускай горутины без понимания, как они завершатся.** Каждая горутина должна иметь явный механизм остановки: `ctx`, `done channel` или `sync.WaitGroup`.
- **Не пиши в закрытые каналы.** Закрывает канал только отправитель, и только когда уверен, что больше не будет писать.
- **Защищай данные.** Либо мьютекс, либо канал, либо иммутабельность. Не оба сразу для одних и тех же данных.
- **`sync.Mutex` — поле структуры без указателя**, но саму структуру передавай по указателю.
- **Detect races в тестах**: всегда гоняй CI с `-race`.
- **Predictability over performance.** Не лепи горутины ради «параллельности», если задача линейная.

## Интерфейсы

- **Маленькие интерфейсы лучше больших.** `io.Reader` — образец. Если в интерфейсе больше 3-4 методов — подумай, нужен ли он целиком потребителю.
- **Не создавай интерфейс заранее «на случай мокирования».** Создавай его в момент, когда появился второй потребитель или реальная необходимость в подмене.
- **Не объявляй интерфейс рядом с реализацией** — он принадлежит потребителю.
- **Не возвращай интерфейсы из конструкторов** без причины. `func NewUserService() *UserService` лучше, чем `func NewUserService() UserServiceInterface`. Возврат конкретного типа даёт пользователю гибкость; возврат интерфейса — отнимает её.

## Структуры и конструкторы

- Если у структуры больше 3-4 полей при создании — используй functional options или builder, а не позиционные аргументы.
- Конструктор называется `New<TypeName>` или `New` если в пакете один основной тип.
- Не делай конструктор, который ничего не делает, кроме `return &T{}`. Пусть пользователь сам пишет литерал.
- Zero value должно быть полезным, где возможно. `var b bytes.Buffer` готов к работе — образец.

```go
// Functional options
type ServerOption func(*Server)

func WithTimeout(d time.Duration) ServerOption {
    return func(s *Server) { s.timeout = d }
}

func NewServer(addr string, opts ...ServerOption) *Server {
    s := &Server{addr: addr, timeout: defaultTimeout}
    for _, opt := range opts {
        opt(s)
    }
    return s
}
```

## Управление зависимостями (для расширяемости)

- **Dependency Injection через конструкторы**, а не через глобальные переменные или `init()`.
- **Зависимости — это интерфейсы, объявленные в потребляющем пакете**, а не конкретные типы из других пакетов.
- **Никаких глобальных singleton-ов** (logger, db, config) внутри бизнес-кода. Прокидывай явно. Это критично для тестов и расширяемости.
- **Логгер — тоже зависимость.** Используй `log/slog` (стандарт с Go 1.21+), передавай его как поле структуры.

```go
// internal/order/service.go
package order

// UserFetcher объявлен здесь, в потребителе.
type UserFetcher interface {
    Fetch(ctx context.Context, id string) (User, error)
}

type Service struct {
    users  UserFetcher
    repo   Repository
    logger *slog.Logger
}

func NewService(users UserFetcher, repo Repository, logger *slog.Logger) *Service {
    return &Service{users: users, repo: repo, logger: logger}
}
```

## Тестирование

- **Table-driven тесты** — стандарт для проверки многих случаев. Используй `t.Run(tc.name, ...)` для подсказок при падении.
- **`t.Parallel()`** в каждом тесте и подтесте, где это безопасно. Не забудь захватить переменную цикла, если Go < 1.22.
- **Не используй внешние моки-фреймворки без необходимости.** Пиши простые фейки руками — они проще читаются и не разваливаются при рефакторинге.
- **Тестируй поведение, а не реализацию.** Не проверяй «был вызван метод X с аргументом Y» — проверяй наблюдаемый результат.
- **`testdata/`** — стандартное имя для тестовых файлов, Go-тулинг его игнорирует.
- **Покрытие — индикатор, не цель.** Лучше 60% хороших тестов, чем 95% тестов на геттеры.
- **Используй `testing.T.Cleanup`** вместо defer для очистки ресурсов в тестах.
- **`httptest`, `iotest`, `fstest`** — используй стандартные тестовые утилиты.

```go
func TestParse(t *testing.T) {
    t.Parallel()
    cases := []struct {
        name    string
        input   string
        want    Result
        wantErr error
    }{
        {"empty", "", Result{}, ErrEmpty},
        {"valid", "abc", Result{Value: "abc"}, nil},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            got, err := Parse(tc.input)
            if !errors.Is(err, tc.wantErr) {
                t.Fatalf("err = %v, want %v", err, tc.wantErr)
            }
            if got != tc.want {
                t.Errorf("got %v, want %v", got, tc.want)
            }
        })
    }
}
```

## API и HTTP

- Используй `net/http` или тонкие обёртки (chi, echo). Не тащи тяжёлые фреймворки без причины.
- Хендлер — это `func(http.ResponseWriter, *http.Request)`, бизнес-логика живёт в сервисах, не в хендлерах.
- Валидируй вход на границе. Внутри сервиса данные уже валидны.
- Версионируй API в URL (`/api/v1/...`) или через заголовок — но последовательно.
- Отдавай нормальные коды ошибок: 400 для ошибок клиента, 500 для серверных, 404 для отсутствия. Не отдавай 200 с `{"error": "..."}`.
- Таймауты на сервере: `ReadTimeout`, `WriteTimeout`, `IdleTimeout` — обязательно. Без них продакшен рано или поздно ляжет.
- Graceful shutdown через `http.Server.Shutdown(ctx)`.

## Конфигурация

- Конфиг загружается **один раз** на старте, в `main`. Дальше передаётся как обычная структура.
- Env-переменные для secrets и значений, меняющихся между окружениями. Файлы — для сложных конфигов.
- Не размазывай конфиг по всему коду через `os.Getenv` — это анти-паттерн. Только в одном месте на старте.
- Падай быстро: если обязательный конфиг отсутствует, программа должна сразу завершиться с понятной ошибкой.

## Логирование и observability

- `log/slog` со структурированными полями. Никакого `fmt.Println` и `log.Printf` в продакшен-коде.
- Уровни: `Debug` — для разработчика, `Info` — что система делает, `Warn` — подозрительно но работает, `Error` — что-то сломалось.
- Не логируй на каждом шаге. Лог — это сигнал, а не дамп.
- Trace ID протаскивай через контекст, добавляй в каждую запись.
- Метрики через `expvar` или Prometheus client. Старайся не изобретать свои протоколы.

## Производительность

- **Сначала пиши понятно, потом оптимизируй по профилю.** `pprof` — твой друг, догадки — нет.
- Аллокации — основной враг. Используй `sync.Pool` для горячих путей с большими объектами.
- `[]byte` и `string` конвертации стоят аллокаций. В горячем пути избегай.
- Бенчмарки (`testing.B`) для критичных мест, с фиксацией в репозитории, чтобы ловить регрессии.
- Не используй reflection в hot path.

## Линтеры и форматирование

- `gofmt` / `goimports` — обязательно (обычно делает IDE автоматически).
- `golangci-lint` с минимальным набором: `govet`, `staticcheck`, `errcheck`, `gocritic`, `revive`, `gosec`, `ineffassign`, `unused`.
- `go vet ./...` должен проходить чисто.
- `go mod tidy` перед коммитом.

## Документация

- Каждый экспортированный идентификатор — комментарий, начинающийся с его имени: `// User represents...`.
- Пакет документируется в `doc.go` или в комментарии перед `package` в одном из файлов.
- Примеры использования через `Example`-функции — они проверяются `go test`.
- README в корне с: что это, как запустить, как тестировать, как добавить фичу.

## Что НЕ делать

- Не используй `panic`/`recover` для control flow.
- Не делай `interface{}` (`any`) там, где можно конкретный тип или дженерик.
- Не пиши «универсальные» функции на дженериках раньше времени — сначала два дублирующихся куска кода, потом обобщение.
- Не делай круговые зависимости между пакетами. Если возникают — значит, абстракция размещена не там.
- Не используй `init()` для side-эффектов вроде регистрации в глобальном реестре, если это не идиоматично (типа драйверов БД).
- Не игнорируй контекст: если функция принимает `ctx`, она обязана его проверять в долгих операциях.
- Не используй `go func() { ... }()` без обоснования жизненного цикла.
- Не пиши `if err == nil { ... } else { ... }` — это перевёрнутая логика, переписывай.

## Чек-лист перед PR

1. `go build ./...` — собирается.
2. `go test -race ./...` — тесты проходят, гонок нет.
3. `go vet ./...` и линтер — чисто.
4. `go mod tidy` — выполнено.
5. Публичные API задокументированы.
6. Новые ошибки оборачиваются с контекстом.
7. Логирование — структурированное и не избыточное.
8. Изменения не нарушают обратную совместимость публичного API (или есть основание).


This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**priceScount** — a microservices-based price monitoring system. Users send a Wildberries or Ozon product URL to the Telegram bot; the system periodically fetches the price via platform APIs, stores history in PostgreSQL, and sends alerts when the price crosses user-defined thresholds.

## Tech Stack

- **Language**: Go 1.22+
- **Database**: PostgreSQL (`pgx/v5`, raw SQL — no ORM)
- **Messaging**: RabbitMQ (`amqp091-go`)
- **Key-Value**: Redis (sorted sets for scheduling, dedup TTL, user session state)
- **Telegram**: `go-telegram-bot-api/v5`
- **Deployment**: Docker & Docker Compose

## Architecture

Four Go services communicate over RabbitMQ. Redis handles URL scheduling, deduplication, and bot session state. PostgreSQL stores price history and subscriptions.

```
[Telegram User]
      │ (sends WB or Ozon product URL)
      ▼
[Bot Service]
  - validates URL (wildberries.ru / ozon.ru only)
  - fetches product name + price via marketplace API
  - saves subscription to PostgreSQL
  - publishes to discovery.urls
      │
      ▼
[Scheduler Service] ──scraper.tasks (hourly tick)──▶ [Extractor Service]
                                                             │
                                               WB: card.wb.ru JSON API
                                               Ozon: HTTP + JSON parsing
                                                             │
                                                       price.results
                                                             ▼
                                                    [Notifier Service]
                                                   PostgreSQL + Telegram alert
```

### Services

| Service | Consumes | Produces | Status |
|---------|----------|----------|--------|
| `services/scheduler` | `discovery.urls` | `scraper.tasks` | ✅ done |
| `services/extractor` | `scraper.tasks` | `price.results` | ✅ done |
| `services/notifier`  | `price.results` | — | ✅ done |
| `services/bot`       | — (polls Telegram) | `discovery.urls` | ✅ done |

### Shared module (`shared/`)

Used by all services. Module path: `github.com/Gergov00/pricescount/shared`.

- `pkg/broker` — `Connection` wraps amqp091-go: `ConnectWithRetry`, `DeclareQueue`, `Publish`, `Consume`. QoS prefetch = 10; all consumers ack/nack manually. Queue name constants: `QueueDiscoveryURLs`, `QueueScraperTasks`, `QueuePriceResults`.
- `pkg/contracts` — typed message structs for all queues. Always use these, never raw maps.
- `pkg/marketplace` — `WBClient`, `OzonClient`, `DetectPlatform(url)`, `NormalizeWBURL`, `NormalizeOzonURL`.

```go
type DiscoveredURL struct { ProductID, ProductName, URL, Platform, Source string; DiscoveredAt time.Time }
type ScraperTask   struct { TaskID, ProductID, URL, Platform string; ScheduledAt time.Time; Force bool }
type PriceResult   struct { TaskID, ProductID, URL, Currency string; Price float64; ScrapedAt time.Time; Success bool; Error string }
```

### Marketplace clients (`shared/pkg/marketplace`)

- `WBClient.FetchProduct(url)` — calls `card.wb.ru/cards/v2/detail?nm={id}`. Price in kopecks (÷100 = rubles). No auth needed.
- `OzonClient.FetchProduct(url)` — calls `ozon.ru/api/composer-api.bx/page/json/v2?url={path}`, parses `widgetStates` for price. May need proxy in production if blocked.
- `DetectPlatform(url)` — returns `"wb"` or `"ozon"`, error otherwise.
- `NormalizeWBURL` / `NormalizeOzonURL` — canonical URL form (strips query params etc.).

### Scheduler Service internals

- `internal/consumer` — reads `discovery.urls`, stores URL + platform via `ZADD NX`.
- `internal/store` — sorted set `pricescount:urls` (score = next check unix), hash `pricescount:url_meta` (url → `productID|platform`).
- `internal/scheduler` — tick loop (`CHECK_INTERVAL_MINUTES`, default 60): due URLs → publish `ScraperTask` with `Platform` → reschedule.

### Extractor Service internals

- `internal/consumer` — reads `scraper.tasks`; routes by `task.Platform`: `"wb"` → `WBClient`, `"ozon"` → `OzonClient`; checks Redis dedup; publishes `PriceResult`.
- `internal/dedup` — Redis dedup TTL (default 1 h, `SCRAPED_TTL`).
- `internal/publisher` — publishes `PriceResult` to RabbitMQ.

Config: `RABBITMQ_URL`, `REDIS_URL`, `SCRAPED_TTL`.

### Notifier Service internals

- `internal/consumer` — reads `price.results`; saves price to PostgreSQL; queries active non-paused subscriptions; fires alert if price < `min_price` or price > `max_price`.
- `internal/store` — `SavePrice()` upserts `products` + `tracked_urls`, inserts `price_history`; `TriggeredSubscriptions()` returns matching subscriptions.
- `internal/alert` — Telegram Bot API HTTP client; sends formatted message (📉 drop / 📈 rise).

Config: `RABBITMQ_URL`, `POSTGRES_DSN`, `TELEGRAM_BOT_TOKEN`.

### Bot Service internals

Telegram bot UI — no HTTP server, polls Telegram via long-polling.

User flow:
1. User sends a Wildberries or Ozon product URL.
2. Bot validates URL, fetches product name + current price via marketplace API.
3. Bot prompts for min/max price thresholds.
4. Subscription saved to PostgreSQL; URL published to `discovery.urls`.
5. `/mylist` — shows tracked products with pause/resume/delete/refresh/edit buttons.

- `internal/bot/bot.go` — main dispatcher; commands `/start`, `/cancel`, `/mylist`.
- `internal/bot/search.go` — URL submit handler (`handleURLSubmit`), min/max price collection, subscription save + publish.
- `internal/bot/mylist.go` — tracked products list, inline keyboard, history, force check.
- `internal/bot/edit.go` — edit price thresholds handler.
- `internal/bot/helpers.go` — `parsePrice`, `parseIndex`.
- `internal/state/state.go` — Redis user session state.

Config: `TELEGRAM_BOT_TOKEN`, `REDIS_URL`, `POSTGRES_DSN`, `RABBITMQ_URL`.

### Database schema (`migrations/init.sql`)

```sql
products      (id UUID PK, name TEXT, created_at)
tracked_urls  (id, product_id FK, url UNIQUE, source TEXT,   -- source = "wb" or "ozon"
               last_checked_at, check_interval_hours, active)
price_history (id, url_id FK, price NUMERIC, currency VARCHAR(3), scraped_at)
subscriptions (id, product_id FK, chat_id BIGINT, min_price, max_price, currency, active, paused, created_at)
  UNIQUE(product_id, chat_id)
```

## Commands

### Setup

```bash
cp .env.example .env    # fill in TELEGRAM_BOT_TOKEN
```

### Run

```bash
docker compose up --build       # full stack
docker compose up --build -d    # detached
```

### Build

```bash
# all services from workspace root
go build github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/... \
         github.com/Gergov00/pricescount/services/bot/...
# note: ./... does not work across workspace module boundaries
```

### Test & Lint

```bash
go test ./...
golangci-lint run
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
- Platform routing in extractor: switch on `task.Platform` (`"wb"` / `"ozon"`); unknown platform → nack+drop.

## Go Workspace Notes

Each service `go.mod` has both a `require` and a `replace` directive for `shared`:

```go
require github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
replace github.com/Gergov00/pricescount/shared => ../../shared
```

`go.work` handles multi-module builds; `replace` prevents Go from trying to resolve the fake version from GitHub. Both are needed. Do not remove `replace` directives.

When adding a new service that depends on `shared`: add both directives, then run `go mod tidy` from the service directory.
