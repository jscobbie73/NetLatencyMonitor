CREATE TABLE spool (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  enqueued_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  probe_run_id    TEXT NOT NULL UNIQUE,
  payload         TEXT NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  last_attempt_at DATETIME,
  last_error      TEXT
);

CREATE INDEX idx_spool_enqueued ON spool(enqueued_at);
CREATE INDEX idx_spool_attempts_enqueued ON spool(attempts, enqueued_at);

CREATE TABLE spool_meta (
  key   TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);

INSERT OR IGNORE INTO spool_meta(key, value) VALUES ('drops_total', 0);
