-- Products being tracked
CREATE TABLE IF NOT EXISTS products (
    id         UUID PRIMARY KEY,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Individual product URLs to monitor
CREATE TABLE IF NOT EXISTS tracked_urls (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           UUID        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    url                  TEXT        NOT NULL UNIQUE,
    source               TEXT        NOT NULL,
    last_checked_at      TIMESTAMPTZ,
    check_interval_hours INT         NOT NULL DEFAULT 1,
    active               BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Price snapshots over time
CREATE TABLE IF NOT EXISTS price_history (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    url_id     UUID         NOT NULL REFERENCES tracked_urls(id) ON DELETE CASCADE,
    price      NUMERIC(12,2) NOT NULL,
    currency   VARCHAR(3)   NOT NULL DEFAULT 'USD',
    scraped_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- User alert subscriptions
CREATE TABLE IF NOT EXISTS subscriptions (
    id                   UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           UUID         NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    user_id              TEXT         NOT NULL,
    target_price         NUMERIC(12,2) NOT NULL,
    notification_channel TEXT         NOT NULL DEFAULT 'telegram',
    active               BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracked_urls_product_id  ON tracked_urls(product_id);
CREATE INDEX IF NOT EXISTS idx_price_history_url_id     ON price_history(url_id);
CREATE INDEX IF NOT EXISTS idx_price_history_scraped_at ON price_history(scraped_at DESC);
CREATE INDEX IF NOT EXISTS idx_subscriptions_product_id ON subscriptions(product_id);
