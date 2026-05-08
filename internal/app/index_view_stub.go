//go:build !sqlite_fts5

// Package app: index_view_stub.go is the Index Manager implementation
// used when the binary is built without the sqlite_fts5 build tag.
//
// In that configuration the indexer.IndexDB type and the hybrid search
// pipeline are absent, so we cannot open the index database or run an
// embedder. To keep the single-window UI and the tray/home entries
// identical across builds, this file provides a friendly placeholder
// view and a no-op closeSemanticResources. The tagged sibling file
// index_view_indexed.go owns the real implementation.
package app

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// indexManagerView is the stub counterpart of the real Index Manager.
// It explains why semantic indexing is not available in this build so
// a user who stumbles onto the view understands what is missing and
// how to get it. The message is deliberately factual and short.
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

// closeSemanticResources is the no-op counterpart of the real closer
// defined in index_view_indexed.go. It exists so that app.go's
// `defer a.closeSemanticResources()` compiles under both build
// configurations.
func (a *App) closeSemanticResources() {
	// Intentionally empty: nothing is constructed in the default build.
}
