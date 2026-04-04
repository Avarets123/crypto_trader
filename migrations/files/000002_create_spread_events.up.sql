BEGIN;

CREATE TABLE IF NOT EXISTS spread_events (
    id             BIGSERIAL       PRIMARY KEY,
    symbol         VARCHAR(20)     NOT NULL,
    exchange_high  VARCHAR(20)     NOT NULL,
    exchange_low   VARCHAR(20)     NOT NULL,
    opened_at      TIMESTAMPTZ     NOT NULL,
    closed_at      TIMESTAMPTZ,
    duration_ms    BIGINT,
    max_spread_pct DECIMAL(8, 4)   NOT NULL,
    price_high     DECIMAL(20, 8)  NOT NULL,
    price_low      DECIMAL(20, 8)  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spread_events_symbol     ON spread_events (symbol, opened_at DESC);
CREATE INDEX IF NOT EXISTS idx_spread_events_active     ON spread_events (closed_at) WHERE closed_at IS NULL;

COMMIT;
