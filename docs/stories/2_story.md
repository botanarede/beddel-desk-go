# Story 2: Backend Configuration and Local References

## Goal

Allow users to configure local session backends and persist only lightweight references required to find, favorite, and reopen local session sources.

## Acceptance Criteria

- [x] The application stores backend configuration locally.
- [x] The application stores favorites and recent references locally.
- [x] Stored local references do not include processed session content.
- [x] Backend configuration supports local session source paths.
- [x] Favorites and recent references can be listed from the UI.
- [x] The application can reopen a referenced session source using stored path metadata.

## Implementation Tasks

- [x] Use cross-platform user config directories instead of hardcoded Linux-only paths.
- [x] Validate backend names, categories, and source paths before saving.
- [x] Prevent duplicate backend names.
- [x] Persist backend source paths only as local filesystem references.
- [x] Persist favorites without transcript text, previews, snippets, chunks, embeddings, or derived labels.
- [x] Persist recent references without transcript text, previews, snippets, chunks, embeddings, or derived labels.
- [x] Deduplicate favorites and recents by backend plus session path.
- [x] Cap recent references to a small fixed limit.
- [x] Add UI for creating, listing, updating, and removing backend configuration.
- [x] Add UI for listing and removing favorites.
- [x] Add UI for listing recent references.
- [x] Add user-visible error messages for invalid configuration and storage failures.

## Verification Tasks

- [x] Add tests for config path resolution and validation.
- [x] Add tests for backend add/update/remove behavior.
- [x] Add tests proving storage JSON contains only lightweight references.
- [x] Add tests for favorite and recent deduplication.
