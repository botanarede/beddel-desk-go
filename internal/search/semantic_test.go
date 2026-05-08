//go:build sqlite_fts5

// Package search: semantic_test.go exercises SearchSemantic against
// in-memory fakes so the tests stay fast and free of CGO / ONNX /
// SQLite boot costs. The build tag matches semantic.go because the
// tests reference indexer.IndexedChunk, which itself lives behind
// the tag.
package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/botanarede/beddel-desk-go/internal/indexer"
)

// fakeEmbedder returns a canned vector, captures the query text for
// assertions, and can be told to fail. The canned vector is not
// interpreted by the DB fake (which does its own matching) but
// exists so tests can prove the vector was threaded through.
type fakeEmbedder struct {
	vec       []float32
	err       error
	callCount int
	lastText  string
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.callCount++
	f.lastText = text
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

// fakeSemanticDB records the arguments each call receives and
// returns a canned slice of chunks. Set err to simulate a backend
// failure; set hasBackendFn to customize HasBackend responses.
type fakeSemanticDB struct {
	chunks []indexer.IndexedChunk
	err    error

	// capture fields recorded from the latest SearchHybrid call.
	lastBackend string
	lastQuery   string
	lastVec     []float32
	lastTopK    int
	calls       int
}

func (f *fakeSemanticDB) HasBackend(string) (bool, error) { return true, nil }

func (f *fakeSemanticDB) SearchHybrid(backend, query string, vec []float32, topK int) ([]indexer.IndexedChunk, error) {
	f.calls++
	f.lastBackend = backend
	f.lastQuery = query
	f.lastVec = append([]float32(nil), vec...)
	f.lastTopK = topK
	if f.err != nil {
		return nil, f.err
	}
	return f.chunks, nil
}

// fakeEngine composes the two fakes as a SemanticEngine.
type fakeEngine struct {
	emb SemanticEmbedder
	db  SemanticDB
}

func (e *fakeEngine) Embedder() SemanticEmbedder { return e.emb }
func (e *fakeEngine) DB() SemanticDB             { return e.db }

// makeSession creates a file on disk so os.Stat inside SearchSemantic
// succeeds. It returns the absolute path.
func makeSession(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session %q: %v", path, err)
	}
	return path
}

func TestSearchSemantic_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	a := makeSession(t, tmp, "a.json", "alpha")
	b := makeSession(t, tmp, "b.json", "beta")
	c := makeSession(t, tmp, "c.json", "gamma")

	db := &fakeSemanticDB{chunks: []indexer.IndexedChunk{
		{ID: 1, SessionPath: a, ChunkIndex: 0, Role: "user", Content: "alpha body", Score: 0.9},
		{ID: 2, SessionPath: b, ChunkIndex: 2, Role: "assistant", Content: "beta body", Score: 0.6},
		{ID: 3, SessionPath: c, ChunkIndex: 4, Role: "user", Content: "gamma body", Score: 0.3},
	}}
	emb := &fakeEmbedder{vec: []float32{1, 0, 0}}
	eng := &fakeEngine{emb: emb, db: db}

	resp, err := SearchSemantic(context.Background(), Query{
		Text:        "alpha",
		BackendName: "Kiro",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", resp.Warnings)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}

	// Ordering must match the DB's order verbatim: no re-sort.
	wantPaths := []string{a, b, c}
	for i, r := range resp.Results {
		if r.FilePath != wantPaths[i] {
			t.Errorf("result[%d].FilePath = %q, want %q", i, r.FilePath, wantPaths[i])
		}
	}

	// Field mapping: LineNumber = ChunkIndex + 1, Score/Role/ChunkIndex preserved.
	if resp.Results[0].LineNumber != 1 || resp.Results[1].LineNumber != 3 || resp.Results[2].LineNumber != 5 {
		t.Errorf("line numbers: %d %d %d; want 1 3 5",
			resp.Results[0].LineNumber, resp.Results[1].LineNumber, resp.Results[2].LineNumber)
	}
	if resp.Results[0].Role != "user" || resp.Results[1].Role != "assistant" {
		t.Errorf("roles not preserved: %+v", resp.Results)
	}
	if resp.Results[0].Score != 0.9 || resp.Results[2].Score != 0.3 {
		t.Errorf("scores not preserved: %+v", resp.Results)
	}
	if resp.Results[0].ChunkIndex != 0 || resp.Results[2].ChunkIndex != 4 {
		t.Errorf("chunk indices not preserved: %+v", resp.Results)
	}
	if resp.Results[0].BackendName != "Kiro" {
		t.Errorf("backend name not propagated: %q", resp.Results[0].BackendName)
	}
	if resp.Results[0].FileModTime.IsZero() {
		t.Errorf("FileModTime not populated from os.Stat")
	}

	// The embedder must have received the query text and the vector
	// must have been threaded through to the DB.
	if emb.callCount != 1 || emb.lastText != "alpha" {
		t.Errorf("embedder not called as expected: callCount=%d text=%q", emb.callCount, emb.lastText)
	}
	if len(db.lastVec) != 3 || db.lastVec[0] != 1 {
		t.Errorf("db.lastVec = %v, want [1 0 0]", db.lastVec)
	}
	if db.lastBackend != "Kiro" || db.lastQuery != "alpha" {
		t.Errorf("db call args = (%q, %q), want (Kiro, alpha)", db.lastBackend, db.lastQuery)
	}
}

func TestSearchSemantic_MatchLineTruncatedByRune(t *testing.T) {
	tmp := t.TempDir()
	// Build a 400-rune body composed of a 2-byte rune so byte
	// truncation would split a codepoint. We expect rune truncation
	// to stop exactly at matchLineCap runes (280).
	body := strings.Repeat("é", 400)
	path := makeSession(t, tmp, "long.json", body)

	db := &fakeSemanticDB{chunks: []indexer.IndexedChunk{
		{SessionPath: path, ChunkIndex: 0, Content: body},
	}}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{0.1}}, db: db}

	resp, err := SearchSemantic(context.Background(), Query{
		Text: "anything", BackendName: "Kiro",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(resp.Results))
	}
	got := resp.Results[0].MatchLine
	if n := len([]rune(got)); n != matchLineCap {
		t.Errorf("MatchLine rune length = %d, want %d", n, matchLineCap)
	}
	// No replacement characters, which would signal a mid-codepoint split.
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("MatchLine contains U+FFFD replacement: %q", got)
	}
}

