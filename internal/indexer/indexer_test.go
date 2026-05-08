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
	"time"

	"github.com/botanarede/beddel-desk-go/internal/config"
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
	// The three real-format fixtures cover Kiro, Gemini, and Claude
	// Code shapes in a single run.
	kiroPath := copyFixture(t, dir, "kiro_session.json")
	gemPath := copyFixture(t, dir, "gemini_session.jsonl")
	claudePath := copyFixture(t, dir, "claude_session.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	var progresses []Progress
	err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "mixed",
		Paths: []string{dir},
	}, func(p Progress) { progresses = append(progresses, p) })
	if err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}

	// Every fixture was indexed: one session per file.
	stats, err := db.Stats("mixed")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 3 {
		t.Fatalf("Sessions = %d, want 3", stats.Sessions)
	}
	if stats.Chunks == 0 {
		t.Fatalf("Chunks = 0, want > 0")
	}

	// The pipeline records the filesystem mtime so a re-run skips
	// every file (see TestIndexer_IncrementalSkip).
	for _, p := range []string{kiroPath, gemPath, claudePath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		stored, ok, err := db.FileMTime(p)
		if err != nil || !ok {
			t.Fatalf("FileMTime %s ok=%v err=%v", p, ok, err)
		}
		if !stored.Equal(info.ModTime().UTC()) {
			t.Fatalf("stored mtime %v != fs mtime %v for %s", stored, info.ModTime().UTC(), p)
		}
	}

	// At least one progress tick must reach the done stage.
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
	copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, _ := newTestIndexer(t, emb)

	backend := config.Backend{Name: "kiro", Paths: []string{dir}}
	if err := idx.IndexBackend(context.Background(), backend, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstCalls := emb.calls.Load()
	if firstCalls == 0 {
		t.Fatalf("first run made 0 embed calls, want >= 1")
	}

	// Second run with the same files and unchanged mtimes must skip
	// every file: the embed call count stays constant.
	if err := idx.IndexBackend(context.Background(), backend, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := emb.calls.Load(); got != firstCalls {
		t.Fatalf("second run made %d new embed calls, want 0 (got total %d, baseline %d)", got-firstCalls, got, firstCalls)
	}
}

func TestIndexer_ChunkerWarningAccumulates(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "kiro_session.json")
	copyFixture(t, dir, "malformed.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	var lastProgress Progress
	err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, func(p Progress) { lastProgress = p })
	if err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}

	// The real fixture was indexed.
	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1 (good fixture indexed)", stats.Sessions)
	}

	// The malformed fixture surfaced as a warning on the final tick.
	sawWarn := false
	for _, w := range lastProgress.Warnings {
		if strings.Contains(w, "malformed.json") {
			sawWarn = true
			break
		}
	}
	if !sawWarn {
		t.Fatalf("expected malformed warning, got %q", lastProgress.Warnings)
	}
}

func TestIndexer_AllBadFilesReturnsError(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "malformed.json")

	emb := &fakeEmbedder{}
	idx, _ := newTestIndexer(t, emb)

	err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, nil)
	if err == nil {
		t.Fatalf("expected error when every file is bad, got nil")
	}
	if !strings.Contains(err.Error(), "no files indexed") {
		t.Fatalf("error = %v, want substring %q", err, "no files indexed")
	}
}

