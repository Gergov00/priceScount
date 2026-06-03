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

---

## Project

**priceScount** — система мониторинга цен на Wildberries. Пользователь отправляет ссылку на товар Telegram-боту; система периодически парсит цену через headless Chrome, хранит историю в PostgreSQL и шлёт алерт когда цена выходит за заданный диапазон.

## Tech Stack

- **Language**: Go 1.22+
- **Database**: PostgreSQL (`pgx/v5`, raw SQL — без ORM)
- **Messaging**: RabbitMQ (`amqp091-go`)
- **Telegram**: `go-telegram-bot-api/v5`
- **Scraping**: chromedp + headless Chromium (только в Extractor)
- **Deployment**: Docker & Docker Compose

Redis не используется. Всё хранится в PostgreSQL.

## Architecture

Пять Go-сервисов. Bot — тонкий HTTP-клиент к Gateway. Gateway — stateless HTTP-сервер. Chromedp строго только в Extractor.

```
[Telegram User]
      │
      ▼
[Bot]  ──REST──▶  [Gateway]  ──lookup.tasks──▶  [Extractor]
                     │                                │
                     │◀────── price.results ──────────┘
                     │
                     ├──track.requests──▶  [Scheduler]
                     │                         │
                     │                    scraper.tasks
                     │                         │
                     │◀────── price.results ──[Extractor]
                     │
                     └──notify.tasks──▶  [Notifier] ──▶ Telegram
```

### Services

| Сервис | Потребляет | Публикует |
|--------|-----------|----------|
| `services/bot` | — (Telegram long-poll) | REST → Gateway |
| `services/gateway` | `price.results` | `lookup.tasks`, `track.requests`, `notify.tasks` |
| `services/scheduler` | `track.requests` | `scraper.tasks` |
| `services/extractor` | `lookup.tasks`, `scraper.tasks` | `price.results` |
| `services/notifier` | `notify.tasks` | — |

### RabbitMQ Queues

```
lookup.tasks    — разовые fetch-запросы от Gateway (lookup + force check)
                  prefetch=1 в Extractor
scraper.tasks   — периодические задачи от Scheduler
                  prefetch=2 в Extractor
price.results   — результаты от Extractor к Gateway
track.requests  — команды изменения подписок от Gateway к Scheduler
                  action: add | pause | resume | delete | force
notify.tasks    — готовые сообщения от Gateway к Notifier
                  channel: telegram | email | push
```

### Queue Contracts (`shared/pkg/contracts`)

```go
type LookupTask struct {
    TaskID   string    `json:"task_id"`
    LookupID string    `json:"lookup_id"`
    URL      string    `json:"url"`
    Platform string    `json:"platform"`
}

type ScraperTask struct {
    TaskID      string    `json:"task_id"`
    ProductID   string    `json:"product_id"`
    URL         string    `json:"url"`
    Platform    string    `json:"platform"`
    ScheduledAt time.Time `json:"scheduled_at"`
    Force       bool      `json:"force,omitempty"`
}

type PriceResult struct {
    TaskID    string    `json:"task_id"`
    LookupID  string    `json:"lookup_id,omitempty"`  // если это lookup, не мониторинг
    ProductID string    `json:"product_id,omitempty"` // если это мониторинг
    URL       string    `json:"url"`
    Price     float64   `json:"price"`
    Currency  string    `json:"currency"`
    Name      string    `json:"name,omitempty"`       // только для lookup
    ScrapedAt time.Time `json:"scraped_at"`
    Success   bool      `json:"success"`
    Error     string    `json:"error,omitempty"`
}

type TrackRequest struct {
    Action          string `json:"action"` // add | pause | resume | delete | force
    ProductID       string `json:"product_id"`
    URL             string `json:"url"`
    Platform        string `json:"platform"`
    IntervalHours   int    `json:"interval_hours,omitempty"`
}

type NotifyTask struct {
    Channel   string `json:"channel"`  // telegram | email | push
    Target    string `json:"target"`   // chat_id, email, device token
    Text      string `json:"text"`
    Direction string `json:"direction,omitempty"` // up | down
}
```

