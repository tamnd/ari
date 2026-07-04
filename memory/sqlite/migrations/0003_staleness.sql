-- 0003_staleness.sql: the reversible demotion flag the fold's invalidation
-- pass writes. A memory anchored to a file that changed since its
-- anchor_commit, or whose stored file_hash no longer matches the file's
-- content, is marked stale and recall downranks it until a re-verify (M4) or a
-- re-matching hash clears the flag. It is a flag, not a destroyed score, so the
-- demotion is reversible: verified_at advances when the flag clears (D11,
-- research recommendation 6).

ALTER TABLE memories ADD COLUMN stale INTEGER NOT NULL DEFAULT 0;

CREATE INDEX memories_ns_stale ON memories (namespace, stale);
