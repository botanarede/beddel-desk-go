//go:build sqlite_fts5

package indexer

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/botanarede/beddel-desk-go/internal/embedding"
)

// newTempDB returns a fresh IndexDB on a throwaway path. Tests must
// use a real on-disk SQLite file because sqlite_vec.Auto() behavior
// with multiple in-memory databases in the same process is not
// guaranteed to match the production path.
func newTempDB(t *testing.T) (*IndexDB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	db, err := OpenIndexDB(path)
	if err != nil {
		t.Fatalf("OpenIndexDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// randomVec returns a deterministic "random" unit-norm 384-dim vector
// seeded by the caller. The test suite wants reproducible distances
// so we avoid time-seeded math/rand.
func randomVec(seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	vec := make([]float32, embedding.EmbeddingDim)
	var sum float64
	for i := range vec {
		v := r.NormFloat64()
		vec[i] = float32(v)
		sum += v * v
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}

// oneHotVec returns a unit vector that points along axis `i`. Used
// to construct vectors whose cosine distance we can reason about:
// oneHotVec(0) and oneHotVec(0) are identical; oneHotVec(0) and
// oneHotVec(1) are orthogonal.
func oneHotVec(i int) []float32 {
	v := make([]float32, embedding.EmbeddingDim)
	v[i%embedding.EmbeddingDim] = 1
	return v
}

func sampleChunks(sessionPath string, count int) ([]Chunk, [][]float32) {
	chunks := make([]Chunk, count)
	vecs := make([][]float32, count)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		chunks[i] = Chunk{
			SessionPath: sessionPath,
			ChunkIndex:  i,
			Role:        "user",
			Content:     "alpha beta gamma chunk " + string(rune('A'+i)),
			Timestamp:   now.Add(time.Duration(i) * time.Minute),
		}
		vecs[i] = randomVec(int64(i + 1))
	}
	return chunks, vecs
}

func TestOpenIndexDB_AppliesMigrationsOnFirstOpen(t *testing.T) {
	db, _ := newTempDB(t)

	var version int
	if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d, want 1", version)
	}

	// Every schema object must exist.
	for _, obj := range []string{"chunks", "chunks_fts", "chunks_vec", "file_index"} {
		var count int
		err := db.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, obj,
		).Scan(&count)
		if err != nil {
			t.Fatalf("probe %s: %v", obj, err)
		}
		if count == 0 {
			t.Fatalf("schema object %q missing after migration", obj)
		}
	}
}

func TestOpenIndexDB_ReopenReusesSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")

	first, err := OpenIndexDB(path)
	if err != nil {
		t.Fatalf("first OpenIndexDB: %v", err)
	}
	// Close before reopening; we want this test to simulate a process
	// restart, not a concurrent handle.
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := OpenIndexDB(path)
	if err != nil {
		t.Fatalf("second OpenIndexDB: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	var rows int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_version row count = %d, want 1 (migrations re-ran)", rows)
	}
}

func TestReplaceSessionChunks_InsertAndRetrieve(t *testing.T) {
	db, _ := newTempDB(t)

	session := "/tmp/session-A.json"
	chunks, vecs := sampleChunks(session, 3)
	mtime := time.Date(2024, 5, 8, 12, 0, 0, 0, time.UTC)

	if err := db.ReplaceSessionChunks("kiro", session, mtime, chunks, vecs); err != nil {
		t.Fatalf("ReplaceSessionChunks: %v", err)
	}

	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 1 || stats.Chunks != 3 {
		t.Fatalf("Stats = %+v, want 1 session / 3 chunks", stats)
	}

	has, err := db.HasBackend("kiro")
	if err != nil {
		t.Fatalf("HasBackend: %v", err)
	}
	if !has {
		t.Fatalf("HasBackend(kiro) = false, want true")
	}

	got, ok, err := db.FileMTime(session)
	if err != nil {
		t.Fatalf("FileMTime: %v", err)
	}
	if !ok {
		t.Fatalf("FileMTime ok = false, want true")
	}
	if !got.Equal(mtime) {
		t.Fatalf("FileMTime = %s, want %s", got, mtime)
	}
}

