# 02. Architecture: Semantic Search + Single-Window UX

> **Target:** `Beddel Desk 0.2.0`
>
> **Companion to:** [02. Epic V2 PRD](../prd/02-epic-v2-semantic-search.md)

## Objective

Design a local-first, opt-in semantic search engine and a single-window UI that fit the
existing Go + Fyne codebase without bloating the repository or the compiled binary.

## Design Principles (inherited from V1, extended)

- local-first
- no remote dependency **at runtime** after one-time first-run asset download
- no background watchers
- persistent storage of processed session content is allowed **only** inside the opt-in
  `index.db`
- small repository (no model weights, no native libraries checked in)
- small binary (no `go:embed` of model or runtime)
- every behavior that could surprise a contributor reading the source must be disclosed
  to the user before it happens

## High-Level Structure

```text
cmd/beddel-desk/
  main.go                          # unchanged entry point

internal/
  app/
    app.go                         # Fyne wiring, single-window shell
    navigator.go                   # navigation stack (PR #1)
    index_view.go                  # Index Manager view (V2.5)

  config/                          # unchanged
  storage/                         # unchanged
  version/                         # unchanged

  search/
    search.go                      # lexical grep search, kept as fallback
    semantic.go                    # NEW: SearchSemantic wrapper using indexer + embedder

  embedding/
    download/
      manager.go                   # NEW: asset download, checksum, system probe
      manifest.go                  # NEW: SHA-256 manifest + platform matrix
    embedder.go                    # NEW: ONNX runtime wrapper (384-dim vectors)
    tokenizer.go                   # NEW: sugarme/tokenizer wrapper

  indexer/
    chunker.go                     # NEW: JSON/JSONL session parser -> chunks
    index_db.go                    # NEW: SQLite + FTS5 + sqlite-vec + RRF
    indexer.go                     # NEW: walk -> chunk -> embed -> store pipeline
```

## Component Map

```
                ┌──────────────────────────────────────────┐
                │            App (Fyne window)             │
                │  ┌────────────────────────────────────┐  │
                │  │          Navigator (PR #1)         │  │
                │  │ Home / Search / Favs / Recent /    │  │
                │  │ Settings / Index Mgr / Detail      │  │
                │  └────────────┬───────────────────────┘  │
                └───────────────┼──────────────────────────┘
                                │
             ┌──────────────────┼──────────────────┐
             ▼                  ▼                  ▼
        config.AppConfig   storage.Store      search.Search
                                                   │
                               hybrid path ◀───────┤  grep fallback
                                    │              │
                                    ▼              │
                          ┌────────────────┐       │
                          │   indexer      │◀──────┘
                          │  ├─ chunker    │
                          │  ├─ index_db   │  (SQLite + FTS5 + sqlite-vec)
                          │  └─ indexer    │
                          └────────┬───────┘
                                   │
                                   ▼
                          ┌────────────────┐
                          │   embedding    │
                          │  ├─ tokenizer  │ (sugarme/tokenizer)
                          │  ├─ embedder   │ (yalue/onnxruntime_go)
                          │  └─ download   │ (asset manager)
                          └────────────────┘
```

## Data Flow

### Lexical search (unchanged from V1)

```
User -> searchView -> search.Search() -> filesystem scan -> results in memory -> UI
```

### Semantic indexing

```
User -> Index Manager -> indexer.IndexBackend(backend)
  ├─ ensure embedder initialized (download.Manager.EnsureAssets)
  ├─ walk backend.Paths
  ├─ for each changed/new file: chunker.ChunkSession() -> []Chunk
  ├─ embedder.EmbedBatch(chunk texts) -> [][]float32
  ├─ index_db.Store(chunk, embedding)
  └─ index_db.RecordFile(path, mtime, chunkCount)
```

### Hybrid search

```
User -> searchView -> search.SearchSemantic(query, backend)
  ├─ embedder.Embed(query)   -> []float32
  ├─ index_db.SearchHybrid(query, queryVec, backendName, topK)
  │     ├─ FTS5 MATCH -> ranked chunk ids
  │     ├─ vec KNN    -> ranked chunk ids
  │     └─ RRF merge  -> final ordered list
  └─ map chunks to search.Result (open-by-path still works)
```

