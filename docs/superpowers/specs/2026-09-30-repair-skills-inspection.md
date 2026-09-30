# Скиллы для исправлений: подбор и проверка

Поиск выполнен через `find-skills`: `golang testing`, `postgres best practices`, затем `golang-testing --owner samber`. Используются уже доступные локальные копии. Ничего дополнительно не установлено.

| Скилл | Назначение | SkillSpector | Итог ручной проверки |
|---|---|---|---|
| samber/cc-skills-golang@golang-testing | регрессионные, race и integration тесты | 51/100 HIGH, DO_NOT_INSTALL, partial | APPROVE: все четыре HIGH-срабатывания разобраны и объяснены ниже |
| supabase/agent-skills@supabase-postgres-best-practices | транзакции, блокировки, индексы и миграции | 0/100 LOW, CAUTION | APPROVE: инструкции/SQL-примеры, нет скрытого исполнения |
| superpowers:brainstorming 6.4.2 | архитектурный дизайн | 80/100 HIGH, DO_NOT_INSTALL | CAUTION: необязательные server/background/file-cleanup операции; в этой задаче используется текстовый дизайн |
| superpowers:writing-plans 6.4.2 | implementation plan после одобрения дизайна | 0/100 LOW, CAUTION | APPROVE: текстовая последовательность планирования |

Проверка — static scan `--no-llm` и ручной source-aware анализ. Итог ручной проверки отличается от scanner recommendation только после чтения контекста срабатываний. LLM-анализаторы не запускались, часть ссылок на документы не разрешена; нулевой score не является гарантией безопасности.

## HIGH-срабатывания golang-testing

- PE3 в `evals/evals.json:164,165,169`: фраза `access tokenCache` описывает закрытый cache токенизатора в учебной задаче. Это не access token авторизации, не чтение credential files и не выполняемый код. Сам сценарий учит не обращаться к внутреннему cache.
- P6 в `references/examples.md:32`: `Output directives` — комментарии Go `// Output:` и `// Unordered output:`, используемые для проверки stdout Example-функций. Инструкций раскрывать системный prompt нет.
- `SKILL.md` содержит обычную явную команду установки gotests; автоустановка не выполнялась. Применение рекомендаций тестирования не требует запуска eval prompts или установки дополнительных генераторов.

## Поведение brainstorming

- P2 в `scripts/server.cjs:174`: HTML страницы ожидания локального визуального помощника; это текст UI, а не команда отменить системные инструкции.
- TM1 в `scripts/stop-server.sh:22`: очистка session server-info; скрипт также останавливает свой процесс по instance ID и удаляет временный каталог `/tmp`. Это реальное изменение файлов/процесса, явно связанное с lifecycle помощника, поэтому пакет получает CAUTION.
- RA2/PE2: nohup/disown/chmod создают необязательный фоновый сервер и защищают его state. Это не требуется текстовому планированию.
- Сервер содержит внешнюю branding-image ссылку и работу с session key; визуальный помощник в этой задаче не запускался. Scripts target скиллов не исполнялись во время проверки.

В brainstorming есть обязательный пользовательский gate: архитектурный письменный дизайн должен быть просмотрен и одобрен до writing-plans/реализации. Этот gate явно влияет на текущий этап, в отличие от рекомендаций сканера.

## Результаты сканирования

- `D:/pricescount-golang-testing-inspector.json`
- `D:/pricescount-postgres-inspector.json`
- `D:/pricescount-brainstorming-inspector.json`
- `D:/pricescount-writing-plans-inspector.json`

Subagent-driven-development и TDD будут применены на этапе реализации после design gate; их полная проверка исполнения и релевантных supporting files выполняется перед использованием. Подбор скиллов не является заявлением о закрытии багов.
