# Audit Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Закрыть 15 замечаний из PROJECT_REVIEW.md без потери пользовательских данных и событий при временных сбоях.

**Architecture:** PostgreSQL-транзакции сохраняют изменения и outbox-события вместе. Общий доставщик публикует их с подтверждением RabbitMQ; versioned snapshots защищают Scheduler от устаревших команд. Bot сериализует диалог, Notifier ограничивает сетевые операции и сохраняет временно недоставленные сообщения.

**Tech Stack:** Go 1.26, PostgreSQL 16, RabbitMQ, pgx/v5, amqp091-go, fx, Gin, Telegram Bot API, chromedp.

**Spec:** `docs/superpowers/specs/2026-09-30-audit-repair-design.md`

## Global Constraints

- Go 1.26, PostgreSQL 16, RabbitMQ и существующие библиотеки; дополнительные production-зависимости без необходимости не вводить.
- Сохранить существующие пользовательские данные. Миграции добавляют поля/таблицы и backfill; никакого `down -v`.
- Интерфейсы определяет потребитель, wiring выполняется через fx; соблюдать `CLAUDE.md`.
- Ни один сетевой вызов не выполняется внутри PostgreSQL-транзакции.
- Субагенты: только `gpt-6-luna`, `reasoning_effort=low`; не повышать модель без нового указания пользователя.
- Не отправлять реальные сообщения пользователям Telegram и не использовать production-данные в тестах.
- По последнему указанию пользователя сначала исправляются баги, затем добавляются недостающие тесты. Уже существующие регрессионные проверки используются для подтверждения результата; длительные и инфраструктурные тесты имеют build tag `integration`.
- Не обещать exactly-once Telegram: возможен дубль после успешного send и падения до Ack. Цель — отсутствие тихой потери и идемпотентность до внешней доставки.
- Работать одним implementer за раз, затем отдельный reviewer Luna/low. Проверки должны соответствовать новому контракту, а не сохранять удалённые вызовы Publish из старых mocks.
- Публичные REST маршруты и JSON формы сохранить. Production-инфраструктуру автоматически не мигрировать и не перезапускать; deployment подготовить как проверенную инструкцию.

## Review Focus

- Publish принят брокером, но процесс упал до mark: повтор допустим; стабильный ID предотвращает повторную обработку downstream. Проверки в задачах 1–3.
- Lease истёк во время Publish: старый воркер не может завершить lease нового. Проверка в задаче 1.
- Поздний PriceResult после нового результата: история сохраняется, актуальное alert_state не откатывается. Проверка в задаче 2.
- Force для paused подписки и ошибка scraper: ответ получает только инициатор, расписание остаётся прежним. Проверки в задачах 2–3.
- /cancel, новый lookup и удаление последнего элемента страницы: поздний ответ не восстанавливает диалог, список остаётся доступным. Проверки в задаче 5.

## Карта файлов

Сохранять существующую раскладку; отделять новые обязанности в файлы своего пакета:

- `shared/pkg/broker/rabbitmq.go`: соединение, отдельные каналы consumer/publisher, confirms и returns.
- `shared/pkg/outbox/worker.go`: цикл доставки и consumer-owned Store/Publisher interfaces, без SQL.
- `shared/pkg/contracts/messages.go`: versioned TrackRequest, стабильные IDs команд и уведомлений.
- `migrations/init.sql`: idempotent обновление схемы; уже существующая точка инициализации БД.
- `services/{gateway,scheduler}/internal/store/outbox.go`: операции своих outbox-таблиц.
- `services/gateway/internal/store/monitoring.go`: атомарные изменения подписок, snapshots и force.
- `services/gateway/internal/store/results.go`: атомарные результаты, дедупликация и notify outbox.
- `services/scheduler/internal/store/dispatch.go`: snapshots, due tasks и одноразовые force tasks.
- `services/notifier/internal/alert/alert.go`, `consumer/consumer.go`: context, доставка и retry routing.
- `services/bot/internal/bot/dispatch.go`, `mylist.go`, `state/state.go`: порядок updates, страницы и безопасные сессии.
- Существующие cmd/config/consumer/handler файлы: интерфейсы и fx lifecycle нового поведения.
- `docker-compose.yaml`, `docker-compose.test.yaml`, `.github/workflows/test.yml`, `README.md`, `tests/README.md`: persistence, проверки и переход протокола.

