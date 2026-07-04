-- The trails table is the colony's fitness memory: one row per ant per task
-- class, holding the Beta counts the queen's Thompson sampler routes on
-- (doc 06 sections 4.2, 4.4, 9.1, D13). It is written at task end and read
-- only by the router. The whole schema lands here in M3 even though M3 reads
-- only part of it, because doc 06 section 9.1 owns the table and D2 freezes a
-- schema once, so M4 needs no migration.
--
-- success and failure are REAL, not INTEGER, on purpose: decayed counts are
-- continuous, and if a slowly-fading ant's success count rounded to a hard
-- zero it would be indistinguishable from an ant that failed, when in truth
-- it is one we have simply stopped testing. Floats let a fading ant drift
-- smoothly back toward the beta(1,1) prior, the correct belief that we no
-- longer know whether it is good and should re-explore it.
CREATE TABLE trails (
  ant        TEXT    NOT NULL,           -- ant id
  class      TEXT    NOT NULL,           -- task class
  centroid   BLOB,                       -- mean embedding; written in M3, read in M4's over-breadth fork test
  success    REAL    NOT NULL DEFAULT 0, -- decayed success count, as of updated_at
  failure    REAL    NOT NULL DEFAULT 0, -- decayed failure count, as of updated_at
  tokens     INTEGER NOT NULL DEFAULT 0, -- lifetime token cost, feeds mean-tokens
  wall_ms    INTEGER NOT NULL DEFAULT 0, -- lifetime wall time, a display and calibration total
  n          INTEGER NOT NULL DEFAULT 0, -- raw task count, divides tokens for the budget projection
  updated_at INTEGER NOT NULL,           -- unix seconds, the clock decay reads from
  PRIMARY KEY (ant, class)
);
CREATE INDEX trails_class ON trails(class);
