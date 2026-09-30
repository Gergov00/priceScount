# Audit repair implementation results

Implementation evidence for the 15 findings S1–S8/F4–F10 is summarized below. Task 1–5 production changes and scoped commits were independently reviewed by the controller. Task 6 persistence, deployment, CI, and missing-test work is implemented on top of `3cbf96e`; a fresh independent review of the final Task 6 diff is pending. This report records evidence, not a production deployment approval.

| Finding | Evidence in implementation and tests | Verification |
| --- | --- | --- |
| S1 / F3 | Bot chat FIFO, copy-safe session state, conditional lookup completion; `TestDispatcherPreservesChatFIFOAndRunsOtherChats` and `TestDelayedPollLookupAfterCancelOrReplacementDoesNotSendOrOverwrite` additionally prove ordering and suppression of late ignored-context completion. | Six-module race suite and `-Coverage` / `-Regression` runner PASS. |
| S2 / F1 | Gateway transactions persist state and outbox together; `TestNotificationOutboxSurvivesBrokerUnavailable`, `TestPriceResultRollbackLeavesNoPartialEffects`, `TestDeleteRetryBrokerUnavailable`. | Gateway PostgreSQL/RabbitMQ integration and race PASS. |
| S3 / F2 | Notifier classifies transient/permanent delivery; malformed 400/403 responses use permanent status diagnostics; `TestNonJSONClientErrorsArePermanentWithHTTPDiagnostic`, `TestPermanentDLQDiagnosticIsRedacted`, `TestRetryPublishFailureDoesNotAck`. | Notifier unit and broker integration/race PASS. |
| S4 | Context-bound Telegram delivery and retry waits; `TestSendCancelled`, `TestContextCancellationDoesNotAck`; Bot fake-server token sanitization coverage. | Unit/race PASS; fake Telegram endpoint only. |
| S5 | Browser fetch context cancellation; controlled local-page browser cancellation test. | Existing controller evidence: local Chrome controlled cancellation with race PASS; no Wildberries request. |
| S6 / F6 | Scheduler due/outbox and force schedule invariants; `TestForceScheduleUnchanged`, `TestForceReplaySingleTask`, `TestSuccessfulForceTimestampBlocksOlderPeriodicResult`. | Scheduler integration/race PASS. |
| S7 | Named RabbitMQ volume and stable nodename/hostname; `tests/recover-broker.ps1` publishes confirmed persistent body, forcibly recreates only its own broker using the same named volume, and consumes that body. | Script PASS on 2026-10-01; cleanup scoped to its unique test project. |
| S8 | Sanitized delivery diagnostics; `TestPermanentDLQDiagnosticIsRedacted`, `TestTransferFailureEmitsRedactedBrokerDiagnostic`. | Notifier tests PASS, assert diagnostic content and absence of fake token. |
| F4 | Product lock serializes concurrent pause/add snapshot mutations; `TestConcurrentPauseAndAddKeepsLatestSnapshotActive` exercises both lock orders and asserts latest active snapshot. | PostgreSQL integration/race PASS. |
| F5 | Force result carries requester identity; `TestForcePausedAndFailedFetch`. | Gateway PostgreSQL/RabbitMQ integration/race PASS. |
| F7 | Changed/restored subscription thresholds reset stale state; `TestPausedIdenticalCreatePreservesAlertAndReactivatesSnapshot`, `TestRestoredSubscriptionWithChangedThresholdsClearsAlert`. | Gateway integration/race PASS. |
| F8 | TaskID result deduplication and timestamp policy; `TestConcurrentResultSingleTransition`, `TestLookupResultTaskIDDeduplicatesAtomically`, `TestOlderResultDoesNotRevertState`, `TestEqualTimestampDifferentTaskIDsKeepBothHistoryRowsAndTransitions`. | Gateway PostgreSQL integration/race PASS. |
| F9 | Idempotent delete persists event in outbox despite broker outage; `TestDeleteRetryBrokerUnavailable`. | Gateway integration/race PASS. |
| F10 | Paginated list behavior and UTF-16 bounds; `TestMyListUnicodePagesKeepEveryProductAccessible`, `TestMyListPageClampsAfterDeletingLastItem`, `TestBuildMyListPagesStayWithinTelegramMessageLimit`. | Bot race suite PASS. |

Task 6 adds `TestLegacySchemaUpgradeTwicePreservesUserSubscriptionHistoryAndSchedule` and `TestLegacyDuplicateScheduledProductsFailWithoutDeletingData`. The historical fixture uses the pre-repair schema; it confirms additive migration can run twice, preserves seeded records and backfills the latest result, and duplicate product schedules fail without deleting either row. Production guidance requires manual duplicate reconciliation before a backup-backed migration invoked with `psql -v ON_ERROR_STOP=1 --single-transaction`.

Task 6 adds Fx lifecycle checks in Gateway, Scheduler, Extractor, Bot, and Notifier command packages. All use controlled queues, fake dependencies, or a fake Telegram endpoint. The Notifier lifecycle test holds an in-flight sender after cancellation and proves `app.Stop` waits before closing a registered dependency. The other command tests verify controlled start/stop wiring, not dependency ordering beyond what their assertions exercise.

Deployment documentation now explains RabbitMQ's stable hostname/nodename requirement and preservation of existing anonymous-volume data. Mnesia data must retain the actual old node name and compatible version; definitions export/import does not preserve queued message bodies. README cutover steps halt ingress/new ticks, drain `track.requests` with the old Scheduler, then keep old Gateway/Extractor/Notifier consumers active while dependent queues drain; migration is additive and transactional, and outbox/queue health is checked before deleting old recovery evidence. No production cutover was run.

## Verification on 2026-10-01

- All six Go modules: `go test -race -count=1 -timeout=120s ./...` passed.
- `scripts/test-integration.ps1 -Coverage` passed on isolated PostgreSQL 16/RabbitMQ 3.13 project `pricescount-tests-7c21802065b4`; statement coverage was 55.6%. The runner removed its project.
- `scripts/test-integration.ps1 -Regression` passed on isolated project `pricescount-tests-02b1edaaf331`; the runner removed its project.
- Controller independently confirmed all-six integration/regression race tests, `go build`, and `go vet`; after the last helper-only change, `shared` race and vet passed again.
- All five root-context Docker builds passed with `pricescount-audit-*` tags.
- Broker recovery initially hit two test-harness defects (strict access to a not-yet-present management counter; then an incorrect assumption that the production wrapper's `Close` returns an error). The two isolated failed projects are recorded in `tests/TEST_REPORT.md`, inspected and removed. After fixes, a fresh run recovered the original confirmed persistent message and cleaned its own project.
- `git diff --check` passed. Browser cancellation PASS is prior controller evidence, not a test rerun for Task 6.

Coverage is not 100%. No real Telegram or Wildberries calls, production containers, production volumes, or production data were accessed. The final independent controller review is pending.