## Task 1: Подтверждённая публикация и outbox primitive

**Files:** Modify `shared/pkg/broker/rabbitmq.go`; create `shared/pkg/outbox/worker.go`, `worker_test.go`; extend `shared/pkg/broker/rabbitmq_integration_test.go`.

**Interfaces:**
- Сохранить `Connection.Publish(ctx context.Context, queue string, v any) error` и существующие Consume API.
- Outbox `Event` имеет `ID, LeaseToken, Queue string`, `Payload json.RawMessage`, `Attempts int`.
- Consumer-owned `Store`: `Claim(ctx context.Context, limit int, lease time.Duration) ([]Event, error)`, `MarkPublished(ctx context.Context, id, leaseToken string) error`, `Retry(ctx context.Context, id, leaseToken, lastError string, delay time.Duration) error`.
- Consumer-owned `Publisher`: существующий Publish signature.
- `New(st Store, publisher Publisher) *Worker`, `(*Worker).Run(ctx context.Context) error`; claim one event immediately before publishing, lease=60s, publish timeout=10s, backoff=1s…60s. This adds one claim transaction per event but prevents queued items' leases aging during earlier publishes.

- [x] Implement отдельный publisher channel, один in-flight publish, confirms, mandatory routing/returns и context-bound ожидание. Serial lock ожидания не блокирует получение consumer channel или Close; redial имеет dial deadline. Ошибка routing/confirm не считается успехом.
- [x] Implement claim → Publish → mark/retry loop вне DB-транзакции; ctx отменяет idle/backoff. Для некорректного payload оставить событие с ошибкой, без удаления и без tight loop.
- [x] Add tests `TestPublishUnroutableFails`, `TestPublishCancelled`, `TestWorkerRetriesThenPublishes`, `TestWorkerStopsOnCancel`: возвращённое сообщение → error; cancelled ctx → error; publish failure → Retry, без MarkPublished; следующий успех → mark с тем же lease token.
- [x] Add a two-worker controlled-clock regression: successful early confirms advance virtual time by 9s each; while the eighth event is held after the old batch lease expires, the second worker must publish the still-unclaimed ninth event without concurrent delivery of the eighth. The test fails against the former 20-event claim.
- [x] Verify `go test -race -count=1 ./shared/...`; инфраструктурные cases запускаются общим integration runner. PASS, существующие Ack/redelivery/reconnect не нарушены.
- [x] Review diff отдельным Luna/low reviewer, исправить существенные замечания. Commit только файлы этой задачи после проверок, не включать посторонние изменения.

## Task 2: Gateway — атомарные подписки, события и результаты

**Files:** Modify `migrations/init.sql`, gateway `store/store.go`, `handler/handler.go`, `consumer/consumer.go`, `cmd/gateway/main.go`; create `store/outbox.go`, `monitoring.go`, `results.go`; update соответствующие unit/integration tests.

**Interfaces:**
- `TrackRequest` дополнить `TaskID string`, `Version int64`, `Active bool`; новая Action=`set_state`, прежние JSON поля сохранить. `NotifyTask` дополнить `TaskID string`.
- Сохранить внешние signatures CreateSubscription/PauseSubscription/ResumeSubscription/UpdateThresholds/DeleteSubscription; методы становятся транзакционными и создают snapshots.
- Store `CreateLookup` сохраняет lookup и LookupTask вместе; handler больше не Publish после DB commit.
- Store `QueueForce(ctx context.Context, subID, userID string, chatID int64) (string, error)` возвращает стабильный ID сохранённой команды. Force context хранит requester/product; consumer не доверяет произвольному ChatID результата.
- Consumer-owned Store `ProcessPriceResult(ctx context.Context, result contracts.PriceResult) error` выполняет атомарную обработку; consumer Ack только после nil. Ошибка БД → Nack/requeue.
- Store `ReconcileMonitoring(ctx context.Context) error` сохраняет snapshots всех продуктов, включая inactive. Store outbox методы реализуют интерфейс задачи 1 для `gateway_outbox`.

