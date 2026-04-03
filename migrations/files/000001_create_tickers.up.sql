BEGIN;

CREATE TABLE IF NOT EXISTS tickers (
    id          BIGSERIAL       PRIMARY KEY,
    exchange    VARCHAR(20)     NOT NULL,
    symbol      VARCHAR(30)     NOT NULL,
    quote       VARCHAR(20)     NOT NULL DEFAULT '',
    price       NUMERIC(24, 8)  NOT NULL,
    open_24h    NUMERIC(24, 8),
    high_24h    NUMERIC(24, 8),
    low_24h     NUMERIC(24, 8),
    volume_24h  NUMERIC(36, 8),
    change_pct  NUMERIC(10, 4),
    received_at TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tickers_exchange_symbol ON tickers (exchange, symbol);
CREATE INDEX IF NOT EXISTS idx_tickers_received_at     ON tickers (received_at DESC);

COMMIT;
