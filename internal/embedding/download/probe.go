package download

import (
	"os"
	"path/filepath"
)

// probeSystemRuntime locates an already-installed ONNX runtime shared
// library on the host. It returns an absolute path or the empty string
// if no candidate was found. The error channel is reserved for I/O
// problems unexpected at probe time; typical "not found" results use
// ("", nil) to keep the caller's happy path simple.
//
// The actual per-OS search strategy lives in probe_<goos>.go. This
// wrapper exists so tests that are OS-agnostic (manifest checks,
// manager flow) can compile on any platform.
func probeSystemRuntime() (string, error) {
	return systemRuntimeCandidates()
}

// firstExistingFile returns the first element of paths that exists and
// is a regular file. It silently skips entries that fail to stat.
func firstExistingFile(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		return abs
	}
	return ""
}
