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

-- Decoded UWE-4 housekeeping beacons. The same beacon heard by two ground
-- stations has the same bytes, so (norad_id, frame) keeps only the first.
CREATE TABLE IF NOT EXISTS telemetry (
    id              BIGSERIAL   PRIMARY KEY,
    norad_id        INT         NOT NULL,
    frame           BYTEA       NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL, -- when the first station heard it
    observation_id  BIGINT,       -- NULL for frames not uploaded through SatNOGS Network
    station_id      INT,
    batt_a_mv       INT         NOT NULL,
    batt_a_ma       INT         NOT NULL,
    batt_a_temp_c   INT         NOT NULL,
    batt_a_pct      INT         NOT NULL,
    batt_b_mv       INT         NOT NULL,
    batt_b_ma       INT         NOT NULL,
    batt_b_temp_c   INT         NOT NULL,
    batt_b_pct      INT         NOT NULL,
    power_mw        INT         NOT NULL,
    obc_temp_c      INT         NOT NULL,
    uptime_s        BIGINT      NOT NULL,
    UNIQUE (norad_id, frame)
);

CREATE INDEX IF NOT EXISTS telemetry_norad_received
    ON telemetry (norad_id, received_at DESC);
