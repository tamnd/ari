-- 0001_memory.sql: the live memory tables and the FTS5 index over them.
-- The row shape is D11's: provenanced, two-tier by ttl_class, with a
-- namespace column that exists from the first migration but is never crossed
-- in M2, and verified_at/read_only/evidence that M4's verifier will read.

CREATE TABLE memories (
  id            TEXT PRIMARY KEY,           -- ULID, sortable by creation
  namespace     TEXT NOT NULL,              -- the ant's private ns; colony ns dormant in M2
  kind          TEXT NOT NULL,              -- observation | reflection | skill | pin
  label         TEXT NOT NULL,              -- short human-facing handle
  body          TEXT NOT NULL,              -- the memory content
  embedding     BLOB,                       -- float32 vector, NULL when FTS-only
  embed_model   TEXT,                       -- name+version of the embedder, NULL if none
  importance    INTEGER NOT NULL,           -- 1..10, Park's write-time score
  created_at    INTEGER NOT NULL,           -- unix seconds
  accessed_at   INTEGER NOT NULL,           -- unix seconds, refreshed on recall
  access_count  INTEGER NOT NULL DEFAULT 0,
  source_ant    TEXT NOT NULL,              -- provenance: which ant wrote it
  source_task   TEXT,                       -- provenance: the task/turn id
  anchor_commit TEXT,                       -- the commit the memory was true at
  verified_at   TEXT,                       -- last commit an eval confirmed it; M4 fills this
  ttl_class     TEXT NOT NULL,              -- pinned | normal | fast | session evaporation
  read_only     INTEGER NOT NULL DEFAULT 0, -- 1 for human-edited rows, consolidator must skip
  archived_at   INTEGER,                    -- non-null once forgotten-to-archive
  pinned        INTEGER NOT NULL DEFAULT 0  -- 1 if it renders into the pinned index
);

CREATE INDEX memories_ns_kind ON memories (namespace, kind);
CREATE INDEX memories_ns_pinned ON memories (namespace, pinned);

CREATE TABLE memory_anchor (
  memory_id  TEXT NOT NULL REFERENCES memories(id),
  kind       TEXT NOT NULL,   -- file | symbol | command
  ref        TEXT NOT NULL,   -- the path, symbol name, or command string
  file_hash  TEXT,            -- content hash at write time, for file anchors
  PRIMARY KEY (memory_id, kind, ref)
);

CREATE TABLE memory_evidence (
  memory_id   TEXT NOT NULL REFERENCES memories(id),  -- the reflection
  evidence_id TEXT NOT NULL REFERENCES memories(id),  -- the observation it rests on
  PRIMARY KEY (memory_id, evidence_id)
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
  label, body,
  content='memories', content_rowid='rowid',
  tokenize="unicode61 tokenchars '_./:-'"
);

-- External-content FTS5 stays in sync with memories through three triggers,
-- the standard pattern for hybrid search over an existing table.
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, label, body) VALUES (new.rowid, new.label, new.body);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, label, body) VALUES('delete', old.rowid, old.label, old.body);
END;

CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, label, body) VALUES('delete', old.rowid, old.label, old.body);
  INSERT INTO memories_fts(rowid, label, body) VALUES (new.rowid, new.label, new.body);
END;
