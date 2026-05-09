//go:build !sqlite_fts5

// Package app: index_view_stub.go is the Index Manager implementation
// used when the binary is built without the sqlite_fts5 build tag.
//
// In that configuration the indexer.IndexDB type and the hybrid search
// pipeline are absent, so we cannot open the index database or run an
// embedder. To keep the single-window UI and the tray/home entries
// identical across builds, this file provides a friendly placeholder
// view and no-op stubs. The tagged sibling file index_view_indexed.go
// owns the real implementation.
package app

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// indexManagerView is the stub counterpart of the real Index Manager.
func (a *App) indexManagerView() fyne.CanvasObject {
	header := a.viewHeader("Index Manager")

	title := widget.NewLabelWithStyle(
		"Semantic search unavailable",
		fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true},
	)

	body := widget.NewLabel(
		"This binary was built without the sqlite_fts5 build tag, so the " +
			"semantic index database and the hybrid search pipeline are " +
			"not linked in. Lexical search (the V1 grep flow) continues " +
			"to work from the Search view. To enable the Index Manager " +
			"and hybrid search, rebuild with:\n\n" +
			"    CGO_ENABLED=1 go build -tags sqlite_fts5 ./cmd/beddel-desk",
	)
	body.Wrapping = fyne.TextWrapWord

	return container.NewBorder(
		container.NewVBox(header, title, body),
		nil, nil, nil,
		container.NewWithoutLayout(),
	)
}

// closeSemanticResources is the no-op counterpart.
func (a *App) closeSemanticResources() {
	// Intentionally empty: nothing is constructed in the default build.
}

// indexSessionFromResult is a no-op in the default build.
// The "Index" button is hidden when semantic search is not available.
func (a *App) indexSessionFromResult(_, _ string, statusCb func(string)) {
	statusCb("Semantic search not available in this build")
}

// isSessionIndexed always returns false in the default build.
func (a *App) isSessionIndexed(_ string) bool {
	return false
}