## Module Boundaries and Responsibilities

### `internal/app`

- Owns the `fyne.Window`, the `Navigator`, the tray menu, and the main menu.
- Talks to `config`, `storage`, `search`, and `indexer` via function calls only.
- Never imports `onnxruntime_go` or `sqlite-vec` directly; semantic features are
  accessed through `search.SearchSemantic` and `indexer.Indexer`.

### `internal/embedding/download`

- Reads a repo-committed manifest that pins, per platform and architecture:
  - ONNX runtime release URL + SHA-256
  - `all-MiniLM-L6-v2` model URL + SHA-256
  - `tokenizer.json` URL + SHA-256
- Exposes `Manager.EnsureAssets(ctx, progress func) (*Assets, error)`:
  1. Probe system locations for `libonnxruntime.{so,dylib,dll}`. If found and ABI
     compatible, reuse.
  2. Otherwise download from the URL in the manifest, validate SHA-256, atomically
     install into `<UserCacheDir>/beddel-desk/...`.
  3. Return an `Assets` struct with resolved absolute paths.
- `Assets` is the only type the rest of the codebase consumes from this package.

### `internal/embedding`

- `Tokenizer` wraps `sugarme/tokenizer` with a fixed max-length policy (e.g. 256 tokens)
  aligned with the `all-MiniLM-L6-v2` training regime.
- `Embedder` wraps `yalue/onnxruntime_go`:
  - initializes the ONNX session against the downloaded model
  - accepts `Embed(text) []float32` (returns L2-normalized 384-dim)
  - accepts `EmbedBatch(texts []string) [][]float32`
  - exposes `Close()` for graceful shutdown
- Both types are constructed once by the `App` after `download.Manager.EnsureAssets`
  returns successfully, and passed to the `Indexer` and `SearchSemantic` as interfaces so
  tests can fake them.

### `internal/indexer`

- `Chunker` parses a session file into `[]Chunk`:
  - supports Kiro JSON (array or object with `messages[]`)
  - supports Gemini JSONL (one message per line)
  - supports Claude Code session JSON
  - skips unknown formats with a returned warning; never panics
  - each chunk has deterministic identity `(session_path, chunk_index)`
  - each chunk caps at ~400 tokens estimated using the chunker's own splitter
    (the final truncation is re-checked by the tokenizer before embedding)
- `IndexDB` opens `<UserConfigDir>/beddel-desk/index.db` with:
  - migrations applied on open
  - the `sqlite-vec` extension loaded via `sqlite-vec-go-bindings/cgo`
  - FTS5 build tag required
- `Indexer.IndexBackend(ctx, backend, model, progress)`:
  - walks `backend.Paths`
  - for each file: if `file_index.mtime == file.mtime`, skip; else delete stale chunks
    for that `session_path`, chunk, embed, insert fresh
  - reports progress as `(done, total)` to the UI

### `internal/search`

- `Search(q)` unchanged from V1.
- `SearchSemantic(q, backend, embedder, db, topK)` returns `Response` with `Result`
  shaped identically to V1 plus an optional `Score float64` and `Role string` so the UI
  can distinguish hybrid results.
- `searchView` inspects `indexer.IndexDB.HasBackend(name)` and switches between the two
  paths automatically. On any runtime error in the semantic path, it logs a warning and
  retries with grep transparently.

## Architecture Decisions (ADRs)

### ADR-1: Do not embed model or runtime in the Go binary

**Decision:** Download `libonnxruntime` and the ONNX model at first use. Validate with a
committed SHA-256 manifest. Cache in the OS user cache directory.

**Rationale:** committing ~110 MB of platform-specific binaries and model weights to a
Go repository inflates every `git clone` forever, complicates Fyne cross-compile, and
duplicates assets the upstream maintainers already host on stable public URLs
(`github.com/microsoft/onnxruntime/releases`, `huggingface.co/sentence-transformers/...`).

**Alternatives considered:**

- **Git LFS.** Rejected: pushes a vendoring burden onto every contributor for assets
  that do not need to live in Git history.
- **`go:embed` with zstd.** Rejected: binary grows from ~31 MB to ~110 MB on every
  platform even for users who decline semantic search. Cross-compile becomes hostile.
- **Commit raw assets.** Rejected for the same repo-bloat reasons.

