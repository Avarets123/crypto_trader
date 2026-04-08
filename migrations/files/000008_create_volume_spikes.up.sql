BEGIN;

CREATE TABLE IF NOT EXISTS volume_spikes (
    id          BIGSERIAL     PRIMARY KEY,
    symbol      VARCHAR(20)   NOT NULL,
    exchange    VARCHAR(20)   NOT NULL,
    type        VARCHAR(1)    NOT NULL,               -- 'A' | 'B' | 'C'
    spike_ratio DECIMAL(10,4) NOT NULL,               -- current / avg
    volume_usdt DECIMAL(20,2) NOT NULL,               -- текущий объём в USDT
    avg_usdt    DECIMAL(20,2) NOT NULL,               -- скользящее среднее в USDT
    price       DECIMAL(20,8) NOT NULL,
    change_pct  DECIMAL(10,4) NOT NULL,
    detected_at TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_volume_spikes_symbol   ON volume_spikes (symbol, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_volume_spikes_type     ON volume_spikes (type, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_volume_spikes_exchange ON volume_spikes (exchange, detected_at DESC);

COMMIT;
