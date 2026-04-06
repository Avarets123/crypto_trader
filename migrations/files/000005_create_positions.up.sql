BEGIN;

CREATE TABLE IF NOT EXISTS trades (
    id               BIGSERIAL PRIMARY KEY,
    strategy         TEXT          NOT NULL,
    mode             TEXT          NOT NULL,          -- 'test' | 'prod'
    signal_exchange  TEXT          NOT NULL,          -- биржа-источник сигнала ('binance')
    trade_exchange   TEXT          NOT NULL,          -- биржа исполнения ('bybit')
    symbol           TEXT          NOT NULL,
    qty              DECIMAL(20,8) NOT NULL,
    entry_price      DECIMAL(20,8) NOT NULL,
    target_price     DECIMAL(20,8),                   -- цель TP
    stop_loss_price  DECIMAL(20,8),                   -- уровень SL
    exit_price       DECIMAL(20,8),
    exit_reason      TEXT,                            -- 'tp' | 'sl' | 'timeout' | 'manual'
    pnl_usdt         DECIMAL(20,8),
    entry_order_id   TEXT,                            -- ID ордера открытия (NULL в test-режиме)
    exit_order_id    TEXT,                            -- ID ордера закрытия (NULL в test-режиме)
    signal_data      JSONB,
    opened_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    closed_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_trades_strategy_symbol ON trades (strategy, symbol, opened_at DESC);
CREATE INDEX IF NOT EXISTS idx_trades_open ON trades (trade_exchange, mode, closed_at) WHERE closed_at IS NULL;

COMMIT;
