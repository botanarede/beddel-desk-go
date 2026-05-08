# Story 8: Session Chunker for Kiro, Gemini, and Claude Code Formats

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.3
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md).

## Goal

Parse local agent session files into an ordered list of small, deterministically
identified chunks that the indexer can embed and store. The chunker must tolerate
malformed files with a returned warning rather than a panic or a silent skip.

## Package Layout

```
internal/indexer/
  chunker.go
  chunker_test.go
  testdata/
    kiro_session.json
    gemini_session.jsonl
    claude_session.json
    malformed.json
```

## Public API

```go
package indexer

type Chunk struct {
    SessionPath string
    ChunkIndex  int
    Role        string    // "user" | "assistant" | "system" | ""
    Content     string    // final text fed to the embedder (<= ~400 tokens worth)
    Timestamp   time.Time // message timestamp when present, else file mtime
}

type ChunkWarning struct {
    SessionPath string
    Reason      string
}

// ChunkSession parses a single session file and returns its chunks in order.
// A non-nil warning does not necessarily imply an error; malformed files return
// (nil, warning, nil) so the caller can keep indexing the rest of the backend.
func ChunkSession(path string) (chunks []Chunk, warning *ChunkWarning, err error)
```

## Acceptance Criteria

- [ ] `ChunkSession` auto-detects the format from the file extension and a light peek
      at the first few bytes. Order of preference: `.jsonl` -> Gemini, `.json` ->
      try Kiro schema, fall back to Claude Code schema.
- [ ] Kiro schema: top-level object with `messages: [{ role, content, timestamp? }, ...]`
      or top-level array of the same shape.
- [ ] Gemini schema: one JSON object per line, each with `{ role, parts: [{ text }] }`
      or `{ role, text }`. Blank lines are skipped.
- [ ] Claude Code schema: top-level object with `conversation: [{ type, text, ... }]`
      or `messages` with `type` instead of `role`.
- [ ] Roles are normalized to lowercase `"user"`, `"assistant"`, `"system"`. Unknown
      roles map to `""`.
- [ ] Each message becomes **at least one** chunk. Messages longer than approximately
      400 estimated tokens are split at word boundaries; very long individual words
      are split at the 400-token boundary to guarantee termination.
- [ ] `ChunkIndex` is dense (0, 1, 2, ...) within the file and deterministic across
      re-runs.
- [ ] Files the detector does not recognize return `(nil, &ChunkWarning{Reason:
      "unknown format"}, nil)`.
- [ ] JSON parse errors return `(nil, &ChunkWarning{Reason: "malformed: ..."}, nil)`,
      never `err`. Actual I/O errors (`os.Open`) do return `err`.
- [ ] Timestamp extraction prefers message-level timestamps in RFC 3339 or Unix epoch;
      falls back to `os.Stat(path).ModTime()`.

## Implementation Tasks

- [ ] Implement `ChunkSession` with three unexported helpers:
      `parseKiro`, `parseGemini`, `parseClaude`. Each returns
      `([]rawMessage, error)` so the chunking pass is shared.
- [ ] Implement the splitter: count whitespace-separated tokens as a cheap proxy for
      model tokens. The tokenizer from Story 7 does the final truncation when
      embedding, so this pass exists only to bound chunk count.
- [ ] Add committed fixtures under `testdata/`. Keep them tiny (handful of messages)
      and include one malformed file with a trailing comma and one file with no
      recognizable schema.

## Verification Tasks

- [ ] `chunker_test.go` covers each format with its fixture and asserts:
      - chunk count matches expected
      - roles are normalized
      - chunk indices are dense
      - timestamps fall back to mtime when absent
- [ ] A test with a synthetic 3000-word message asserts the splitter emits multiple
      chunks, all within the estimated budget, and that the concatenation with
      whitespace restores the original text.
- [ ] A malformed fixture test asserts `(nil, warning, nil)`.
- [ ] A non-existent path test asserts `err != nil` and `chunks == nil`.
- [ ] Ensure no test depends on network or on the embedder (this story is pure CPU).

## Out of Scope

- embedding chunks (Stories 7, 10)
- writing to disk (Stories 9, 10)
- incremental change detection (Story 10)

## Constraints

- All code and comments in English.
- Fixture files are kept under 4 KB each to avoid inflating the repo.
- No use of `panic`. Unknown fields are ignored, not rejected.
