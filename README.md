# priceScount

Система мониторинга цен на Wildberries. Пользователь отправляет ссылку на товар в Telegram-бот, задаёт диапазон цен — система периодически проверяет цену и присылает уведомление когда цена выходит за границы.

## Как это работает

```
[Telegram User]
      │ long-poll
      ▼
   [Bot]  ──REST──▶  [Gateway]  ──lookup.tasks──▶  [Extractor]
                         │                               │
                         │◀──────── price.results ───────┘
                         │
                         ├──track.requests──▶  [Scheduler]
                         │                          │
                         │                    scraper.tasks
                         │                          │
                         │◀──────── price.results ──┘
                         │
                         └──notify.tasks──▶  [Notifier] ──▶ Telegram
```

**Bot** — тонкий клиент, только REST к Gateway. Никакой БД, никаких очередей.

**Gateway** — stateless HTTP-сервер. Хранит пользователей, продукты, подписки и историю цен. Изменения подписок, lookup, результатов и уведомлений записываются вместе с transactional outbox; публикация не зависит от доступности RabbitMQ в момент HTTP-запроса.

**Scheduler** — управляет отдельными таблицами расписания в том же PostgreSQL deployment. Версионированные snapshots сохраняют active и inactive состояние, а due/force задачи попадают в собственный outbox.

**Extractor** — единственный сервис с Chromium. Два consumer goroutine с разным prefetch.

**Notifier** — отправляет Telegram-уведомления с ограниченными HTTP/retry операциями; временные ошибки уходят в `notify.retry`, постоянные и малформированные задачи — в `notify.dead`.

**Bot** — обрабатывает updates ограниченным числом chat-FIFO workers. Диалог использует копируемое состояние, а `/mylist` показывает управляемые страницы.

## Стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Go 1.26 |
| База данных | PostgreSQL 16 (pgx/v5, без ORM) |
| Очередь | RabbitMQ 3.13 (amqp091-go) |
| Бот | go-telegram-bot-api/v5 |
| HTTP | Gin |
| DI | uber/fx |
| Скрапинг | Chromium + chromedp (headless) |
| Деплой | Docker + Docker Compose |

Redis не используется.

## Запуск

### 1. Зависимости

- Docker и Docker Compose
- Telegram Bot Token (получить у `@BotFather`)

### 2. Переменные окружения

```bash
cp .env.example .env
```

Заполнить `TELEGRAM_BOT_TOKEN` в `.env`.
`INTERNAL_TOKEN` должен быть случайным непустым shared secret. `RABBITMQ_USER`, `RABBITMQ_PASSWORD` и `POSTGRES_PASSWORD` можно переопределить; локальные значения по умолчанию (`pricescount`) нельзя использовать в production.

### 3. Запуск

```bash
docker compose up --build -d
```

Сервисы поднимаются в правильном порядке. RabbitMQ UI: http://localhost:15672. По умолчанию пользователь и пароль равны `pricescount`; задайте свои значения в `.env` перед совместным запуском.

### Остановка

```bash
docker compose down        # данные сохраняются
```

`docker compose down -v` удаляет и PostgreSQL, и named volume RabbitMQ вместе с их данными. Не используйте эту команду для применения схемы или обновления приложения.

### Обновление схемы существующей базы

`migrations/init.sql` применяется автоматически только при первом создании PostgreSQL volume. Для существующей БД сначала сделайте проверенную резервную копию и проверьте, нет ли нескольких строк расписания для одного товара:

```sql
SELECT product_id, COUNT(*)
FROM scheduled_urls
GROUP BY product_id
HAVING COUNT(*) > 1;
```

Если запрос вернул строки, остановитесь и вручную установите для каждой пары `product_id`/URL правильное расписание и состояние. Миграция намеренно завершается ошибкой и не удаляет дубликаты.

После ручного разрешения всех конфликтов примените additive migration одной транзакцией:

```bash
docker compose exec -T postgres psql -U pricescount -d pricescount \
  -v ON_ERROR_STOP=1 --single-transaction \
  -f /docker-entrypoint-initdb.d/init.sql
```

