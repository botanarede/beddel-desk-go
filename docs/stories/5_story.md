# Story 5: Packaging, Docs, and Usability Polish

## Goal

Make the project buildable and understandable for contributors across Linux, macOS, and Windows while keeping documented scope aligned with Version 1.

## Acceptance Criteria

- [x] The README documents cloning and local binary generation for Linux, macOS, and Windows.
- [x] The repository includes a license.
- [x] The repository includes BMAD-style product documentation in English.
- [x] The repository includes a clear feedback contact line for criticism and suggestions.
- [x] The documented scope matches the actual Version 1 product boundaries.
- [x] There are no stub callbacks, task-marker-only implementations, or speculative future-facing user docs.

## Implementation Tasks

- [x] Declare all required Go dependencies in `go.mod`.
- [ ] Commit or generate `go.sum` when dependencies are available.
- [x] Update README build instructions for the chosen desktop toolkit prerequisites.
- [x] Document local storage paths and the no-processed-content guarantee.
- [x] Document the current UI flows: Search, Favorites, Recent, Settings, Quit.
- [x] Document cross-platform build commands.
- [x] Keep docs limited to Version 1 local search behavior.
- [x] Add tests for core non-UI behavior.
- [x] Avoid hidden services, network requirements, watchers, daemons, and persistent indexes.

## Verification Tasks

- [ ] Run `go test ./...`.
- [ ] Run `go build ./cmd/beddel-desk`.
- [x] Check for unfinished task markers and old click-string callbacks.
- [x] Review persisted JSON schemas for product-boundary compliance.
