-- Phase 5 additions: target derivation from the nodes table.
--
-- A node's "address" is host:port that other agents dial when probing it.
-- Required for hub-role nodes (they're the targets); optional for spokes.
--
-- "disabled" lets an operator take a hub out of rotation without deleting
-- its history. Disabled hubs don't appear in /api/v1/targets results.
--
-- controller_meta.targets_version is a monotonic integer bumped on every
-- mutation that affects the active hub set. It rides on POST /api/v1/results
-- responses so agents can detect target changes without polling.

ALTER TABLE nodes ADD COLUMN address TEXT;
ALTER TABLE nodes ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;

CREATE TABLE controller_meta (
  key   TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);

INSERT INTO controller_meta(key, value) VALUES ('targets_version', 1);
