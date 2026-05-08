//go:build !sqlite_fts5

// Package app: search_view_semantic_stub.go is the default-build
// counterpart of search_view_semantic.go. It exists so the
// tag-free searchView in app.go can call a.currentSearchMode and
// a.trySemanticSearch without branching on build tags itself.
//
// In this build configuration there is no index database and no
// embedder, so every query degrades to the V1 lexical path. The
// stub reports that truthfully via currentSearchMode and returns a
// "not available" sentinel from trySemanticSearch so the caller
// falls through to search.Search silently (Story 11 "silent
// fallback" constraint).
package app

import (
	"context"

	"github.com/botanarede/beddel-desk-go/internal/search"
)

// currentSearchMode is the tag-free fallback reported in the
// search view's Mode label. Without the sqlite_fts5 tag the hybrid
// engine is not linked in, so there is nothing to report other
// than "lexical".
func (a *App) currentSearchMode(_ string) string {
	return "lexical"
}

// trySemanticSearch is the silent-fallback hook. It returns
// (zero Response, false, nil) to mean: the semantic engine is not
// available in this build; the caller should run the V1 lexical
// search without surfacing a warning to the user.
//
// Returning a nil error here is intentional. Only real semantic
// failures (engine present but misbehaving) should surface a
// warning; that branch lives in search_view_semantic.go.
func (a *App) trySemanticSearch(_ context.Context, _ search.Query) (search.Response, bool, error) {
	return search.Response{}, false, nil
}
