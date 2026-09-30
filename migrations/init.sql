-- ─────────────────────────────────────────────────────────────────────────────
-- Gateway PostgreSQL schema
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id    BIGINT      NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS products (
    id         UUID        PRIMARY KEY,
    name       TEXT        NOT NULL,
    url        TEXT        NOT NULL UNIQUE,
    platform   TEXT        NOT NULL, -- "wb"
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE products ADD COLUMN IF NOT EXISTS monitor_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE products ADD COLUMN IF NOT EXISTS latest_result_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS subscriptions (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id  UUID         NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    min_price   NUMERIC(12,2),
    max_price   NUMERIC(12,2),
    paused      BOOLEAN      NOT NULL DEFAULT FALSE,
    active      BOOLEAN      NOT NULL DEFAULT TRUE,
    alert_state TEXT         NOT NULL DEFAULT '', -- '' | 'up' | 'down': direction of last alert
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, product_id)
);

-- Idempotent upgrade for databases created before alert_state existed.
-- This whole file is safe to re-run against an existing database:
--   docker compose exec postgres psql -U pricescount -d pricescount -f /docker-entrypoint-initdb.d/init.sql
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS alert_state TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS alert_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS price_history (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID         NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    price      NUMERIC(12,2) NOT NULL,
    currency   VARCHAR(3)   NOT NULL DEFAULT 'RUB',
    scraped_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
ALTER TABLE price_history ADD COLUMN IF NOT EXISTS task_id UUID;
CREATE UNIQUE INDEX IF NOT EXISTS idx_price_history_task_id ON price_history(task_id) WHERE task_id IS NOT NULL;
UPDATE products p
SET latest_result_at = h.latest_scraped_at
FROM (
    SELECT product_id, MAX(scraped_at) AS latest_scraped_at
    FROM price_history
    GROUP BY product_id
) h
WHERE p.id = h.product_id AND p.latest_result_at IS NULL;

CREATE TABLE IF NOT EXISTS processed_price_results (
    task_id UUID PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS force_requests (
    task_id UUID PRIMARY KEY,
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS gateway_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_key TEXT NOT NULL UNIQUE,
    queue TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts INT NOT NULL DEFAULT 0,
    lease_token UUID,
    locked_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_gateway_outbox_outstanding ON gateway_outbox(available_at, created_at) WHERE published_at IS NULL;
CREATE TABLE IF NOT EXISTS scheduler_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_key TEXT NOT NULL UNIQUE,
    queue TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts INT NOT NULL DEFAULT 0,
    lease_token UUID,
    locked_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_scheduler_outbox_outstanding ON scheduler_outbox(available_at, created_at) WHERE published_at IS NULL;

-- Temporary table for async one-time product lookups.
-- Rows expire after 10 minutes and are cleaned up by the Gateway TTL cleaner.
CREATE TABLE IF NOT EXISTS lookup_requests (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    url        TEXT        NOT NULL,
    status     TEXT        NOT NULL DEFAULT 'pending', -- pending | done | failed
    name       TEXT,
    price      NUMERIC(12,2),
    error      TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '10 minutes'
);

-- ─────────────────────────────────────────────────────────────────────────────
-- Scheduler PostgreSQL schema
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS scheduled_urls (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           UUID        NOT NULL,
    url                  TEXT        NOT NULL UNIQUE,
    platform             TEXT        NOT NULL, -- "wb"
    next_check_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    check_interval_hours INT         NOT NULL DEFAULT 1,
    active               BOOLEAN     NOT NULL DEFAULT TRUE
);
ALTER TABLE scheduled_urls ADD COLUMN IF NOT EXISTS monitor_version BIGINT NOT NULL DEFAULT 0;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM scheduled_urls GROUP BY product_id HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'scheduled_urls has multiple rows for one product; resolve duplicates before applying scheduler identity constraint';
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_urls_product_id ON scheduled_urls(product_id);

CREATE TABLE IF NOT EXISTS processed_force_commands (
    task_id UUID PRIMARY KEY,
    product_id UUID NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─────────────────────────────────────────────────────────────────────────────
-- Indexes
-- ─────────────────────────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_subscriptions_user_id      ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_product_id   ON subscriptions(product_id);
CREATE INDEX IF NOT EXISTS idx_price_history_product_id   ON price_history(product_id);
CREATE INDEX IF NOT EXISTS idx_price_history_scraped_at   ON price_history(scraped_at DESC);
CREATE INDEX IF NOT EXISTS idx_lookup_requests_expires_at ON lookup_requests(expires_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_urls_next_check  ON scheduled_urls(next_check_at) WHERE active = TRUE;
