# Beddel Desk Go

Beddel Desk is a lean desktop utility for searching local agent session history on demand.

Version 1 is intentionally small:
- local-only
- tray-oriented product scope
- no remote services
- no background watchers
- no persistent storage of processed session content

The repository is open source and intended to stay easy to clone, inspect, build, and improve.

## Scope

The first implementation focuses on:
- choosing a configured backend from the desktop menu or tray menu when supported by the platform
- running an on-demand search against that backend's local session files
- showing results with lightweight filters
- storing only local references such as favorites, session file names, and backend categories

The project does not persist normalized transcripts, extracted message text, embeddings, or any other processed session content.

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
| macOS Intel | `beddel-desk-darwin-amd64` |
| macOS Apple Silicon | `beddel-desk-darwin-arm64` |
| Windows amd64 | `beddel-desk-windows-amd64.exe` |

On Debian/Ubuntu, a `.deb` package is also available:

```bash
# Download and install
wget https://github.com/botanarede/beddel-desk-go/releases/latest/download/beddel-desk_0.1.0_amd64.deb
sudo dpkg -i beddel-desk_0.1.0_amd64.deb
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
go build -o bin/beddel-desk ./cmd/beddel-desk
```

### macOS

```bash
go build -o bin/beddel-desk ./cmd/beddel-desk
```

### Windows

```powershell
go build -o bin/beddel-desk.exe ./cmd/beddel-desk
```

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

- `Search`
- `Favorites`
- `Recent`
- `Settings`
- `Quit`

## Local Storage

Configuration and reference storage use the operating system user config directory under `beddel-desk`.

Persisted data is limited to:

- backend names and categories
- local source paths
- favorite session file references
- recent session file references
- timestamps for favorites and recent opens

Search result content, matched lines, parsed transcript data, normalized transcript data, chunks, embeddings, and indexes are not written to disk.

## Documentation

The full Version 1 definition lives in:
- [docs/prd/index.md](docs/prd/index.md)
- [docs/prd/01-epic-v1-local-search.md](docs/prd/01-epic-v1-local-search.md)

## Feedback

Criticism, suggestions, and implementation feedback:

`dev@botanarede.com.br`

## License

[MIT](LICENSE)
