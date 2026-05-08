package download

import (
	"errors"
	"regexp"
	"testing"
)

var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestDefaultManifestCoversReleasePlatforms asserts the runtime map at
// minimum covers the three supported release platforms.
func TestDefaultManifestCoversReleasePlatforms(t *testing.T) {
	required := []PlatformKey{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
	}
	for _, key := range required {
		if _, ok := DefaultManifest.Runtimes[key]; !ok {
			t.Errorf("missing manifest entry for %s/%s", key.GOOS, key.GOARCH)
		}
	}
}

// TestDefaultManifestEntriesWellFormed verifies that every committed
// entry has a non-empty URL, a well-formed 64-char hex SHA-256 value
// (the placeholder sentinel counts as well-formed), a positive size, and
// a non-empty version string.
func TestDefaultManifestEntriesWellFormed(t *testing.T) {
	check := func(name string, e Entry) {
		t.Helper()
		if e.URL == "" {
			t.Errorf("%s: URL must be non-empty", name)
		}
		if !sha256HexRe.MatchString(e.SHA256) {
			t.Errorf("%s: SHA256 must be 64-char hex, got %q", name, e.SHA256)
		}
		if e.Size <= 0 {
			t.Errorf("%s: Size must be positive, got %d", name, e.Size)
		}
		if e.Version == "" {
			t.Errorf("%s: Version must be non-empty", name)
		}
	}
	for key, entry := range DefaultManifest.Runtimes {
		check("runtime "+key.GOOS+"/"+key.GOARCH, entry)
	}
	check("model", DefaultManifest.Model)
	check("tokenizer", DefaultManifest.Tokenizer)
}

// TestRuntimeEntryForUnsupportedPlatform confirms that unknown GOOS/
// GOARCH pairs return a wrapped ErrUnsupportedPlatform so the caller can
// render a dedicated "semantic search not supported on this platform"
// message.
func TestRuntimeEntryForUnsupportedPlatform(t *testing.T) {
	_, err := runtimeEntryFor(DefaultManifest, "plan9", "mips64")
	if err == nil {
		t.Fatal("expected error for unsupported platform, got nil")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("expected ErrUnsupportedPlatform, got %v", err)
	}
}

// TestRuntimeEntryForKnownPlatform confirms the lookup returns the
// pinned entry unchanged for a supported pair.
func TestRuntimeEntryForKnownPlatform(t *testing.T) {
	entry, err := runtimeEntryFor(DefaultManifest, "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Version != "1.19.2" {
		t.Fatalf("unexpected version: %q", entry.Version)
	}
	if entry.URL == "" {
		t.Fatal("expected a URL for the linux/amd64 entry")
	}
}

// TestPlaceholderSentinelShape locks the sentinel format so downstream
// code keeps matching it.
func TestPlaceholderSentinelShape(t *testing.T) {
	if len(PlaceholderSHA256) != 64 {
		t.Fatalf("PlaceholderSHA256 must be 64 hex chars, got %d", len(PlaceholderSHA256))
	}
	if !sha256HexRe.MatchString(PlaceholderSHA256) {
		t.Fatalf("PlaceholderSHA256 must be hex, got %q", PlaceholderSHA256)
	}
}

// TestRuntimeLibFileName covers the per-GOOS file name mapping.
func TestRuntimeLibFileName(t *testing.T) {
	cases := map[string]string{
		"linux":   "libonnxruntime.so",
		"darwin":  "libonnxruntime.dylib",
		"windows": "onnxruntime.dll",
		"plan9":   "libonnxruntime",
	}
	for goos, want := range cases {
		if got := runtimeLibFileName(goos); got != want {
			t.Errorf("runtimeLibFileName(%q) = %q, want %q", goos, got, want)
		}
	}
}
