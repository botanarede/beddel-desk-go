//go:build linux

package download

import (
	"os"
	"path/filepath"
	"strings"
)

// systemRuntimeCandidates searches well-known Linux locations for the
// ONNX runtime shared library. The lookup order is:
//  1. any directory listed in LD_LIBRARY_PATH (so tests and advanced
//     users can override),
//  2. the standard system library directories installed by apt or
//     pacman packages.
//
// It returns ("", nil) when no candidate is found.
func systemRuntimeCandidates() (string, error) {
	libName := runtimeLibFileName("linux")
	var roots []string
	if env := os.Getenv("LD_LIBRARY_PATH"); env != "" {
		roots = append(roots, strings.Split(env, string(os.PathListSeparator))...)
	}
	roots = append(roots,
		"/usr/lib",
		"/usr/lib/x86_64-linux-gnu",
		"/usr/lib/aarch64-linux-gnu",
		"/usr/local/lib",
		"/lib",
		"/lib/x86_64-linux-gnu",
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
