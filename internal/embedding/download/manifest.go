// Package download resolves the ONNX runtime shared library and the
// all-MiniLM-L6-v2 model files required by the semantic search feature.
// It reuses a system-installed runtime when one is present, downloads the
// missing assets otherwise, validates them against a committed SHA-256
// manifest, and caches them under a user-provided cache root.
package download

import (
	"errors"
	"fmt"
)

// PlaceholderSHA256 is the sentinel written in the committed manifest for
// every asset whose real checksum has not been populated yet. The code
// detects this value at runtime and surfaces ErrPlaceholderChecksum
// instead of issuing a download that would inevitably fail the integrity
// check.
const PlaceholderSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

// Sentinel errors exported by the package. Wrappers enrich these with the
// offending asset name or platform.
var (
	// ErrPlaceholderChecksum is returned when the manifest still carries
	// its placeholder sentinel for an asset that needs to be downloaded.
	ErrPlaceholderChecksum = errors.New("download: placeholder checksum in manifest, fill at release time")

	// ErrUnsupportedPlatform is returned when the active GOOS/GOARCH pair
	// has no matching ONNX runtime entry in the manifest.
	ErrUnsupportedPlatform = errors.New("download: no ONNX runtime entry for this platform")

	// ErrChecksumMismatch is returned when a downloaded asset's SHA-256
	// disagrees with the manifest.
	ErrChecksumMismatch = errors.New("download: checksum mismatch")
)

// Source identifies where a resolved ONNX runtime came from.
type Source string

const (
	// SourceSystem means the runtime library was found already installed
	// on the host and no download occurred.
	SourceSystem Source = "system"

	// SourceDownloaded means the runtime library was downloaded from the
	// manifest URL and cached under the configured cache root.
	SourceDownloaded Source = "downloaded"
)

// assetKind enumerates the named assets tracked by the manifest.
type assetKind string

const (
	assetRuntime   assetKind = "onnxruntime"
	assetModel     assetKind = "model"
	assetTokenizer assetKind = "tokenizer"
)

// Progress stage strings emitted on the progress callback.
const (
	stageProbing     = "probing"
	stageDownloading = "downloading"
	stageVerifying   = "verifying"
	stageReady       = "ready"
)

// Entry is a single downloadable artifact pinned in the manifest.
type Entry struct {
	URL     string
	SHA256  string
	Size    int64
	Version string
}

// PlatformKey identifies an ONNX runtime release variant.
type PlatformKey struct {
	GOOS   string
	GOARCH string
}

// Manifest pins the set of artifacts the manager knows how to download.
// The runtime matrix contains one entry per supported (GOOS, GOARCH)
// pair; the model and tokenizer are global because the same ONNX file is
// used on every platform.
type Manifest struct {
	Runtimes  map[PlatformKey]Entry
	Model     Entry
	Tokenizer Entry
}

// Progress is delivered to the optional progress callback passed to
// EnsureAssets. Zero-value fields are valid: Total is 0 when the server
// did not advertise a Content-Length.
type Progress struct {
	Stage     string
	Current   int64
	Total     int64
	AssetName string
}

// DefaultManifest is the committed manifest used by production code.
// The SHA-256 values and sizes are intentional placeholders until
// release cut; see docs/prd/02-epic-v2-semantic-search.md section V2.1.
//
// The URLs point at the pinned ONNX runtime release tag on GitHub and at
// the canonical all-MiniLM-L6-v2 model hosted on Hugging Face. They are
// reviewed as part of the manifest and will not change silently.
var DefaultManifest = Manifest{
	Runtimes: map[PlatformKey]Entry{
		{GOOS: "linux", GOARCH: "amd64"}: {
			URL:     "https://github.com/microsoft/onnxruntime/releases/download/v1.19.2/onnxruntime-linux-x64-1.19.2.tgz",
			SHA256:  PlaceholderSHA256, // TODO: fill at release time
			Size:    37748736,          // TODO: fill at release time (~36 MB placeholder)
			Version: "1.19.2",
		},
		{GOOS: "darwin", GOARCH: "arm64"}: {
			URL:     "https://github.com/microsoft/onnxruntime/releases/download/v1.19.2/onnxruntime-osx-arm64-1.19.2.tgz",
			SHA256:  PlaceholderSHA256, // TODO: fill at release time
			Size:    18874368,          // TODO: fill at release time (~18 MB placeholder)
			Version: "1.19.2",
		},
		{GOOS: "windows", GOARCH: "amd64"}: {
			URL:     "https://github.com/microsoft/onnxruntime/releases/download/v1.19.2/onnxruntime-win-x64-1.19.2.zip",
			SHA256:  PlaceholderSHA256, // TODO: fill at release time
			Size:    12582912,          // TODO: fill at release time (~12 MB placeholder)
			Version: "1.19.2",
		},
	},
	Model: Entry{
		URL:     "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx",
		SHA256:  PlaceholderSHA256, // TODO: fill at release time
		Size:    94371840,          // TODO: fill at release time (~90 MB placeholder)
		Version: "v1",
	},
	Tokenizer: Entry{
		URL:     "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer.json",
		SHA256:  PlaceholderSHA256, // TODO: fill at release time
		Size:    716800,            // TODO: fill at release time (~700 KB placeholder)
		Version: "v1",
	},
}

// runtimeEntryFor resolves the ONNX runtime entry for a (goos, goarch)
// pair. It returns ErrUnsupportedPlatform wrapped with the pair when no
// entry matches so the caller can render a user-visible message.
func runtimeEntryFor(m Manifest, goos, goarch string) (Entry, error) {
	entry, ok := m.Runtimes[PlatformKey{GOOS: goos, GOARCH: goarch}]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, goos, goarch)
	}
	return entry, nil
}

// runtimeLibFileName returns the conventional shared-library file name
// for the given GOOS. Versioning is represented in the parent cache
// directory, not in the file name.
func runtimeLibFileName(goos string) string {
	switch goos {
	case "linux":
		return "libonnxruntime.so"
	case "darwin":
		return "libonnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "libonnxruntime"
	}
}