Не применяйте эту команду к production до проверки резервной копии и cutover процедуры ниже.

### Переход на versioned queue protocol

Нельзя выполнять rolling deploy старых и новых Gateway/Scheduler/Extractor процессов: старые queue-команды не содержат version и стабильных IDs. Перейдите в maintenance и выполняйте шаги в указанном порядке:

1. На изолированной копии проверьте DB migration дважды, заранее разрешите дубликаты `scheduled_urls` вручную и репетируйте восстановление RabbitMQ по [процедуре recovery](tests/BROKER_RECOVERY.md). Создайте проверенную резервную копию PostgreSQL.
2. Закройте новый входящий трафик от bot и внешних REST callers и запретите новые периодические ticks. Дайте старым `track.requests` завершиться старым Scheduler; затем остановите только Scheduler, чтобы он больше не создавал периодические задания. Оставьте Gateway `price.results` consumer и старые Extractor/Notifier consumers работающими.
3. Дождитесь нулевых `messages_ready` и `messages_unacknowledged` в `track.requests`, затем проверьте и осушите `scraper.tasks`, `lookup.tasks`, `price.results` и `notify.tasks` в порядке зависимостей, пока старые совместимые consumers продолжают обработку. Используйте `rabbitmqctl list_queues name durable messages_ready messages_unacknowledged consumers`. Если очередь не осушается или есть необработанные deliveries, не запускайте новую версию: восстановите старую обработку и повторите drain.
4. После проверки нулевого остатка остановите оставшиеся старые consumers и сервисы, затем брокер. Сохраните старый RabbitMQ container и его anonymous volume. Зафиксируйте фактический старый nodename/hostname и версию; Mnesia привязана к nodename. До старта замены задайте в `.env` ОБА параметра `RABBITMQ_HOSTNAME` и `RABBITMQ_NODENAME` ровно равными записанным старым значениям. Если старый узел использовал long names, задайте также `RABBITMQ_USE_LONGNAME=true` и проверьте это на rehearsal. Проверьте разрешённую конфигурацию безопасно с фальшивыми значениями, не запуская сервисы:

   ```powershell
   $validationEnv = Join-Path $env:TEMP 'pricescount-compose-validation.env'
   @('RABBITMQ_HOSTNAME=old-broker-host', 'RABBITMQ_NODENAME=rabbit@old-broker-host') |
     Set-Content -LiteralPath $validationEnv
   docker compose --env-file $validationEnv config rabbitmq
   Remove-Item -LiteralPath $validationEnv
   ```

   Этот пример проверяет, что Compose принимает overrides, не включая production-конфигурацию. Перед запуском с восстановленной копией установите в `.env` точные записанные старые hostname и nodename (и `RABBITMQ_USE_LONGNAME=true`, если нужно); не используйте fake значения с реальными данными. Снимите проверенную offline-копию всего старого каталога данных и перенесите копию в `rabbitmq_data`, сохраняя ownership и permissions. Используйте совместимую версию RabbitMQ. При изменении имени узла или версии выполните предварительную поддерживаемую migration rehearsal и проверьте queued/unacked messages: definitions export/import не переносит тела сообщений.
5. Ещё раз проверьте отсутствие дубликатов, примените `init.sql` командой с `--single-transaction` выше, затем запустите полный новый набор сервисов совместно. Не смешивайте producers/consumers двух протоколов.
6. Gateway reconciliation пересоздаёт snapshots всех products, в том числе inactive. Проверьте ошибки reconciliation и `SELECT COUNT(*) FROM gateway_outbox WHERE published_at IS NULL` и аналогичный запрос для `scheduler_outbox`; после успешной публикации оба числа должны прийти к нулю. Проверьте RabbitMQ readiness/alarms, durable очереди и отсутствие зависших `messages_unacknowledged`.

