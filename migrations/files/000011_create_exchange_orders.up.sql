CREATE TABLE IF NOT EXISTS exchange_orders (
    id         BIGSERIAL    PRIMARY KEY,
    exchange   VARCHAR(20)  NOT NULL,
    symbol     VARCHAR(20)  NOT NULL,
    trade_id   BIGINT       NOT NULL,
    price      NUMERIC      NOT NULL,
    quantity   NUMERIC      NOT NULL,
    side       VARCHAR(4)   NOT NULL,
    trade_time TIMESTAMPTZ  NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE (exchange, symbol, trade_id)
);

CREATE INDEX IF NOT EXISTS idx_exchange_orders_symbol     ON exchange_orders (symbol);
CREATE INDEX IF NOT EXISTS idx_exchange_orders_exchange   ON exchange_orders (exchange);
CREATE INDEX IF NOT EXISTS idx_exchange_orders_trade_time ON exchange_orders (trade_time DESC);
