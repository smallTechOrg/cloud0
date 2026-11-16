-- +goose Up
-- cloud0 Phase 2 schema (spec/api.md → Phase 2). Snapshots cover every
-- project; scope filtering happens at read time (as in Phase 1). Cells and
-- provenance are stored as JSON TEXT per row so the Metrics block can add or
-- reorder columns without a migration.

-- Month refreshes need a third run trigger. SQLite cannot ALTER a CHECK, so the
-- constraint text in the stored schema is relaxed in place (the documented
-- writable_schema procedure for loosening a CHECK); no data is rewritten.
PRAGMA writable_schema = ON;
UPDATE sqlite_master
   SET sql = replace(sql, '(''refresh_week'', ''refresh_all'')', '(''refresh_week'', ''refresh_all'', ''refresh_month'')')
 WHERE type = 'table' AND name = 'runs';
PRAGMA writable_schema = OFF;

CREATE TABLE metric_snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    iso_year        INTEGER NOT NULL,
    iso_week        INTEGER NOT NULL,
    week_start      TEXT    NOT NULL,
    week_end        TEXT    NOT NULL,
    fetched_at      TEXT    NOT NULL,
    status_at_fetch TEXT    NOT NULL CHECK (status_at_fetch IN ('partial', 'settling', 'final')),
    run_id          INTEGER NOT NULL REFERENCES runs (id),
    row_count       INTEGER NOT NULL,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_metric_snapshots_week_latest ON metric_snapshots (iso_year, iso_week, id DESC);
CREATE INDEX idx_metric_snapshots_run_id ON metric_snapshots (run_id);

CREATE TABLE metric_rows (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id     INTEGER NOT NULL REFERENCES metric_snapshots (id) ON DELETE CASCADE,
    grp             TEXT    NOT NULL,
    project_id      TEXT    NOT NULL,
    resource        TEXT    NOT NULL DEFAULT '',
    cells_json      TEXT    NOT NULL
);
CREATE INDEX idx_metric_rows_snapshot ON metric_rows (snapshot_id, project_id);

CREATE TABLE error_rows (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id          INTEGER NOT NULL REFERENCES runs (id),
    iso_year        INTEGER NOT NULL,
    iso_week        INTEGER NOT NULL,
    fetched_at      TEXT    NOT NULL,
    status_at_fetch TEXT    NOT NULL CHECK (status_at_fetch IN ('partial', 'settling', 'final')),
    source          TEXT    NOT NULL,
    project_id      TEXT    NOT NULL,
    service         TEXT    NOT NULL DEFAULT '',
    c2xx            INTEGER NULL,
    c3xx            INTEGER NULL,
    c4xx            INTEGER NULL,
    c5xx            INTEGER NULL,
    err_groups      INTEGER NULL,
    occurrences     INTEGER NULL,
    gap_reason      TEXT    NOT NULL DEFAULT '',
    fix_hint        TEXT    NOT NULL DEFAULT '',
    provenance_json TEXT    NOT NULL
);
CREATE INDEX idx_error_rows_week ON error_rows (iso_year, iso_week, run_id DESC);

CREATE TABLE month_snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    month           TEXT    NOT NULL,
    month_start     TEXT    NOT NULL,
    month_end       TEXT    NOT NULL,
    fetched_at      TEXT    NOT NULL,
    status_at_fetch TEXT    NOT NULL CHECK (status_at_fetch IN ('partial', 'settling', 'final')),
    run_id          INTEGER NOT NULL REFERENCES runs (id),
    run_source_id   INTEGER NULL REFERENCES run_sources (id),
    row_count       INTEGER NOT NULL,
    gross_total     REAL    NOT NULL,
    credits_total   REAL    NOT NULL,
    net_total       REAL    NOT NULL,
    created_at      TEXT    NOT NULL
);
CREATE INDEX idx_month_snapshots_latest ON month_snapshots (month, id DESC);
CREATE INDEX idx_month_snapshots_run_id ON month_snapshots (run_id);

CREATE TABLE month_rows (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id     INTEGER NOT NULL REFERENCES month_snapshots (id) ON DELETE CASCADE,
    month           TEXT    NOT NULL,
    invoice_month   TEXT    NOT NULL DEFAULT '',
    project_id      TEXT    NOT NULL,
    service_id      TEXT    NOT NULL DEFAULT '',
    service         TEXT    NOT NULL,
    gross           REAL    NOT NULL,
    credits         REAL    NOT NULL,
    net             REAL    NOT NULL
);
CREATE INDEX idx_month_rows_snapshot ON month_rows (snapshot_id, project_id);

-- +goose Down
DROP TABLE IF EXISTS month_rows;
DROP TABLE IF EXISTS month_snapshots;
DROP TABLE IF EXISTS error_rows;
DROP TABLE IF EXISTS metric_rows;
DROP TABLE IF EXISTS metric_snapshots;
-- The relaxed runs CHECK is left in place on down (loosened constraints are harmless).