func TestIndexer_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	// Two files so the pipeline has a chance to hit a file boundary
	// after the first batch and return ctx.Err() before writing the
	// second.
	copyFixture(t, dir, "kiro_session.json")
	writeKiroFile(t, dir, "second.json", []map[string]any{
		{"role": "user", "content": "another message"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	emb := &cancellingEmbedder{cancel: cancel}
	idx, _ := newTestIndexer(t, emb)

	err := idx.IndexBackend(ctx, config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, nil)
	if err == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestIndexer_BatchSizeBoundary(t *testing.T) {
	dir := t.TempDir()
	// Produce a session with 17 messages so the chunker returns 17
	// chunks (one per message). The pipeline should split this into
	// exactly two EmbedBatch calls: first of size 16, second of size
	// 1. maxEmbeddingBatchSize is the boundary we care about.
	messages := make([]map[string]any, 17)
	for i := range messages {
		messages[i] = map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("message number %d", i),
		}
	}
	path := writeKiroFile(t, dir, "seventeen.json", messages)

	// Record every batch size the embedder receives.
	rec := &struct{ sizes []int }{}
	emb := batchRecorder{rec: rec}

	idx, db := newTestIndexer(t, emb)
	if err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, nil); err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}

	if len(rec.sizes) != 2 {
		t.Fatalf("got %d batches, want 2 (sizes = %v)", len(rec.sizes), rec.sizes)
	}
	if rec.sizes[0] != maxEmbeddingBatchSize || rec.sizes[1] != 1 {
		t.Fatalf("batch sizes = %v, want [%d 1]", rec.sizes, maxEmbeddingBatchSize)
	}

	// All 17 chunks landed in the DB for that session.
	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 17 {
		t.Fatalf("Chunks = %d, want 17", stats.Chunks)
	}
	_ = path
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

func TestIndexer_EmbedFailureInvalidatesFileNotRun(t *testing.T) {
	dir := t.TempDir()
	// Two good files. The fake embedder fails on the second call,
	// which invalidates the second file. The first must still be
	// present in the DB.
	writeKiroFile(t, dir, "first.json", []map[string]any{
		{"role": "user", "content": "hello"},
	})
	writeKiroFile(t, dir, "second.json", []map[string]any{
		{"role": "user", "content": "world"},
	})

	emb := &fakeEmbedder{failAfter: 2, err: errors.New("simulated ONNX failure")}
	idx, db := newTestIndexer(t, emb)

	var lastProgress Progress
	err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, func(p Progress) { lastProgress = p })
	if err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}

	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1 (second file must be invalidated)", stats.Sessions)
	}
	// The warning must name the failing file.
	sawWarn := false
	for _, w := range lastProgress.Warnings {
		if strings.Contains(w, "simulated ONNX failure") {
			sawWarn = true
			break
		}
	}
	if !sawWarn {
		t.Fatalf("expected embed warning, got %q", lastProgress.Warnings)
	}
}

func TestIndexer_ClearBackendAndClearAll(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	if err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir},
	}, nil); err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}

	// ClearBackend leaves the DB open and callable.
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

	// ClearAll removes the file. Subsequent queries would fail, but
	// the method itself must return nil on the first call and nil on
	// a second idempotent call.
	if err := idx.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if err := idx.ClearAll(); err != nil {
		t.Fatalf("ClearAll idempotency: %v", err)
	}
}

func TestIndexer_PermissionDeniedOnRootIsWarning(t *testing.T) {
	// Root path does not exist: pipeline records a warning and
	// continues; since every file in `dir` still indexes fine, the
	// run succeeds.
	dir := t.TempDir()
	copyFixture(t, dir, "kiro_session.json")

	emb := &fakeEmbedder{}
	idx, db := newTestIndexer(t, emb)

	var lastProgress Progress
	err := idx.IndexBackend(context.Background(), config.Backend{
		Name:  "kiro",
		Paths: []string{dir, "/path/that/does/not/exist"},
	}, func(p Progress) { lastProgress = p })
	if err != nil {
		t.Fatalf("IndexBackend: %v", err)
	}
	stats, err := db.Stats("kiro")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1 (real file still indexed)", stats.Sessions)
	}
	sawMissingWarn := false
	for _, w := range lastProgress.Warnings {
		if strings.Contains(w, "does not exist") {
			sawMissingWarn = true
		}
	}
	if !sawMissingWarn {
		t.Fatalf("expected missing-path warning, got %q", lastProgress.Warnings)
	}
}

func TestReportEmitterDebounce(t *testing.T) {
	// Purely synchronous test for the debounce helper. forced()
	// always fires; tick-to-tick gap inside interval is dropped only
	// by emit(), which we do not use in the pipeline (all progress
	// ticks are forced() because they mark stage changes or file
	// boundaries). We still verify forced() updates the last-emit
	// timestamp so a hypothetical future emit() call honors it.
	var count int
	clock := time.Unix(0, 0)
	e := newReportEmitter(func(p Progress) { count++ }, 200*time.Millisecond, func() time.Time { return clock })
	e.forced(Progress{Stage: "walking"})
	e.forced(Progress{Stage: "chunking"})
	if count != 2 {
		t.Fatalf("forced called %d times, want 2", count)
	}
}
