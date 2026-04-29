# Story 3: On-Demand Local Search Flow

## Goal

Implement the user-triggered local search flow that scans configured session sources only when the user starts a search.

## Acceptance Criteria

- [x] The user can open a search flow from the desktop UI.
- [x] The user can choose a configured backend before searching.
- [x] The user can enter a plain-text query.
- [x] The tool scans local sources only when the user initiates a search.
- [x] Runtime parsing and matching remain in memory only for the active search flow.
- [x] Search results include enough source information to reopen the original session file.
- [x] Missing or unreadable files are handled gracefully and visibly.
- [x] The user can narrow by path or directory.
- [x] The user can narrow by date range when file modification time is available.
- [x] The user can search favorites only.

## Implementation Tasks

- [x] Replace filesystem traversal that ignores errors with traversal that records warnings.
- [x] Support large session lines without silently failing at `bufio.Scanner` defaults.
- [x] Remove JSON tags from in-memory search result fields that contain matched content.
- [x] Do not persist matched lines, snippets, normalized content, indexes, or processed chunks.
- [x] Normalize and validate path filters in a cross-platform way.
- [x] Add date range filters based on local file modification time.
- [x] Add favorites-only filtering using stored lightweight favorite paths.
- [x] Sort results predictably by modification time and then path.
- [x] Cap results after sorting or maintain best results without filesystem-order bias.
- [x] Return search warnings for skipped missing, unreadable, oversized, or binary files.
- [x] Add UI controls for backend selection, query, path filter, date filters, and favorites-only mode.
- [x] Add UI feedback for empty query, missing backend, no results, and partial search warnings.

## Verification Tasks

- [x] Add tests for plain-text matching.
- [x] Add tests for unreadable or missing paths.
- [x] Add tests for long-line session files.
- [x] Add tests for path filtering.
- [x] Add tests for date filtering.
- [x] Add tests for favorites-only filtering.
- [x] Add tests confirming processed search content is not written to disk.
