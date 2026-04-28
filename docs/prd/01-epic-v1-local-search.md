# 01. Epic V1: Local Session Search

> **Status:** Draft
>
> **Target:** `Beddel Desk`

## Goal

Deliver a small desktop utility that lets a user search local agent session history on
demand from a tray-oriented interface.

The tool must stay intentionally narrow:
- no remote backend
- no background file watchers
- no persistent storage of processed session content
- no speculative features outside the local search workflow

## Product Summary

Version 1 provides a local search experience for agent sessions already present on the
user's machine. The user opens the tray menu, chooses a backend, enters a query, and
runs a search against that backend's local session files.

The application may store small local references that help the user return to known
sessions quickly, such as favorites, backend categories, file names, session identifiers,
and recent selections. It must not persist normalized transcripts, extracted message
content, or any other processed session output.

## Users

- Developers who use local coding-agent tools and need to find previous sessions quickly.
- Operators who want a lightweight desktop tool instead of manual shell scripts.
- Technical users who prefer local processing and minimal retention.

## Scope

| Story | Description | SP |
|------|-------------|----|
| V1.1 | Project bootstrap and desktop shell | 3 |
| V1.2 | Backend configuration and local references | 3 |
| V1.3 | On-demand local search flow | 5 |
| V1.4 | Results, favorites, and recent references | 3 |
| V1.5 | Packaging, docs, and usability polish | 2 |
| Total | Version 1 | 16 |

## Non-Goals

The following are explicitly out of scope for Version 1:
- remote services
- synchronized preferences across devices
- background watchers
- indexing daemons
- persistent full-text search indexes built from session content
- embeddings
- semantic search
- transcript summarization
- automated tagging based on processed content
- team collaboration features

## Key Product Rules

1. Search is initiated by the user.
2. Search runs only against local data already present on the machine.
3. Processed session content is kept in memory only for the lifetime of the search flow.
4. Only lightweight local references may be stored on disk.
5. The product must remain understandable to an open-source contributor without relying
   on hidden services or undocumented infrastructure.

## Functional Requirements

1. The application starts as a desktop utility with a tray entry point.
2. The tray exposes a way to open search, inspect favorites, inspect recent references,
   open settings, and quit.
3. The user can configure at least one local backend source.
4. Each backend stores only source configuration and lightweight references needed to
   locate sessions later.
5. The user can choose a backend before executing a search.
6. The user can enter a plain-text query and run a search on demand.
7. The search implementation may use file-name prefiltering, directory prefiltering,
   timestamp checks, and other runtime optimizations that do not persist processed content.
8. Search results must identify enough information for the user to reopen the original
   local session source.
9. The user can mark a result or session as a favorite.
10. The user can reopen a recently accessed session reference.

## Local Storage Rules

Allowed on disk:
- backend names
- backend categories
- session file paths
- session identifiers when present
- file names
- favorite references
- recent references
- timestamps such as last opened or last scanned
- small source metadata such as file size or modification time

Not allowed on disk:
- extracted message text
- normalized transcript content
- cached previews derived from transcript content
- processed chunks
- embeddings
- semantic labels generated from transcript content
- full-text indexes containing session text

## User Experience

### Tray Menu

The tray menu must include:
- `Search`
- `Favorites`
- `Recent`
- `Settings`
- `Quit`

### Search Flow

1. The user opens the tray menu.
2. The user selects `Search`.
3. The user chooses a backend.
4. The user enters a query.
5. The user optionally narrows the search with simple filters.
6. The tool scans local session sources for that backend on demand.
7. The tool shows the results.
8. The user can open the original session source or mark it as a favorite.

### Filters

Version 1 filters should remain simple:
- backend
- path or directory
- date range if available from the source
- favorites only

## Stories

## Story V1.1: Project Bootstrap and Desktop Shell

### User Story

As a developer, I want a small desktop application shell with a tray entry point, so that
I can start the tool and access its main actions without using the terminal.

### Acceptance Criteria

1. The repository builds as a Go project with a documented entry point.
2. The application starts successfully on supported desktop targets during development.
3. The tray entry point is present in the product shell design.
4. The shell exposes the primary Version 1 menu actions: search, favorites, recent,
   settings, and quit.
5. The repository includes open-source metadata, build instructions, and contributor-facing
   documentation in English.

### Story Points

`3 SP`

### Out of Scope

- advanced UI styling
- accessibility refinements beyond basic platform support

## Story V1.2: Backend Configuration and Local References

### User Story

As a user, I want to configure local backends and keep lightweight local references, so
that I can search known session sources without manually locating them every time.

### Acceptance Criteria

1. The application stores backend configuration locally.
2. The application stores favorites and recent references locally.
3. Stored local references do not include processed session content.
4. Backend configuration supports local session source paths.
5. Favorites and recent references can be listed from the UI.
6. The application can reopen a referenced session source using the stored path or
   identifier metadata.

### Story Points

`3 SP`

### Out of Scope

- backend-specific deep integration beyond local source configuration
- cross-device sync

## Story V1.3: On-Demand Local Search Flow

### User Story

As a user, I want to choose a backend and run an on-demand local search, so that I can
find a previous session without persistent indexing or background processing.

### Acceptance Criteria

1. The user can open a search flow from the tray UI.
2. The user can choose a configured backend before searching.
3. The user can enter a plain-text query.
4. The tool scans local sources only when the user initiates a search.
5. The tool may use temporary in-memory parsing and runtime optimizations, but it must
   not persist processed session content.
6. The tool returns results with enough source information to reopen the original session.
7. The tool handles missing or unreadable files gracefully.
8. The tool supports simple narrowing by path or directory when available.

### Story Points

`5 SP`

### Out of Scope

- semantic search
- ranking models
- background indexing

## Story V1.4: Results, Favorites, and Recent References

### User Story

As a user, I want to work from the search results and keep a short list of useful session
references, so that I can revisit important sessions quickly.

### Acceptance Criteria

1. Search results can be opened from the UI.
2. A result can be added to favorites.
3. Recently accessed session references are tracked locally.
4. Favorites and recent references remain lightweight and do not store processed session
   content.
5. The UI clearly distinguishes favorites and recent items.

### Story Points

`3 SP`

### Out of Scope

- collaborative lists
- shared team bookmarks

## Story V1.5: Packaging, Docs, and Usability Polish

### User Story

As a contributor, I want clear documentation and a simple local build path, so that I can
clone the repository, build the binary, and start contributing without hidden context.

### Acceptance Criteria

1. The README documents cloning and local binary generation for Linux, macOS, and Windows.
2. The repository includes a license.
3. The repository includes BMAD-style product documentation in English.
4. The repository includes a clear feedback contact line for criticism and suggestions.
5. The documented scope matches the actual Version 1 product boundaries.

### Story Points

`2 SP`

## Dependencies

- Go toolchain
- local filesystem access to session sources selected by the user
- desktop runtime support for the chosen UI implementation

## Risks

- some session source formats may vary significantly across local tools
- parsing large local sources on demand may require careful runtime optimization
- desktop packaging details differ across Linux, macOS, and Windows

## Success Criteria

Version 1 is successful when:
- a user can configure a local backend source
- a user can run an on-demand search from the tray flow
- a user can reopen relevant local session sources
- the tool does not retain processed session content on disk

## Change Log

| Date | Version | Notes |
|------|---------|-------|
| 2026-04-28 | 0.1 | Initial complete Version 1 PRD for the local-only tray search tool |

