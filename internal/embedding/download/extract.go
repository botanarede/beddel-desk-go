package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractSharedLib looks for the ONNX runtime shared library inside an archive
// (tgz or zip) and extracts it to destPath. We identify the correct library by
// looking for files matching the expected name prefix and taking the largest one
// to avoid extracting symlinks or tiny provider plugins.
func extractSharedLib(archivePath, destPath string) error {
	ext := strings.ToLower(filepath.Ext(archivePath))
	if ext == ".zip" {
		return extractZip(archivePath, destPath)
	}
	return extractTgz(archivePath, destPath)
}

func isTargetLibrary(name string) bool {
	base := filepath.Base(name)
	// Match libonnxruntime.so, libonnxruntime.so.1.19.2, libonnxruntime.dylib, onnxruntime.dll
	return strings.HasPrefix(base, "libonnxruntime.so") ||
		strings.HasPrefix(base, "libonnxruntime.dylib") ||
		strings.HasPrefix(base, "onnxruntime.dll")
}

func extractTgz(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tgz: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var targetHeader *tar.Header
	var maxSize int64

	// First pass: find the largest matching library file (ignore symlinks)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		if header.Typeflag == tar.TypeReg && isTargetLibrary(header.Name) {
			if header.Size > maxSize {
				maxSize = header.Size
				targetHeader = header
			}
		}
	}

	if targetHeader == nil {
		return errors.New("target shared library not found in tgz archive")
	}

	// Second pass: extract it
	// Rewind the file
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek: %w", err)
	}
	gzr2, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader 2: %w", err)
	}
	defer gzr2.Close()
	tr2 := tar.NewReader(gzr2)

	for {
		header, err := tr2.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read 2: %w", err)
		}
		if header.Name == targetHeader.Name {
			return copyToFile(tr2, destPath)
		}
	}

	return errors.New("failed to extract library in second pass")
}

func extractZip(archivePath, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var targetFile *zip.File
	var maxSize int64

	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && isTargetLibrary(f.Name) {
			size := int64(f.UncompressedSize64)
			if size > maxSize {
				maxSize = size
				targetFile = f
			}
		}
	}

	if targetFile == nil {
		return errors.New("target shared library not found in zip archive")
	}

	rc, err := targetFile.Open()
	if err != nil {
		return fmt.Errorf("open zip entry: %w", err)
	}
	defer rc.Close()

	return copyToFile(rc, destPath)
}

func copyToFile(src io.Reader, destPath string) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return out.Sync()
}
