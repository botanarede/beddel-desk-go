//go:build !sqlite_fts5

// Package app: search_view_semantic_test.go covers the stub
// implementations from search_view_semantic_stub.go. The tagged
// build has its own behavior that requires a real IndexDB and
// embedder; that path is exercised end-to-end by manual
// verification in the PR body (per Story 11) rather than by unit
// tests, so there is no sibling file under -tags sqlite_fts5.
package app

import (
	"context"
	"testing"

	"github.com/botanarede/beddel-desk-go/internal/search"
)

// TestCurrentSearchModeStubAlwaysLexical: in the default build
// there is no index DB, so every backend must report "lexical".
// The stub ignores its argument on purpose; we still pass a
// non-empty backend name to catch accidental panics in the future.
func TestCurrentSearchModeStubAlwaysLexical(t *testing.T) {
	a := &App{}
	for _, name := range []string{"", "Kiro", "Gemini", "does-not-exist"} {
		if got := a.currentSearchMode(name); got != "lexical" {
			t.Errorf("currentSearchMode(%q) = %q, want %q", name, got, "lexical")
		}
	}
}

// TestTrySemanticSearchStubReturnsNotAvailable: the stub must
// signal "engine unavailable" by returning ok=false and err=nil
// so the searchView falls through to search.Search silently.
// Returning a non-nil error here would surface as a warning in the
// UI, which violates the Story 11 "silent fallback" constraint.
func TestTrySemanticSearchStubReturnsNotAvailable(t *testing.T) {
	a := &App{}
	resp, ok, err := a.trySemanticSearch(context.Background(), search.Query{
		Text:        "whatever",
		BackendName: "Kiro",
	})
	if ok {
		t.Errorf("ok = true, want false (stub must never claim the engine is available)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil (silent fallback)", err)
	}
	if len(resp.Results) != 0 || len(resp.Warnings) != 0 {
		t.Errorf("resp should be zero-valued, got %+v", resp)
	}
}
