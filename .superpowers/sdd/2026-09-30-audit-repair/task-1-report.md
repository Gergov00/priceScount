# Task 1 Report — Confirmed publishing and outbox worker

## Changes

- RabbitMQ consumers retain their consumer connection and channels. Publishing uses a separate AMQP connection and channel with confirms and `mandatory=true`; returned, negatively acknowledged, and missing-confirm publishes return errors.
- The publisher notification listeners are registered once per channel. One in-flight publish is enforced with a context-aware gate. Mandatory returns are retained until the matching confirm arrives, so a return cannot be mistaken for success or leave a stale confirm for the next call.
- The publisher transport applies a bounded write deadline and a context cancellation hook. Cancelled or failed publisher channels and their streams are invalidated before releasing the publish gate, then redialed independently of consumers. Close does not acquire the publish gate.
- Added outbox event/store/publisher interfaces and a worker that claims at most 20 events with 60-second leases, publishes with a 10-second timeout, and marks success or retries with exponential backoff capped at 60 seconds. Payloads remain `json.RawMessage` to preserve large integer values; invalid JSON is retained via `Retry`. Retry and mark failures are logged.
- Added worker retry/cancellation tests and broker integration tests for unroutable messages and cancelled contexts. Existing Ack/requeue and publisher reconnect coverage remains in place.

## Verification

- `go test -race -count=1 ./shared/...` — PASS.
- `TEST_RABBITMQ_URL=amqp://pricescount_test:pricescount_test@127.0.0.1:64939/ go test -race -count=1 -tags=integration ./shared/pkg/broker` — PASS.
- `git diff --check` — PASS.

Independent review will be run by the controller after this scoped commit.
