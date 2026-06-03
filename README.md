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

**Gateway** — stateless HTTP-сервер. Хранит пользователей, продукты, подписки, историю цен. Принимает решения по алертам.

**Scheduler** — управляет расписанием проверок в своей PostgreSQL. Публикует задачи скрапинга по тику.

**Extractor** — единственный сервис с Chromium. Два consumer goroutine с разным prefetch.

**Notifier** — тупая доставка: consume notify.tasks → Telegram Bot API.

## Стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Go 1.22+ |
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

### 3. Запуск

```bash
docker compose up --build -d
```

Сервисы поднимаются в правильном порядке. RabbitMQ UI: http://localhost:15672 (guest/guest).

### Остановка

```bash
docker compose down        # данные сохраняются
docker compose down -v     # удалить все данные (PostgreSQL тома)
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
  init.sql          — схема Gateway PostgreSQL
  scheduler.sql     — схема Scheduler PostgreSQL
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
| `track.requests` | Gateway | Scheduler | add / pause / resume / delete / force |
| `notify.tasks` | Gateway | Notifier | channel: telegram |

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

# тесты
go test -race ./...

# линтер
golangci-lint run
```

## Известные ограничения

- **Только Wildberries** — Ozon не поддерживается (Cloudflare блокирует headless).
- **Схема БД не мигрирует автоматически** — `init.sql` применяется только при первом создании тома. При изменении схемы: `docker compose down -v && docker compose up -d`.
- **Скрапинг медленный** — каждый fetch занимает ~30–40 секунд из-за запуска Chromium и ожидания рендера.
