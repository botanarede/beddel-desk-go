# 01. System Overview

## Objective

Provide a simple technical frame for implementing the local-only Version 1 scope described
in the PRD.

## Design Principles

- local-first
- no remote dependency
- no background watchers
- no persistent storage of processed session content
- small repository and low contributor overhead

## High-Level Structure

```text
cmd/beddel-desk/
  main.go

internal/
  app/ (desktop UI wiring)
  config/ (backend configuration)
  search/ (on-demand local search)
  storage/ (favorites and recent references)
  version/ (version string)
```

This repository starts with a minimal bootstrap structure. Additional packages should be
introduced only when needed by the scoped Version 1 work.

## Data Boundary

Persistent local state may include:
- backend configuration
- favorites
- recent references
- local file and session identifiers

Persistent local state must not include:
- extracted transcript text
- normalized session content
- derived search indexes containing session text

## Search Boundary

Version 1 search is:
- user-triggered
- local
- temporary in memory

Any parsing or matching result generated from session content should exist only for the
active search flow and then be discarded.

## Build Boundary

The repository must remain buildable with standard Go commands and documented clearly for:
- Linux
- macOS
- Windows

