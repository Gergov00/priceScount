# priceScount

Система мониторинга цен на основе микросервисов. Пользователь вводит название товара в Telegram-бот, система находит магазины, периодически проверяет цену и присылает уведомление когда цена выходит за установленный диапазон.

## Как это работает

```
Пользователь (Telegram-бот)
        │
        ▼
 [Discovery Service]  — ищет товар через Google Shopping (Serper API)
        │  discovery.urls
        ▼
 [Scheduler Service]  — хранит URL в Redis, раз в час запускает проверку
        │  scraper.tasks
        ▼
 [Extractor Service]  — скрапит страницу, извлекает цену
        │  price.results
        ▼
 [Notifier Service]   — сохраняет цену в PostgreSQL, шлёт алерт в Telegram
```

**Стратегия извлечения цены (в порядке приоритета):**
1. JSON-LD (`schema.org/Product`) из обычного HTTP-ответа
2. `__NEXT_DATA__` — для сайтов на Next.js (Gold Apple и др.)
3. LLM (Groq, llama-3.3-70b) на сыром HTML
4. Headless Chrome (Chromium) — для JS-rendered страниц и сайтов, блокирующих обычные запросы
5. Повтор шагов 1–3 на HTML от headless

## Стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Go 1.26 |
| База данных | PostgreSQL 16 (pgx/v5, без ORM) |
| Очередь сообщений | RabbitMQ 3.13 |
| Кэш / дедупликация | Redis 7 |
| Бот | go-telegram-bot-api/v5 |
| Поиск товаров | Serper API (Google Shopping) |
| LLM | Groq API (llama-3.3-70b-versatile) |
| Headless браузер | Chromium + chromedp |

## Запуск

### 1. Зависимости

- Docker и Docker Compose
- Аккаунты: [Serper](https://serper.dev), [Groq](https://console.groq.com), Telegram Bot Token (`@BotFather`)

### 2. Переменные окружения

```bash
cp .env.example .env
```

Заполнить в `.env`:

```env
TELEGRAM_BOT_TOKEN=...   # от @BotFather
SERPER_API_KEY=...        # от serper.dev
GROQ_API_KEY=...          # от console.groq.com
```

### 3. Запуск

```bash
docker compose up --build -d
```

Сервисы поднимаются в правильном порядке автоматически. RabbitMQ management UI: http://localhost:15672 (guest/guest).

### 4. Проверка

Открыть бот в Telegram и написать название товара, например: `iPhone 15 Pro`.

### Остановка

```bash
docker compose down        # остановить, данные сохраняются
docker compose down -v     # остановить и удалить все данные (PostgreSQL, Redis)
```

## Функции бота

| Действие | Как |
|----------|-----|
| Найти товар | Написать название в чат |
| Выбрать магазины | Inline-кнопки с названием, ценой и ссылкой |
| Отмена поиска | Кнопка 🚫 Отмена в списке магазинов |
| Мои товары | Кнопка «📋 Мои товары» или `/mylist` |
| Поставить на паузу | Кнопка ⏸ в списке товаров |
| Возобновить | Кнопка ▶ в списке товаров |
| Изменить диапазон цен | Кнопка ✏️ в списке товаров |
| История цен | Кнопка 📊 История |
| Принудительная проверка | Кнопка 🔄 Проверить |
| Удалить товар | Кнопка 🗑 Удалить (снизу каждого товара) |

## Структура проекта

```
services/
  bot/          — Telegram-бот (пользовательский интерфейс)
  discovery/    — HTTP API, поиск товаров через Serper
  scheduler/    — тик-луп, планирование проверок через Redis
  extractor/    — скрапинг цен (HTTP + headless + LLM)
  notifier/     — сохранение цен, отправка алертов
shared/
  pkg/broker/      — обёртка над RabbitMQ (amqp091-go)
  pkg/contracts/   — типы сообщений для всех очередей
migrations/
  init.sql      — схема БД (применяется при первом запуске)
```

## Известные проблемы

### Магазины, которые не парсятся

| Магазин | Проблема | Статус |
|---------|----------|--------|
| **Ozon** | TLS fingerprint блокировка — соединение обрывается до получения ответа | Частично: headless Chrome использует реальный browser fingerprint, но Ozon может блокировать и его |
| **Amazon** | Возвращает 200 OK с CAPTCHA-страницей вместо товара | Не решено: статус 200 не триггерит headless fallback |
| **Wildberries** | Цена рендерится через JS после загрузки страницы | Частично: headless ждёт 3 сек после появления `body`, но цена может не успеть отрендериться |
| **Gold Apple** | Next.js SSR: цена лежит в `__NEXT_DATA__` JSON | Добавлен парсер `__NEXT_DATA__`, требует тестирования |

### Другие известные проблемы

- **Каталожные URL в поиске** — если запрос слишком общий (например, «кроссовки Nike»), discovery возвращает ссылки на каталоги, а не конкретные товары. Обходной путь: вводить точную модель.
- **Схема БД не мигрирует автоматически** — `init.sql` применяется только при первом создании контейнера PostgreSQL. При изменении схемы нужно пересоздать том: `docker compose down -v && docker compose up -d`.
- **Amazon CAPTCHA не детектируется** — сайт возвращает 200 OK, поэтому headless fallback не активируется. Нужна отдельная проверка содержимого ответа.

## Локальная разработка

```bash
# запустить только инфраструктуру
docker compose up -d rabbitmq redis postgres

# запустить один сервис локально
cd services/bot && go run ./cmd/

# собрать все сервисы
go build github.com/Gergov00/pricescount/services/bot/... \
         github.com/Gergov00/pricescount/services/discovery/... \
         github.com/Gergov00/pricescount/services/scheduler/... \
         github.com/Gergov00/pricescount/services/extractor/... \
         github.com/Gergov00/pricescount/services/notifier/...

# линтер
golangci-lint run
```
