# Результаты проверки 2026-09-30

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

Production-код не изменён. Успешный стандартный набор не означает устранения дефектов из PROJECT_REVIEW.md. Регрессионные проверки следует переносить в основной набор вместе с исправлениями. Это не полное покрытие всех файлов: cmd wiring, настоящий браузер, внешняя доставка и аварийное восстановление приложения остаются за пределами этого запуска.

Runner создаёт отдельный Compose-проект с динамическими localhost-портами и удаляет свои контейнеры после успешного и неуспешного запуска. Схемы БД и очереди изолированы. Инструкции запуска: [README.md](README.md).
