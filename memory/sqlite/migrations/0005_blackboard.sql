-- The blackboard is the colony's live coordination table: one row per task
-- or subtask, the envelope around a typed handoff. It keeps only what is
-- live, hours not days, and is swept when its task graph closes; anything
-- worth keeping longer is memory, not blackboard (doc 09 section 2, D10).
--
-- parent and origin carry parentage and provenance: a subtask points at its
-- parent and is born on the board, a foreground task has neither. labels is
-- the trust level, inherited from the parent so a subtask spawned from
-- untrusted content can never quietly gain automation rights (D16, D19).
CREATE TABLE blackboard (
  id          TEXT PRIMARY KEY,           -- time-sortable id
  session_id  TEXT NOT NULL,              -- owning foreground session
  task_id     TEXT,                       -- task graph node this row belongs to
  parent      TEXT,                       -- parent task id, empty for a root
  origin      TEXT NOT NULL,              -- foreground | blackboard
  kind        TEXT NOT NULL,              -- goal | claim | partial | question | verdict
  goal        TEXT NOT NULL,              -- one-line human-readable statement
  payload     TEXT NOT NULL,              -- JSON-encoded handoff, never a transcript
  agent       TEXT,                       -- claiming ant id, empty while open
  state       TEXT NOT NULL,              -- open | claimed | done | failed | expired
  claim_count INTEGER NOT NULL DEFAULT 0, -- failed-claim counter, feeds poison detection
  labels      TEXT NOT NULL DEFAULT '[]', -- content-trust labels, inherited from parent
  created_at  INTEGER NOT NULL,           -- unix seconds
  expires_at  INTEGER NOT NULL            -- unix seconds, hours from created_at
);
CREATE INDEX blackboard_open ON blackboard(session_id, state, kind);
CREATE INDEX blackboard_task ON blackboard(task_id);
CREATE INDEX blackboard_parent ON blackboard(parent);
