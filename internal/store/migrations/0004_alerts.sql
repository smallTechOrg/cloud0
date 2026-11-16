-- +goose Up
-- Append-only audit log of every write cloud0 performs against GCP (policies,
-- uptime checks, channels, budgets, snoozes) and of local alert configuration
-- (kind 'routing'). before_json/after_json are redacted JSON or NULL.
--
-- Settings keys used by the Alerts feature (rows in the existing `settings`
-- table, created on first PUT; no seed):
--   alerts.routing.critical | alerts.routing.error | alerts.routing.warning
--       comma-separated full notification channel resource names
--       (projects/<p>/notificationChannels/<id>); absent = default routing to
--       the channel(s) the server resolves as "Alert" (display name).
--   budget.suggest.multiplier   (default 1.2)  budget suggestion headroom
--   budget.suggest.weeks        (default 4)    complete weeks averaged
--   target.monthly_total        (existing)     preferred account budget amount
CREATE TABLE change_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           TEXT NOT NULL,
    actor        TEXT NOT NULL,
    kind         TEXT NOT NULL,
    action       TEXT NOT NULL,
    target       TEXT NOT NULL,
    before_json  TEXT NULL,
    after_json   TEXT NULL,
    preview_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_change_history_at ON change_history (at DESC, id DESC);
CREATE INDEX idx_change_history_kind ON change_history (kind, id DESC);

-- +goose Down
DROP INDEX idx_change_history_kind;
DROP INDEX idx_change_history_at;
DROP TABLE change_history;
