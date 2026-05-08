package download

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFirstExistingFileSkipsMissingAndDirectories asserts the helper
// returns the first regular-file path and skips directories and
// non-existent entries.
func TestFirstExistingFileSkipsMissingAndDirectories(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "somedir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "lib.fake")
	if err := os.WriteFile(file, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := firstExistingFile([]string{
		"",
		filepath.Join(tmp, "does-not-exist"),
		dir,
		file,
	})
	if got == "" {
		t.Fatal("expected firstExistingFile to find the stub file")
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != abs {
		t.Fatalf("firstExistingFile returned %q, want %q", got, abs)
	}
}

// TestFirstExistingFileEmptyInput confirms empty input returns the empty
// string and does not panic.
func TestFirstExistingFileEmptyInput(t *testing.T) {
	if got := firstExistingFile(nil); got != "" {
		t.Fatalf("expected empty result, got %q", got)
	}
}

// TestProbeSystemRuntimeWithFakeLibrary plants a fake shared library in
// a temporary directory and verifies the platform probe finds it via
// the well-known environment variable (LD_LIBRARY_PATH on linux,
// DYLD_LIBRARY_PATH on darwin, PATH on windows).
func TestProbeSystemRuntimeWithFakeLibrary(t *testing.T) {
	tmp := t.TempDir()
	libName := runtimeLibFileName(runtime.GOOS)
	if libName == "libonnxruntime" {
		t.Skipf("no platform-specific library name for GOOS=%s", runtime.GOOS)
	}
	path := filepath.Join(tmp, libName)
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	switch runtime.GOOS {
	case "linux":
		t.Setenv("LD_LIBRARY_PATH", tmp)
	case "darwin":
		t.Setenv("DYLD_LIBRARY_PATH", tmp)
	case "windows":
		t.Setenv("PATH", tmp)
	default:
		t.Skipf("unsupported test GOOS=%s", runtime.GOOS)
	}

	got, err := probeSystemRuntime()
	if err != nil {
		t.Fatalf("probeSystemRuntime returned error: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != abs {
		t.Fatalf("probeSystemRuntime = %q, want %q", got, abs)
	}
}
