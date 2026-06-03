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

CREATE TABLE IF NOT EXISTS subscriptions (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id UUID         NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    min_price  NUMERIC(12,2),
    max_price  NUMERIC(12,2),
    paused     BOOLEAN      NOT NULL DEFAULT FALSE,
    active     BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, product_id)
);

CREATE TABLE IF NOT EXISTS price_history (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID         NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    price      NUMERIC(12,2) NOT NULL,
    currency   VARCHAR(3)   NOT NULL DEFAULT 'RUB',
    scraped_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

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

-- ─────────────────────────────────────────────────────────────────────────────
-- Indexes
-- ─────────────────────────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_subscriptions_user_id      ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_product_id   ON subscriptions(product_id);
CREATE INDEX IF NOT EXISTS idx_price_history_product_id   ON price_history(product_id);
CREATE INDEX IF NOT EXISTS idx_price_history_scraped_at   ON price_history(scraped_at DESC);
CREATE INDEX IF NOT EXISTS idx_lookup_requests_expires_at ON lookup_requests(expires_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_urls_next_check  ON scheduled_urls(next_check_at) WHERE active = TRUE;
