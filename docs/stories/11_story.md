# Story 11: Hybrid Search Engine and Search View Upgrade

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.6
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md).

## Goal

Wrap `IndexDB.SearchHybrid` behind a `search.SearchSemantic` function that speaks the
same `Response` shape as the existing V1 grep search, and upgrade `searchView` so it
picks the right engine automatically per backend with a transparent fallback to grep.

Depends on: **Story 7** (`Embedder`), **Story 9** (`IndexDB.SearchHybrid`),
**Story 10** (`App` owns an `*IndexDB` and a lazy embedder).

## Package Layout

```
internal/search/
  semantic.go
  semantic_test.go
internal/app/
  app.go               # searchView() is updated here
```

## Public API

```go
package search

// Result is extended with two optional fields. Zero values preserve V1 behavior.
type Result struct {
    BackendName string
    FilePath    string
    LineNumber  int
    MatchLine   string
    FileModTime time.Time

    // V2 additions, populated only when hybrid search produced the row:
    Score float64
    Role  string
    ChunkIndex int
}

type SemanticEngine interface {
    Embedder() SemanticEmbedder
    DB() SemanticDB
}

type SemanticEmbedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}

type SemanticDB interface {
    HasBackend(backendName string) (bool, error)
    SearchHybrid(backendName, query string, queryVec []float32, topK int) (
        []indexer.IndexedChunk, error)
}

func SearchSemantic(ctx context.Context, q Query, eng SemanticEngine) (Response, error)
```

## Acceptance Criteria

- [ ] `SearchSemantic` embeds `q.Text` once and calls `SearchHybrid` with `topK = 50` by
      default. The `topK` is configurable via a new optional field `Query.TopK`; zero
      keeps the default.
- [ ] Each returned `IndexedChunk` is mapped to a `search.Result`:
      - `FilePath = session_path`
      - `LineNumber = ChunkIndex + 1` (one-based, for display continuity with V1 grep)
      - `MatchLine = first-N characters of Content` (N=280)
      - `FileModTime` from `os.Stat` with the same "missing file is a warning" behavior
        V1 uses
      - `Score`, `Role`, `ChunkIndex` populated from the chunk
- [ ] `SearchSemantic` honors `q.PathFilter`, `q.From`, `q.To`, and `q.Favorites` the
      same way V1 does. Filtering happens after the hybrid query returns.
- [ ] `SearchSemantic` preserves hybrid ranking order inside the final result slice.
- [ ] `SearchSemantic` never panics on missing files; unreadable sources become
      `Response.Warnings` entries, the matching `Result` is dropped.
- [ ] The existing `Search` function is not modified. V2 adds on top.

## `searchView` Upgrade

- [ ] The view builds a `SemanticEngine` from `*App` when available; otherwise it
      sets `eng = nil` and always uses the grep path.
- [ ] Before running, it calls `db.HasBackend(backend.Name)` to decide the engine. A
      small `widget.Label` next to the backend selector reads `hybrid (FTS5 + vector)`
      or `lexical`. Errors from `HasBackend` degrade to `lexical`.
- [ ] When `lexical` is in use but the backend could be indexed (assets ready and
      backend not yet indexed), the view shows a low-key hint linking to the Index
      Manager.
- [ ] On any error from `SearchSemantic`, the view logs the error, falls back to
      `search.Search(q)` transparently, and surfaces a non-fatal warning in the
      warnings label.

## Verification Tasks

- [ ] `semantic_test.go` uses:
      - a `fakeEmbedder` returning `[]float32{1, 0, 0, ...}` to prove the vector is
        threaded through
      - a `fakeSemanticDB` returning synthetic chunks
      and covers:
      - happy path (hybrid results mapped and ordered)
      - empty query text still returns chunks via the semantic path
      - missing file on disk yields a warning, that row is dropped
      - path filter / date filter / favorites filter respected post-mapping
- [ ] A behavioral test asserts that `SearchSemantic` preserves the input ordering of
      `SearchHybrid` (i.e., this layer never re-sorts).
- [ ] `go test ./internal/search/...` is green under `-short`.
- [ ] Manual verification note (documented in the PR body, not a test): index a small
      backend, run a query that does not appear verbatim in any message, confirm the
      hybrid path returns a relevant message the grep path would miss.

## Out of Scope

- rewriting V1's grep engine
- Lucene-style query parsing (free-text is passed to FTS5 as-is)
- server-side batching of embedding for search queries
- on-the-fly indexing from the search view (has to go through Index Manager)

## Constraints

- All code and comments in English.
- `SearchSemantic` must not import any `internal/app` code; the engine is provided from
  above. This keeps `internal/search` testable without a Fyne runtime.
- Do not change `Response.Warnings` semantics; treat "semantic engine unavailable" as a
  silent fallback (logged, not a warning).