- [x] Extend schema idempotently: gateway/scheduler outbox таблицы с unique event_key и lease полями; products.monitor_version/latest_result_at, subscriptions.alert_version, price_history.task_id с partial unique index, processed_price_results и force_requests. Для gateway_outbox payload JSONB, attempts, available_at, lease_token, locked_until, published_at, last_error; partial индекс outstanding. Две outbox-таблицы имеют одинаковый набор полей. Существующие строки/индексы сохранить.
- [x] Implement product row lock прежде изменения подписки; recompute desired_active, version и outbox snapshot в одной транзакции. Delete повтор владельца → nil, неизвестный/чужой → ErrNotFound; restore/новые пороги сбрасывают alert_state, идентичный Create сохраняет его.
- [x] Implement atomic results: unique TaskID, history, timestamp ordering, serialized threshold transition, stable notify key по subscription+alert_version. Duplicate result → nil без побочных действий. Missing TaskID в новом протоколе reject без горячего requeue; старые сообщения обрабатываются до cutover по инструкции задачи 6.
- [x] Implement saved force request: ответ инициатору и при paused, и при fetch failure; unknown/mismatched force TaskID не создаёт чужих уведомлений. Periodic failure не меняет alert_state.
- [x] Adapt handler/consumer к transactional Store, убрать DB-then-Publish пути; добавить outbox lifecycle worker и retryable reconciliation в fx. Старые helper методы не оставить альтернативным production-путём обхода outbox.
- [x] Add DB tests `TestSubscriptionSnapshotAtomic`, `TestDeleteRetryBrokerUnavailable`, `TestConcurrentResultSingleTransition`, `TestOlderResultDoesNotRevertState`, `TestForcePausedAndFailedFetch`, `TestLeaseOwnership`: rollback не оставляет событие; два DELETE→204/204; две DB connections с TaskID дают history=1/notify=1; старый timestamp не меняет зону; force адресован одному requester; mark старого lease не меняет запись.
- [x] Promote corrected F7 regression в обычный integration набор. S2 regression переписать на гарантию transaction commit/outbox delivery вместо требования синхронного Publish, подтвердить сохранение задачи при брокере offline.
- [x] Verify targeted gateway `-race`, общим integration runner реальные DB и RabbitMQ сценарии, миграцию дважды и сохранность старой fixture. Review Luna/low и commit scoped files.

## Task 3: Scheduler — версии, атомарный tick и force

**Files:** scheduler `store/store.go`, `consumer/consumer.go`, `scheduler/scheduler.go`, `cmd/main.go`; create `store/outbox.go`, `dispatch.go`; extend schema и tests.

**Interfaces:**
- `ApplySnapshot(ctx context.Context, request contracts.TrackRequest) error`: только Version больше хранимой, inactive сохраняет строку.
- `EnqueueDue(ctx context.Context) (int, error)`: transaction SKIP LOCKED, scraper outbox и advance together; не возвращает задания для прямого Publish.
- `EnqueueForce(ctx context.Context, request contracts.TrackRequest) error`: dedup стабильного TaskID, одноразовый scraper event.
- `scheduled_urls.monitor_version BIGINT NOT NULL DEFAULT 0`; force дедупликация отдельной таблицей обработанных команд. Store реализует outbox интерфейс задачи 1 для scheduler_outbox.

- [x] Implement versioned snapshot upsert: обратный порядок и равная версия не меняют active/interval; inactive tombstone не удаляется. URL/product invariant проверяется, некорректные versioned команды не запускают мониторинг.
- [x] Implement atomic EnqueueDue: уникальный TaskID каждой принятой due работы, общий commit переноса даты и event, ограниченная партия=100. Tick не Publish напрямую.
- [x] Implement EnqueueForce по ID: не менять active/next_check_at, не создавать periodic row для одноразового запроса; повтор команды не создаёт второй scraper event. Consumer Ack после commit.
- [x] Wire scheduler outbox worker и shutdown ожидание; удалить вызовы AdvanceNextCheck/SetNextCheck из force production path.
- [x] Add tests `TestSnapshotReverseOrder`, `TestInactiveTombstone`, `TestDueOutboxRollback`, `TestForceScheduleUnchanged`, `TestForceReplaySingleTask`: версии 3 inactive, затем 2 active → inactive; rollback оставляет прежнюю дату и 0 events; force сохраняет обе schedule fields; повтор → 1 outbox event.
- [x] F6 regression перенести на новый force path и включить в обычные integration tests. Прежний delete-replay контроль заменить проверкой tombstone+version.
- [x] Verify scheduler `-race`, интеграции с несколькими workers и остановленным брокером: работа остаётся в outbox и доставляется после восстановления. Review Luna/low, commit scoped files.

