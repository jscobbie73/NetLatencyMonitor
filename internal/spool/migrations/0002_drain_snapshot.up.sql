-- Persist the post-drain spool snapshot so the next cycle can report
-- accurate post-drain metadata rather than a pre-drain estimate.
INSERT OR IGNORE INTO spool_meta(key, value) VALUES ('last_depth', 0);
INSERT OR IGNORE INTO spool_meta(key, value) VALUES ('last_oldest_age_secs', 0);
INSERT OR IGNORE INTO spool_meta(key, value) VALUES ('last_drops_total', 0);
