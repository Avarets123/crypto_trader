CREATE TABLE IF NOT EXISTS news (
    id           BIGSERIAL    PRIMARY KEY,
    source       TEXT         NOT NULL,
    guid         TEXT         NOT NULL UNIQUE,
    title        TEXT         NOT NULL,
    link         TEXT         NOT NULL,
    description  TEXT,
    summary      TEXT,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_news_source       ON news (source);
CREATE INDEX IF NOT EXISTS idx_news_published_at ON news (published_at DESC);
