-- Controller base schema. Per spec §3.5, the spool_* columns on `nodes`
-- come in via ALTER; we collapse the v1.0 base + v1.3 additions into a
-- single create here since this is a greenfield build.

CREATE TABLE nodes (
  id                 TEXT PRIMARY KEY,
  role               TEXT NOT NULL CHECK (role IN ('spoke', 'hub', 'listener')),
  secret_hash        TEXT NOT NULL,                          -- sha256 hex of bearer
  created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at       DATETIME,
  spool_depth        INTEGER NOT NULL DEFAULT 0,
  spool_oldest_ts    DATETIME,
  spool_drops_total  INTEGER NOT NULL DEFAULT 0,
  spool_updated_at   DATETIME
);

CREATE TABLE probe_results (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id    TEXT NOT NULL,
  probe_run_id TEXT NOT NULL,
  target_id    TEXT NOT NULL,
  observed_at  DATETIME NOT NULL,
  latency_ms   REAL,                  -- NULL when probe failed
  error        TEXT,                  -- NULL on success
  received_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (source_id, probe_run_id, target_id),
  FOREIGN KEY (source_id) REFERENCES nodes(id)
);

CREATE INDEX idx_results_source_observed   ON probe_results(source_id, observed_at);
CREATE INDEX idx_results_target_observed   ON probe_results(target_id, observed_at);
CREATE INDEX idx_results_received          ON probe_results(received_at);
