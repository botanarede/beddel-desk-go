//go:build sqlite_fts5

// Package app: search_view_semantic.go is the tagged side of the
// Mode-label + silent-fallback plumbing for searchView.
//
// Build tag: this file is compiled only with the sqlite_fts5 tag,
// so it may import indexer and embedding directly. The default
// build uses search_view_semantic_stub.go, which defines the same
// methods with a no-op "lexical always" behavior.
//
// Responsibilities:
//
//  1. currentSearchMode(backendName) decides what the "Mode" label
//     displays for the selected backend. The decision is a best
//     effort: we never block the UI on the DB, and any error
//     degrades silently to "lexical".
//
//  2. semanticEngine() assembles a search.SemanticEngine from the
//     app's cached *indexer.IndexDB and Embedder. A missing
//     dependency returns nil — this is the "engine not ready"
//     signal that trySemanticSearch turns into the silent fallback.
//
//  3. trySemanticSearch runs SearchSemantic when the engine is
//     ready; otherwise it returns (Response{}, false, nil). The
//     boolean is the only way the caller distinguishes "engine ran
//     and had an opinion" from "engine unavailable, use lexical".
package app

import (
	"context"

	"github.com/botanarede/beddel-desk-go/internal/embedding"
	"github.com/botanarede/beddel-desk-go/internal/indexer"
	"github.com/botanarede/beddel-desk-go/internal/search"
)

// semanticEngineAdapter is the small glue type that turns the app's
// cached *indexer.IndexDB + embedding.Embedder pair into the
// narrower search.SemanticEngine interface. Kept unexported because
// no other file constructs it.
type semanticEngineAdapter struct {
	emb embedding.Embedder
	db  *indexer.IndexDB
}

// Embedder returns the cached embedder as a SemanticEmbedder.
// Returning a nil embedding.Embedder is represented as a nil
// SemanticEmbedder so SearchSemantic skips the Embed call cleanly.
func (a *semanticEngineAdapter) Embedder() search.SemanticEmbedder {
	if a.emb == nil {
		return nil
	}
	return a.emb
}

// DB returns the cached *indexer.IndexDB as a SemanticDB. The
// indexer's IndexDB satisfies the SemanticDB interface implicitly
// via HasBackend and SearchHybrid.
func (a *semanticEngineAdapter) DB() search.SemanticDB {
	if a.db == nil {
		return nil
	}
	return a.db
}

// semanticEngine assembles the engine from the app's cached
// resources. It returns nil when either dependency is missing,
// which makes trySemanticSearch fall through to the lexical path.
//
// Important: we do NOT open the IndexDB or construct the embedder
// here. The search view must not trigger the first-run asset
// download or any other side effect; that flow lives in the Index
// Manager. This method is pure discovery — if the user has never
// indexed a backend, the engine is simply not available.
func (a *App) semanticEngine() search.SemanticEngine {
	a.semMu.Lock()
	db, _ := a.semIndexDB.(*indexer.IndexDB)
	emb, _ := a.semEmbedder.(embedding.Embedder)
	a.semMu.Unlock()

	if db == nil {
		return nil
	}
	// A nil embedder is acceptable: SearchSemantic degrades to a
	// pure-FTS5 query in that case, which is still strictly better
	// than the grep path for an indexed backend.
	return &semanticEngineAdapter{emb: emb, db: db}
}

// currentSearchMode returns the human-readable label shown next to
// the backend selector. It reflects the backend the user currently
// has selected; changing the selector re-calls this helper.
//
// Decision table:
//
//   - No index database cached: "lexical". We cannot know whether
//     the backend is indexed without opening the DB, and opening
//     it from the search view would violate the "no side effects
//     outside Index Manager" rule.
//   - DB cached but HasBackend errors out: "lexical" (silent
//     degrade, per Story 11 constraint #3).
//   - DB cached and HasBackend returns true: "hybrid (FTS5 + vector)".
//   - DB cached but backend not indexed: "lexical — indexing
//     available". This last case hints the user toward the Index
//     Manager without forcing a modal.
func (a *App) currentSearchMode(backendName string) string {
	a.semMu.Lock()
	db, _ := a.semIndexDB.(*indexer.IndexDB)
	a.semMu.Unlock()

	if db == nil {
		return "lexical"
	}
	has, err := db.HasBackend(backendName)
	if err != nil {
		return "lexical"
	}
	if has {
		return "hybrid (FTS5 + vector)"
	}
	return "lexical — indexing available"
}

// trySemanticSearch is the adapter searchView calls before falling
// back to search.Search. Contract:
//
//   - (resp, true,  nil): the semantic engine ran and produced resp.
//   - (resp, true,  err): the semantic engine ran and failed; the
//     caller should log the error, surface a non-fatal warning,
//     and still fall back to search.Search. The resp value is
//     meaningless in this branch.
//   - (Response{}, false, nil): the semantic engine is not
//     available (no index yet, no embedder yet). This is the
//     "silent fallback" case; the caller should use lexical search
//     without warning the user.
func (a *App) trySemanticSearch(ctx context.Context, q search.Query) (search.Response, bool, error) {
	eng := a.semanticEngine()
	if eng == nil {
		return search.Response{}, false, nil
	}
	resp, err := search.SearchSemantic(ctx, q, eng)
	return resp, true, err
}
