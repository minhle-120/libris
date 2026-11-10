-- The first schema. Later migrations are additive only: this file is never
-- edited, because a user's database may already be several versions ahead.

CREATE TABLE notebooks (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

-- A source is the uploaded file, keyed by its SHA-256 so the same bytes added
-- to two notebooks are stored, rasterised and extracted exactly once.
--
-- Deleting a notebook removes its memberships and chats, never the source or
-- its files: a citation in any remaining notebook must still open the
-- original, and there is no garbage collector to decide what is safe to drop.
CREATE TABLE sources (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL,
  sha256        TEXT NOT NULL,
  bytes         INTEGER NOT NULL,
  original_path TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('processing','ready','unsupported','failed')),
  detail        TEXT NOT NULL DEFAULT '',
  warnings      TEXT NOT NULL DEFAULT '[]',
  created_at    INTEGER NOT NULL
);

CREATE TABLE notebook_sources (
  notebook_id TEXT NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
  source_id   TEXT NOT NULL REFERENCES sources (id)   ON DELETE CASCADE,
  added_at    INTEGER NOT NULL,
  PRIMARY KEY (notebook_id, source_id)
);

CREATE INDEX notebook_sources_source ON notebook_sources (source_id);

-- Derived files: page rasters, audio slices, extracted images. Path is the
-- absolute location on disk; id is the name the ingest sink gave the file,
-- which is also the URL path the UI asks for.
CREATE TABLE assets (
  id        TEXT PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  path      TEXT NOT NULL,
  mime      TEXT NOT NULL,
  bytes     INTEGER NOT NULL
);

-- One row per embeddable unit. `text` is empty for media segments until
-- transcription exists, which is why the embedding column is nullable: ingest
-- and storage work before any model is configured.
CREATE TABLE segments (
  id         TEXT PRIMARY KEY,
  source_id  TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  doc_id     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ordinal    INTEGER NOT NULL,
  text       TEXT NOT NULL DEFAULT '',
  page       INTEGER NOT NULL DEFAULT 0,
  page_end   INTEGER NOT NULL DEFAULT 0,
  char_start INTEGER NOT NULL DEFAULT 0,
  char_end   INTEGER NOT NULL DEFAULT 0,
  t0         REAL NOT NULL DEFAULT 0,
  t1         REAL NOT NULL DEFAULT 0,
  asset_id   TEXT REFERENCES assets (id) ON DELETE SET NULL,
  embedding  BLOB,
  dim        INTEGER
);

CREATE INDEX segments_source ON segments (source_id);
CREATE INDEX segments_doc    ON segments (doc_id, ordinal);

CREATE TABLE chats (
  id          TEXT PRIMARY KEY,
  notebook_id TEXT NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
  title       TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

-- citations is a JSON array of {chunk, segment_id, doc_id, quote}: what
-- [[Chunk N]] resolved to at the time the answer was written, so history still
-- renders after a re-ingest changes the registry.
CREATE TABLE messages (
  id         INTEGER PRIMARY KEY,
  chat_id    TEXT NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
  role       TEXT NOT NULL,
  content    TEXT NOT NULL,
  citations  TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL
);

CREATE INDEX messages_chat ON messages (chat_id, id);
