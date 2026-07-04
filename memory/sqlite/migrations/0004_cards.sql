-- The card row is the router's query surface: the denormalized columns it
-- prefilters on without rehydrating card_json, plus the full Card as JSON
-- for the rare rehydration. Content is file-first (card.json is the source
-- of truth for authored fields); this row is the source of truth for the
-- live status and the derived embedding (doc 06 section 2.3, D10, D11).
CREATE TABLE cards (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  status      TEXT NOT NULL,          -- provisional | active | archived
  tier        TEXT NOT NULL,
  classes     TEXT NOT NULL,          -- JSON array, for the class prefilter
  tools       TEXT NOT NULL,          -- JSON array, for the hard tool filter
  skill_vec   BLOB NOT NULL,          -- float32 embedding of Discovery.Summary
  embed_model TEXT NOT NULL,          -- name+version, for lazy re-embed
  signals     TEXT NOT NULL,          -- JSON array of string cues
  prefers     TEXT NOT NULL,          -- JSON array of task classes
  card_json   TEXT NOT NULL,          -- the full Card, for rehydration
  born        INTEGER NOT NULL,
  revised     INTEGER NOT NULL
);
CREATE INDEX cards_status ON cards(status);