func TestSearchSemantic_EmptyQueryTextSkipsEmbed(t *testing.T) {
	tmp := t.TempDir()
	path := makeSession(t, tmp, "a.json", "body")
	db := &fakeSemanticDB{chunks: []indexer.IndexedChunk{
		{SessionPath: path, ChunkIndex: 0, Content: "body"},
	}}
	emb := &fakeEmbedder{vec: []float32{1, 2, 3}}
	eng := &fakeEngine{emb: emb, db: db}

	resp, err := SearchSemantic(context.Background(), Query{
		Text:        "",
		BackendName: "Kiro",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result for empty-text query, got %d", len(resp.Results))
	}
	// With empty text we do NOT call Embed; the DB receives a nil vec.
	if emb.callCount != 0 {
		t.Errorf("embedder should not be called for empty text, got callCount=%d", emb.callCount)
	}
	if db.lastVec != nil && len(db.lastVec) != 0 {
		t.Errorf("db.lastVec = %v, want nil/empty for empty-text query", db.lastVec)
	}
}

func TestSearchSemantic_MissingFileDroppedWithWarning(t *testing.T) {
	tmp := t.TempDir()
	exists := makeSession(t, tmp, "ok.json", "ok")
	gone := filepath.Join(tmp, "vanished.json") // not created

	db := &fakeSemanticDB{chunks: []indexer.IndexedChunk{
		{SessionPath: exists, ChunkIndex: 0, Content: "ok"},
		{SessionPath: gone, ChunkIndex: 0, Content: "gone"},
	}}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{0.1}}, db: db}

	resp, err := SearchSemantic(context.Background(), Query{
		Text: "hello", BackendName: "Kiro",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].FilePath != exists {
		t.Fatalf("expected only the existing file, got %+v", resp.Results)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "missing") {
		t.Fatalf("expected missing-file warning, got %v", resp.Warnings)
	}
	if !strings.Contains(resp.Warnings[0], gone) {
		t.Errorf("warning should mention vanished path: %q", resp.Warnings[0])
	}
}

