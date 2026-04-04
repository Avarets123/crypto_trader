BEGIN;

CREATE TABLE IF NOT EXISTS price_change_detections (
    id           BIGSERIAL      PRIMARY KEY,
    type         VARCHAR(10)    NOT NULL,
    symbol       VARCHAR(20)    NOT NULL,
    exchange     VARCHAR(20)    NOT NULL,
    detected_at  TIMESTAMPTZ    NOT NULL,
    window_sec   INT            NOT NULL,
    price_before DECIMAL(20,8)  NOT NULL,
    price_now    DECIMAL(20,8)  NOT NULL,
    change_pct   DECIMAL(10,4)  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_price_change_detections_symbol ON price_change_detections (symbol, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_price_change_detections_type   ON price_change_detections (type, detected_at DESC);

COMMIT;
