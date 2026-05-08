# Story 6: Asset Download Manager + System-Library Probe + Checksum Cache

Reference:
[docs/prd/02-epic-v2-semantic-search.md](../prd/02-epic-v2-semantic-search.md) Story V2.1
· [docs/architecture/02-semantic-search.md](../architecture/02-semantic-search.md)
ADR-1, ADR-2.

## Goal

Provide a reliable, local, opt-in asset manager that resolves the ONNX runtime and the
`all-MiniLM-L6-v2` model on first use, reusing a system-installed ONNX runtime when one
is present, and that validates every downloaded asset against a SHA-256 manifest checked
into the repository.

## Package Layout

```
internal/embedding/download/
  manifest.go        # types + committed manifest data
  manifest_test.go
  manager.go         # public API
  manager_test.go
  probe.go           # system-library probe (per-GOOS build tags)
  probe_test.go
```

## Public API

```go
package download

type Assets struct {
    RuntimeLibraryPath string // resolved absolute path to libonnxruntime.{so,dylib,dll}
    RuntimeSource      Source // SourceSystem or SourceDownloaded
    ModelPath          string // absolute path to model.onnx
    TokenizerPath      string // absolute path to tokenizer.json
}

type Source string
const (
    SourceSystem     Source = "system"
    SourceDownloaded Source = "downloaded"
)

type Progress struct {
    Stage       string // "probing" | "downloading" | "verifying" | "ready"
    Current     int64  // bytes downloaded so far for the current asset
    Total       int64  // bytes expected for the current asset (0 if unknown)
    AssetName   string // "onnxruntime" | "model" | "tokenizer"
}

type Manager struct { /* unexported */ }

func NewManager(cacheRoot string, client *http.Client) *Manager
func (m *Manager) EnsureAssets(ctx context.Context, report func(Progress)) (*Assets, error)
func (m *Manager) ClearCache() error
```

## Acceptance Criteria

- [ ] `manifest.go` pins one entry per `(GOOS, GOARCH)` for the ONNX runtime and one
      global entry for the model and for the tokenizer. Each entry carries URL, SHA-256,
      expected byte size, and version string.
- [ ] `EnsureAssets` returns immediately (no network, no disk write) when the cache
      already contains valid assets.
- [ ] `EnsureAssets` probes known system locations for `libonnxruntime.*` before
      downloading. On hit, `RuntimeSource == SourceSystem`.
- [ ] `EnsureAssets` downloads missing assets to a temp file, verifies SHA-256, and
      atomically renames into place. Failed checksum removes the temp file and returns an
      error that names the asset.
- [ ] `EnsureAssets` honors `context.Context` cancellation between HTTP reads.
- [ ] `EnsureAssets` accepts a nil `report` callback.
- [ ] The cache layout is stable and documented:
      `<cacheRoot>/onnxruntime/<version>/<goos>-<goarch>/libonnxruntime.<ext>`,
      `<cacheRoot>/models/all-MiniLM-L6-v2-<version>/{model.onnx,tokenizer.json}`.
- [ ] `ClearCache` deletes the entire `<cacheRoot>` subtree and returns nil on success.
- [ ] All network calls go through the injected `*http.Client` so tests can use
      `httptest.NewServer`.

## Implementation Tasks

- [ ] Define `Entry`, `Manifest`, `assetKind` in `manifest.go`. Keep the manifest in a
      Go literal, not a JSON file, so compile-time checks catch typos.
- [ ] Select the correct ONNX runtime entry using `runtime.GOOS` and `runtime.GOARCH`.
      Return a typed error when the platform is unsupported so the caller can render
      a visible "semantic search not supported on this platform" message.
- [ ] Implement the downloader: `GET` with `Accept-Encoding: identity`, stream through
      `sha256.New()`, write to `*.partial`, fsync, rename.
- [ ] Implement `probe.go` with build-tagged helpers:
      `probe_linux.go`, `probe_darwin.go`, `probe_windows.go`. Each returns a candidate
      path or `"", nil`. Check common paths (`/usr/lib`, `/usr/local/lib`,
      `$(brew --prefix)/lib`, `%ProgramFiles%`). A found library is not yet loaded;
      compatibility is asserted later by the embedder.
- [ ] Expose progress via the callback with coarse bytes-per-asset. The download code
      must debounce to at most one callback per 200 ms to avoid UI jitter (ADR
      constraint called out in architecture doc).

## Verification Tasks

- [ ] `manifest_test.go` asserts every entry has a non-empty URL, a 64-char hex SHA-256,
      a positive size, and that the runtime map covers at least the three release
      platforms (linux/amd64, darwin/arm64, windows/amd64).
- [ ] `manager_test.go` uses `httptest.NewServer` and covers:
      - fresh download path, valid checksum
      - fresh download path, bad checksum (must error, partial removed)
      - cached path (second call skips network)
      - context cancellation mid-download
      - missing-platform manifest entry returns typed error
- [ ] `probe_test.go` uses `t.Setenv` to point at a temporary directory with a fake
      shared library file, and asserts the probe returns that path. OS-specific probe
      files are covered with their respective build tags; CI uses only the Linux variant.
- [ ] No test performs a real network call.

## Out of Scope

- actually loading the runtime (Story 7)
- using the model for anything (Story 7, Story 10)
- negotiating HTTP proxies (use default Go transport)
- LFS, GitHub release API pagination, or checksum auto-refresh

## Constraints

- All identifiers, comments, and tests are in English.
- The manifest ships with placeholder SHA-256 values flagged `// TODO: fill at release
  time` with a referenced issue or the V2 PRD section. The code must fail loudly when a
  placeholder is used at runtime (detect the magic value and return a typed error).
