-- +goose Up
-- Phase 3 attribution + insights (spec/api.md → "Attribution additions").
--
-- 1. GCP labels captured with every billing row so rules can match labels.
--    JSON arrays as produced by BigQuery TO_JSON_STRING(labels) /
--    TO_JSON_STRING(system_labels): [{"key":"env","value":"prod"}]. NULL for
--    rows stored before this migration; rules then fall back to name matching.
ALTER TABLE snapshot_rows ADD COLUMN labels_json TEXT NULL;
ALTER TABLE snapshot_rows ADD COLUMN system_labels_json TEXT NULL;

-- 2. Ordered attribution rules. PUT /attribution/rules replaces the whole set.
--    match_key is only used by match_field = 'label'. Exactly one of
--    (product, shared=1) or environment (or product+environment) is set.
CREATE TABLE attribution_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    priority     INTEGER NOT NULL CHECK (priority >= 0),
    match_field  TEXT    NOT NULL CHECK (match_field IN ('label', 'project', 'resource_name', 'sku', 'service')),
    match_op     TEXT    NOT NULL CHECK (match_op IN ('equals', 'contains', 'prefix', 'regex')),
    match_key    TEXT    NOT NULL DEFAULT '',
    match_value  TEXT    NOT NULL,
    product      TEXT    NOT NULL DEFAULT '',
    environment  TEXT    NOT NULL DEFAULT '',
    shared       INTEGER NOT NULL DEFAULT 0 CHECK (shared IN (0, 1)),
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    source       TEXT    NOT NULL CHECK (source IN ('seed', 'user', 'proposed')),
    note         TEXT    NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);
CREATE INDEX idx_attribution_rules_order ON attribution_rules (priority, id);

-- 3. Monthly targets, one row per (product, month_from); product '*' is the
--    account target. amount NULL clears the target from month_from onward.
--    History is retained (a later month_from supersedes, never deletes).
CREATE TABLE targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    product     TEXT    NOT NULL,
    month_from  TEXT    NOT NULL,
    amount      REAL    NULL CHECK (amount IS NULL OR amount >= 0),
    created_at  TEXT    NOT NULL,
    UNIQUE (product, month_from)
);
CREATE INDEX idx_targets_product ON targets (product, month_from DESC);

-- Settings keys used by Phase 3 (rows in `settings`, created on first PUT):
--   attribution.allocate        none | even | proportional   (default none)
--   attribution.seed.aliases    JSON object alias -> product
--   rightsizing.cpu_pct (15) · rightsizing.weeks (3) · rightsizing.stale_snapshot_days (30) · rightsizing.idle_run_min_net (1)

-- +goose Down
DROP INDEX idx_targets_product;
DROP TABLE targets;
DROP INDEX idx_attribution_rules_order;
DROP TABLE attribution_rules;
ALTER TABLE snapshot_rows DROP COLUMN system_labels_json;
ALTER TABLE snapshot_rows DROP COLUMN labels_json;
