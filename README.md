# Beddel Desk Go

Beddel Desk is a lean desktop utility for searching local agent session history on demand.

Version 0.2.0 adds opt-in semantic search to the local-first Version 1 foundation:
- local-only by default
- tray-oriented single-window UX
- no remote services at runtime after a one-time first-run asset download
- no background watchers
- persistent storage of processed session content is allowed only inside the opt-in `index.db`

The repository is open source and intended to stay easy to clone, inspect, build, and improve.

## Scope

Version 0.2.0 supports:
- choosing a configured backend from the desktop menu or tray menu when supported by the platform
- running an on-demand search against that backend's local session files — hybrid (FTS5 + vector) when an index exists, lexical (grep) otherwise
- optionally building a per-backend semantic index via the **Index Manager** view, with a first-run disclosure for the ~110 MB of model + runtime assets
- showing results with lightweight filters (path, date, favorites-only)
- storing only local references (favorites, session file names, backend categories) plus the opt-in `index.db` that the user can delete at any time

The project does not ship the ONNX model or the ONNX runtime inside the binary. They are downloaded on first indexing, validated against a committed SHA-256 manifest, and cached under the user's OS cache directory. If `libonnxruntime` is already installed on the system, the application reuses it and skips the runtime download.

## Repository Layout

```text
cmd/beddel-desk/         Go entrypoint
internal/                Internal packages
docs/prd/                BMAD-style product requirements
docs/architecture/       BMAD-style architecture notes
```

## Download

Pre-built binaries for Linux, macOS, and Windows are available on the
[Releases](https://github.com/botanarede/beddel-desk-go/releases/latest) page.

| Platform | Binary |
|----------|--------|
| Linux amd64 | `beddel-desk-linux-amd64` |
| macOS Apple Silicon | `beddel-desk-darwin-arm64` |
| Windows amd64 | `beddel-desk-windows-amd64.exe` |

On Debian/Ubuntu, a `.deb` package is also available:

```bash
# Download and install
wget https://github.com/botanarede/beddel-desk-go/releases/latest/download/beddel-desk_0.2.0_amd64.deb
sudo dpkg -i beddel-desk_0.2.0_amd64.deb
```

## Clone

```bash
git clone https://github.com/botanarede/beddel-desk-go.git
cd beddel-desk-go
```

## Build

Beddel Desk is implemented with Go and Fyne. You need the Go toolchain and the native desktop prerequisites required by Fyne for your OS.

Install dependencies after cloning:

```bash
go mod download
```

### Linux

```bash
# Default (lexical search only):
go build -o bin/beddel-desk ./cmd/beddel-desk

# With semantic search (hybrid FTS5 + vector):
CGO_ENABLED=1 go build -tags sqlite_fts5 -o bin/beddel-desk ./cmd/beddel-desk
```

### macOS

```bash
# Default (lexical search only):
go build -o bin/beddel-desk ./cmd/beddel-desk

# With semantic search:
CGO_ENABLED=1 go build -tags sqlite_fts5 -o bin/beddel-desk ./cmd/beddel-desk
```

### Windows

```powershell
# Default (lexical search only):
go build -o bin/beddel-desk.exe ./cmd/beddel-desk

# With semantic search:
$env:CGO_ENABLED="1"
go build -tags sqlite_fts5 -o bin/beddel-desk.exe ./cmd/beddel-desk
```

> The `sqlite_fts5` build links SQLite FTS5 (via `github.com/mattn/go-sqlite3`) and the sqlite-vec extension (via `github.com/asg017/sqlite-vec-go-bindings/cgo`). Release binaries use this configuration.

## Verify

```bash
./bin/beddel-desk version
```

On Windows:

```powershell
.\bin\beddel-desk.exe version
```

## Run

```bash
go run ./cmd/beddel-desk
```

The app opens a desktop window and registers a system tray menu on Fyne desktop drivers that support it. The shell exposes:

- `Search` — hybrid when the selected backend has an index, lexical (V1 grep) otherwise
- `Favorites`
- `Recent`
- `Index Manager` — opt into semantic search per backend, view index status, clear individual indexes or the whole index database
- `Settings`
- `Quit`

## Local Storage

Configuration and reference storage use the operating system user config directory under `beddel-desk`.

**Always persisted (V1 compatible):**

- backend names and categories
- local source paths
- favorite session file references
- recent session file references
- timestamps for favorites and recent opens

**Persisted only when the user opts into semantic search via the Index Manager:**

- `index.db` (SQLite + FTS5 + sqlite-vec): chunk rows, the FTS5 shadow, the 384-dim vector shadow, and `file_index` rows tracking per-file mtime

**Downloaded assets (cached under the OS user cache directory, never shipped with the binary):**

- `libonnxruntime.{so,dylib,dll}` for the current platform — reused from the system when a compatible copy is already installed
- `sentence-transformers/all-MiniLM-L6-v2` ONNX model and `tokenizer.json`

Both the index database and the downloaded assets can be cleared at any time (Index Manager > Clear All for the database, or delete the cache directory directly for the assets).

Search match lines, parsed transcript data, embeddings outside `index.db`, and any other processed session content are never written to disk.

## Documentation

Product requirements and architecture docs live under `docs/`:

- [docs/prd/index.md](docs/prd/index.md) — PRD index, active and shipped epics
- [docs/prd/02-epic-v2-semantic-search.md](docs/prd/02-epic-v2-semantic-search.md) — active epic: semantic search and single-window UX for 0.2.0
- [docs/prd/01-epic-v1-local-search.md](docs/prd/01-epic-v1-local-search.md) — shipped epic: local session search
- [docs/architecture/index.md](docs/architecture/index.md) — architecture index
- [docs/stories/](docs/stories/) — implementation stories, one per slice

## Feedback

Criticism, suggestions, and implementation feedback:

`dev@botanarede.com.br`

## License

[MIT](LICENSE)
