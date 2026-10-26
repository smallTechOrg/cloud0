-- +goose Up
-- cloud0 Phase 1 schema (spec/data.md). SQLite dialect: AUTOINCREMENT and the
-- strftime() seed timestamp are the only SQLite-isms and live in this file only
-- (a postgres/ migrations directory would use BIGSERIAL and to_char(now(), ...)).

CREATE TABLE runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    "trigger"       TEXT    NOT NULL CHECK ("trigger" IN ('refresh_week', 'refresh_all')),
    from_iso_year   INTEGER NOT NULL,
    from_iso_week   INTEGER NOT NULL,
    to_iso_year     INTEGER NOT NULL,
    to_iso_week     INTEGER NOT NULL,
    started_at      TEXT    NOT NULL,
    finished_at     TEXT    NULL,
    duration_ms     INTEGER NULL,
    status          TEXT    NOT NULL CHECK (status IN ('running', 'ok', 'partial', 'failed')),
    error           TEXT    NULL,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_runs_started_at ON runs (started_at DESC);

CREATE TABLE run_sources (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id          INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    source          TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('ok', 'failed')),
    started_at      TEXT    NOT NULL,
    finished_at     TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    sql_text        TEXT    NOT NULL,
    params_json     TEXT    NOT NULL,
    job_id          TEXT    NULL,
    bytes_processed INTEGER NULL,
    bytes_billed    INTEGER NULL,
    cache_hit       INTEGER NULL,
    rows_returned   INTEGER NULL,
    error_code      TEXT    NULL,
    error_message   TEXT    NULL,
    fix_hint        TEXT    NULL
);
CREATE INDEX idx_run_sources_run_id ON run_sources (run_id);

CREATE TABLE snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    iso_year        INTEGER NOT NULL,
    iso_week        INTEGER NOT NULL,
    week_start      TEXT    NOT NULL,
    week_end        TEXT    NOT NULL,
    fetched_at      TEXT    NOT NULL,
    status_at_fetch TEXT    NOT NULL CHECK (status_at_fetch IN ('partial', 'settling', 'final')),
    run_id          INTEGER NOT NULL REFERENCES runs (id),
    run_source_id   INTEGER NOT NULL REFERENCES run_sources (id),
    row_count       INTEGER NOT NULL,
    gross_total     REAL    NOT NULL,
    credits_total   REAL    NOT NULL,
    net_total       REAL    NOT NULL,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_snapshots_week_latest ON snapshots (iso_year, iso_week, id DESC);
CREATE INDEX idx_snapshots_run_id ON snapshots (run_id);

CREATE TABLE snapshot_rows (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id          INTEGER NOT NULL REFERENCES snapshots (id) ON DELETE CASCADE,
    project_id           TEXT    NOT NULL,
    service_id           TEXT    NOT NULL DEFAULT '',
    service              TEXT    NOT NULL,
    sku_id               TEXT    NOT NULL DEFAULT '',
    sku                  TEXT    NOT NULL,
    resource_name        TEXT    NULL,
    resource_global_name TEXT    NULL,
    gross                REAL    NOT NULL,
    credits              REAL    NOT NULL,
    net                  REAL    NOT NULL,
    line_items           INTEGER NOT NULL
);
CREATE INDEX idx_snapshot_rows_service ON snapshot_rows (snapshot_id, service);
CREATE INDEX idx_snapshot_rows_service_sku ON snapshot_rows (snapshot_id, service, sku);
CREATE INDEX idx_snapshot_rows_project ON snapshot_rows (snapshot_id, project_id);

CREATE TABLE annotations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    iso_year    INTEGER NOT NULL,
    iso_week    INTEGER NOT NULL,
    body        TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    UNIQUE (iso_year, iso_week)
);

CREATE TABLE settings (
    "key"       TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

INSERT INTO settings ("key", value, updated_at)
VALUES ('anomaly_pct_threshold', '20', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO settings ("key", value, updated_at)
VALUES ('anomaly_abs_threshold_usd', '50', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

-- +goose Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS annotations;
DROP TABLE IF EXISTS snapshot_rows;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS run_sources;
DROP TABLE IF EXISTS runs;
