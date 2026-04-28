# AGENTS.md — Agent Guidelines for `beddel-desk-go`

This repository follows a lean BMAD-style documentation layout.

## Mission

Implement `Beddel Desk` as a small, local-first desktop utility for searching local agent
session history on demand.

## Product Boundaries

Version 1 is intentionally narrow:
- local-only
- no remote service dependency
- no session watchers
- no persistent storage of normalized or processed session content
- only lightweight local references may be stored, such as favorites, file names,
  backend categories, and recent selections

## Documentation Layout

```text
docs/prd/            Product requirements
docs/architecture/   Technical architecture
```

## BMAD Guidance

Use the repository docs in this order:
1. `docs/prd/index.md`
2. `docs/prd/01-epic-v1-local-search.md`
3. `docs/architecture/index.md`
4. `docs/architecture/01-system-overview.md`

Keep implementation aligned to the Version 1 scope documented there.

## Coding Guidance

- Language: Go
- Keep the repository small and readable.
- Prefer standard library solutions unless a dependency is clearly justified.
- Do not introduce any storage of processed session content.
- Do not add speculative future-facing features to user-facing docs.

