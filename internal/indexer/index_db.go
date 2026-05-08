//go:build sqlite_fts5

// Package indexer's index database is the single on-disk file that
// backs Beddel Desk's semantic search: chunk rows, their FTS5 shadow,
// and their sqlite-vec shadow all live in one SQLite database.
//
// Build tag: this file MUST be built with the `sqlite_fts5` build tag
// so that github.com/mattn/go-sqlite3 exposes the FTS5 virtual table
// module. Building without the tag will simply omit index_db.go from
// the build (see ADR-3 in docs/architecture/02-semantic-search.md).
// The release workflow already enables `-tags sqlite_fts5`.
//
// CGO: the sqlite-vec extension is registered via
// sqlite_vec.Auto() which calls sqlite3_auto_extension. That call is
// process-wide, so we guard it with a sync.Once and never call it
// again for the life of the process.
package indexer

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/botanarede/beddel-desk-go/internal/embedding"
)

// ErrFTS5Unavailable is returned by OpenIndexDB when the linked SQLite
// build does not expose FTS5. This is usually a missing `sqlite_fts5`
// build tag on the go-sqlite3 driver; see the package doc.
var ErrFTS5Unavailable = errors.New("indexer: SQLite FTS5 extension is not available; rebuild with -tags sqlite_fts5")

// rrfK is the Reciprocal Rank Fusion constant from ADR-4: combined
// score = sum(1 / (rrfK + rank_i)) across rankers in which the chunk
// appeared. k=60 is the canonical value from Cormack et al. (2009) and
// is also the value the architecture document pins.
const rrfK = 60

// autoOnce runs sqlite_vec.Auto() exactly once per process. The call
// registers the sqlite-vec extension on every future database/sql
// connection, including the ones opened by OpenIndexDB below.
var autoOnce sync.Once

// registerSQLiteVec installs the sqlite-vec auto-extension hook on
// first use. Safe to call repeatedly.
func registerSQLiteVec() {
	autoOnce.Do(func() {
		sqlite_vec.Auto()
	})
}

// IndexDB owns the single SQLite file that backs semantic search. It
// is safe for concurrent use; the underlying *sql.DB handles pooling.
type IndexDB struct {
	db   *sql.DB
	path string
}

// IndexedChunk is the row shape returned by SearchHybrid and other
// read APIs. Score is populated by SearchHybrid and is zero outside
// of a search call.
type IndexedChunk struct {
	ID          int64
	BackendName string
	SessionPath string
	ChunkIndex  int
	Role        string
	Content     string
	Timestamp   time.Time
	Score       float64
}

// BackendStats summarizes how much of the index a single backend
// owns. BytesOnDisk is a proportional estimate computed from the
// SQLite page count; see Stats for the exact formula.
type BackendStats struct {
	BackendName string
	Sessions    int
	Chunks      int
	BytesOnDisk int64
}

// OpenIndexDB opens (or creates) the index database at path, enables
// WAL journaling and foreign keys, loads sqlite-vec on every
// connection, verifies that FTS5 is available, and applies any
// pending migrations. The returned IndexDB is ready for concurrent
// use; call Close when done.
func OpenIndexDB(path string) (*IndexDB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("indexer: index database path must not be empty")
	}

	// Ensure sqlite-vec is registered on every future connection.
	// Auto() installs a process-wide sqlite3_auto_extension hook, so
	// any sql.Open / connection pool entry will inherit it.
	registerSQLiteVec()

	// Pragmas are supplied via the DSN so they apply to every pooled
	// connection, not only the first one we manually issue them on.
	// _txlock=immediate makes every sql.Tx opened by the driver use
	// BEGIN IMMEDIATE, which is the locking mode we want for the
	// write paths (see ReplaceSessionChunks) so writers fail fast on
	// contention instead of mid-transaction.
	dsn := path + "?_journal=WAL&_foreign_keys=1&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("indexer: open %q: %w", path, err)
	}
	// A single writer + several readers is the classic SQLite pattern;
	// keep the write path serialized to avoid SQLITE_BUSY on WAL.
	db.SetMaxOpenConns(1)

	if err := verifyFTS5(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := ensureSchemaVersionTable(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("indexer: initialize schema_version: %w", err)
	}

	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &IndexDB{db: db, path: path}, nil
}

