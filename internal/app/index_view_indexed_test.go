//go:build sqlite_fts5

// Package app: index_view_indexed_test.go covers the pure helpers
// in index_view_indexed.go that consume tagged types
// (indexer.BackendStats, indexer.Progress). Tag-free helpers are
// covered by index_view_test.go. Keeping the tagged helper tests
// here preserves the "no real Fyne runtime needed" rule from
// Story 10 even under -tags sqlite_fts5.
package app

import (
	"strings"
	"testing"

	"github.com/botanarede/beddel-desk-go/internal/indexer"
)

func TestFormatBackendStatus_NotIndexed(t *testing.T) {
	got := formatBackendStatus(indexer.BackendStats{BackendName: "kiro"})
	if got != "Not indexed" {
		t.Fatalf("got %q, want %q", got, "Not indexed")
	}
}

func TestFormatBackendStatus_IndexedRendersCountAndBytes(t *testing.T) {
	got := formatBackendStatus(indexer.BackendStats{
		BackendName: "kiro",
		Sessions:    7,
		Chunks:      42,
		BytesOnDisk: 2 * 1024 * 1024,
	})
	if !strings.Contains(got, "7 session") {
		t.Errorf("got %q, missing session count", got)
	}
	if !strings.Contains(got, "2.0 MiB") {
		t.Errorf("got %q, missing disk usage", got)
	}
	// Must NOT include the raw chunk count: the UI only shows
	// sessions and bytes per Story 10 wording guidance.
	if strings.Contains(got, "42") {
		t.Errorf("got %q, should not expose chunk count", got)
	}
}

func TestFormatIndexProgress_DoneStage(t *testing.T) {
	got := formatIndexProgress(indexer.Progress{
		Stage: "done",
		Total: 12,
	})
	if !strings.Contains(got, "12") || !strings.Contains(got, "Indexed") {
		t.Fatalf("got %q, want indexed-total wording", got)
	}
}

func TestFormatIndexProgress_InFlightStage(t *testing.T) {
	got := formatIndexProgress(indexer.Progress{
		Stage: "embedding",
		Done:  3,
		Total: 10,
	})
	for _, needle := range []string{"Indexing", "3", "10", "embedding"} {
		if !strings.Contains(got, needle) {
			t.Errorf("got %q, missing %q", got, needle)
		}
	}
}
