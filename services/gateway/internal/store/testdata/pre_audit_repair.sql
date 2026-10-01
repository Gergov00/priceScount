-- The pre-repair schema from the original 2026-09-30 review baseline.
-- Intentionally excludes repair-added columns, indexes, and outbox tables.
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id BIGINT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE products (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    url TEXT NOT NULL UNIQUE,
    platform TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    min_price NUMERIC(12,2),
    max_price NUMERIC(12,2),
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    alert_state TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, product_id)
);
CREATE TABLE price_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    price NUMERIC(12,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'RUB',
    scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE scheduled_urls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL,
    url TEXT NOT NULL UNIQUE,
    platform TEXT NOT NULL,
    next_check_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    check_interval_hours INT NOT NULL DEFAULT 1,
    active BOOLEAN NOT NULL DEFAULT TRUE
);