## Task 4: Notifier — отменяемая доставка и безопасные ошибки

**Files:** notifier `alert/alert.go`, `consumer/consumer.go`, `config/config.go`, `cmd/main.go`; shared broker queue declarations/constants; соответствующие tests.

**Interfaces:**
- `(*TelegramSender).Send(ctx context.Context, chatID int64, text string) error`; consumer-owned Sender соответствует этой сигнатуре.
- `NewTelegramSender(token string, opts ...Option) *TelegramSender`, `WithHTTPClient(client *http.Client) Option`; default HTTP timeout=10s, injected clients тоже получают request deadline.
- Export `ErrPermanent`, typed `RetryError` с `After time.Duration`; retry_after влияет на ожидание. Default local attempts=4, cancellable delays 0/3/6/12s.
- RabbitMQ durable `notify.retry` delay queue TTL=30s и dead-letter routing=`notify.tasks`; durable `notify.dead` для постоянных ошибок.

- [x] Implement context requests/retry waits и safe transport errors: извлечь url.Error.Err, удалить token из API descriptions; не логировать исходный URL. Классификация сети/429/5xx transient, остальных 4xx permanent.
- [x] Implement consumer: успех Ack; permanent → confirmed publish DLQ затем Ack; transient → confirmed publish retry затем Ack; publish failure → Nack/requeue с отменяемой паузой без tight loop. Shutdown не Ack недоставленное сообщение. Retry_after дольше 30s сохраняется с задачей и учитывается перед следующей попыткой.
- [x] Wire contexts и WaitGroup lifecycle так, чтобы sender stopped до закрытия broker. Малформированный JSON в DLQ, а не вечный retry.
- [x] Adapt existing S3/S8 tests to new APIs and promote to ordinary suite. Add `TestSendCancelled`, `TestRetryAfter`, `TestPermanentDeliveryDeadLetters`, `TestRetryPublishFailureDoesNotAck`: server block отменяется; 429 delay учтён; DLQ success только затем Ack; retry publish failure→не Ack; fake token отсутствует в возвращаемой ошибке и captured log.
- [x] Verify notifier unit `-race` и real broker delay/DLQ routing integration без реального Telegram. Review Luna/low, commit scoped files.

## Task 5: Bot — порядок диалога, страницы; WB — cancellation

**Files:** bot `state/state.go`, `bot/bot.go`, `search.go`, `edit.go`, `mylist.go`, `helpers.go`, `cmd/main.go`; create `bot/dispatch.go`; shared `marketplace/wb.go`; corresponding tests.

**Interfaces:**
- `Store.Get(chatID int64) *Session` возвращает копию, Set сохраняет копию; `Update(chatID int64, fn func(*Session))` выполняет atomic mutation; `CompleteLookup(chatID int64, lookupID string, result Session) bool` применяет результат только текущего waiting lookup.
- Bounded dispatcher `Run(ctx context.Context, updates <-chan tgbotapi.Update) error`: 8 фиксированных FIFO workers, chat hash распределяет updates, callback/query chat учитывается. Lookup I/O запускается отдельно, завершение проверяет ID; все goroutines ожидаются при shutdown.
- `buildMyListPage(subs []gateway.Subscription, page int) (text string, keyboard tgbotapi.InlineKeyboardMarkup, actualPage int)`; максимум 10 товаров, максимум 4096 UTF-16 units текста, длинное имя ограничить 200 units без разрыва Unicode. Navigation callback=`page:<n>`; текущая page в Session.
- Сохранить `FetchProduct(ctx context.Context, url string)` API, browser context наследует cancellation входного ctx и ограничен 40s.