### Bot (`services/bot`)

Telegram bot, только HTTP-клиент к Gateway. Никакого прямого доступа к БД или очередям.

- In-memory сессии: `map[int64]*Session{Step, LookupID, MinPrice}`
- Шаги: `StepIdle → StepWaitingLookup → StepWaitingMinPrice → StepWaitingMaxPrice`
- Polling lookup: `GET /lookup/:id` каждые 2с, до 60с
- Команды: `/start`, `/cancel`, `/mylist`
- Действия: pause · resume · delete · force check · history · edit thresholds

Config: `TELEGRAM_BOT_TOKEN`, `GATEWAY_URL`

### Gateway (`services/gateway`)

Stateless HTTP-сервер + два фоновых goroutine. Никакого chromedp.

**REST API:**
- `POST /lookup` — INSERT lookup_requests(pending), publish lookup.tasks → 202 {lookup_id}
- `GET /lookup/:id` — SELECT lookup_requests → 200 done / 202 pending
- `POST /subscriptions` — INSERT products + subscriptions, publish track.requests{add}
- `GET /subscriptions?chat_id=` — список подписок пользователя
- `PATCH /subscriptions/:id` — pause / resume / edit thresholds, publish track.requests
- `DELETE /subscriptions/:id` — soft delete, publish track.requests{delete}
- `GET /subscriptions/:id/history` — точки графика цены
- `POST /subscriptions/:id/check` — publish track.requests{force}

**Background goroutines:**
- `price.results` consumer: если `lookup_id != ""` → UPDATE lookup_requests(done); иначе → INSERT price_history + проверка порогов + publish notify.tasks
- TTL cleaner: DELETE lookup_requests WHERE expires_at < NOW() каждые 5 минут

Config: `POSTGRES_DSN`, `RABBITMQ_URL`, `GATEWAY_ADDR`

### Scheduler (`services/scheduler`)

Потребляет команды, управляет расписанием в PostgreSQL.

- Consume `track.requests`:
  - `add` → INSERT scheduled_urls
  - `pause` → UPDATE active=false
  - `resume` → UPDATE next_check_at=NOW()
  - `delete` → DELETE
  - `force` → UPDATE next_check_at=NOW()
- Tick loop каждые N минут: `SELECT WHERE next_check_at ≤ NOW() AND active FOR UPDATE SKIP LOCKED` → publish scraper.tasks → UPDATE next_check_at += interval

Config: `POSTGRES_DSN`, `RABBITMQ_URL`, `CHECK_INTERVAL_MINUTES`

### Extractor (`services/extractor`)

Единственный сервис с chromedp. Два consumer goroutine с разным prefetch.

- Goroutine 1: consume `lookup.tasks` (prefetch=1) → chromedp → publish price.results{lookup_id}
- Goroutine 2: consume `scraper.tasks` (prefetch=2) → chromedp → publish price.results{product_id}
- `lookup_id` проходит насквозь без изменений
- Только Wildberries. Ozon не поддерживается.

Config: `RABBITMQ_URL`

### Notifier (`services/notifier`)

Тупая доставка. Никакой бизнес-логики.

- Consume `notify.tasks`, роутинг по полю `channel`
- `telegram` → sendMessage в Telegram Bot API
- `email`, `push` — future

Config: `RABBITMQ_URL`, `TELEGRAM_BOT_TOKEN`

### Database Schema

**Gateway PostgreSQL:**