Если новая версия не запускается, остановите её producers/consumers и сохраняйте БД, `gateway_outbox`, `scheduler_outbox`, и новый broker volume. Исправляйте запуск forward или восстанавливайте только заранее проверенную совместимую копию. Не запускайте старую версию поверх живых versioned queues и не удаляйте outbox при rollback: сначала нужна проверенная обработка/републикация всех pending outbox events совместимой версией. Старые volumes и backups удаляйте отдельной maintenance-процедурой после reconciliation и подтверждения сохранности данных.
```

## Функции бота

| Действие | Как |
|----------|-----|
| Начать отслеживание | Отправить ссылку WB |
| Мои товары | «📋 Мои товары» или `/mylist` |
| Поставить на паузу | ⏸ в списке |
| Возобновить | ▶ в списке |
| Изменить диапазон цен | ✏️ в списке |
| История цен | 📊 История |
| Принудительная проверка | 🔄 Проверить — сразу присылает текущую цену |
| Удалить | 🗑 Удалить |

## Структура проекта

```
services/
  bot/          — Telegram-бот, HTTP-клиент к Gateway
  gateway/      — HTTP API, PostgreSQL, оркестрация очередей
  scheduler/    — расписание проверок, PostgreSQL scheduled_urls
  extractor/    — скрапинг через headless Chromium
  notifier/     — доставка уведомлений в Telegram
shared/
  pkg/broker/       — обёртка над RabbitMQ
  pkg/contracts/    — типы сообщений для всех очередей
  pkg/marketplace/  — WBClient (chromedp)
  pkg/platform/     — DetectPlatform, NormalizeWB
migrations/
  init.sql          — additive/idempotent схема Gateway и Scheduler PostgreSQL
```

## Конфигурация

| Переменная | Сервис | Описание |
|------------|--------|----------|
| `TELEGRAM_BOT_TOKEN` | bot, notifier | Обязательно |
| `GATEWAY_URL` | bot | URL Gateway, например `http://gateway:8080` |
| `POSTGRES_DSN` | gateway, scheduler | Строка подключения PostgreSQL |
| `RABBITMQ_URL` | gateway, scheduler, extractor, notifier | URL брокера |
| `GATEWAY_ADDR` | gateway | Адрес HTTP-сервера, по умолчанию `:8080` |
| `CHECK_INTERVAL_MINUTES` | scheduler | Интервал тика проверок, по умолчанию `60` |

## RabbitMQ очереди

| Очередь | Откуда | Куда | Заметки |
|---------|--------|------|---------|
| `lookup.tasks` | Gateway | Extractor | prefetch=1, только первичный lookup |
| `scraper.tasks` | Scheduler | Extractor | prefetch=2, периодика + force check |
| `price.results` | Extractor | Gateway | lookup_id или product_id |
| `track.requests` | Gateway | Scheduler | versioned `set_state` / `force` commands |
| `notify.tasks` | Gateway | Notifier | channel: telegram |
| `notify.retry` | Notifier | Notifier | durable TTL queue, возвращает временные ошибки в `notify.tasks` |
| `notify.dead` | Notifier | — | durable DLQ для постоянных и малформированных задач |

## Локальная разработка

```bash
# только инфраструктура
docker compose up -d rabbitmq postgres

# запустить сервис локально
cd services/gateway && go run ./cmd/gateway/

# собрать все сервисы (от корня workspace)
go build github.com/Gergov00/pricescount/services/bot/... \
         github.com/Gergov00/pricescount/services/gateway/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...

# тесты всех шести модулей
go test -race -count=1 ./services/bot/... ./services/gateway/... ./services/scheduler/... ./services/extractor/... ./services/notifier/... ./shared/...

# линтер
golangci-lint run
```

## Известные ограничения

- **Только Wildberries** — Ozon не поддерживается (Cloudflare блокирует headless).
- **Схема БД не мигрирует автоматически** — `init.sql` применяется только при первом создании PostgreSQL volume. Для существующих данных используйте проверенную transactional migration и versioned-protocol cutover выше; `down -v` удаляет и PostgreSQL, и RabbitMQ данные.
- **Скрапинг медленный** — каждый fetch занимает ~30–40 секунд из-за запуска Chromium и ожидания рендера.