// verifyFTS5 asks the linked SQLite build whether the FTS5 module was
// compiled in. A missing FTS5 would manifest later as a CREATE VIRTUAL
// TABLE error; we prefer a typed, early failure.
func verifyFTS5(db *sql.DB) error {
	var enabled int
	err := db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&enabled)
	if err != nil {
		return fmt.Errorf("indexer: probe FTS5: %w", err)
	}
	if enabled == 0 {
		return ErrFTS5Unavailable
	}
	return nil
}

// ensureSchemaVersionTable creates the schema_version tracker if it is
// missing. The table is intentionally NOT part of migrations[] because
// migrations themselves rely on it to decide what to run.
func ensureSchemaVersionTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`)
	return err
}

// applyMigrations runs every migration whose target version exceeds
// the highest version currently recorded in schema_version. Each
// migration runs in its own transaction so a mid-script failure never
// leaves the DB half-migrated.
func applyMigrations(db *sql.DB) error {
	current, err := currentSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("indexer: read schema_version: %w", err)
	}
	target := len(migrations)
	for v := current + 1; v <= target; v++ {
		script := migrations[v-1]
		if err := runMigration(db, v, script); err != nil {
			return fmt.Errorf("indexer: apply migration v%d: %w", v, err)
		}
	}
	return nil
}

// currentSchemaVersion returns the largest version present in
// schema_version, or 0 when the table is empty.
func currentSchemaVersion(db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// runMigration executes one migration's SQL script and records its
// version stamp inside the same transaction.
func runMigration(db *sql.DB, version int, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(script); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, version); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close releases the underlying database handle. Safe to call more
// than once; subsequent calls return nil.
func (d *IndexDB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}

// HasBackend reports whether the index contains at least one chunk
// for the named backend. Useful for switching between lexical and
// hybrid search paths.
func (d *IndexDB) HasBackend(backendName string) (bool, error) {
	var one int
	err := d.db.QueryRow(
		`SELECT 1 FROM chunks WHERE backend_name = ? LIMIT 1`,
		backendName,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("indexer: HasBackend(%q): %w", backendName, err)
	}
	return true, nil
}

// Stats returns session / chunk counts for backendName plus a
// proportional BytesOnDisk estimate. When the backend is unknown the
// struct is zero-valued and the error is nil.
//
// BytesOnDisk is intentionally an estimate: SQLite does not track
// per-row disk usage. We compute
//
//	total_bytes = PRAGMA page_count * PRAGMA page_size
//	backend_bytes = total_bytes * backend_chunks / total_chunks
//
// which is accurate to within normal B-tree overhead when most chunk
// rows are similar in size.
func (d *IndexDB) Stats(backendName string) (BackendStats, error) {
	stats := BackendStats{BackendName: backendName}

	err := d.db.QueryRow(
		`SELECT COUNT(DISTINCT session_path), COUNT(*) FROM chunks WHERE backend_name = ?`,
		backendName,
	).Scan(&stats.Sessions, &stats.Chunks)
	if err != nil {
		return BackendStats{}, fmt.Errorf("indexer: Stats(%q): %w", backendName, err)
	}

	if stats.Chunks == 0 {
		return stats, nil
	}

	bytes, err := d.estimatedBytes(stats.Chunks)
	if err != nil {
		return BackendStats{}, err
	}
	stats.BytesOnDisk = bytes
	return stats, nil
}

// AllStats returns one BackendStats entry per distinct backend_name
// currently present in the index. The result is sorted by backend
// name for deterministic ordering.
func (d *IndexDB) AllStats() ([]BackendStats, error) {
	rows, err := d.db.Query(`
		SELECT backend_name,
		       COUNT(DISTINCT session_path) AS sessions,
		       COUNT(*)                    AS chunks
		FROM   chunks
		GROUP  BY backend_name
		ORDER  BY backend_name
	`)
	if err != nil {
		return nil, fmt.Errorf("indexer: AllStats: %w", err)
	}
	defer rows.Close()

	var result []BackendStats
	totalChunks := 0
	for rows.Next() {
		var s BackendStats
		if err := rows.Scan(&s.BackendName, &s.Sessions, &s.Chunks); err != nil {
			return nil, fmt.Errorf("indexer: AllStats scan: %w", err)
		}
		totalChunks += s.Chunks
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("indexer: AllStats iterate: %w", err)
	}

	if totalChunks == 0 {
		return result, nil
	}

	totalBytes, err := d.totalBytesOnDisk()
	if err != nil {
		return nil, err
	}
	for i := range result {
		if result[i].Chunks == 0 {
			continue
		}
		result[i].BytesOnDisk = int64(float64(totalBytes) *
			float64(result[i].Chunks) / float64(totalChunks))
	}
	return result, nil
}

// estimatedBytes returns a proportional slice of the DB's total size
// based on backendChunks / totalChunks. Returns 0 when totalChunks is
// 0 or unavailable.
func (d *IndexDB) estimatedBytes(backendChunks int) (int64, error) {
	totalBytes, err := d.totalBytesOnDisk()
	if err != nil {
		return 0, err
	}
	var totalChunks int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&totalChunks); err != nil {
		return 0, fmt.Errorf("indexer: count chunks: %w", err)
	}
	if totalChunks == 0 {
		return 0, nil
	}
	return int64(float64(totalBytes) * float64(backendChunks) / float64(totalChunks)), nil
}

// totalBytesOnDisk returns page_count * page_size as an int64. We use
// page-based math rather than os.Stat so that the numbers stay
// consistent with what SQLite itself reports, which matters for
// "Indexed: X MB" labels in the UI.
func (d *IndexDB) totalBytesOnDisk() (int64, error) {
	var pageCount, pageSize int64
	if err := d.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("indexer: pragma page_count: %w", err)
	}
	if err := d.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("indexer: pragma page_size: %w", err)
	}
	return pageCount * pageSize, nil
}

// FileMTime returns the last recorded mtime for sessionPath. The
// second return value is false when the path has never been indexed.
// A miss does NOT return an error.
func (d *IndexDB) FileMTime(sessionPath string) (time.Time, bool, error) {
	var mtimeStr string
	err := d.db.QueryRow(
		`SELECT mtime FROM file_index WHERE file_path = ?`,
		sessionPath,
	).Scan(&mtimeStr)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("indexer: FileMTime(%q): %w", sessionPath, err)
	}
	t, err := time.Parse(time.RFC3339Nano, mtimeStr)
	if err != nil {
		// Fall back to RFC3339 for older rows without sub-second
		// precision; be strict otherwise so we never hand back a
		// silently-zero time.
		if t2, err2 := time.Parse(time.RFC3339, mtimeStr); err2 == nil {
			return t2, true, nil
		}
		return time.Time{}, false, fmt.Errorf("indexer: FileMTime(%q): parse mtime %q: %w", sessionPath, mtimeStr, err)
	}
	return t, true, nil
}

// ReplaceSessionChunks is the transactional mutation used by the
// indexer pipeline. It atomically:
//
//  1. deletes every existing chunk for sessionPath (with its FTS and
//     vec shadows),
//  2. inserts the new chunks + embeddings,
//  3. upserts file_index with the new mtime and chunk count.
//
// On any failure the whole transaction rolls back and the database is
// left exactly as it was before the call.
//
// len(embeddings) must equal len(chunks) and each embedding must have
// exactly embedding.EmbeddingDim components. An empty chunks slice is
// a valid "this session produced no content" signal and still
// refreshes file_index so the incremental-indexing check sees the new
// mtime.
func (d *IndexDB) ReplaceSessionChunks(
	backendName, sessionPath string,
	mtime time.Time,
	chunks []Chunk,
	embeddings [][]float32,
) error {
	if strings.TrimSpace(backendName) == "" {
		return errors.New("indexer: ReplaceSessionChunks: backend name is required")
	}
	if strings.TrimSpace(sessionPath) == "" {
		return errors.New("indexer: ReplaceSessionChunks: session path is required")
	}
	if len(chunks) != len(embeddings) {
		return fmt.Errorf(
			"indexer: ReplaceSessionChunks: chunks/embeddings length mismatch (%d vs %d)",
			len(chunks), len(embeddings))
	}
	for i, vec := range embeddings {
		if len(vec) != embedding.EmbeddingDim {
			return fmt.Errorf(
				"indexer: ReplaceSessionChunks: embedding %d has %d dimensions, want %d",
				i, len(vec), embedding.EmbeddingDim)
		}
	}

	// database/sql Tx uses BEGIN IMMEDIATE because the DSN pins
	// _txlock=immediate; see OpenIndexDB. The write lock is acquired
	// up front so concurrent readers never see a half-replaced
	// session and we fail fast on contention.
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("indexer: begin tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Step 1: fetch old chunk ids for session_path so we can remove
	// their FTS and vec shadows explicitly. The FTS table is
	// contentless and the vec table is virtual, so neither supports
	// ON DELETE CASCADE; we must delete from all three.
	oldIDs, oldContents, err := selectChunkIDs(tx, sessionPath)
	if err != nil {
		return fmt.Errorf("indexer: list old chunks: %w", err)
	}

	if err := deleteShadows(tx, oldIDs, oldContents); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE session_path = ?`, sessionPath); err != nil {
		return fmt.Errorf("indexer: delete old chunks: %w", err)
	}

	// Step 2: insert the new chunks, their FTS shadow row, and their
	// vec shadow row. We prepare each statement once per call.
	insertChunk, err := tx.Prepare(`
		INSERT INTO chunks (backend_name, session_path, chunk_index, role, content, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("indexer: prepare insert chunk: %w", err)
	}
	defer insertChunk.Close()

	insertFTS, err := tx.Prepare(`INSERT INTO chunks_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("indexer: prepare insert fts: %w", err)
	}
	defer insertFTS.Close()

	insertVec, err := tx.Prepare(`INSERT INTO chunks_vec(chunk_id, embedding) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("indexer: prepare insert vec: %w", err)
	}
	defer insertVec.Close()

	for i, ch := range chunks {
		ts := timestampToSQL(ch.Timestamp)
		res, err := insertChunk.Exec(
			backendName, sessionPath, ch.ChunkIndex, ch.Role, ch.Content, ts,
		)
		if err != nil {
			return fmt.Errorf("indexer: insert chunk %d: %w", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("indexer: last insert id for chunk %d: %w", i, err)
		}
		if _, err := insertFTS.Exec(id, ch.Content); err != nil {
			return fmt.Errorf("indexer: insert fts for chunk %d: %w", i, err)
		}
		blob, err := sqlite_vec.SerializeFloat32(embeddings[i])
		if err != nil {
			return fmt.Errorf("indexer: serialize embedding %d: %w", i, err)
		}
		if _, err := insertVec.Exec(id, blob); err != nil {
			return fmt.Errorf("indexer: insert vec for chunk %d: %w", i, err)
		}
	}

	// Step 3: upsert file_index so the incremental-indexing check
	// sees the new mtime + chunk count on the next run.
	if _, err := tx.Exec(`
		INSERT INTO file_index(file_path, mtime, backend_name, chunk_count)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
		    mtime = excluded.mtime,
		    backend_name = excluded.backend_name,
		    chunk_count = excluded.chunk_count
	`, sessionPath, mtime.UTC().Format(time.RFC3339Nano), backendName, len(chunks)); err != nil {
		return fmt.Errorf("indexer: upsert file_index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("indexer: commit: %w", err)
	}
	tx = nil // suppress the deferred Rollback after a successful commit.
	return nil
}

// selectChunkIDs returns every chunk id + content pair for session_path
// so deleteShadows can address them individually. A session with no
// prior rows returns (nil, nil, nil).
func selectChunkIDs(tx *sql.Tx, sessionPath string) ([]int64, []string, error) {
	rows, err := tx.Query(
		`SELECT id, content FROM chunks WHERE session_path = ?`,
		sessionPath,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var ids []int64
	var contents []string
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		contents = append(contents, content)
	}
	return ids, contents, rows.Err()
}

// deleteShadows removes every row in chunks_fts and chunks_vec whose
// rowid matches an entry in ids. Contentless FTS5 needs the original
// content on deletion so the inverted index stays consistent.
func deleteShadows(tx *sql.Tx, ids []int64, contents []string) error {
	if len(ids) == 0 {
		return nil
	}
	deleteFTS, err := tx.Prepare(
		`INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("indexer: prepare delete fts: %w", err)
	}
	defer deleteFTS.Close()

	deleteVec, err := tx.Prepare(`DELETE FROM chunks_vec WHERE chunk_id = ?`)
	if err != nil {
		return fmt.Errorf("indexer: prepare delete vec: %w", err)
	}
	defer deleteVec.Close()

	for i, id := range ids {
		if _, err := deleteFTS.Exec(id, contents[i]); err != nil {
			return fmt.Errorf("indexer: delete fts row %d: %w", id, err)
		}
		if _, err := deleteVec.Exec(id); err != nil {
			return fmt.Errorf("indexer: delete vec row %d: %w", id, err)
		}
	}
	return nil
}

