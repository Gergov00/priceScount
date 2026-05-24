# priceScount

Система мониторинга цен на основе микросервисов. Пользователь отправляет ссылку на товар с Wildberries в Telegram-бот, система периодически проверяет цену и присылает уведомление когда цена выходит за установленный диапазон.

## Как это работает

```
Пользователь (Telegram-бот)
        │  отправляет ссылку WB
        ▼
   [Bot Service]       — валидирует URL, получает цену и название,
        │                сохраняет подписку в PostgreSQL
        │  discovery.urls
        ▼
 [Scheduler Service]   — хранит URL в Redis, по расписанию публикует задачи
        │  scraper.tasks
        ▼
 [Extractor Service]   — headless Chromium скрапит страницу, извлекает цену
        │  price.results
        ▼
 [Notifier Service]    — сохраняет цену в PostgreSQL, шлёт алерт в Telegram
```

## Стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Go 1.26 |
| База данных | PostgreSQL 16 (pgx/v5, без ORM) |
| Очередь сообщений | RabbitMQ 3.13 |
| Кэш / дедупликация / сессии | Redis 7 |
| Бот | go-telegram-bot-api/v5 |
| Скрапинг | Chromium + chromedp (headless) |
| Деплой | Docker + Docker Compose |

## Запуск

### 1. Зависимости

- Docker и Docker Compose
- Telegram Bot Token (получить у `@BotFather`)

### 2. Переменные окружения

```bash
cp .env.example .env
```

Заполнить в `.env`:

```env
TELEGRAM_BOT_TOKEN=...   # от @BotFather
```

### 3. Запуск

```bash
docker compose up --build -d
```

Сервисы поднимаются в правильном порядке автоматически. RabbitMQ management UI: http://localhost:15672 (guest/guest).

### 4. Проверка

Открыть бот в Telegram и отправить ссылку на товар, например:
```
https://www.wildberries.ru/catalog/303271048/detail.aspx
```

### Остановка

```bash
docker compose down        # остановить, данные сохраняются
docker compose down -v     # остановить и удалить все данные (PostgreSQL, Redis)
```

## Функции бота

| Действие | Как |
|----------|-----|
| Начать отслеживание | Отправить ссылку на товар WB |
| Мои товары | Кнопка «📋 Мои товары» или `/mylist` |
| Поставить на паузу | Кнопка ⏸ в списке товаров |
| Возобновить | Кнопка ▶ в списке товаров |
| Изменить диапазон цен | Кнопка ✏️ в списке товаров |
| История цен | Кнопка 📊 История |
| Принудительная проверка | Кнопка 🔄 Проверить — сразу присылает текущую цену |
| Удалить товар | Кнопка 🗑 Удалить |

## Структура проекта

```
services/
  bot/          — Telegram-бот (пользовательский интерфейс)
  scheduler/    — тик-луп, планирование проверок через Redis
  extractor/    — скрапинг цен через headless Chromium
  notifier/     — сохранение цен, отправка алертов
shared/
  pkg/broker/       — обёртка над RabbitMQ (amqp091-go)
  pkg/contracts/    — типы сообщений для всех очередей
  pkg/marketplace/  — клиенты WB и Ozon
migrations/
  init.sql      — схема БД (применяется при первом запуске)
```

## Конфигурация

| Переменная | Сервис | По умолчанию | Описание |
|------------|--------|-------------|----------|
| `TELEGRAM_BOT_TOKEN` | bot, notifier | — | Обязательно |
| `POSTGRES_DSN` | bot, notifier | localhost | Строка подключения к PostgreSQL |
| `RABBITMQ_URL` | все | guest/guest@localhost | URL брокера |
| `REDIS_URL` | bot, scheduler, extractor | localhost:6379 | URL Redis |
| `CHECK_INTERVAL_MINUTES` | scheduler, extractor | 60 | Интервал проверки цен и TTL дедупликации |

## Известные ограничения

- **Ozon временно отключён** — Cloudflare блокирует все запросы (HTTP и headless). Будет включён после добавления обхода TLS fingerprinting.
- **Схема БД не мигрирует автоматически** — `init.sql` применяется только при первом создании тома PostgreSQL. При изменении схемы: `docker compose down -v && docker compose up -d`.
- **WB headless медленный** — каждый скрейп занимает ~30–40 секунд из-за запуска Chromium и ожидания рендера страницы.

## Локальная разработка

```bash
# запустить только инфраструктуру
docker compose up -d rabbitmq redis postgres

# запустить один сервис локально
cd services/bot && go run ./cmd/

# собрать все сервисы
go build github.com/Gergov00/pricescount/services/bot/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...

# линтер
golangci-lint run
```
