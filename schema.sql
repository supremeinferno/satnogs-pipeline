CREATE TABLE IF NOT EXISTS observations (
    id         BIGINT      PRIMARY KEY, -- SatNOGS's own observation id
    norad_id   INT         NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at   TIMESTAMPTZ NOT NULL,
    status     TEXT        NOT NULL,
    frame_files INT        NOT NULL DEFAULT 0, -- demod files SatNOGS has for it
    stored_at  TIMESTAMPTZ NOT NULL DEFAULT now() -- when store first wrote the row
);

-- For a database made before stored_at existed (CREATE TABLE IF NOT EXISTS
-- does not add columns). Its old rows get the time of this statement.
ALTER TABLE observations ADD COLUMN IF NOT EXISTS stored_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS observations_norad_started
    ON observations (norad_id, started_at DESC);

-- Battery readings from every satellite's beacons: one row per battery per
-- frame (UWE-4 has two batteries, "a" and "b"; the others one, "main"). The same
-- beacon heard by two ground stations has the same bytes, so (norad_id, frame,
-- battery) keeps only the first. The optional values are NULL when a beacon does
-- not carry them.
CREATE TABLE IF NOT EXISTS battery_readings (
    id              BIGSERIAL   PRIMARY KEY,
    norad_id        INT         NOT NULL,
    battery         TEXT        NOT NULL,
    frame           BYTEA       NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL, -- when the first station heard it
    observation_id  BIGINT,       -- NULL for frames not uploaded through SatNOGS Network
    station_id      INT,
    voltage_mv      INT         NOT NULL,
    current_ma      INT,          -- positive charging
    temp_c          INT,
    charge_pct      INT,
    UNIQUE (norad_id, frame, battery)
);

CREATE INDEX IF NOT EXISTS battery_readings_series
    ON battery_readings (norad_id, battery, received_at DESC);

-- A database made before several satellites were supported kept UWE-4's
-- readings in a table called telemetry. Move them, once, in one transaction.
DO $$
BEGIN
    IF to_regclass('public.telemetry') IS NOT NULL THEN
        INSERT INTO battery_readings (norad_id, battery, frame, received_at, observation_id, station_id, voltage_mv, current_ma, temp_c, charge_pct)
            SELECT norad_id, 'a', frame, received_at, observation_id, station_id, batt_a_mv, batt_a_ma, batt_a_temp_c, batt_a_pct FROM telemetry
            ON CONFLICT DO NOTHING;
        INSERT INTO battery_readings (norad_id, battery, frame, received_at, observation_id, station_id, voltage_mv, current_ma, temp_c, charge_pct)
            SELECT norad_id, 'b', frame, received_at, observation_id, station_id, batt_b_mv, batt_b_ma, batt_b_temp_c, batt_b_pct FROM telemetry
            ON CONFLICT DO NOTHING;
        DROP TABLE telemetry;
    END IF;
END $$;