// timestampToSQL renders a Chunk timestamp into the string shape the
// schema stores. A zero time becomes an empty string so the CHECKs on
// TEXT columns stay predictable.
func timestampToSQL(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// DeleteBackend removes every chunk (plus shadows) and every
// file_index row belonging to backendName, then runs VACUUM so the
// on-disk file shrinks back. Call sites use this to implement the
// per-backend "Clear" button in the Index Manager.
func (d *IndexDB) DeleteBackend(backendName string) error {
	if strings.TrimSpace(backendName) == "" {
		return errors.New("indexer: DeleteBackend: backend name is required")
	}

	// Collect rows to be removed before we start mutating so the FTS
	// and vec shadows get the right content / chunk_id pairs.
	rows, err := d.db.Query(
		`SELECT id, content FROM chunks WHERE backend_name = ?`,
		backendName,
	)
	if err != nil {
		return fmt.Errorf("indexer: list chunks for backend %q: %w", backendName, err)
	}
	var ids []int64
	var contents []string
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			rows.Close()
			return fmt.Errorf("indexer: scan chunk id: %w", err)
		}
		ids = append(ids, id)
		contents = append(contents, content)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("indexer: iterate backend chunks: %w", err)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("indexer: begin DeleteBackend tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := deleteShadows(tx, ids, contents); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE backend_name = ?`, backendName); err != nil {
		return fmt.Errorf("indexer: delete chunks for backend %q: %w", backendName, err)
	}
	if _, err := tx.Exec(`DELETE FROM file_index WHERE backend_name = ?`, backendName); err != nil {
		return fmt.Errorf("indexer: delete file_index for backend %q: %w", backendName, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("indexer: commit DeleteBackend: %w", err)
	}
	committed = true

	// VACUUM cannot run inside a transaction; it must be a top-level
	// statement. It reclaims freelist pages so "Indexed: X MB" goes
	// back down after a Clear.
	if _, err := d.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("indexer: vacuum after DeleteBackend: %w", err)
	}
	return nil
}

