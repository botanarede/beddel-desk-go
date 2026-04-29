// Package search provides on-demand text search across local session files.
package search

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	MaxResults      = 200
	defaultMaxBytes = 100 << 20 // 100 MB
)

// Result represents a single search match.
type Result struct {
	FilePath    string
	BackendName string
	MatchLine   string
	LineNumber  int
	FileModTime time.Time
}

// Query defines the parameters for a search operation.
type Query struct {
	Text        string
	BackendName string
	Paths       []string // directories to scan
	PathFilter  string   // optional: filter by subdirectory
	From        time.Time
	To          time.Time
	Favorites   map[string]struct{}
	MaxFileSize int64
	MaxResults  int
}

// Response contains search results and non-fatal traversal warnings.
type Response struct {
	Results  []Result
	Warnings []string
}

// Search walks the paths in q.Paths on demand and returns matching lines sorted by
// modification time descending. Processed content is kept only in returned memory.
func Search(q Query) (Response, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return Response{}, errors.New("query text is required")
	}
	if strings.TrimSpace(q.BackendName) == "" {
		return Response{}, errors.New("backend name is required")
	}
	if len(q.Paths) == 0 {
		return Response{}, errors.New("at least one source path is required")
	}
	maxFileSize := q.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = defaultMaxBytes
	}
	maxResults := q.MaxResults
	if maxResults <= 0 {
		maxResults = MaxResults
	}
	lower := strings.ToLower(q.Text)
	pathFilter := normalizePathFilter(q.PathFilter)
	var results []Result
	var warnings []string

	for _, root := range q.Paths {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("cannot access %s: %v", path, err))
				return nil
			}
			info, statErr := entry.Info()
			if statErr != nil {
				warnings = append(warnings, fmt.Sprintf("cannot inspect %s: %v", path, statErr))
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if info.Size() > maxFileSize {
				warnings = append(warnings, fmt.Sprintf("skipped %s: file is larger than %d bytes", path, maxFileSize))
				return nil
			}
			if !matchesPathFilter(path, pathFilter) || !matchesDate(info.ModTime(), q.From, q.To) || !matchesFavorites(path, q.Favorites) {
				return nil
			}
			if binary, err := isBinary(path); err != nil {
				warnings = append(warnings, fmt.Sprintf("cannot read %s: %v", path, err))
				return nil
			} else if binary {
				return nil
			}
			fileResults, fileWarnings := scanFile(path, info, q.BackendName, lower)
			warnings = append(warnings, fileWarnings...)
			results = append(results, fileResults...)
			return nil
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot scan %s: %v", root, err))
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if !results[i].FileModTime.Equal(results[j].FileModTime) {
			return results[i].FileModTime.After(results[j].FileModTime)
		}
		if results[i].FilePath != results[j].FilePath {
			return results[i].FilePath < results[j].FilePath
		}
		return results[i].LineNumber < results[j].LineNumber
	})
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return Response{Results: results, Warnings: warnings}, nil
}

func scanFile(path string, info os.FileInfo, backend, lower string) ([]Result, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot open %s: %v", path, err)}
	}
	defer f.Close()

	modTime := info.ModTime()
	reader := bufio.NewReaderSize(f, 64*1024)
	lineNum := 0
	var results []Result
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
		}
		if len(line) == 0 && err == io.EOF {
			break
		}
		lineNum++
		text := string(line)
		if matchLine(text, lower) {
			results = append(results, Result{
				FilePath:    path,
				BackendName: backend,
				MatchLine:   text,
				LineNumber:  lineNum,
				FileModTime: modTime,
			})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return results, []string{fmt.Sprintf("cannot finish reading %s: %v", path, err)}
		}
	}
	return results, nil
}

// isBinary reads the first 512 bytes and returns true if any null byte is found.
func isBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true, nil
		}
	}
	return false, nil
}

// matchLine performs a case-insensitive substring check.
func matchLine(line, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(line), lowerQuery)
}

func normalizePathFilter(filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return ""
	}
	return strings.ToLower(filepath.Clean(filter))
}

func matchesPathFilter(path, filter string) bool {
	if filter == "" {
		return true
	}
	clean := strings.ToLower(filepath.Clean(path))
	return strings.Contains(clean, filter)
}

func matchesDate(modTime, from, to time.Time) bool {
	if !from.IsZero() && modTime.Before(from) {
		return false
	}
	if !to.IsZero() && modTime.After(to) {
		return false
	}
	return true
}

func matchesFavorites(path string, favorites map[string]struct{}) bool {
	if len(favorites) == 0 {
		return true
	}
	_, ok := favorites[filepath.Clean(path)]
	return ok
}
