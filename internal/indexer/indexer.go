//go:build sqlite_fts5

// Package indexer's indexer.go wires the chunker, the embedder, and
// the index database into a single "index this backend" pipeline.
//
// Build tag: the pipeline depends on IndexDB (sqlite_fts5 tag), so
// indexer.go itself lives behind the same tag. The default build
// simply omits this file and the app falls back to lexical-only.
//
// The pipeline is intentionally sequential: one goroutine, one file
// at a time. This keeps the progress reporting simple, avoids
// interleaving errors from concurrent file reads, and matches the
// UI's "one indexing action at a time" contract (see Story 10).
package indexer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/botanarede/beddel-desk-go/internal/config"
)

// Progress describes one tick of the indexing pipeline. A report
// callback observes it at least once per file and at most once per
// 200 ms (see indexerDebounceInterval below).
//
// Stage transitions within a file: walking (before any file is
// selected), chunking (on file open), embedding (after ChunkSession
// returns), writing (after EmbedBatch returns), done (after the DB
// replace commits). Warnings accumulate for the whole run so the UI
// can surface them at the end.
type Progress struct {
	BackendName string
	Stage       string // "walking" | "chunking" | "embedding" | "writing" | "done"
	CurrentFile string
	Done        int
	Total       int
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

// indexerDebounceInterval caps the rate at which the report callback
// receives mid-file progress updates. File-boundary ticks bypass the
// debounce so the UI always sees the per-file count advance.
const indexerDebounceInterval = 200 * time.Millisecond

// maxEmbeddingBatchSize is the per-call ceiling for EmbedBatch. The
// all-MiniLM-L6-v2 ONNX model is small (~22 M params) and 16 chunks
// fit comfortably in CPU memory on every supported platform; the
// number also matches the throughput recommendation in Story 10.
const maxEmbeddingBatchSize = 16

// Indexer runs the walk, chunk, embed, store pipeline for a single
// backend at a time. It owns no mutable state other than the DB and
// embedder references, so it is trivially safe to construct once per
// process.
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

// IndexBackend walks every path in backend.Paths, chunks each regular
// file, embeds its chunks in batches of up to maxEmbeddingBatchSize,
// and replaces the session in the index database atomically per file.
//
// report may be nil. When non-nil it is invoked with a Progress value
// at least once per file and at most once per indexerDebounceInterval
// across mid-file ticks.
//
// The pipeline does NOT fail the whole run on one bad file. Chunker
// warnings, per-file embedding failures, and per-file DB replace
// failures are all recorded in the accumulating Warnings slice and
// the loop continues. The function returns an error only when:
//
//  1. ctx is canceled (returns ctx.Err() unwrapped at the next file
//     boundary), or
//  2. zero files were successfully indexed AND the run collected at
//     least one warning (in which case the aggregated message is
//     returned so the UI can surface it).
//
// Otherwise the return is nil, even if some files were skipped for
// incremental reasons and some produced warnings.
func (i *Indexer) IndexBackend(ctx context.Context, backend config.Backend, report func(Progress)) error {
	emit := newReportEmitter(report, indexerDebounceInterval, time.Now)

	// Step 1: collect every regular file under backend.Paths. We
	// resolve the full list up front so Progress.Total is accurate
	// for the UI before any embedding starts.
	emit.forced(Progress{BackendName: backend.Name, Stage: "walking"})
	files, walkWarnings, err := collectFiles(ctx, backend.Paths)
	if err != nil {
		return err
	}

	warnings := walkWarnings
	total := len(files)
	indexed := 0

	for fi, path := range files {
		// Honor cancellation at every file boundary.
		if err := ctx.Err(); err != nil {
			return err
		}

		// Incremental skip: if the stored mtime matches the file's
		// current mtime, the session is already indexed and we move
		// on without touching the DB.
		skipped, mtime, skipErr := i.shouldSkip(path)
		if skipErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: stat or lookup failed: %s", path, skipErr.Error()))
			emit.forced(Progress{
				BackendName: backend.Name,
				Stage:       "chunking",
				CurrentFile: path,
				Done:        fi,
				Total:       total,
				Warnings:    warnings,
			})
			continue
		}
		if skipped {
			emit.forced(Progress{
				BackendName: backend.Name,
				Stage:       "chunking",
				CurrentFile: path,
				Done:        fi + 1,
				Total:       total,
				Warnings:    warnings,
			})
			continue
		}

		emit.forced(Progress{
			BackendName: backend.Name,
			Stage:       "chunking",
			CurrentFile: path,
			Done:        fi,
			Total:       total,
			Warnings:    warnings,
		})

		chunks, warn, chunkErr := ChunkSession(path)
		if chunkErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", path, chunkErr.Error()))
			continue
		}
		if warn != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", warn.SessionPath, warn.Reason))
			continue
		}

		emit.forced(Progress{
			BackendName: backend.Name,
			Stage:       "embedding",
			CurrentFile: path,
			Done:        fi,
			Total:       total,
			Warnings:    warnings,
		})

		embeddings, embedErr := i.embedInBatches(ctx, chunks)
		if embedErr != nil {
			if errors.Is(embedErr, context.Canceled) || errors.Is(embedErr, context.DeadlineExceeded) {
				return embedErr
			}
			// Per Story 10: a batch failure invalidates its file but
			// the pipeline keeps running. Record the cause and move
			// on; do NOT call ReplaceSessionChunks so the existing
			// rows for this session (if any) stay intact.
			warnings = append(warnings, fmt.Sprintf("%s: embed: %s", path, embedErr.Error()))
			continue
		}

		emit.forced(Progress{
			BackendName: backend.Name,
			Stage:       "writing",
			CurrentFile: path,
			Done:        fi,
			Total:       total,
			Warnings:    warnings,
		})

		if err := i.db.ReplaceSessionChunks(backend.Name, path, mtime, chunks, embeddings); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: store: %s", path, err.Error()))
			continue
		}
		indexed++

		emit.forced(Progress{
			BackendName: backend.Name,
			Stage:       "writing",
			CurrentFile: path,
			Done:        fi + 1,
			Total:       total,
			Warnings:    warnings,
		})
	}

	emit.forced(Progress{
		BackendName: backend.Name,
		Stage:       "done",
		Done:        total,
		Total:       total,
		Warnings:    warnings,
	})

	// If we found candidate files but managed to index zero of them
	// AND we collected warnings along the way, bubble the aggregate
	// up as an error so the UI can show the user something went
	// wrong. A clean "no files match" run is not an error.
	if indexed == 0 && len(warnings) > 0 && total > 0 {
		return fmt.Errorf(
			"index backend %q: no files indexed: %s",
			backend.Name,
			strings.Join(warnings, "; "),
		)
	}
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
// Story 10 has the UI construct a fresh IndexDB on next user action.
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
//
// On a batch failure the entire file is invalidated (see
// IndexBackend), so this function returns the error and the caller
// decides whether to surface it or record a warning.
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

