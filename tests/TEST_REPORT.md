# Исторический baseline: 2026-09-30

Запуск `scripts/test-integration.ps1 -Coverage` завершился успешно: unit и integration тесты всех шести Go-модулей с race detector, PostgreSQL 16 и RabbitMQ 3.13. Покрытие statements: **44,6%**, включая cmd без тестов. `go vet` всех модулей также прошёл.

Проверены HTTP → PostgreSQL → RabbitMQ → price consumer → очередь уведомлений, конкурентные операции БД, владение подписками, история цен, расписания и SKIP LOCKED, Ack/Nack/redelivery и восстановление publisher. Внешние Wildberries и Telegram контролируются doubles.

`scripts/test-integration.ps1 -Regression` завершился с ожидаемым кодом 1 и воспроизвёл семь незакрытых дефектов:

| Проверка | Наблюдаемая ошибка |
| --- | --- |
| ConcurrentMutationOfSharedChatSessionIsSafe | Data race при изменении общей сессии |
| BuildMyListStaysWithinTelegramMessageLimit | 10013 символов вместо допустимых 4096 |
| NotifyPublishFailureMustRequeue | Ошибка публикации не вызывает requeue |
| CreateSubscriptionClearsStaleAlertRegression | Восстановленная подписка сохраняет старый alert_state |
| AdvanceNextCheckKeepsPausedScheduleRegression | Force включает приостановленное расписание |
| TransportErrorDoesNotExposeBotToken | Ошибка содержит тестовый токен |
| TransientSendFailureMustRequeue | Временная ошибка доставки не вызывает requeue |

Production-код на момент baseline не был изменён. Эти результаты сохранены как историческая точка сравнения; текущие результаты см. ниже. Coverage не означает полного покрытия всех файлов.

Runner создаёт отдельный Compose-проект с динамическими localhost-портами и удаляет свои контейнеры после успешного и неуспешного запуска. Схемы БД и очереди изолированы. Инструкции запуска: [README.md](README.md).

## Итоговая проверка Task 6: 2026-10-01

На этой ревизии `scripts/test-integration.ps1 -Coverage` завершился exit 0: все шесть модулей, race detector, PostgreSQL 16 и RabbitMQ 3.13; runner удалил только созданный им проект `pricescount-tests-7c21802065b4`. Общая statement coverage — **55,6%** (`go tool cover '-func=tests/results/coverage.out'`); это не означает полного покрытия.

`scripts/test-integration.ps1 -Regression` завершился exit 0 на всех шести модулях и удалил только созданный им проект `pricescount-tests-02b1edaaf331`. Прямой unit/race прогон всех шести модулей `go test -race -count=1 -timeout=120s ./services/bot/... ./services/gateway/... ./services/scheduler/... ./services/extractor/... ./services/notifier/... ./shared/...` завершился exit 0. Контроллер независимо подтвердил полный `go test -race -count=1 -timeout=120s '-tags=integration,regression'`, `go build` и `go vet` по всем модулям. После изменения только test-helper close handling `shared` повторно прошёл race и vet.

Пять root-context image builds прошли: `pricescount-audit-bot`, `pricescount-audit-gateway`, `pricescount-audit-scheduler`, `pricescount-audit-extractor`, `pricescount-audit-notifier`. В broker recovery script первый запуск остановился до recreate из-за временно отсутствующего `messages` поля management response в строгом режиме; второй — на компиляции helper, где production wrapper `Close` не имеет возвращаемого значения. Эти два только-тестовых Compose проекта (`pricescount-recovery-194e0cfd0e7f` и `pricescount-recovery-9846d46979ff`) проверены и удалены отдельно. После правки script/helper повторный запуск завершился PASS: publisher-confirmed persistent body пережил forced recreate того же брокера и был получен; script удалил только `pricescount-recovery-11a3744911ef` и его named volume.

В рамках Task 6 выполнены `TestConcurrentPauseAndAddKeepsLatestSnapshotActive`, `TestEqualTimestampDifferentTaskIDsKeepBothHistoryRowsAndTransitions`, `TestLegacySchemaUpgradeTwicePreservesUserSubscriptionHistoryAndSchedule`, `TestLegacyDuplicateScheduledProductsFailWithoutDeletingData`, `TestDelayedPollLookupAfterCancelOrReplacementDoesNotSendOrOverwrite`, Notifier non-JSON 4xx tests, emitted broker diagnostic redaction test и пять cmd Fx smoke tests. Gateway interleaving/upgrade и Notifier shutdown ordering прошли в реальном integration/race запуске; Browser cancellation локальной страницы с Chrome ранее подтверждён контроллером. Реальный Telegram/Wildberries и production containers/data не использовались.

Подробная матрица закрытия и ограничений: [AUDIT_REPAIR_RESULT.md](../docs/AUDIT_REPAIR_RESULT.md). Независимый финальный review контроллера этой Task 6 ревизии ещё ожидается.

Follow-up configuration review: Compose was checked using an isolated minimal config fixture, empty fake env file, and fake legacy hostname/nodename/longname env file. Defaults and explicit overrides resolved as expected; no service was started. Existing RabbitMQ restoration docs now require setting both identity values to their recorded values and retaining the source container/volume until restored ready and unacknowledged messages are verified. This is a config/doc-only follow-up; no production data or container was accessed.
