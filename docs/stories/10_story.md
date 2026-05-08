# Story 10: Indexer Pipeline and Index Manager UI

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.5
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md).

## Goal

Glue Stories 6, 7, 8, and 9 together into a user-visible flow: the user opens the Index
Manager, picks a backend, confirms the first-run download disclosure, watches a progress
bar, and ends up with a queryable index. Deletion works the same way in reverse.

Depends on: **Story 6** (`Manager.EnsureAssets`), **Story 7** (`Embedder`,
`Tokenizer`), **Story 8** (`ChunkSession`), **Story 9** (`IndexDB`).

## Package Layout

```
internal/indexer/
  indexer.go           # pipeline
  indexer_test.go

internal/app/
  index_view.go        # Fyne view returned to Navigator
  index_view_test.go   # logic-only tests over a fake indexer
```

## Public API

```go
package indexer

type Progress struct {
    BackendName string
    Stage       string // "walking" | "chunking" | "embedding" | "writing" | "done"
    CurrentFile string
    Done        int
    Total       int
    Warnings    []string
}

type Indexer struct { /* unexported */ }

type Embedder interface { // same interface as internal/embedding.Embedder
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Dim() int
}

func NewIndexer(db *IndexDB, emb Embedder) *Indexer

func (i *Indexer) IndexBackend(ctx context.Context, backend config.Backend,
    report func(Progress)) error

func (i *Indexer) ClearBackend(backendName string) error
func (i *Indexer) ClearAll() error
```

## Acceptance Criteria

- [ ] `IndexBackend` walks every path in `backend.Paths`, treats non-regular entries
      as no-ops, and never panics on symlink cycles (rely on `filepath.WalkDir` and skip
      `os.ErrPermission`).
- [ ] For each candidate file, the pipeline calls `FileMTime`. When the stored
      `mtime` matches the filesystem `mtime`, the file is skipped ("incremental").
- [ ] Chunker warnings do not abort the run; they accumulate in `Progress.Warnings` and
      in the final error only when **no** file could be indexed.
- [ ] Embedding happens in batches of up to 16 chunks. A batch failure invalidates its
      file (rollback) but keeps the pipeline running.
- [ ] Progress callback fires at least once per file, at most once per 200 ms.
- [ ] `ctx.Done()` interrupts the pipeline at the next file boundary and returns
      `ctx.Err()` unwrapped.
- [ ] `ClearBackend` and `ClearAll` both return cleanly without re-opening the DB.

## Index Manager View (`internal/app/index_view.go`)

- [ ] Header uses `a.viewHeader("Index Manager")` so the back button behavior matches
      the rest of the app.
- [ ] Body lists every configured backend in a `widget.List` or `container.VBox` of
      rows. Each row shows the status string from `IndexDB.Stats` ("Not indexed" /
      "Indexed: N sessions, X MB"). A pending indexing run overrides that with
      "Indexing: done/total".
- [ ] Each row has two buttons: `Index` and `Clear`. `Clear` opens a confirmation
      dialog before calling `ClearBackend`.
- [ ] Footer has `Clear All` with confirmation and, on success, refreshes the list.
- [ ] The first time the user taps `Index`, the view runs `EnsureAssets` with a
      disclosure modal summarizing what will be downloaded (pulled from the manifest).
      Declining leaves the view in its current state with no side effects.
- [ ] Indexing runs on a goroutine; UI updates go through `fyne.Do`.
- [ ] Errors surface via `dialog.ShowError` anchored to `a.main`.

## App Wiring

- [ ] `App.Run` constructs the `IndexDB` and keeps it on the struct. It does **not**
      construct the `Embedder` eagerly; the embedder is lazily built inside
      `indexView` on first indexing action so the app starts fast.
- [ ] A helper `a.ensureEmbedder(ctx)` handles the lazy build, showing the disclosure
      modal the first time and caching the constructed embedder on `*App`.
- [ ] Tray menu gains `Index Manager` (between `Recent` and `Settings`).
- [ ] Home view gains an `Index Manager` button.
- [ ] Closing the app calls `Close` on `IndexDB` and on the embedder if built.

## Verification Tasks

- [ ] `indexer_test.go` runs the pipeline with:
      - a fake `IndexDB` recording calls in order
      - a fake `Embedder` returning `[]float32{<chunk-index-modulo-M>}` so assertions
        can match chunks to embeddings
      - a temp directory of fixtures (reuse the Story 8 fixtures)
      Covers: happy path, incremental skip, chunker warning accumulation,
      context cancellation, batch-size boundary (17 chunks -> 2 batches).
- [ ] `index_view_test.go` exercises the view's logic without a real Fyne runtime:
      extract the list-building and state transitions into pure helpers that can be
      asserted. (Follow the pattern from `navigator_test.go` which uses a `fakeHost`.)
- [ ] Running `go test ./internal/indexer/... ./internal/app/...` passes under
      `-short` (no ONNX runtime required).

## Out of Scope

- the download manager internals (Story 6)
- the embedder internals (Story 7)
- the search integration (Story 11)
- background or scheduled re-indexing
- progress bars with sub-file granularity

## Constraints

- All code and comments in English.
- Do not construct the embedder on app startup. First-run download is disclosed and
  triggered only when the user taps `Index`.
- UI never blocks the event loop for file I/O or embedding.
