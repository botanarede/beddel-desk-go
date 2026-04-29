# Story 1: Project Bootstrap and Desktop Shell

## Goal

Implement a cross-platform desktop application shell for Beddel Desk with a visible entry point to the Version 1 local search workflow.

## Acceptance Criteria

- [x] The repository builds as a Go project with a documented entry point.
- [ ] The application starts successfully on Linux, macOS, and Windows targets supported by the chosen desktop toolkit.
- [x] The product shell exposes the primary Version 1 actions: Search, Favorites, Recent, Settings, and Quit.
- [x] The shell opens real application windows or views for primary actions instead of printing stub messages.
- [x] The repository includes open-source metadata, build instructions, and contributor-facing documentation in English.

## Implementation Tasks

- [x] Replace stub tray callbacks in `cmd/beddel-desk/main.go` with application lifecycle wiring.
- [x] Implement a desktop application package that initializes the UI toolkit once.
- [x] Provide menu actions for Search, Favorites, Recent, Settings, and Quit.
- [x] Ensure UI actions run on the correct toolkit thread/event loop.
- [x] Add a safe app startup path that loads configuration and lightweight reference storage.
- [x] Add user-visible error handling for startup failures.
- [x] Set an application title/name consistently across windows.
- [x] Avoid background watchers, background indexing, or remote dependencies.

## Verification Tasks

- [ ] Run `go test ./...`.
- [ ] Run `go build ./cmd/beddel-desk`.
- [ ] Confirm `beddel-desk version` prints the current version.
- [ ] Confirm the desktop shell opens locally.
