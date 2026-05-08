// Package app: index_view_helpers.go holds tag-free helpers used by
// the Index Manager. These helpers do not import indexer or embedding
// so they live outside any build-tag boundary and can be covered by
// index_view_test.go under both `go test ./...` and
// `go test -tags sqlite_fts5 ./...`.
//
// Helpers that take tagged types (for example indexer.BackendStats)
// are defined in index_view_indexed.go instead; see that file for
// formatBackendStatus and formatIndexProgress.
package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/botanarede/beddel-desk-go/internal/embedding/download"
)

// disclosureMessage builds the body text shown in the first-run
// disclosure modal. The sizes and versions are pulled from the
// download manifest so the wording cannot drift from what the
// download manager actually fetches.
//
// The message is intentionally conservative: it names each asset,
// its pinned version, and its size rounded to the nearest megabyte.
// We do not include the full URLs so the modal remains scannable;
// the URLs live in the manifest file for anyone who wants to audit.
func disclosureMessage(m download.Manifest) string {
	var runtimeSize int64
	var runtimeVersion string
	for _, entry := range m.Runtimes {
		// Sizes are the same order of magnitude across platforms
		// (plain CPU build of ONNX Runtime); pick any entry so the
		// message is deterministic without hard-coding the current
		// host's GOOS/GOARCH.
		if entry.Version != "" {
			runtimeVersion = entry.Version
			runtimeSize = entry.Size
			break
		}
	}
	return fmt.Sprintf(
		"The first indexing run downloads the ONNX runtime and the "+
			"all-MiniLM-L6-v2 embedding model so searches can run "+
			"locally.\n\n"+
			"  - ONNX Runtime %s (~%s) — reused from the system when a "+
			"compatible copy is already installed.\n"+
			"  - all-MiniLM-L6-v2 model %s (~%s).\n"+
			"  - all-MiniLM-L6-v2 tokenizer %s (~%s).\n\n"+
			"Assets are verified against a SHA-256 manifest committed "+
			"to the repository and cached locally. Nothing is sent to "+
			"any third party.",
		runtimeVersion, humanBytes(runtimeSize),
		m.Model.Version, humanBytes(m.Model.Size),
		m.Tokenizer.Version, humanBytes(m.Tokenizer.Size),
	)
}

// formatDownloadProgress renders a download.Progress value into the
// single-line status string shown in the Index Manager row while the
// download is in flight. Non-downloading stages still produce a
// sensible message so the UI never goes blank.
func formatDownloadProgress(p download.Progress) string {
	switch p.Stage {
	case "probing":
		return "Checking system for ONNX runtime..."
	case "downloading":
		if p.Total > 0 {
			return fmt.Sprintf(
				"Downloading %s: %s / %s",
				p.AssetName,
				humanBytes(p.Current),
				humanBytes(p.Total),
			)
		}
		return fmt.Sprintf(
			"Downloading %s: %s",
			p.AssetName,
			humanBytes(p.Current),
		)
	case "verifying":
		return fmt.Sprintf("Verifying %s...", p.AssetName)
	case "ready":
		return "Runtime ready."
	default:
		if p.AssetName != "" {
			return fmt.Sprintf("%s: %s", p.Stage, p.AssetName)
		}
		return p.Stage
	}
}

// humanBytes renders a byte count at one decimal place using binary
// prefixes (Ki, Mi, Gi). A zero or negative count returns "0 B" so
// the UI never shows "-1 B" or similar oddities.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	const prefixes = "KMGTPE"
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), prefixes[exp])
}

// userCacheDir returns the OS-preferred cache directory. It is a
// thin wrapper around os.UserCacheDir that also creates the parent
// directory so the download manager can write into it immediately.
// Extracted into a helper so the Index Manager's semantic cache root
// logic stays short and the error wrapping is consistent.
func userCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %q: %w", base, err)
	}
	return filepath.Clean(base), nil
}
