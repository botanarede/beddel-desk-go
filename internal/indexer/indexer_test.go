//go:build sqlite_fts5

package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/botanarede/beddel-desk-go/internal/embedding"
)

// fakeEmbedder implements Embedder without the ONNX runtime. It
// returns a 384-dim vector whose first component is the rune length
// of the input text, so tests can match chunks back to embeddings.
type fakeEmbedder struct {
	calls atomic.Int32
	// failAfter, when > 0, makes EmbedBatch return err on the Nth
	// call. A zero value means "never fail".
	failAfter int32
	err       error
}

func (f *fakeEmbedder) Dim() int { return embedding.EmbeddingDim }

func (f *fakeEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := f.calls.Add(1)
	if f.failAfter > 0 && n >= f.failAfter && f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, embedding.EmbeddingDim)
		vec[0] = float32(len([]rune(t)))
		out[i] = vec
	}
	return out, nil
}

// cancellingEmbedder cancels ctxHolder once the first batch arrives,
// then returns ctx.Err() on every subsequent call. Used to exercise
// the pipeline's cancellation path without relying on timing.
type cancellingEmbedder struct {
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *cancellingEmbedder) Dim() int { return embedding.EmbeddingDim }

func (c *cancellingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if c.fired.CompareAndSwap(false, true) {
		c.cancel()
	}
	// Return a successful first batch so the pipeline writes at
	// least once before cancellation bites at the next file boundary.
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, embedding.EmbeddingDim)
		vec[0] = float32(len([]rune(t)))
		out[i] = vec
	}
	return out, nil
}

// writeKiroFile writes a minimal Kiro session under dir with the
// given messages. Returns the absolute file path and the current
// mtime for later comparison.
func writeKiroFile(t *testing.T, dir, name string, messages []map[string]any) string {
	t.Helper()
	payload := map[string]any{"messages": messages}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// copyFixture copies a file from testdata into dir so the pipeline
// can walk a real-looking directory without touching the testdata
// tree directly.
func copyFixture(t *testing.T, dir, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("copy fixture %s: %v", name, err)
	}
	return dst
}

func newTestIndexer(t *testing.T, emb Embedder) (*Indexer, *IndexDB) {
	t.Helper()
	db, _ := newTempDB(t)
	return NewIndexer(db, emb), db
}

func TestIndexer_HappyPath(t *testing.T) {
	dir := t.TempDir()
	kiroPath := copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	var progresses []Progress
	err := idx.IndexSession(context.Background(), "kiro", kiroPath, func(p Progress) { progresses = append(progresses, p) })
	if err != nil {
		t.Fatalf("IndexSession: %v", err)
	}

	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1", stats.Sessions)
	}
	if stats.Chunks == 0 {
		t.Fatalf("Chunks = 0, want > 0")
	}

	info, err := os.Stat(kiroPath)
	if err != nil {
		t.Fatalf("stat %s: %v", kiroPath, err)
	}
	stored, ok, err := db.FileMTime(kiroPath)
	if err != nil || !ok {
		t.Fatalf("FileMTime %s ok=%v err=%v", kiroPath, ok, err)
	}
	if !stored.Equal(info.ModTime().UTC()) {
		t.Fatalf("stored mtime %v != fs mtime %v", stored, info.ModTime().UTC())
	}

	sawDone := false
	for _, p := range progresses {
		if p.Stage == "done" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("no done stage in progress ticks")
	}
}

func TestIndexer_IncrementalSkip(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, _ := newTestIndexer(t, emb)

	if err := idx.IndexSession(context.Background(), "kiro", path, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstCalls := emb.calls.Load()
	if firstCalls == 0 {
		t.Fatalf("first run made 0 embed calls, want >= 1")
	}

	if err := idx.IndexSession(context.Background(), "kiro", path, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := emb.calls.Load(); got != firstCalls {
		t.Fatalf("second run made %d new embed calls, want 0", got-firstCalls)
	}
}

func TestIndexer_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	// session with 17 messages to ensure batch splitting
	messages := make([]map[string]any, 17)
	for i := range messages {
		messages[i] = map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("message number %d", i),
		}
	}
	path := writeKiroFile(t, dir, "seventeen.json", messages)

	ctx, cancel := context.WithCancel(context.Background())
	emb := &cancellingEmbedder{cancel: cancel}
	idx, _ := newTestIndexer(t, emb)

	err := idx.IndexSession(ctx, "kiro", path, nil)
	if err == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestIndexer_BatchSizeBoundary(t *testing.T) {
	dir := t.TempDir()
	messages := make([]map[string]any, 17)
	for i := range messages {
		messages[i] = map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("message number %d", i),
		}
	}
	path := writeKiroFile(t, dir, "seventeen.json", messages)

	rec := &struct{ sizes []int }{}
	emb := batchRecorder{rec: rec}

	idx, db := newTestIndexer(t, emb)
	if err := idx.IndexSession(context.Background(), "kiro", path, nil); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}

	if len(rec.sizes) != 2 {
		t.Fatalf("got %d batches, want 2 (sizes = %v)", len(rec.sizes), rec.sizes)
	}
	if rec.sizes[0] != maxEmbeddingBatchSize || rec.sizes[1] != 1 {
		t.Fatalf("batch sizes = %v, want [%d 1]", rec.sizes, maxEmbeddingBatchSize)
	}

	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 17 {
		t.Fatalf("Chunks = %d, want 17", stats.Chunks)
	}
}

// batchRecorder captures the sizes of every EmbedBatch call.
type batchRecorder struct {
	rec *struct{ sizes []int }
}

func (b batchRecorder) Dim() int { return embedding.EmbeddingDim }

func (b batchRecorder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.rec.sizes = append(b.rec.sizes, len(texts))
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, embedding.EmbeddingDim)
		vec[0] = float32(len([]rune(t)))
		out[i] = vec
	}
	return out, nil
}

func TestIndexer_EmbedFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeKiroFile(t, dir, "first.json", []map[string]any{
		{"role": "user", "content": "hello"},
	})

	emb := &fakeEmbedder{failAfter: 1, err: errors.New("simulated ONNX failure")}
	idx, _ := newTestIndexer(t, emb)

	err := idx.IndexSession(context.Background(), "kiro", path, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "simulated ONNX failure") {
		t.Fatalf("expected embed error, got %v", err)
	}
}

func TestIndexer_ClearBackendAndClearAll(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	if err := idx.IndexSession(context.Background(), "kiro", path, nil); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}

	if err := idx.ClearBackend("kiro"); err != nil {
		t.Fatalf("ClearBackend: %v", err)
	}
	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats after ClearBackend: %v", err)
	}
	if stats.Chunks != 0 {
		t.Fatalf("Chunks = %d after ClearBackend, want 0", stats.Chunks)
	}

	if err := idx.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if err := idx.ClearAll(); err != nil {
		t.Fatalf("ClearAll idempotency: %v", err)
	}
}