func TestReplaceSessionChunks_ReplacesOldChunks(t *testing.T) {
	db, _ := newTempDB(t)
	session := "/tmp/session-B.json"

	first, firstVecs := sampleChunks(session, 3)
	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(100, 0), first, firstVecs); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second call with two chunks must leave exactly two rows in
	// chunks, two rows in chunks_fts, and two rows in chunks_vec.
	second, secondVecs := sampleChunks(session, 2)
	// Replace content so an FTS query on the old word fails and an
	// FTS query on the new word succeeds.
	second[0].Content = "replacement alpha"
	second[1].Content = "replacement beta"
	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(200, 0), second, secondVecs); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var chunks, fts, vec int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE session_path = ?`, session).Scan(&chunks); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM chunks_fts`).Scan(&fts); err != nil {
		t.Fatalf("count chunks_fts: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM chunks_vec`).Scan(&vec); err != nil {
		t.Fatalf("count chunks_vec: %v", err)
	}
	if chunks != 2 || fts != 2 || vec != 2 {
		t.Fatalf("after replace, chunks=%d fts=%d vec=%d, want all 2", chunks, fts, vec)
	}

	// FTS5 should no longer match the old token "gamma".
	results, err := db.SearchHybrid("kiro", "gamma", nil, 5)
	if err != nil {
		t.Fatalf("SearchHybrid gamma: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected zero results for gamma after replace, got %d", len(results))
	}

	// FTS5 must match the new token "replacement".
	results, err = db.SearchHybrid("kiro", "replacement", nil, 5)
	if err != nil {
		t.Fatalf("SearchHybrid replacement: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected two results for replacement, got %d", len(results))
	}

	// file_index must reflect the new mtime and chunk count.
	got, ok, err := db.FileMTime(session)
	if err != nil || !ok {
		t.Fatalf("FileMTime after replace: ok=%v err=%v", ok, err)
	}
	if !got.Equal(time.Unix(200, 0).UTC()) {
		t.Fatalf("FileMTime = %s, want %s", got, time.Unix(200, 0).UTC())
	}
}

func TestFileMTime_HitAndMiss(t *testing.T) {
	db, _ := newTempDB(t)

	// Miss before any insert.
	got, ok, err := db.FileMTime("/nonexistent.json")
	if err != nil {
		t.Fatalf("FileMTime miss error: %v", err)
	}
	if ok {
		t.Fatalf("FileMTime ok = true, want false")
	}
	if !got.IsZero() {
		t.Fatalf("FileMTime miss time = %v, want zero", got)
	}

	// Hit after insert.
	session := "/tmp/session-C.json"
	chunks, vecs := sampleChunks(session, 1)
	mtime := time.Date(2024, 2, 2, 2, 2, 2, 0, time.UTC)
	if err := db.ReplaceSessionChunks("kiro", session, mtime, chunks, vecs); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	got, ok, err = db.FileMTime(session)
	if err != nil {
		t.Fatalf("FileMTime hit error: %v", err)
	}
	if !ok {
		t.Fatalf("FileMTime ok = false, want true")
	}
	if !got.Equal(mtime) {
		t.Fatalf("FileMTime hit time = %s, want %s", got, mtime)
	}
}

