# Story 7: Tokenizer and ONNX Embedding Runtime Wrapper

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.2
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md).

## Goal

Turn a raw string into a 384-dimensional, L2-normalized `[]float32` by wiring
`sugarme/tokenizer` to `yalue/onnxruntime_go` behind a small interface that the rest of
the codebase can mock.

Depends on **Story 6** for resolved paths to `libonnxruntime.*`, `model.onnx`, and
`tokenizer.json`.

## Package Layout

```
internal/embedding/
  tokenizer.go
  tokenizer_test.go
  embedder.go
  embedder_test.go
  doc.go               # package-level rationale + thread-safety notes
```

## Public API

```go
package embedding

type Tokenizer interface {
    Tokenize(text string) (InputIDs, AttentionMask []int64, err error)
    Close() error
}

type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Dim() int            // 384 for all-MiniLM-L6-v2
    Close() error
}

func NewTokenizer(tokenizerJSONPath string) (Tokenizer, error)
func NewEmbedder(runtimeLibPath, modelPath string, tok Tokenizer) (Embedder, error)
```

## Acceptance Criteria

- [ ] `NewTokenizer` loads `tokenizer.json` via `sugarme/tokenizer` and enforces the
      model's pad/mask/truncation policy: max length 256, `[CLS]` / `[SEP]` inserted,
      truncation from the right.
- [ ] `NewEmbedder` calls `onnxruntime_go.SetSharedLibraryPath(runtimeLibPath)`, then
      `InitializeEnvironment()` at most once per process (guard with `sync.Once`).
- [ ] `Embed` returns a slice of length exactly `Dim()` (384). Every returned vector is
      L2-normalized (sum of squares equals `1.0 ± 1e-4`).
- [ ] `EmbedBatch` produces vectors in the same order as the input texts and matches the
      single-text `Embed` output element-wise for any one text.
- [ ] Calling `Close` releases ONNX session resources and makes subsequent `Embed` calls
      return a "closed" error.
- [ ] `Embedder` is safe for concurrent `Embed` / `EmbedBatch` calls. If upstream
      serialization is required, the wrapper does it with an internal mutex.
- [ ] Empty input text is accepted and yields a valid (but semantically meaningless)
      384-dim vector without panicking.

## Implementation Tasks

- [ ] Add dependencies to `go.mod`:
      `github.com/yalue/onnxruntime_go`,
      `github.com/sugarme/tokenizer` (pin to latest `v0.x`).
- [ ] In `tokenizer.go`, wrap the sugarme API and convert the returned `Encoding` into
      the two `[]int64` slices needed by ONNX.
- [ ] In `embedder.go`:
      - Create the ONNX `AdvancedSession` with three `int64` input tensors
        (`input_ids`, `attention_mask`, `token_type_ids`).
      - The model outputs `last_hidden_state` (shape `[batch, seq, 384]`); apply mean
        pooling masked by `attention_mask`, then L2-normalize per row.
      - Cache input/output tensor shapes to avoid reallocation per call.
- [ ] Emit a descriptive error when the ONNX runtime fails to load (mention probed path
      and platform).

## Verification Tasks

- [ ] `tokenizer_test.go` uses a committed tiny `testdata/tokenizer.json` fixture (or
      skips when missing) to assert:
      - known input produces expected token ids
      - max-length truncation is respected
      - unicode surrogate pairs do not panic
- [ ] `embedder_test.go` contains two kinds of tests:
      - Fast tests with a `fakeTokenizer` and a `fakeEmbedder` (interface-level) that
        verify mean pooling, L2 normalization, and batch ordering in isolation.
      - End-to-end tests gated on `BEDDEL_EMBED_E2E=1` that actually load ONNX runtime
        and the real model. These run `testing.Short()` skip under `go test -short`.
- [ ] An ordering-stability test: `Embed("hello")` equals `EmbedBatch([]string{"hello"})[0]`
      within a small epsilon.

## Out of Scope

- download and caching of assets (Story 6)
- chunking (Story 8)
- search integration (Story 11)
- GPU execution providers

## Constraints

- All code and comments in English.
- Do not leak ONNX runtime types out of this package.
- If `yalue/onnxruntime_go` requires a global init, that init happens the first time
  `NewEmbedder` runs, never at package `init()`.