// DeleteAll closes the DB handle and removes the SQLite file plus its
// journal / WAL / SHM siblings. A missing file is NOT an error: this
// method is idempotent and safe to call right after OpenIndexDB fails.
//
// After DeleteAll returns, a subsequent OpenIndexDB(path) recreates
// the database from scratch.
func (d *IndexDB) DeleteAll() error {
	if d == nil {
		return nil
	}
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			return fmt.Errorf("indexer: close before DeleteAll: %w", err)
		}
		d.db = nil
	}
	if d.path == "" {
		return nil
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		p := d.path + suffix
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("indexer: remove %q: %w", p, err)
		}
	}
	return nil
}

// SearchHybrid runs a Reciprocal Rank Fusion join across an FTS5
// BM25-ranked list and a sqlite-vec KNN list. Both lists are filtered
// by backendName and capped at topK. The merged list is then sorted
// by combined score desc and truncated at topK.
//
// Empty query strings are tolerated: an empty query skips the FTS5
// branch; a nil/empty queryVec skips the vec branch; both empty
// returns (nil, nil) with no error.
func (d *IndexDB) SearchHybrid(
	backendName, query string,
	queryVec []float32,
	topK int,
) ([]IndexedChunk, error) {
	if topK <= 0 {
		return nil, nil
	}

	queryText := strings.TrimSpace(query)
	hasText := queryText != ""
	hasVec := len(queryVec) == embedding.EmbeddingDim
	if !hasText && !hasVec {
		return nil, nil
	}
	if len(queryVec) != 0 && len(queryVec) != embedding.EmbeddingDim {
		return nil, fmt.Errorf(
			"indexer: SearchHybrid: query vector has %d dims, want %d or 0",
			len(queryVec), embedding.EmbeddingDim)
	}

	var ftsRanks []int64
	var vecRanks []int64

	if hasText {
		var err error
		ftsRanks, err = d.runFTSRanks(backendName, queryText, topK)
		if err != nil {
			return nil, err
		}
	}
	if hasVec {
		var err error
		vecRanks, err = d.runVecRanks(backendName, queryVec, topK)
		if err != nil {
			return nil, err
		}
	}

	merged := rrfMerge(ftsRanks, vecRanks, rrfK, topK)
	if len(merged) == 0 {
		return nil, nil
	}
	return d.hydrateChunks(merged)
}

