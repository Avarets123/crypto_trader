BEGIN;

-- Удаляем signal_data из trades
ALTER TABLE trades DROP COLUMN IF EXISTS signal_data;

-- Добавляем spread_id: ссылка на спред, который спровоцировал открытие сделки
ALTER TABLE trades
    ADD COLUMN IF NOT EXISTS spread_id BIGINT REFERENCES spreads(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_trades_spread_id ON trades (spread_id) WHERE spread_id IS NOT NULL;

COMMIT;