- [x] Implement copy-safe Session и conditional lookup completion. FIFO dispatcher заменяет goroutine на каждый update; /cancel очищает state, позднее завершение не восстанавливает его. Удалить раскрытие pointers и несогласованные read-modify-write paths.
- [x] Implement page rendering/callbacks, ограничение UTF-16, кнопки только текущей страницы; pause/edit/delete сохраняют page с clamp при удалении последнего элемента. Ошибки Send/Request вернуть или обработать safe diagnostic.
- [x] Sanitize Bot transport/API errors перед slog; не писать ошибки библиотеки с URL token в лог. Runtime stop отменяет lookup и waits.
- [x] Implement WB cancellation: already-cancelled ctx не запускает Chrome; context.AfterFunc связывает parent cancellation с browser cancel, освобождается при завершении.
- [x] Update/promote S1/F10 regression. Add `TestLookupCompletionAfterCancel`, `TestChatFIFO`, `TestMyListUnicodePages`, `TestDeleteLastPageItem`, `TestCancelledFetch`: cancel/new ID предотвращают старое completion; FIFO updates не переупорядочены; все страницы <=4096 units, каждый sub доступен; последний элемент clamp; cancellation timely. Browser mid-fetch test под integration с контролируемым локальным server и отдельным Chrome, не реальным WB.
- [x] Verify bot/shared `-race`; ошибка token diagnostics covered for Bot; reviewer Luna/low и scoped commit.

## Task 6: Persistence, cutover, CI и итоговый review

**Files:** `docker-compose.yaml`, `docker-compose.test.yaml`, `.github/workflows/test.yml`, `README.md`, `tests/README.md`, `tests/TEST_REPORT.md`; create `tests/BROKER_RECOVERY.md`, `docs/AUDIT_REPAIR_RESULT.md`.

**Interfaces:** запуск остаётся `scripts/test-integration.ps1`; CI Linux запускает workspace-модули явно, infrastructure tests только с заданными TEST_POSTGRES_DSN/TEST_RABBITMQ_URL. cmd fx smoke checks используют controlled resources.

- [x] Add named volume для `/var/lib/rabbitmq` и стабильный hostname. Документировать сохранение существующих broker данных перед переходом на новый volume; не выполнять удаление рабочего контейнера/volume.
- [x] Write cutover: проверенная DB backup и broker recovery, остановка ingress/ticks, последовательный drain `track.requests` и зависимых очередей старой версией, transactional `init.sql`, запуск полного нового набора, reconciliation и outbox/queue health проверки; rollback сохраняет outboxes.
- [x] Add CI unit/integration race, build и vet всех шести модулей; build Docker images; cache Go. Перевести закрытые regression checks в основной набор.
- [x] Add isolated broker recovery script: unique Compose project с собственным named volume, durable queue и publisher-confirmed persistent task; forced recreate с тем же hostname/volume и consumption прежнего body. Добавить PostgreSQL upgrade fixture и duplicate preflight test.
- [x] Run all six module unit/race, `scripts/test-integration.ps1 -Coverage` and `-Regression`, build/vet всех модулей, Docker builds, recovery script и shutdown smoke checks. Записать фактические результаты и ограничения Chrome/externals.
- [ ] Fresh independent controller review всего diff против spec и CLAUDE.md, особо lease races/confirm returns/deploy compatibility. Исправить существенные замечания и повторить затронутые проверки.
- [x] Write closure matrix S1–S8/F4–F10 с production files, test names и результатами; scoped commit, не push/merge/deploy.

## Self-review и handoff

Все строки coverage matrix spec распределены по задачам: S1/F10/S5 — 5; S2/F4/F5/F7/F8/F9 — 2; S6/F6 — 3; S3/S4/S8 — 4–5; S7 — 6. Review Focus имеет конкретные проверки в задачах. Интерфейсы outbox общие, SQL принадлежит service store; sequential execution исключает одновременные правки contracts/schema/wiring.

Пользователь уже выбрал исполнение с субагентами Luna/low. Перед началом production-кода требуется просмотр этого плана пользователем согласно writing-plans; следующий шаг после подтверждения — subagent-driven-development, один implementer и затем reviewer на этап. Уже написанные тесты сохраняются и адаптируются только при смене внутреннего контракта, недостающие добавляются после исправлений по последнему указанию пользователя.
