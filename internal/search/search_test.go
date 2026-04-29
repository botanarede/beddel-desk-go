package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSearchMatchesPlainTextAndSortsByModTime(t *testing.T) {
	tmp := t.TempDir()
	oldFile := writeSession(t, tmp, "old.jsonl", "alpha\nneedle old\n")
	newFile := writeSession(t, tmp, "new.jsonl", "needle new\n")
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newFile, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	resp, err := Search(Query{Text: "needle", BackendName: "Codex", Paths: []string{tmp}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].FilePath != newFile {
		t.Fatalf("expected newest file first, got %s", resp.Results[0].FilePath)
	}
}

func TestSearchHandlesLongLines(t *testing.T) {
	tmp := t.TempDir()
	longLine := strings.Repeat("x", 200*1024) + " needle"
	file := writeSession(t, tmp, "long.jsonl", longLine)

	resp, err := Search(Query{Text: "needle", BackendName: "Codex", Paths: []string{tmp}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result from %s, got %d", file, len(resp.Results))
	}
}

func TestSearchFiltersByPathDateAndFavorites(t *testing.T) {
	tmp := t.TempDir()
	keepDir := filepath.Join(tmp, "keep")
	skipDir := filepath.Join(tmp, "skip")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := writeSession(t, keepDir, "session.jsonl", "needle\n")
	skip := writeSession(t, skipDir, "session.jsonl", "needle\n")
	modTime := time.Date(2026, 4, 28, 12, 0, 0, 0, time.Local)
	for _, path := range []string{keep, skip} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := Search(Query{
		Text:        "needle",
		BackendName: "Codex",
		Paths:       []string{tmp},
		PathFilter:  "keep",
		From:        modTime.Add(-time.Hour),
		To:          modTime.Add(time.Hour),
		Favorites:   map[string]struct{}{filepath.Clean(keep): {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].FilePath != keep {
		t.Fatalf("expected only keep favorite result, got %#v", resp.Results)
	}
}

func TestSearchReportsMissingPath(t *testing.T) {
	tmp := t.TempDir()
	resp, err := Search(Query{Text: "needle", BackendName: "Codex", Paths: []string{filepath.Join(tmp, "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("expected missing path warning")
	}
}

func writeSession(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

