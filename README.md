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
- choosing a configured backend from the tray UI
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

## Clone

```bash
git clone https://github.com/botanarede/beddel-desk-go.git
cd beddel-desk-go
```

## Build

The current scaffold builds with the Go standard toolchain.

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

## Documentation

The full Version 1 definition lives in:
- [docs/prd/index.md](docs/prd/index.md)
- [docs/prd/01-epic-v1-local-search.md](docs/prd/01-epic-v1-local-search.md)

## Feedback

Criticism, suggestions, and implementation feedback:

`dev@botanarede.com.br`

## License

[MIT](LICENSE)