func TestSearchSemantic_PathFilterDateAndFavorites(t *testing.T) {
	tmp := t.TempDir()
	keepDir := filepath.Join(tmp, "keep")
	skipDir := filepath.Join(tmp, "skip")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := makeSession(t, keepDir, "s.json", "k")
	skipPath := makeSession(t, skipDir, "s.json", "s")

	// Force predictable mod times so the date filter is meaningful.
	modTime := time.Date(2026, 4, 28, 12, 0, 0, 0, time.Local)
	for _, p := range []string{keepPath, skipPath} {
		if err := os.Chtimes(p, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	// Path-filter run: only "keep" should survive, and the ranking
	// order of the fake DB is preserved among the survivors.
	db := &fakeSemanticDB{chunks: []indexer.IndexedChunk{
		{SessionPath: skipPath, ChunkIndex: 0, Content: "x"},
		{SessionPath: keepPath, ChunkIndex: 0, Content: "y"},
	}}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{0.1}}, db: db}
	resp, err := SearchSemantic(context.Background(), Query{
		Text:        "anything",
		BackendName: "Kiro",
		PathFilter:  "keep",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].FilePath != keepPath {
		t.Fatalf("path filter not honored: %+v", resp.Results)
	}

	// Date filter: a tight window that covers modTime survives.
	resp2, err := SearchSemantic(context.Background(), Query{
		Text:        "anything",
		BackendName: "Kiro",
		From:        modTime.Add(-time.Hour),
		To:          modTime.Add(time.Hour),
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp2.Results) != 2 {
		t.Fatalf("date filter too strict: %+v", resp2.Results)
	}

	// Date filter: a window that ends before modTime drops everything.
	resp3, err := SearchSemantic(context.Background(), Query{
		Text:        "anything",
		BackendName: "Kiro",
		From:        modTime.Add(-2 * time.Hour),
		To:          modTime.Add(-time.Hour),
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp3.Results) != 0 {
		t.Fatalf("date filter too loose: %+v", resp3.Results)
	}

	// Favorites filter: only the keepPath is in the favorites set.
	resp4, err := SearchSemantic(context.Background(), Query{
		Text:        "anything",
		BackendName: "Kiro",
		Favorites:   map[string]struct{}{filepath.Clean(keepPath): {}},
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp4.Results) != 1 || resp4.Results[0].FilePath != keepPath {
		t.Fatalf("favorites filter not honored: %+v", resp4.Results)
	}
}

func TestSearchSemantic_PreservesHybridOrder(t *testing.T) {
	tmp := t.TempDir()
	// Build five sessions; the DB returns them in reverse-score
	// order (it has already sorted; this layer must not re-sort).
	paths := make([]string, 5)
	chunks := make([]indexer.IndexedChunk, 5)
	for i := range paths {
		paths[i] = makeSession(t, tmp, "s"+string(rune('A'+i))+".json", "body")
		chunks[i] = indexer.IndexedChunk{
			SessionPath: paths[i],
			ChunkIndex:  i,
			Content:     "body",
			Score:       float64(5 - i), // 5, 4, 3, 2, 1
		}
	}
	db := &fakeSemanticDB{chunks: chunks}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{1}}, db: db}

	resp, err := SearchSemantic(context.Background(), Query{
		Text: "x", BackendName: "Kiro",
	}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("want 5 results, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.FilePath != paths[i] {
			t.Errorf("order broken at %d: got %q, want %q", i, r.FilePath, paths[i])
		}
	}
}

func TestSearchSemantic_TopKDefaultAndOverride(t *testing.T) {
	db := &fakeSemanticDB{}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{1}}, db: db}

	// Default: zero TopK means 50.
	if _, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro",
	}, eng); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db.lastTopK != 50 {
		t.Errorf("default topK = %d, want 50", db.lastTopK)
	}

	// Explicit override.
	if _, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro", TopK: 7,
	}, eng); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db.lastTopK != 7 {
		t.Errorf("override topK = %d, want 7", db.lastTopK)
	}
}

func TestSearchSemantic_NilEngineRejected(t *testing.T) {
	_, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro",
	}, nil)
	if err == nil {
		t.Fatal("expected error for nil engine, got nil")
	}
}

func TestSearchSemantic_NilDBRejected(t *testing.T) {
	// Engine present but DB nil: should error, not panic.
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{1}}, db: nil}
	_, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro",
	}, eng)
	if err == nil {
		t.Fatal("expected error for nil db, got nil")
	}
}

func TestSearchSemantic_BackendRequired(t *testing.T) {
	db := &fakeSemanticDB{}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{1}}, db: db}
	_, err := SearchSemantic(context.Background(), Query{Text: "q"}, eng)
	if err == nil {
		t.Fatal("expected error for missing backend name, got nil")
	}
}

func TestSearchSemantic_EmbedderErrorPropagated(t *testing.T) {
	sentinel := errors.New("boom")
	db := &fakeSemanticDB{}
	eng := &fakeEngine{
		emb: &fakeEmbedder{err: sentinel},
		db:  db,
	}
	_, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro",
	}, eng)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	// Importantly, the DB must NOT have been called once the
	// embedder failed.
	if db.calls != 0 {
		t.Errorf("db should not be called when embedder fails, got calls=%d", db.calls)
	}
}

func TestSearchSemantic_DBErrorPropagated(t *testing.T) {
	sentinel := errors.New("db fail")
	db := &fakeSemanticDB{err: sentinel}
	eng := &fakeEngine{emb: &fakeEmbedder{vec: []float32{1}}, db: db}
	_, err := SearchSemantic(context.Background(), Query{
		Text: "q", BackendName: "Kiro",
	}, eng)
	if err == nil || !strings.Contains(err.Error(), "db fail") {
		t.Fatalf("expected wrapped db error, got %v", err)
	}
}