```sql
users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id    BIGINT UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)

products (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    url        TEXT NOT NULL UNIQUE,
    platform   TEXT NOT NULL,  -- "wb"
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)

subscriptions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    product_id UUID NOT NULL REFERENCES products(id),
    min_price  NUMERIC(12,2),
    max_price  NUMERIC(12,2),
    paused     BOOLEAN NOT NULL DEFAULT FALSE,
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, product_id)
)

price_history (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id),
    price      NUMERIC(12,2) NOT NULL,
    currency   VARCHAR(3) NOT NULL DEFAULT 'RUB',
    scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)

lookup_requests (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url        TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',  -- pending | done | failed
    name       TEXT,
    price      NUMERIC(12,2),
    error      TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '10 minutes'
)
```

**Scheduler PostgreSQL:**

```sql
scheduled_urls (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           UUID NOT NULL,
    url                  TEXT NOT NULL UNIQUE,
    platform             TEXT NOT NULL,
    next_check_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    check_interval_hours INT NOT NULL DEFAULT 1,
    active               BOOLEAN NOT NULL DEFAULT TRUE
)
```

### Shared Module (`shared/`)

Module path: `github.com/Gergov00/pricescount/shared`

- `pkg/broker` — `Connection` wraps amqp091-go: `ConnectWithRetry`, `DeclareQueue`, `Publish`, `Consume`. Все consumer-ы ack/nack вручную. Константы очередей: `QueueLookupTasks`, `QueueScraperTasks`, `QueuePriceResults`, `QueueTrackRequests`, `QueueNotifyTasks`.
- `pkg/contracts` — типизированные структуры для всех очередей. Всегда использовать их, никогда raw map.
- `pkg/marketplace` — `WBClient` (chromedp), `DetectPlatform(url)`, `NormalizeWBURL`.

## Commands

### Setup

```bash
cp .env.example .env    # заполнить TELEGRAM_BOT_TOKEN
```

### Run

```bash
docker compose up --build
docker compose up --build -d
```

### Build

```bash
# от корня workspace
go build github.com/Gergov00/pricescount/services/bot/... \
         github.com/Gergov00/pricescount/services/gateway/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...
# ./... не работает через границы модулей workspace
```

### Test & Lint

```bash
go test ./...
golangci-lint run
```

## Git Workflow

После того как пользователь одобрил изменение — коммитить немедленно:

```bash
git add <changed files>
git commit -m "feat|fix|refactor: short description"
```

- Не упоминать Claude в commit messages.
- Один коммит на логическое изменение, не на файл.
- Никогда не коммитить `.env`.

## Coding Rules

- Всегда проверять `err` сразу после вызова который его возвращает.
- `log/slog` с `NewJSONHandler` везде; никогда `fmt.Println` в продакшен-коде.
- Конфиг загружать из env vars в `internal/config/config.go`.
- Использовать `shared/pkg/contracts` для всех RabbitMQ-сообщений — никогда не определять типы сообщений внутри сервиса.
- RabbitMQ consumer-ы: ack при успехе, nack+requeue при транзиентных ошибках (сеть, БД), nack+drop при постоянных (malformed JSON, unknown platform).
- Extractor: prefetch=1 для `lookup.tasks`, prefetch=2 для `scraper.tasks`.
- Scheduler tick: `SELECT FOR UPDATE SKIP LOCKED` — обязательно для корректной работы нескольких реплик.
- Gateway — stateless. Никакого in-memory состояния кроме map pending lookup channels (только если будет нужно в будущем).
- Docker builds: repo root как context; все Dockerfile-ы копируют все `go.mod` перед `go work sync`.

## Go Workspace Notes

Каждый сервис в `go.mod` имеет `require` и `replace` для `shared`:

```go
require github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
replace github.com/Gergov00/pricescount/shared => ../../shared
```

`go.work` управляет multi-module сборкой; `replace` нужен чтобы Go не пытался резолвить фейковую версию с GitHub. Оба нужны. Не удалять `replace` директивы.

При добавлении нового сервиса зависящего от `shared`: добавить оба directive, затем `go mod tidy` из директории сервиса.