// runFTSRanks returns up to topK chunk ids ordered by BM25 score
// (lower is better in FTS5), filtered by backendName. Errors from the
// FTS5 parser (for example a query containing a stray colon) are
// reported verbatim so the caller can degrade to the vec branch.
func (d *IndexDB) runFTSRanks(backendName, query string, topK int) ([]int64, error) {
	// FTS5 requires the virtual-table name itself (not an alias) as
	// the left operand of MATCH, so we reference chunks_fts directly
	// in both the FROM and WHERE clauses.
	rows, err := d.db.Query(`
		SELECT c.id
		FROM   chunks_fts
		JOIN   chunks c ON c.id = chunks_fts.rowid
		WHERE  chunks_fts MATCH ?
		  AND  c.backend_name = ?
		ORDER  BY bm25(chunks_fts)
		LIMIT  ?
	`, sanitizeFTSQuery(query), backendName, topK)
	if err != nil {
		return nil, fmt.Errorf("indexer: FTS5 query: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("indexer: FTS5 scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// sanitizeFTSQuery wraps the user's query in double quotes so FTS5
// parses it as a single phrase. This is the minimum defense against
// accidental syntax errors from punctuation; the indexer pipeline can
// add a smarter parser later if needed.
func sanitizeFTSQuery(q string) string {
	escaped := strings.ReplaceAll(q, `"`, `""`)
	return `"` + escaped + `"`
}

// runVecRanks returns up to topK chunk ids ordered by ascending
// distance (closer first), filtered by backendName.
//
// sqlite-vec's KNN filter requires both `embedding MATCH ?` and
// `k = ?` in the WHERE clause, and orders by `distance` as a virtual
// column provided by vec0. We then JOIN chunks to apply the backend
// filter; if the backend filter eliminates all KNN hits, we return an
// empty list rather than widen the KNN.
func (d *IndexDB) runVecRanks(backendName string, queryVec []float32, topK int) ([]int64, error) {
	blob, err := sqlite_vec.SerializeFloat32(queryVec)
	if err != nil {
		return nil, fmt.Errorf("indexer: serialize query vector: %w", err)
	}
	// Over-fetch a bit so the backend filter still produces topK hits
	// when the closest neighbors belong to another backend. In the
	// worst case this is O(k * backendCount), which is bounded and
	// small in practice.
	overFetch := topK * 4
	if overFetch < topK {
		overFetch = topK
	}
	rows, err := d.db.Query(`
		SELECT v.chunk_id
		FROM   chunks_vec v
		JOIN   chunks c ON c.id = v.chunk_id
		WHERE  v.embedding MATCH ?
		  AND  v.k = ?
		  AND  c.backend_name = ?
		ORDER  BY v.distance
		LIMIT  ?
	`, blob, overFetch, backendName, topK)
	if err != nil {
		return nil, fmt.Errorf("indexer: vec query: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("indexer: vec scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// rrfMerge implements Reciprocal Rank Fusion: for every chunk id in
// either list it accumulates 1/(k+rank_i) across lists in which the
// chunk appears, then returns the top `limit` ids by combined score.
// The returned slice carries (id, score) pairs so hydrateChunks can
// populate IndexedChunk.Score for the UI.
func rrfMerge(ftsIDs, vecIDs []int64, k, limit int) []rrfHit {
	scores := make(map[int64]float64, len(ftsIDs)+len(vecIDs))
	insertion := make(map[int64]int, len(ftsIDs)+len(vecIDs))
	next := 0
	accumulate := func(ids []int64) {
		for rank, id := range ids {
			scores[id] += 1.0 / float64(k+rank+1)
			if _, seen := insertion[id]; !seen {
				insertion[id] = next
				next++
			}
		}
	}
	accumulate(ftsIDs)
	accumulate(vecIDs)

	out := make([]rrfHit, 0, len(scores))
	for id, s := range scores {
		out = append(out, rrfHit{ID: id, Score: s})
	}
	// Stable-looking sort: higher score first; ties broken by the
	// order in which the id was first observed, so the output is
	// deterministic across Go map iteration seeds.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return insertion[out[i].ID] < insertion[out[j].ID]
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// rrfHit is the intermediate shape the RRF merge produces.
type rrfHit struct {
	ID    int64
	Score float64
}

// hydrateChunks fills IndexedChunk rows for every id in hits, in the
// same order as hits (so the caller sees the RRF-ranked order).
func (d *IndexDB) hydrateChunks(hits []rrfHit) ([]IndexedChunk, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(hits))
	args := make([]any, len(hits))
	for i, h := range hits {
		placeholders[i] = "?"
		args[i] = h.ID
	}
	q := fmt.Sprintf(`
		SELECT id, backend_name, session_path, chunk_index, COALESCE(role, ''), content, COALESCE(timestamp, '')
		FROM   chunks
		WHERE  id IN (%s)
	`, strings.Join(placeholders, ","))
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("indexer: hydrate chunks: %w", err)
	}
	defer rows.Close()

	byID := make(map[int64]IndexedChunk, len(hits))
	for rows.Next() {
		var c IndexedChunk
		var ts string
		if err := rows.Scan(&c.ID, &c.BackendName, &c.SessionPath, &c.ChunkIndex, &c.Role, &c.Content, &ts); err != nil {
			return nil, fmt.Errorf("indexer: hydrate scan: %w", err)
		}
		if ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				c.Timestamp = t
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				c.Timestamp = t
			}
		}
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("indexer: hydrate iterate: %w", err)
	}

	out := make([]IndexedChunk, 0, len(hits))
	for _, h := range hits {
		c, ok := byID[h.ID]
		if !ok {
			// Row disappeared between ranking and hydration (e.g. a
			// concurrent DeleteBackend). Skip rather than fail the
			// whole search.
			continue
		}
		c.Score = h.Score
		out = append(out, c)
	}
	return out, nil
}