func TestSearchHybrid_LexicalOnly(t *testing.T) {
	db, _ := newTempDB(t)

	// A chunk whose content matches the query text but whose
	// embedding is arbitrary. The vec branch should return nothing
	// close to the query vector; FTS5 alone must surface the chunk.
	session := "/tmp/lex.json"
	chunks := []Chunk{
		{SessionPath: session, ChunkIndex: 0, Role: "user", Content: "quantum entanglement explained"},
		{SessionPath: session, ChunkIndex: 1, Role: "user", Content: "pasta recipes for beginners"},
	}
	vecs := [][]float32{randomVec(7), randomVec(8)}

	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(1, 0), chunks, vecs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	results, err := db.SearchHybrid("kiro", "quantum entanglement", nil, 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ChunkIndex != 0 {
		t.Fatalf("got chunk_index = %d, want 0", results[0].ChunkIndex)
	}
	if results[0].Score <= 0 {
		t.Fatalf("got score = %v, want > 0", results[0].Score)
	}
}

func TestSearchHybrid_SemanticOnly(t *testing.T) {
	db, _ := newTempDB(t)

	session := "/tmp/sem.json"
	chunks := []Chunk{
		{SessionPath: session, ChunkIndex: 0, Role: "user", Content: "apples oranges bananas"},
		{SessionPath: session, ChunkIndex: 1, Role: "user", Content: "carrots broccoli spinach"},
	}
	// Vector 0 points along axis 0; vector 1 points along axis 1.
	// The query vector is the same as vector 0, so chunk 0 wins the
	// KNN. Neither chunk's text contains the query phrase.
	vecs := [][]float32{oneHotVec(0), oneHotVec(1)}

	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(1, 0), chunks, vecs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	queryVec := oneHotVec(0)
	results, err := db.SearchHybrid("kiro", "xxxzzz-never-matches", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("got 0 results, want at least 1")
	}
	if results[0].ChunkIndex != 0 {
		t.Fatalf("top chunk_index = %d, want 0", results[0].ChunkIndex)
	}
}

func TestSearchHybrid_RRFPrefersBothListMatches(t *testing.T) {
	db, _ := newTempDB(t)

	session := "/tmp/rrf.json"
	// Three chunks:
	//  - chunk 0 matches FTS (contains "overlap") AND vec (its vector
	//    matches the query vector exactly).
	//  - chunk 1 matches FTS only.
	//  - chunk 2 matches vec only (orthogonal text).
	chunks := []Chunk{
		{SessionPath: session, ChunkIndex: 0, Role: "user", Content: "overlap top candidate"},
		{SessionPath: session, ChunkIndex: 1, Role: "user", Content: "overlap weak textual match"},
		{SessionPath: session, ChunkIndex: 2, Role: "user", Content: "completely unrelated text"},
	}
	// Build vectors so chunk 0 is closest to the query, chunk 2
	// second closest, and chunk 1 far away.
	v0 := oneHotVec(0)
	v2 := make([]float32, embedding.EmbeddingDim)
	v2[0] = 0.9
	v2[1] = 0.1
	// Normalize v2.
	var s float64
	for _, x := range v2 {
		s += float64(x) * float64(x)
	}
	inv := float32(1.0 / math.Sqrt(s))
	for i := range v2 {
		v2[i] *= inv
	}
	v1 := oneHotVec(100) // far from v0

	vecs := [][]float32{v0, v1, v2}
	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(1, 0), chunks, vecs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	queryVec := oneHotVec(0)
	results, err := db.SearchHybrid("kiro", "overlap", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("got %d results, want >= 2", len(results))
	}
	if results[0].ChunkIndex != 0 {
		t.Fatalf("top chunk_index = %d, want 0 (present in both FTS and vec)", results[0].ChunkIndex)
	}
	// Scores should be strictly decreasing in RRF order.
	for i := 1; i < len(results); i++ {
		if results[i-1].Score < results[i].Score {
			t.Fatalf("scores not sorted desc: %+v", results)
		}
	}
}

func TestSearchHybrid_EmptyBothReturnsNil(t *testing.T) {
	db, _ := newTempDB(t)
	results, err := db.SearchHybrid("kiro", "", nil, 5)
	if err != nil {
		t.Fatalf("SearchHybrid empty: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

func TestDeleteBackend_IsolatesOneBackend(t *testing.T) {
	db, _ := newTempDB(t)

	sessA := "/tmp/a.json"
	sessB := "/tmp/b.json"
	chunksA, vecsA := sampleChunks(sessA, 2)
	chunksB, vecsB := sampleChunks(sessB, 3)

	if err := db.ReplaceSessionChunks("kiro", sessA, time.Unix(1, 0), chunksA, vecsA); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := db.ReplaceSessionChunks("gemini", sessB, time.Unix(2, 0), chunksB, vecsB); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	if err := db.DeleteBackend("kiro"); err != nil {
		t.Fatalf("DeleteBackend kiro: %v", err)
	}

	// kiro must be gone.
	statsKiro, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("stats kiro: %v", err)
	}
	if statsKiro.Chunks != 0 || statsKiro.Sessions != 0 {
		t.Fatalf("kiro stats = %+v, want zeros", statsKiro)
	}
	if _, ok, err := db.FileMTime(sessA); err != nil || ok {
		t.Fatalf("FileMTime(sessA) ok=%v err=%v, want ok=false", ok, err)
	}

	// gemini must still be intact.
	statsGem, err := db.Stats("gemini")
	if err != nil {
		t.Fatalf("stats gemini: %v", err)
	}
	if statsGem.Chunks != 3 || statsGem.Sessions != 1 {
		t.Fatalf("gemini stats = %+v, want chunks=3 sessions=1", statsGem)
	}

	// FTS and vec shadows for kiro must be gone, too.
	var ftsCount, vecCount int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM chunks_fts`).Scan(&ftsCount); err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM chunks_vec`).Scan(&vecCount); err != nil {
		t.Fatalf("count vec: %v", err)
	}
	if ftsCount != 3 || vecCount != 3 {
		t.Fatalf("after DeleteBackend(kiro), fts=%d vec=%d, want 3 each", ftsCount, vecCount)
	}
}

func TestDeleteAll_RemovesFileAndAllowsReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")

	db, err := OpenIndexDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	session := "/tmp/x.json"
	chunks, vecs := sampleChunks(session, 2)
	if err := db.ReplaceSessionChunks("kiro", session, time.Unix(1, 0), chunks, vecs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := db.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("index file still exists after DeleteAll (err=%v)", err)
	}

	// Idempotent: a second DeleteAll returns nil.
	if err := db.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll idempotency: %v", err)
	}

	// Reopen must recreate the schema from scratch.
	reopened, err := OpenIndexDB(path)
	if err != nil {
		t.Fatalf("reopen after DeleteAll: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	var version int
	if err := reopened.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version after reopen: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version after reopen = %d, want 1", version)
	}
	// No leftover chunks from before DeleteAll.
	stats, err := reopened.Stats("kiro")
	if err != nil {
		t.Fatalf("stats after reopen: %v", err)
	}
	if stats.Chunks != 0 {
		t.Fatalf("stats.Chunks = %d, want 0 (DB should be empty)", stats.Chunks)
	}
}

func TestAllStats(t *testing.T) {
	db, _ := newTempDB(t)

	chunksA, vecsA := sampleChunks("/tmp/sa.json", 2)
	chunksB, vecsB := sampleChunks("/tmp/sb.json", 4)
	if err := db.ReplaceSessionChunks("alpha", "/tmp/sa.json", time.Unix(1, 0), chunksA, vecsA); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	if err := db.ReplaceSessionChunks("zeta", "/tmp/sb.json", time.Unix(2, 0), chunksB, vecsB); err != nil {
		t.Fatalf("seed zeta: %v", err)
	}

	all, err := db.AllStats()
	if err != nil {
		t.Fatalf("AllStats: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("AllStats len = %d, want 2", len(all))
	}
	if all[0].BackendName != "alpha" || all[1].BackendName != "zeta" {
		t.Fatalf("AllStats order = %q,%q, want alpha,zeta", all[0].BackendName, all[1].BackendName)
	}
	if all[0].Chunks != 2 || all[1].Chunks != 4 {
		t.Fatalf("AllStats chunk counts = %d,%d, want 2,4", all[0].Chunks, all[1].Chunks)
	}
}

func TestRRFMerge_SyntheticRanks(t *testing.T) {
	// fts list: chunk 1 at rank 0, chunk 2 at rank 1
	// vec list: chunk 3 at rank 0, chunk 1 at rank 1
	//
	// Expected RRF scores (k=60):
	//   chunk 1: 1/(60+1) + 1/(60+2) = 1/61 + 1/62
	//   chunk 2: 1/(60+2)           = 1/62
	//   chunk 3: 1/(60+1)           = 1/61
	// Ordering must be: 1 (both lists), then 3 (1/61), then 2 (1/62).
	fts := []int64{1, 2}
	vec := []int64{3, 1}
	merged := rrfMerge(fts, vec, 60, 10)

	if len(merged) != 3 {
		t.Fatalf("got %d hits, want 3", len(merged))
	}
	if merged[0].ID != 1 {
		t.Fatalf("top id = %d, want 1", merged[0].ID)
	}
	wantTop := 1.0/61.0 + 1.0/62.0
	if math.Abs(merged[0].Score-wantTop) > 1e-9 {
		t.Fatalf("top score = %v, want %v", merged[0].Score, wantTop)
	}
	if merged[1].ID != 3 {
		t.Fatalf("second id = %d, want 3", merged[1].ID)
	}
	if math.Abs(merged[1].Score-1.0/61.0) > 1e-9 {
		t.Fatalf("second score = %v, want %v", merged[1].Score, 1.0/61.0)
	}
	if merged[2].ID != 2 {
		t.Fatalf("third id = %d, want 2", merged[2].ID)
	}
	if math.Abs(merged[2].Score-1.0/62.0) > 1e-9 {
		t.Fatalf("third score = %v, want %v", merged[2].Score, 1.0/62.0)
	}
}

func TestRRFMerge_BothListsTopRank(t *testing.T) {
	// A chunk ranked 0 in both lists must score 2/(60+1), the
	// canonical "1/(k + rank_i)" sum from ADR-4.
	merged := rrfMerge([]int64{42}, []int64{42}, 60, 10)
	if len(merged) != 1 {
		t.Fatalf("got %d hits, want 1", len(merged))
	}
	want := 2.0 / 61.0
	if math.Abs(merged[0].Score-want) > 1e-9 {
		t.Fatalf("score = %v, want %v", merged[0].Score, want)
	}
}

func TestRRFMerge_Limit(t *testing.T) {
	fts := []int64{1, 2, 3, 4}
	vec := []int64{3, 4, 5, 6}
	merged := rrfMerge(fts, vec, 60, 3)
	if len(merged) != 3 {
		t.Fatalf("merged len = %d, want 3 (limit)", len(merged))
	}
	// The two ids present in both (3, 4) should dominate.
	if merged[0].ID != 3 && merged[0].ID != 4 {
		t.Fatalf("top id = %d, want 3 or 4", merged[0].ID)
	}
}

func TestRRFMerge_EmptyInputs(t *testing.T) {
	if merged := rrfMerge(nil, nil, 60, 10); len(merged) != 0 {
		t.Fatalf("empty inputs produced %d hits, want 0", len(merged))
	}
}

func TestReplaceSessionChunks_LengthMismatch(t *testing.T) {
	db, _ := newTempDB(t)
	chunks, _ := sampleChunks("/tmp/m.json", 2)
	err := db.ReplaceSessionChunks("kiro", "/tmp/m.json", time.Unix(1, 0), chunks, [][]float32{randomVec(1)})
	if err == nil {
		t.Fatalf("expected length mismatch error, got nil")
	}
}

func TestReplaceSessionChunks_WrongDim(t *testing.T) {
	db, _ := newTempDB(t)
	chunks, _ := sampleChunks("/tmp/d.json", 1)
	bad := make([]float32, embedding.EmbeddingDim+1)
	err := db.ReplaceSessionChunks("kiro", "/tmp/d.json", time.Unix(1, 0), chunks, [][]float32{bad})
	if err == nil {
		t.Fatalf("expected dimension error, got nil")
	}
}
