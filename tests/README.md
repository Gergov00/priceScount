# Тесты priceScount

Unit-тесты используют реальные функции, HTTP-клиент с httptest и doubles только для внешних операций. Интеграционные тесты запускаются с build tag `integration`, регрессионные проверки незакрытых дефектов — с `regression`.

## Unit-тесты

Из корня workspace:

```powershell
go test -race -count=1 ./services/bot/... ./services/gateway/... ./services/scheduler/... ./services/extractor/... ./services/notifier/... ./shared/...
```

`go test ./...` из корня не пересекает границы модулей этого workspace.

## Интеграционные тесты

Нужны Go 1.26, рабочий race detector (CGO и C-компилятор), Docker Desktop с Linux containers и PowerShell. Команда создаёт отдельный Compose-проект с уникальным именем, динамическими localhost-портами и тестовыми учётными данными, запускает unit и integration тесты и удаляет свои контейнеры в finally:

```powershell
./scripts/test-integration.ps1
./scripts/test-integration.ps1 -Coverage
```

Профиль покрытия сохраняется в `tests/results/coverage.out` (gitignored):

```powershell
go tool cover -func=tests/results/coverage.out
```

Тесты БД создают отдельную схему на каждый тест, применяют настоящую `migrations/init.sql` и удаляют только созданную схему. Они не очищают общие таблицы. Тесты RabbitMQ используют уникальные имена очередей и удаляют только свои очереди.

Проверяются PostgreSQL lookup/subscription/history lifecycle и ownership, конкурентный upsert товара, конкурентный захват расписания и `SKIP LOCKED`. RabbitMQ проверяет persistent JSON, Ack/Nack и redelivery, ошибки JSON и переподключение publisher. Gateway pipeline соединяет настоящий HTTP handler, PostgreSQL store, RabbitMQ и consumer цены до notify.tasks. Граница скрапера контролируется тестом; реальные Wildberries и Telegram не вызываются.

Для своей изолированной инфраструктуры можно запустить вручную:

```powershell
$env:TEST_POSTGRES_DSN='postgres://test:test@127.0.0.1:5432/testdb?sslmode=disable'
$env:TEST_RABBITMQ_URL='amqp://test:test@127.0.0.1:5672/'
go test -race -count=1 -tags=integration ./services/gateway/... ./services/scheduler/... ./shared/...
```

При явном выборе `integration` отсутствующие переменные/недоступная инфраструктура вызывают FAIL, а не тихий skip.

## Незакрытые дефекты review

```powershell
./scripts/test-integration.ps1 -Regression
```

Этот режим включает проверки желаемого корректного поведения. До исправления production-кода часть проверок падает: гонка Session, oversized /mylist, потеря публикации алерта, Ack недоставленного уведомления, токен в ошибке транспорта, устаревший alert_state после восстановления подписки и включение paused расписания при force. Они не закрепляют ошибочное поведение как норму и не скрываются через t.Skip.

Отдельные регрессионные проверки могут уже проходить — это контроль существующей гарантии. Успех стандартного набора не означает закрытия всего review. Перевести регрессионный тест в обычный набор следует вместе с соответствующим исправлением и проверенным red → green.

Тесты не гарантируют 100% покрытия всех файлов. Главные функции wiring в cmd, настоящий Chromium, внешняя доставка Telegram, аварийное восстановление всего Compose-приложения и outbox/versioning из ещё не реализованного дизайна требуют отдельного этапа. Полные интеграционные проверки нового дизайна добавляются вместе с его реализацией.