**Consequences:** first-run UX requires a visible disclosure and a network call. Air-gapped
users fall back to lexical search. The repo ships only the SHA-256 manifest.

### ADR-2: Reuse system `libonnxruntime` when present

**Decision:** Probe known locations before downloading the ONNX runtime.

**Rationale:** distribution package managers (`apt install onnxruntime`, Homebrew) already
ship the runtime. Reusing the system copy avoids a second ~30 MB download and lets
system administrators control patch-level updates.

**Consequences:** the embedder must tolerate minor version skew and fail-soft when a
probed library turns out to be incompatible.

### ADR-3: SQLite + FTS5 + sqlite-vec in one database file

**Decision:** `index.db` is one SQLite file with three tables: `chunks`, FTS5 shadow
`chunks_fts`, and `vec0` shadow `chunks_vec`.

**Rationale:** a single file is trivial to delete ("Clear All"), easy to back up, easy to
relocate. FTS5 and sqlite-vec both run inside SQLite, so query code is one SQL session.

**Consequences:** the release build must enable CGO, the `fts5` build tag, and link
against the sqlite-vec extension. The release workflow already builds with CGO; the
story updates the tags.

### ADR-4: Reciprocal Rank Fusion for hybrid ranking

**Decision:** combine FTS5 BM25 ranks and sqlite-vec cosine ranks with RRF
(`1 / (k + rank_i)` summed per document), k=60.

**Rationale:** RRF requires only the rank, not calibrated scores, so BM25 and cosine can
be mixed without score normalization. It is robust, O(topK), and easy to unit test.

**Consequences:** the final ordering is unchanged if either ranker returns zero results,
which makes the fallback-to-lexical behavior trivial.

### ADR-5: Navigation stack over multiple windows

**Decision:** every view is a `fyne.CanvasObject` rendered into the main window via
`Navigator`. No call to `fyneApp.NewWindow` outside the initial window construction.

**Rationale:** multiple `fyne.Window` instances fragment keyboard focus, split the tray
state, and force users to hunt windows when returning from detail views. A navigation
stack matches tray-driven usage.

**Status:** shipped in PR #1.

**Consequences:** dialogs are still allowed (`dialog.ShowError`, `dialog.ShowConfirm`),
but every non-modal surface goes through `Navigator`.

## Verification Strategy

- **Unit tests** for every pure-logic component: `navigator_test.go` (done),
  `chunker_test.go`, `index_db_test.go`, `download/manifest_test.go`,
  `embedding/tokenizer_test.go`.
- **Integration tests** gated by an environment variable
  (`BEDDEL_EMBED_E2E=1`) for the embedder path, because it requires the ONNX runtime and
  a downloaded model.
- **Build verification** in the release workflow: `go build ./...` with CGO on all three
  platforms. The workflow stays green without downloading the model: the runtime
  download happens only at application first use.
- **Manual verification** on Linux: index a small backend, run a semantic query,
  confirm the hybrid path returns a relevant result.

## Build Boundary (updated)

- `go build ./cmd/beddel-desk` must succeed with **no network access** and must produce
  a binary close to the V1 size (~31 MB).
- `CGO_ENABLED=1` is required (already set in the release workflow).
- Build tags: `fts5` must be added to enable SQLite FTS5 alongside the existing driver.
- Platform-specific `libonnxruntime` is neither linked nor bundled at build time.

## Data Boundary (updated)

Persistent local state is now:

- everything V1 already allowed (config, favorites, recents, paths, timestamps)
- **plus** `index.db` (opt-in, per-install, deletable via Index Manager)
- **plus** downloaded assets under the OS user cache directory (deletable by the user
  directly or via Index Manager's "Clear All" followed by cache cleanup)

Persistent state still must not include:

- transcript text outside `index.db`
- embeddings outside `index.db`
- remote telemetry of any kind

## Open Risks

1. `sqlite-vec` is a relatively young extension; version pinning matters. The manifest
   pins the extension commit hash.
2. The ONNX runtime release matrix includes many variants (GPU, CUDA, TensorRT, WebGPU);
   the manifest must pick the plain CPU build for each platform.
3. Fyne does not render progress bars at high frequency on all drivers; the progress
   callback must debounce to avoid UI jitter.
