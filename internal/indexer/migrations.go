//go:build sqlite_fts5

// Schema migrations for the semantic-search index database. Each entry
// in migrations[] is a full SQL script that advances the schema from
// version i to version i+1. A migration runs inside a single
// transaction (see applyMigrations in index_db.go).
//
// Migrations are append-only: once a version is shipped, its SQL must
// never change, and future schema edits must be added as new entries.
package indexer

// migrations holds the ordered list of per-version SQL scripts. Index 0
// produces schema version 1, index 1 produces schema version 2, and so
// on. The length of this slice is the "target" schema version the
// current binary knows about.
var migrations = []string{
	// Version 1: initial schema. Creates the core chunks table, the
	// FTS5 contentless shadow, the sqlite-vec shadow (384-dim to match
	// sentence-transformers/all-MiniLM-L6-v2), and the per-file index
	// used to decide whether a session needs re-indexing.
	`
CREATE TABLE IF NOT EXISTS chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    backend_name TEXT NOT NULL,
    session_path TEXT NOT NULL,
    chunk_index  INTEGER NOT NULL,
    role         TEXT,
    content      TEXT NOT NULL,
    timestamp    TEXT,
    UNIQUE(session_path, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_chunks_backend ON chunks(backend_name);
CREATE INDEX IF NOT EXISTS idx_chunks_session ON chunks(session_path);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    content,
    content='chunks',
    content_rowid='id'
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    embedding float[384]
);

CREATE TABLE IF NOT EXISTS file_index (
    file_path    TEXT PRIMARY KEY,
    mtime        TEXT NOT NULL,
    backend_name TEXT NOT NULL,
    chunk_count  INTEGER NOT NULL
);
`,
}
