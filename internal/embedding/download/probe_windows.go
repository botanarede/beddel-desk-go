//go:build windows

package download

import (
	"os"
	"path/filepath"
	"strings"
)

// systemRuntimeCandidates searches typical Windows install directories
// for onnxruntime.dll. The lookup order is:
//  1. every directory in PATH, so a user who unzipped the Microsoft
//     release and added it to PATH is handled automatically,
//  2. ProgramFiles\onnxruntime\bin,
//  3. ProgramFiles(x86)\onnxruntime\bin.
//
// It returns ("", nil) when no candidate is found.
func systemRuntimeCandidates() (string, error) {
	libName := runtimeLibFileName("windows")
	var roots []string
	if env := os.Getenv("PATH"); env != "" {
		roots = append(roots, strings.Split(env, string(os.PathListSeparator))...)
	}
	if pf := strings.TrimSpace(os.Getenv("ProgramFiles")); pf != "" {
		roots = append(roots, filepath.Join(pf, "onnxruntime", "bin"))
	}
	if pfx86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); pfx86 != "" {
		roots = append(roots, filepath.Join(pfx86, "onnxruntime", "bin"))
	}
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
