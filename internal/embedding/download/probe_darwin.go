//go:build darwin

package download

import (
	"os"
	"path/filepath"
	"strings"
)

// systemRuntimeCandidates searches the locations where Homebrew and
// Apple developer tools typically install a shared library. The lookup
// order is:
//  1. DYLD_LIBRARY_PATH entries (tests and advanced users can override),
//  2. HOMEBREW_PREFIX/lib or /opt/homebrew/lib on Apple Silicon,
//  3. /usr/local/lib for Intel Homebrew,
//  4. /usr/lib for system-provided installs.
//
// It returns ("", nil) when no candidate is found.
func systemRuntimeCandidates() (string, error) {
	libName := runtimeLibFileName("darwin")
	var roots []string
	if env := os.Getenv("DYLD_LIBRARY_PATH"); env != "" {
		roots = append(roots, strings.Split(env, string(os.PathListSeparator))...)
	}
	if prefix := strings.TrimSpace(os.Getenv("HOMEBREW_PREFIX")); prefix != "" {
		roots = append(roots, filepath.Join(prefix, "lib"))
	}
	roots = append(roots,
		"/opt/homebrew/lib",
		"/usr/local/lib",
		"/usr/lib",
	)
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		paths = append(paths, filepath.Join(root, libName))
	}
	return firstExistingFile(paths), nil
}
