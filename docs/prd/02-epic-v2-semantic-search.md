# 02. Epic V2: Semantic Search + Single-Window UX

> **Status:** Draft
>
> **Target:** `Beddel Desk 0.2.0`
>
> **Supersedes parts of:** [01. Epic V1](./01-epic-v1-local-search.md) local storage rules
> (see [Reconciliation with V1](#reconciliation-with-v1))

## Goal

Raise search quality from lexical grep to hybrid lexical+semantic, without losing the
local-first, zero-remote-dependency character of Version 1. Collapse the previous
multi-window UX into a single window with a navigation stack so users stop losing context
between Search, Favorites, Recent, Settings, and result details.

Version 2 must stay narrow:

- no remote services at runtime after the one-time first-run asset download
- no background watchers
- indexing is opt-in, per backend, and deletable
- the lexical (grep) search flow from Version 1 remains available as a fallback
- everything still fits a single-developer repository with a clean clone

## Product Summary

Two improvements ship together because they share the same UX surface:

1. **Semantic search via local embeddings.** The user can optionally build a local index
   for a configured backend. Indexing parses session files, splits messages into chunks,
   computes embeddings with a small local model, and stores chunks + vectors + full-text
   tokens in a SQLite database dedicated to the index. Search then runs a hybrid query
   (FTS5 lexical + sqlite-vec cosine KNN, merged with Reciprocal Rank Fusion) against
   that index when it exists, and falls back to grep when it does not.
2. **Single-window UX.** The UI previously opened a new native window per action. In
   Version 2 the main window stays single, and every surface (Search, Favorites, Recent,
   Settings, Result Detail, Index Manager) is rendered through a navigation stack.

The semantic model and the ONNX runtime are **not** committed to the repository and
**not** embedded in the Go binary. They are downloaded from their official public
sources on first use, validated against a SHA-256 manifest checked into the repo, and
cached under the user's OS cache directory. If the user's system already has a compatible
`libonnxruntime` installed (for example via `apt`, `brew`, or Windows PATH), the
application reuses it instead of downloading.

## Users

- Developers who already use Version 1 and want better recall when they remember the gist
  of a conversation but not the exact words.
- Contributors who want the repo small and the binary small.
- Operators on air-gapped machines who will decline the first-run download and keep using
  lexical search.

## Scope

| Story | Description | SP |
|-------|-------------|----|
| V2.0 | Single-window navigator (shipped in PR #1) | 3 |
| V2.1 | Asset download manager + system-library probe + checksum cache | 5 |
| V2.2 | Tokenizer + ONNX embedding runtime wrapper | 5 |
| V2.3 | Session chunker (Kiro / Gemini / Claude Code formats) | 3 |
| V2.4 | Index database (SQLite + FTS5 + sqlite-vec + RRF) | 5 |
| V2.5 | Indexer pipeline + Index Manager UI | 5 |
| V2.6 | Hybrid search engine + Search view upgrade | 3 |
| Total | Version 2 | 29 |

## Non-Goals

- remote inference or hosted embedding services
- background indexing or file watchers
- cross-device index sync
- automatic re-indexing on file change
- committing model weights or native libraries to this repository
- embedding model weights or native libraries into the compiled Go binary
- training, fine-tuning, or exchanging models
- team collaboration features on top of the index

## Key Product Rules

1. Indexing is always user-initiated, per backend, from the Index Manager view.
2. The user can delete one backend's index or delete the entire index database at any
   time, and the application continues to work in lexical-only mode afterwards.
3. The repository stays small: no model weights, no `libonnxruntime`, no LFS. The
   documented reproducible artifacts are SHA-256 checksums only.
4. The compiled binary stays close to its Version 1 size. First-run downloads of the
   model and runtime are disclosed to the user before they happen.
5. If the user declines the first-run download or is offline, the application stays fully
   usable via the Version 1 lexical search flow.
6. The Version 1 local storage rules are relaxed only for the opt-in `index.db` file. All
   other persistent files continue to follow Version 1 rules.

## Functional Requirements

1. The main window never opens a second window for any in-app action.
2. The navigation stack supports push, pop, replace (lateral swap), and reset (new root).
3. The tray menu resets the stack; in-flow actions push on top of it.
4. The user can open the **Index Manager** from the tray and from the main menu. It
   lists every configured backend with an indexing status (not indexed, indexing, indexed
   with session count and disk usage).
5. For each backend, the user can trigger **Index Backend**. Indexing shows a progress
   bar and a running count of sessions processed.
6. The user can **Clear Index** for a single backend, or **Clear All** to delete the
   entire index database. Both actions require confirmation.
7. On first index request, the application discloses what will be downloaded, the size,
   and the destination directory, and asks the user to confirm.
8. Before downloading the ONNX runtime, the application probes known system locations
   (`/usr/lib`, `/usr/local/lib`, `brew --prefix`, `%ProgramFiles%`, the linker search
   path). If a compatible shared library is found, it is reused and no download happens.
9. All downloaded assets are validated against a repository-committed SHA-256 manifest.
   A failed checksum deletes the partial file and surfaces a visible error.
10. Search in Version 2 is **hybrid** when an index exists for the selected backend:
    lexical tokens come from FTS5, semantic neighbors come from sqlite-vec, and the two
    ranked lists are merged with Reciprocal Rank Fusion.
11. Search falls back to the Version 1 grep flow when:
    - the backend has no index, or
    - the embedding runtime is unavailable (download declined, asset missing, runtime
      load error), or
    - a hybrid search call fails at runtime.
12. Each search result is traceable to a source file, chunk offset, and role (user,
    assistant) and remains openable through the existing `openSession` flow.
13. Incremental indexing: a file whose `mtime` has not changed since its last indexing
    run is not reprocessed.

## Local Storage Rules

### Allowed on disk (unchanged from V1, plus V2 additions)

From V1:

- backend names, categories, and session source paths
- favorite references, recent references, timestamps, small source metadata

**Added in V2:**

- `index.db` SQLite file (opt-in, per-install, deletable), containing:
  - chunk rows with `backend_name`, `session_path`, `chunk_index`, `role`, `content`,
    `timestamp`
  - FTS5 shadow table over chunk content
  - sqlite-vec shadow table with 384-dimensional embeddings
  - `file_index` rows tracking `file_path`, `mtime`, `backend_name`, `chunk_count`
- downloaded and validated assets in the user cache directory:
  - `<cache>/beddel-desk/onnxruntime/<version>/<platform>/libonnxruntime.{so,dylib,dll}`
  - `<cache>/beddel-desk/models/all-MiniLM-L6-v2/{model.onnx,tokenizer.json,vocab.txt}`

### Not allowed on disk

- chunk content or embeddings outside `index.db`
- `index.db` rows without a traceable source file path on disk
- caches of model output for non-indexing purposes
- remote telemetry

## Reconciliation with V1

Version 1 declared the following categories **not allowed on disk**:

> processed chunks; embeddings; semantic labels generated from transcript content;
> full-text indexes containing session text

Version 2 relaxes this rule **only** for the opt-in `index.db` file and **only** for the
user who explicitly triggers indexing. All other files (config, favorites, recents)
continue to honor the V1 rules. When the user clicks "Clear All" in the Index Manager,
the application returns to full V1 storage compliance.

## User Experience

### Tray Menu

The tray menu in Version 2 includes:

- `Home`
- `Search`
- `Favorites`
- `Recent`
- `Index Manager`
- `Settings`
- `Quit`

Tray actions **reset** the navigation stack to the chosen view.

### Index Manager View

- Header: `Index Manager`, back button (hidden at root).
- Body: one row per configured backend with columns
  - Name
  - Status (`Not indexed` / `Indexing ... N of M` / `Indexed: N sessions, X MB`)
  - Actions: `Index`, `Clear`
- Footer: `Clear All` button with confirmation dialog.
- Before the first indexing action ever runs, a modal discloses:
  - model asset and size (`all-MiniLM-L6-v2`, ~80 MB)
  - ONNX runtime asset and size (~30 MB, skipped if system install found)
  - destination directory
  - source URLs
  - a `Cancel` option that leaves the user in lexical-only mode

### Search View

- Top form: backend selector, query entry, path filter, date filters, favorites only.
- A small status line next to the backend selector shows either `lexical search` (no
  index for this backend) or `hybrid search (FTS5 + vector)` (index available).
- Results list is unchanged structurally from V1 but now includes a semantic-rank score
  hint when the hybrid path was used.
- When lexical search is in use but the backend has no index, a subtle call-to-action
  links to the Index Manager: `Indexing this backend will enable semantic search.`

### Result Detail View

The result detail view introduced in PR #1 continues to work. It now shows the chunk's
role (user / assistant) when the hybrid path produced the result.

### First-Run Disclosure

The first time the user taps `Index` on any backend, the application shows a disclosure
modal describing the download, its source URLs, its total size, the target cache path,
and the licensing note for the model. The modal offers `Download and Index` and `Cancel`.
Declining leaves the user in lexical-only mode; the modal shows again next time the user
attempts to index.

## Stories

Story files live under `docs/stories/`:

- Story V2.0 — Single-Window Navigator (already shipped in PR #1, documented for history)
- [Story 6](../stories/6_story.md) — Asset download manager + system-library probe + cache
- [Story 7](../stories/7_story.md) — Tokenizer + ONNX embedding runtime wrapper
- [Story 8](../stories/8_story.md) — Session chunker
- [Story 9](../stories/9_story.md) — Index database (schema, FTS5, sqlite-vec, RRF)
- [Story 10](../stories/10_story.md) — Indexer pipeline + Index Manager UI
- [Story 11](../stories/11_story.md) — Hybrid search engine + Search view upgrade

## Dependencies

- Go 1.23+ toolchain (existing)
- Fyne v2 (existing)
- `github.com/mattn/go-sqlite3` — SQLite driver with FTS5 build tag
- `github.com/asg017/sqlite-vec-go-bindings/cgo` — vector similarity extension for SQLite
- `github.com/yalue/onnxruntime_go` — Go wrapper around Microsoft ONNX Runtime
- `github.com/sugarme/tokenizer` — WordPiece tokenizer for BERT-family models
- Runtime assets (downloaded on demand, not committed):
  - Microsoft ONNX Runtime shared library, version pinned in the manifest
  - `sentence-transformers/all-MiniLM-L6-v2` ONNX model and tokenizer files from
    Hugging Face

## Risks

- Some session formats may not parse cleanly; the chunker must skip bad files with a
  visible warning instead of failing the whole indexing run.
- `sqlite-vec` requires CGO and a recent SQLite; the release workflow already builds with
  CGO and must gain an `fts5` build tag.
- The ONNX runtime shared library is platform- and architecture-specific. The download
  manager must pick the right one from Microsoft's release matrix.
- Hugging Face and GitHub Releases can rate-limit anonymous IPs; the download manager
  must surface clear errors and allow retry.
- Size of the first-run download (~80 MB model, ~30 MB runtime) is real; the disclosure
  must be clear and the opt-out path must stay usable.

## Success Criteria

Version 2 is successful when:

- a user can opt into indexing a backend, see progress, and get hybrid search results
- a user can delete one backend's index or all indexes at any time
- a user who declines the first-run download never sees semantic features break the
  lexical flow
- the single-window UX replaces every previous `NewWindow` call
- the repository, clone size, and binary size remain close to Version 1 numbers
- `go build ./...` and `go test ./...` both pass on Linux in CI

## Change Log

| Date       | Version | Notes |
|------------|---------|-------|
| 2026-05-08 | 0.1     | Initial V2 epic. Single-window navigator already landed in PR #1. |
