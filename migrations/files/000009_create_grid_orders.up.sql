CREATE TABLE IF NOT EXISTS grid_orders (
    id           BIGSERIAL PRIMARY KEY,
    session_id   UUID        NOT NULL,
    symbol       TEXT        NOT NULL,
    exchange     TEXT        NOT NULL,
    side         TEXT        NOT NULL,
    level_index  INTEGER     NOT NULL,
    price        NUMERIC(20,8) NOT NULL,
    qty          NUMERIC(20,8) NOT NULL,
    order_id     TEXT        NOT NULL,
    filled_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cycle_pnl_usdt NUMERIC(20,8) NULL
);

CREATE INDEX IF NOT EXISTS idx_grid_orders_session_id ON grid_orders (session_id);
CREATE INDEX IF NOT EXISTS idx_grid_orders_symbol_exchange_filled_at ON grid_orders (symbol, exchange, filled_at DESC);
