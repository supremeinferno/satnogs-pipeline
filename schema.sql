CREATE TABLE IF NOT EXISTS observations (
    id         BIGINT      PRIMARY KEY, -- SatNOGS's own observation id
    norad_id   INT         NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at   TIMESTAMPTZ NOT NULL,
    status     TEXT        NOT NULL,
    frame_files INT        NOT NULL DEFAULT 0 -- demod files SatNOGS has for it
);

CREATE INDEX IF NOT EXISTS observations_norad_started
    ON observations (norad_id, started_at DESC);
