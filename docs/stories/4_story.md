# Story 4: Results, Favorites, and Recent References

## Goal

Let users act on search results, open original local session sources, save useful references as favorites, and revisit recent references.

## Acceptance Criteria

- [x] Search results can be opened from the UI.
- [x] A result can be added to favorites.
- [x] Recently accessed session references are tracked locally.
- [x] Favorites and recent references remain lightweight and do not store processed session content.
- [x] The UI clearly distinguishes favorites and recent items.

## Implementation Tasks

- [x] Implement cross-platform file opening for local session source paths.
- [x] Record a recent reference when a result, favorite, or recent item is opened.
- [x] Add a favorite action on search results.
- [x] Ensure favorite labels are based on lightweight metadata such as file name or user-entered label only.
- [x] Provide Favorites UI with open and remove actions.
- [x] Provide Recent UI with open and clear/remove actions.
- [x] Show backend name, file path, file modification time, and line number in results.
- [x] Keep matched content in memory only for the visible active search result list.
- [x] Do not write result snippets or matched lines to storage.
- [x] Handle deleted or moved files with a user-visible message.

## Verification Tasks

- [ ] Add tests for recent tracking on open.
- [ ] Add tests for favorite creation from a result.
- [x] Add tests for removing favorites.
- [ ] Add tests for opening missing files failing gracefully.
- [ ] Manually verify opening files on supported platforms where available.
