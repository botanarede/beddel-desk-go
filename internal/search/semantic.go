//go:build sqlite_fts5

// Package search: semantic.go is the V2 hybrid-search entry point.
//
// Build tag: this file is compiled only under -tags sqlite_fts5 so
// that the default build of the search package never imports the
// indexer package (which itself links sqlite-vec + FTS5 and requires
// CGO). Every identifier in this file lives inside that tagged
// island; the tag-free V1 Search function in search.go is
// untouched.
//
// Behavior overview: SearchSemantic is a thin adapter on top of
// IndexDB.SearchHybrid. It embeds the query text once, runs the
// hybrid query, maps each IndexedChunk to a search.Result, and
// applies the same path / date / favorites post-filters that the V1
// Search function already owns. The ranking order produced by
// SearchHybrid is preserved end-to-end: this layer never re-sorts.
//
// Non-goals: no Lucene-style parsing, no on-the-fly indexing, no
// retry logic. If anything fails, the error is returned so the
// caller (searchView) can fall back to the lexical path.
package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/botanarede/beddel-desk-go/internal/indexer"
)

// defaultSemanticTopK is the number of hybrid hits requested from
// the indexer when Query.TopK is zero. Fifty is the value pinned in
// the Story 11 spec and matches the cap used by the V2 UX
// guidelines (dense enough to surface useful hits, small enough to
// keep BM25 + vec scans fast).
const defaultSemanticTopK = 50

// matchLineCap is the maximum MatchLine length (in runes) produced
// by the semantic path. Story 11 spells it out as 280; we enforce it
// on rune boundaries so we never split a UTF-8 codepoint and make
// the UI render a replacement character.
const matchLineCap = 280

// SemanticEmbedder is the narrow interface SearchSemantic consumes
// when it needs a vector for the query text. It intentionally does
// NOT reference embedding.Embedder so tests can plug in fakes
// without linking ONNX.
type SemanticEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// SemanticDB is the narrow read surface SearchSemantic needs from an
// *indexer.IndexDB. Defining it here (rather than importing IndexDB
// directly) keeps the package decoupled from the concrete
// implementation and lets semantic_test.go swap in a fake.
type SemanticDB interface {
	HasBackend(backendName string) (bool, error)
	SearchHybrid(backendName, query string, queryVec []float32, topK int) ([]indexer.IndexedChunk, error)
}

// SemanticEngine wraps the two dependencies SearchSemantic needs so
// call sites can inject them together. The App constructs a real
// engine in search_view_semantic.go; tests construct a fake in
// semantic_test.go.
type SemanticEngine interface {
	Embedder() SemanticEmbedder
	DB() SemanticDB
}

// SearchSemantic runs a hybrid (FTS5 + vector) search against the
// index database and returns results shaped like the V1 Response so
// the UI layer does not have to branch on engine.
//
// Contract:
//
//   - q.BackendName must be non-empty.
//   - eng must be non-nil and eng.DB() must be non-nil. A nil
//     embedder is tolerated: it degrades to a pure-FTS5 query.
//   - q.TopK <= 0 falls back to defaultSemanticTopK (50).
//   - An empty q.Text skips the Embed call entirely and passes a
//     nil vector to SearchHybrid. IndexDB.SearchHybrid handles the
//     nil/empty-text cases explicitly (see Story 9).
//   - The returned order exactly mirrors SearchHybrid's order after
//     filters drop rows. We never re-sort here.
//   - Chunks whose SessionPath no longer exists on disk are
//     dropped and their loss is reported in Response.Warnings.
//
// Errors from Embed or SearchHybrid are returned unchanged so the
// caller (searchView) can log and transparently fall back to
// search.Search.
func SearchSemantic(ctx context.Context, q Query, eng SemanticEngine) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if strings.TrimSpace(q.BackendName) == "" {
		return Response{}, errors.New("search: backend name is required")
	}
	if eng == nil {
		return Response{}, errors.New("search: semantic engine is required")
	}
	db := eng.DB()
	if db == nil {
		return Response{}, errors.New("search: semantic engine database is not initialized")
	}

	topK := q.TopK
	if topK <= 0 {
		topK = defaultSemanticTopK
	}

	// Embed the query once. The indexer accepts a nil vector, so if
	// the text is empty we skip the Embed call and rely on the FTS5
	// branch inside SearchHybrid. If Embed returns an error we bubble
	// it up unchanged: Story 11 makes the caller responsible for the
	// lexical fallback.
	var queryVec []float32
	queryText := strings.TrimSpace(q.Text)
	if queryText != "" {
		if emb := eng.Embedder(); emb != nil {
			vec, err := emb.Embed(ctx, queryText)
			if err != nil {
				return Response{}, err
			}
			queryVec = vec
		}
	}

	chunks, err := db.SearchHybrid(q.BackendName, q.Text, queryVec, topK)
	if err != nil {
		return Response{}, fmt.Errorf("search: hybrid query: %w", err)
	}

	pathFilter := normalizePathFilter(q.PathFilter)

	results := make([]Result, 0, len(chunks))
	var warnings []string

	for _, chunk := range chunks {
		// Stat the session file so FileModTime matches what the V1
		// path populates and so the same date filter logic applies.
		// Missing / unreadable files become a warning + drop.
		info, statErr := os.Stat(chunk.SessionPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				warnings = append(warnings,
					fmt.Sprintf("session file missing: %s", chunk.SessionPath))
			} else {
				warnings = append(warnings,
					fmt.Sprintf("cannot stat %s: %v", chunk.SessionPath, statErr))
			}
			continue
		}

		result := Result{
			FilePath:    chunk.SessionPath,
			BackendName: q.BackendName,
			MatchLine:   truncateRunes(chunk.Content, matchLineCap),
			LineNumber:  chunk.ChunkIndex + 1, // 1-based for UI continuity with V1
			FileModTime: info.ModTime(),
			Score:       chunk.Score,
			Role:        chunk.Role,
			ChunkIndex:  chunk.ChunkIndex,
		}

		// Apply the same post-filters V1 Search uses, so the two
		// engines produce the same filtered view of the index.
		// These helpers live unexported in search.go; being in the
		// same package lets us call them directly.
		if !matchesPathFilter(result.FilePath, pathFilter) {
			continue
		}
		if !matchesDate(result.FileModTime, q.From, q.To) {
			continue
		}
		if !matchesFavorites(result.FilePath, q.Favorites) {
			continue
		}

		results = append(results, result)
	}

	// NOTE: we deliberately do NOT sort here. SearchHybrid returns
	// its RRF-ranked order and the UI expects that order to be
	// preserved all the way through.

	return Response{Results: results, Warnings: warnings}, nil
}

// truncateRunes returns s trimmed to at most max runes. Byte
// slicing is unsafe because it can bisect a multi-byte UTF-8
// codepoint; rune slicing is slightly more expensive but always
// correct. A non-positive max returns an empty string.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		// Fast path: if the byte length already fits, the rune count
		// certainly does too.
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
