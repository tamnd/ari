-- 0002_candidate.sql: the pending stage. Candidates wait here between
-- emission by a worker ant and disposal by the consolidator, so nothing an
-- ant proposes is recallable until a fold has looked at it (D12). A
-- candidate carries the same anchors and evidence a live row will, so
-- provenance survives the pending stage rather than being reconstructed at
-- fold time, and folded_at makes a fold idempotent across a crash.

CREATE TABLE memory_candidates (
  id            TEXT PRIMARY KEY,   -- ULID
  namespace     TEXT NOT NULL,
  kind          TEXT NOT NULL,      -- observation | reflection
  body          TEXT NOT NULL,
  importance    INTEGER NOT NULL,
  source_ant    TEXT NOT NULL,
  source_task   TEXT,
  anchor_commit TEXT,
  created_at    INTEGER NOT NULL,
  folded_at     INTEGER             -- non-null once the consolidator has processed it
);

CREATE INDEX candidates_pending ON memory_candidates (namespace, folded_at);

CREATE TABLE candidate_anchor (
  candidate_id TEXT NOT NULL REFERENCES memory_candidates(id),
  kind         TEXT NOT NULL,
  ref          TEXT NOT NULL,
  file_hash    TEXT,
  PRIMARY KEY (candidate_id, kind, ref)
);

CREATE TABLE candidate_evidence (
  candidate_id TEXT NOT NULL REFERENCES memory_candidates(id),
  evidence_id  TEXT NOT NULL,     -- a memory id or another candidate id in the same batch
  PRIMARY KEY (candidate_id, evidence_id)
);
