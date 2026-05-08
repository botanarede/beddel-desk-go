# Story 9: Index Database (SQLite + FTS5 + sqlite-vec + RRF)

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.4
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md)
ADR-3, ADR-4.

## Goal

Provide the single source of truth for indexed data: chunk rows, their FTS5 shadow, and
their 384-dim sqlite-vec shadow. Expose the hybrid-ranking search query. Make "delete
one backend's index" and "delete everything" cheap and reliable.

## Package Layout

```
internal/indexer/
  index_db.go
  index_db_test.go
  migrations.go       # versioned schema steps
```

## Schema

```sql
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);

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
```

## Public API

```go
package indexer

type IndexDB struct { /* unexported */ }

type IndexedChunk struct {
    ID          int64
    BackendName string
    SessionPath string
    ChunkIndex  int
    Role        string
    Content     string
    Timestamp   time.Time
    Score       float64 // populated by search; 0 outside search context
}

type BackendStats struct {
    BackendName string
    Sessions    int
    Chunks      int
    BytesOnDisk int64 // best-effort; 0 when unknown
}

func OpenIndexDB(path string) (*IndexDB, error)
func (d *IndexDB) Close() error

func (d *IndexDB) HasBackend(backendName string) (bool, error)
func (d *IndexDB) Stats(backendName string) (BackendStats, error)
func (d *IndexDB) AllStats() ([]BackendStats, error)

func (d *IndexDB) FileMTime(sessionPath string) (time.Time, bool, error)
func (d *IndexDB) ReplaceSessionChunks(backendName string, sessionPath string,
    mtime time.Time, chunks []Chunk, embeddings [][]float32) error

func (d *IndexDB) DeleteBackend(backendName string) error
func (d *IndexDB) DeleteAll() error

func (d *IndexDB) SearchHybrid(backendName, query string,
    queryVec []float32, topK int) ([]IndexedChunk, error)
```

## Acceptance Criteria

- [ ] `OpenIndexDB` opens with `_pragma=foreign_keys(1)` and `_journal=WAL`, loads
      sqlite-vec via the extension API, applies migrations, and fails loudly if FTS5 is
      unavailable.
- [ ] Migrations are idempotent. Opening an existing DB never re-runs a completed
      migration.
- [ ] `ReplaceSessionChunks` is transactional: it deletes prior chunks for the same
      `session_path`, inserts the new chunks with their embeddings, and updates
      `file_index` in one `BEGIN ... COMMIT`. Failure rolls back and leaves the DB
      unchanged.
- [ ] `FileMTime` returns `(time.Time{}, false, nil)` when the path is unknown and does
      not allocate.
- [ ] `SearchHybrid` runs two subqueries:
      1. FTS5 `MATCH` filtered by `backend_name`, ordered by `bm25(chunks_fts)`, top
         `topK`.
      2. `chunks_vec MATCH` on the query vector filtered by `backend_name` via a join
         to `chunks`, ordered by distance, top `topK`.
      Then merges with RRF (k=60), returns up to `topK` distinct chunks sorted by
      combined score desc.
- [ ] `SearchHybrid` tolerates empty query text (FTS5 branch contributes nothing, vector
      branch still runs) and empty query vector (vector branch contributes nothing).
      When both are empty it returns an empty slice and no error.
- [ ] `DeleteBackend` removes rows from `chunks`, `chunks_fts`, `chunks_vec`, and
      `file_index` for that backend and runs `VACUUM` so disk size reclaims.
- [ ] `DeleteAll` closes the DB, removes the file, and leaves the receiver in a state
      where a subsequent `OpenIndexDB` on the same path works.

## Implementation Tasks

- [ ] Add dependencies:
      `github.com/mattn/go-sqlite3` (with `-tags "sqlite_fts5"` documented in
      `docs/architecture/02-semantic-search.md`),
      `github.com/asg017/sqlite-vec-go-bindings/cgo`.
- [ ] Register sqlite-vec on each new connection via `sqlite3.RegisterExtensions`
      or the binding's `Auto` helper. Document exactly which approach in code comments.
- [ ] Implement BM25 + cosine ranked subqueries and a merge function with an explicit
      unit test.
- [ ] Compute `BytesOnDisk` from the SQLite page count or file size; prefer `PRAGMA
      page_count * PRAGMA page_size` when it works across platforms.

## Verification Tasks

- [ ] `index_db_test.go` uses an in-temp-dir SQLite file (not in-memory, because
      sqlite-vec extension registration can behave differently with `:memory:`). Covers:
      - schema migrations applied on first open
      - `ReplaceSessionChunks` insert + retrieve round trip
      - `ReplaceSessionChunks` replaces correctly (old chunks gone, new count correct)
      - `FileMTime` hit and miss
      - `SearchHybrid`: lexical-only match surfaces via FTS5
      - `SearchHybrid`: semantic-only match surfaces via vec
      - `SearchHybrid`: RRF merge prefers chunks that appear in both lists
      - `DeleteBackend` isolates one backend, leaves another intact
      - `DeleteAll` removes the file and lets `OpenIndexDB` re-create it
- [ ] A focused RRF unit test with synthetic rank lists (no DB) locks in the k=60
      scoring math.

## Out of Scope

- the indexer pipeline (Story 10)
- the search integration (Story 11)
- manual SQL shell access (users are expected to go through the Index Manager UI)

## Constraints

- All code and comments in English.
- The `sqlite_fts5` build tag must be explicitly required by a `//go:build` guard or
  documented as a mandatory build flag so misconfigured builds fail with a clear error.
- Tests skip with an informative message if CGO is disabled.
