//go:build sqlite_fts5

// Package indexer's indexer.go wires the chunker, the embedder, and
// the index database into a single "index this session" pipeline.
//
// Build tag: the pipeline depends on IndexDB (sqlite_fts5 tag), so
// indexer.go itself lives behind the same tag. The default build
// simply omits this file and the app falls back to lexical-only.
//
// The pipeline indexes exactly ONE session file at a time (the file
// the user selected from a lexical search result). This is the
// "Index-on-Demand" strategy: the user finds a session via lexical
// search, then opts to index that specific session for deeper
// semantic queries.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Progress describes one tick of the indexing pipeline. A report
// callback observes it at each stage transition.
//
// Stage transitions: chunking → embedding → writing → done.
// Warnings accumulate for the session so the UI can surface them.
type Progress struct {
	BackendName string
	Stage       string // "chunking" | "embedding" | "writing" | "done"
	SessionPath string
	Warnings    []string
}

// Embedder is the narrow interface the pipeline consumes. It matches
// embedding.Embedder so *embedding.Embedder satisfies it directly,
// but we define the interface here so tests can plug in fakes without
// importing the ONNX runtime.
type Embedder interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
}

// maxEmbeddingBatchSize is the per-call ceiling for EmbedBatch. The
// all-MiniLM-L6-v2 ONNX model is small (~22 M params) and 16 chunks
// fit comfortably in CPU memory on every supported platform; the
// number also matches the throughput recommendation in Story 10.
const maxEmbeddingBatchSize = 16

// Indexer runs the chunk, embed, store pipeline for a single session
// file. It owns no mutable state other than the DB and embedder
// references, so it is trivially safe to construct once per process.
type Indexer struct {
	db  *IndexDB
	emb Embedder
}

// NewIndexer returns an Indexer bound to db and emb. Both must be
// non-nil; the pipeline panics on nil dependencies because there is
// no sensible fallback.
func NewIndexer(db *IndexDB, emb Embedder) *Indexer {
	if db == nil {
		panic("indexer: NewIndexer: db is nil")
	}
	if emb == nil {
		panic("indexer: NewIndexer: embedder is nil")
	}
	return &Indexer{db: db, emb: emb}
}

// IndexSession indexes exactly ONE session file identified by
// sessionPath into the index database under backendName. This is the
// per-session on-demand strategy: the user locates a session via
// lexical search, then chooses to index it for semantic queries.
//
// report may be nil. When non-nil it is invoked with a Progress value
// at each stage transition (chunking, embedding, writing, done).
//
// The function returns nil on success, including when the session was
// skipped because its mtime has not changed. An error is returned
// only for I/O, parse, embedding, or DB failures.
func (i *Indexer) IndexSession(ctx context.Context, backendName, sessionPath string, report func(Progress)) error {
	emit := newReportEmitter(report)

	// Honor cancellation early.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Incremental skip: if the stored mtime matches the file's
	// current mtime, the session is already indexed.
	skipped, mtime, skipErr := i.shouldSkip(sessionPath)
	if skipErr != nil {
		return fmt.Errorf("indexer: stat or lookup failed: %w", skipErr)
	}
	if skipped {
		emit.forced(Progress{
			BackendName: backendName,
			Stage:       "done",
			SessionPath: sessionPath,
		})
		return nil
	}

	emit.forced(Progress{
		BackendName: backendName,
		Stage:       "chunking",
		SessionPath: sessionPath,
	})

	chunks, warn, chunkErr := ChunkSession(sessionPath)
	if chunkErr != nil {
		return fmt.Errorf("indexer: chunk %s: %w", sessionPath, chunkErr)
	}
	if warn != nil {
		return fmt.Errorf("indexer: %s: %s", warn.SessionPath, warn.Reason)
	}

	emit.forced(Progress{
		BackendName: backendName,
		Stage:       "embedding",
		SessionPath: sessionPath,
	})

	embeddings, embedErr := i.embedInBatches(ctx, chunks)
	if embedErr != nil {
		return fmt.Errorf("indexer: embed %s: %w", sessionPath, embedErr)
	}

	emit.forced(Progress{
		BackendName: backendName,
		Stage:       "writing",
		SessionPath: sessionPath,
	})

	if err := i.db.ReplaceSessionChunks(backendName, sessionPath, mtime, chunks, embeddings); err != nil {
		return fmt.Errorf("indexer: store %s: %w", sessionPath, err)
	}

	emit.forced(Progress{
		BackendName: backendName,
		Stage:       "done",
		SessionPath: sessionPath,
	})
	return nil
}

// ClearBackend delegates to IndexDB.DeleteBackend. It exists on the
// Indexer so the UI does not have to reach past the pipeline for the
// Clear action.
func (i *Indexer) ClearBackend(backendName string) error {
	if i == nil || i.db == nil {
		return errors.New("indexer: ClearBackend: indexer not initialized")
	}
	return i.db.DeleteBackend(backendName)
}

// ClearAll delegates to IndexDB.DeleteAll. After a successful call
// the caller must not reuse the Indexer or the underlying IndexDB;
// the UI constructs a fresh IndexDB on next user action.
func (i *Indexer) ClearAll() error {
	if i == nil || i.db == nil {
		return errors.New("indexer: ClearAll: indexer not initialized")
	}
	return i.db.DeleteAll()
}

// shouldSkip returns (skip, mtime, err). A true skip means the stored
// mtime matches the filesystem mtime and the file does not need to be
// reprocessed. The filesystem mtime is returned even when skip is
// false so the caller can pass it to ReplaceSessionChunks.
func (i *Indexer) shouldSkip(path string) (bool, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, time.Time{}, err
	}
	fsMtime := info.ModTime().UTC()
	stored, ok, err := i.db.FileMTime(path)
	if err != nil {
		return false, fsMtime, err
	}
	if !ok {
		return false, fsMtime, nil
	}
	// Stored mtimes are kept at RFC 3339 nanosecond precision; the
	// filesystem mtime is truncated to the OS's mtime resolution.
	// Comparing Equal() handles both sides correctly, and covers the
	// typical Unix FS case where the resolution is 1 ns anyway.
	if stored.Equal(fsMtime) {
		return true, fsMtime, nil
	}
	return false, fsMtime, nil
}

// embedInBatches splits chunks into slices of at most
// maxEmbeddingBatchSize and concatenates the resulting per-batch
// vectors in order. Context cancellation is checked between batches.
func (i *Indexer) embedInBatches(ctx context.Context, chunks []Chunk) ([][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += maxEmbeddingBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + maxEmbeddingBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Content
		}
		vecs, err := i.emb.EmbedBatch(ctx, texts)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(batch) {
			return nil, fmt.Errorf(
				"indexer: embed batch returned %d vectors for %d chunks",
				len(vecs), len(batch))
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// reportEmitter wraps a nullable progress callback. Unlike the
// debounced emitter from the bulk-index era, the per-session pipeline
// is fast enough that every stage transition fires unconditionally.
type reportEmitter struct {
	mu sync.Mutex
	cb func(Progress)
}

// newReportEmitter returns an emitter bound to cb. A nil cb produces
// an emitter that silently drops every call.
func newReportEmitter(cb func(Progress)) *reportEmitter {
	return &reportEmitter{cb: cb}
}

// forced delivers p unconditionally.
func (e *reportEmitter) forced(p Progress) {
	if e == nil || e.cb == nil {
		return
	}
	e.mu.Lock()
	cb := e.cb
	e.mu.Unlock()
	cb(p)
}