// collectFiles walks every root in paths and returns the absolute
// paths of every regular file it finds. A permission-denied error on
// a directory is recorded as a warning (so the user knows why that
// subtree was skipped) but does not abort the walk.
//
// Symlink cycles are NOT followed: filepath.WalkDir does not recurse
// into symlinks, so the default behavior matches the Story 10 spec.
func collectFiles(ctx context.Context, paths []string) ([]string, []string, error) {
	var files []string
	var warnings []string
	seen := map[string]struct{}{}

	for _, root := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(root)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				warnings = append(warnings, fmt.Sprintf("%s: permission denied", root))
				continue
			}
			if errors.Is(err, fs.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("%s: does not exist", root))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("%s: %s", root, err.Error()))
			continue
		}

		// A single-file root is a legitimate configuration: treat it
		// exactly like a directory that contains one file.
		if !info.IsDir() {
			abs, absErr := filepath.Abs(root)
			if absErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %s", root, absErr.Error()))
				continue
			}
			if _, dup := seen[abs]; !dup {
				seen[abs] = struct{}{}
				files = append(files, abs)
			}
			continue
		}

		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// Story 10: permission errors skip the subtree and
				// keep going. Other walk errors also degrade to a
				// warning so the pipeline can continue with the
				// siblings.
				if errors.Is(err, fs.ErrPermission) {
					warnings = append(warnings, fmt.Sprintf("%s: permission denied", p))
					if d != nil && d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				warnings = append(warnings, fmt.Sprintf("%s: %s", p, err.Error()))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				// Symlinks, sockets, devices: ignore silently.
				return nil
			}
			abs, absErr := filepath.Abs(p)
			if absErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %s", p, absErr.Error()))
				return nil
			}
			if _, dup := seen[abs]; dup {
				return nil
			}
			seen[abs] = struct{}{}
			files = append(files, abs)
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.ErrPermission) {
			warnings = append(warnings, fmt.Sprintf("%s: %s", root, walkErr.Error()))
		}
	}
	return files, warnings, nil
}

// reportEmitter debounces mid-file progress updates to at most one
// per interval. File-boundary transitions call forced() instead of
// emit() so the UI always sees the file count advance even under
// heavy throttling.
type reportEmitter struct {
	mu       sync.Mutex
	cb       func(Progress)
	interval time.Duration
	now      func() time.Time
	last     time.Time
}

// newReportEmitter returns an emitter bound to cb. A nil cb produces
// an emitter that silently drops every call; the pipeline still works
// correctly, just without user-visible progress.
func newReportEmitter(cb func(Progress), interval time.Duration, now func() time.Time) *reportEmitter {
	if now == nil {
		now = time.Now
	}
	return &reportEmitter{cb: cb, interval: interval, now: now}
}

// forced delivers p unconditionally. Used at file boundaries and on
// stage transitions where the user must see the update.
func (e *reportEmitter) forced(p Progress) {
	if e == nil || e.cb == nil {
		return
	}
	e.mu.Lock()
	e.last = e.now()
	cb := e.cb
	e.mu.Unlock()
	cb(p)
}
