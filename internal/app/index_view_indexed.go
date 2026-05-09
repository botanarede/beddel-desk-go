//go:build sqlite_fts5

// Package app: index_view_indexed.go is the real Index Manager UI.
//
// Build tag: this file is compiled only when the binary is built with
// the sqlite_fts5 tag. It imports the indexer and the ONNX embedder
// directly, so it must not be linked into the default build. The
// stub sibling index_view_stub.go provides the same function surface
// (indexManagerView, closeSemanticResources, indexSessionFromResult)
// for the default build.
//
// Flow overview (v0.2.1 — per-session on-demand indexing):
//
//  1. The Index Manager lists every configured backend with a status
//     line derived from IndexDB.Stats when the DB has been opened, or
//     "Not indexed" when it has not. Per-backend "Clear" removes
//     indexed sessions. "Clear All" removes the entire database.
//
//  2. Indexing is triggered from search result cards (see resultCard
//     in app.go), NOT from the Index Manager. When the user taps
//     "Index" on a result card:
//     - on first use, shows a disclosure modal describing what will
//       be downloaded (model + ONNX runtime).
//     - calls ensureSemanticRuntime to build the embedder and open
//       the index DB lazily.
//     - calls indexer.IndexSession for that single file.
//
//  3. "Clear" asks for confirmation, then calls
//     indexer.ClearBackend and refreshes the row's status.
//  4. "Clear All" asks for confirmation, then calls
//     indexer.ClearAll, discards the embedder, and refreshes the
//     whole view.
//
// UI updates never run on a worker goroutine; every widget mutation
// goes through fyne.Do.
package app

import (
	"context"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/botanarede/beddel-desk-go/internal/config"
	"github.com/botanarede/beddel-desk-go/internal/embedding"
	"github.com/botanarede/beddel-desk-go/internal/embedding/download"
	"github.com/botanarede/beddel-desk-go/internal/indexer"
)

// indexManagerView builds the Index Manager surface. In v0.2.1 this
// is a status dashboard with Clear actions. Indexing is triggered
// from search result cards, not from here.
func (a *App) indexManagerView() fyne.CanvasObject {
	header := a.viewHeader("Index Manager")

	if len(a.cfg.Backends) == 0 {
		note := widget.NewLabel(
			"No backends configured. Add one in Settings first.")
		note.Wrapping = fyne.TextWrapWord
		return container.NewBorder(container.NewVBox(header, note), nil, nil, nil, nil)
	}

	hint := widget.NewLabel("To index a session, find it via Search and tap \"Index\" on the result card.")
	hint.Wrapping = fyne.TextWrapWord

	rows := container.NewVBox()
	var refreshAll func()
	refreshAll = func() {
		rows.RemoveAll()
		for _, backend := range a.cfg.Backends {
			rows.Add(a.buildBackendRow(backend, refreshAll))
		}
		rows.Refresh()
	}
	refreshAll()

	clearAllButton := widget.NewButton("Clear All", func() {
		dialog.ShowConfirm(
			"Clear all indexes",
			"Delete the entire semantic index database. This cannot be undone.",
			func(ok bool) {
				if !ok {
					return
				}
				go a.runClearAll(refreshAll)
			},
			a.main,
		)
	})

	return container.NewBorder(
		container.NewVBox(header, hint),
		clearAllButton,
		nil,
		nil,
		container.NewVScroll(rows),
	)
}

// buildBackendRow builds one card for a single backend. In v0.2.1
// it only shows status + Clear (no bulk Index).
func (a *App) buildBackendRow(backend config.Backend, refreshAll func()) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(backend.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	status := widget.NewLabel(a.statusFor(backend.Name))
	status.Wrapping = fyne.TextWrapWord

	clearBtn := widget.NewButton("Clear", func() {
		dialog.ShowConfirm(
			"Clear index for "+backend.Name,
			"Remove every indexed session for this backend.",
			func(ok bool) {
				if !ok {
					return
				}
				clearBtn := widget.NewButton("", nil) // placeholder to avoid capture
				_ = clearBtn
				go a.runClearBackend(backend.Name, func() {
					fyne.Do(func() {
						status.SetText(a.statusFor(backend.Name))
					})
				})
			},
			a.main,
		)
	})

	return widget.NewCard("", "", container.NewVBox(
		name,
		status,
		container.NewHBox(clearBtn),
	))
}

// indexSessionFromResult is the per-session indexing entry point
// called from result cards in app.go. It handles the first-run
// disclosure, downloads, and indexes exactly one session file.
func (a *App) indexSessionFromResult(backendName, sessionPath string, statusCb func(string)) {
	// First-run disclosure
	a.semMu.Lock()
	accepted := a.semDisclosureAccepted
	a.semMu.Unlock()

	if !accepted {
		done := make(chan bool, 1)
		fyne.Do(func() {
			dialog.ShowCustomConfirm(
				"Download required",
				"Download and Index",
				"Cancel",
				a.disclosureContent(),
				func(ok bool) {
					done <- ok
				},
				a.main,
			)
		})
		ok := <-done
		if !ok {
			statusCb("Cancelled")
			return
		}
		a.semMu.Lock()
		a.semDisclosureAccepted = true
		a.semMu.Unlock()
	}

	statusCb("Preparing runtime...")

	ctx := context.Background()
	if err := a.ensureSemanticRuntime(ctx, func(p download.Progress) {
		text := formatDownloadProgress(p)
		fyne.Do(func() { statusCb(text) })
	}); err != nil {
		fyne.Do(func() { a.showError("Prepare semantic runtime", err) })
		statusCb("Error")
		return
	}

	db, emb, err := a.semanticDeps()
	if err != nil {
		fyne.Do(func() { a.showError("Prepare semantic runtime", err) })
		statusCb("Error")
		return
	}

	statusCb("Indexing session...")

	idx := indexer.NewIndexer(db, emb)
	err = idx.IndexSession(ctx, backendName, sessionPath, func(p indexer.Progress) {
		text := fmt.Sprintf("Indexing: %s", p.Stage)
		fyne.Do(func() { statusCb(text) })
	})
	if err != nil {
		fyne.Do(func() { a.showError("Index session", err) })
		statusCb("Error")
		return
	}

	statusCb("Indexed ✓")
}

// isSessionIndexed checks if a session file is already in the index.
// Returns false if the DB is not open (no side effects from search view).
func (a *App) isSessionIndexed(sessionPath string) bool {
	a.semMu.Lock()
	db, _ := a.semIndexDB.(*indexer.IndexDB)
	a.semMu.Unlock()
	if db == nil {
		return false
	}
	has, err := db.HasSession(sessionPath)
	if err != nil {
		return false
	}
	return has
}

// disclosureContent builds the body of the first-run disclosure
// modal. The sizes and version numbers come from the download
// manifest so the UI and the real download stay in sync.
func (a *App) disclosureContent() fyne.CanvasObject {
	msg := widget.NewLabel(disclosureMessage(download.DefaultManifest))
	msg.Wrapping = fyne.TextWrapWord
	return msg
}

// runClearBackend calls IndexDB.DeleteBackend on a worker goroutine.
func (a *App) runClearBackend(backendName string, refreshRow func()) {
	db, err := a.ensureIndexDB()
	if err != nil {
		fyne.Do(func() { a.showError("Open index database", err) })
		refreshRow()
		return
	}
	if err := db.DeleteBackend(backendName); err != nil {
		fyne.Do(func() { a.showError("Clear "+backendName, err) })
	}
	refreshRow()
}

// runClearAll closes the embedder session, calls IndexDB.DeleteAll,
// and refreshes the whole view.
func (a *App) runClearAll(refreshAll func()) {
	db, err := a.ensureIndexDB()
	if err != nil {
		fyne.Do(func() { a.showError("Open index database", err) })
		return
	}
	if err := db.DeleteAll(); err != nil {
		fyne.Do(func() { a.showError("Clear all", err) })
		return
	}

	// After DeleteAll the DB handle is closed. Drop our reference so
	// the next action reopens a fresh database.
	a.semMu.Lock()
	a.semIndexDB = nil
	a.semMu.Unlock()

	fyne.Do(refreshAll)
}

// statusFor returns the status line for a backend.
func (a *App) statusFor(backendName string) string {
	a.semMu.Lock()
	db, _ := a.semIndexDB.(*indexer.IndexDB)
	a.semMu.Unlock()
	if db == nil {
		return "Not indexed"
	}
	stats, err := db.Stats(backendName)
	if err != nil {
		return "Status unavailable: " + err.Error()
	}
	return formatBackendStatus(stats)
}

// ensureSemanticRuntime is the lazy-build entry point for the
// download manager, the tokenizer, and the embedder.
func (a *App) ensureSemanticRuntime(ctx context.Context, report func(download.Progress)) error {
	a.semMu.Lock()
	defer a.semMu.Unlock()

	if a.semDownloadManager == nil {
		root, err := semanticCacheRoot()
		if err != nil {
			return fmt.Errorf("cache path: %w", err)
		}
		a.semDownloadManager = download.NewManager(root, nil)
	}

	if a.semAssets == nil {
		assets, err := a.semDownloadManager.EnsureAssets(ctx, report)
		if err != nil {
			return err
		}
		a.semAssets = assets
	}

	if a.semTokenizer == nil {
		tok, err := embedding.NewTokenizer(a.semAssets.TokenizerPath)
		if err != nil {
			return fmt.Errorf("tokenizer: %w", err)
		}
		a.semTokenizer = tok
	}

	if a.semEmbedder == nil {
		tok, ok := a.semTokenizer.(embedding.Tokenizer)
		if !ok {
			return fmt.Errorf("tokenizer: unexpected type %T", a.semTokenizer)
		}
		emb, err := embedding.NewEmbedder(a.semAssets.RuntimeLibraryPath, a.semAssets.ModelPath, tok)
		if err != nil {
			return fmt.Errorf("embedder: %w", err)
		}
		a.semEmbedder = emb
	}

	if a.semIndexDB == nil {
		db, err := openIndexDBAtDefaultPath()
		if err != nil {
			return fmt.Errorf("open index database: %w", err)
		}
		a.semIndexDB = db
	}
	return nil
}

// ensureIndexDB opens the index database without booting the embedder.
func (a *App) ensureIndexDB() (*indexer.IndexDB, error) {
	a.semMu.Lock()
	defer a.semMu.Unlock()

	if db, ok := a.semIndexDB.(*indexer.IndexDB); ok && db != nil {
		return db, nil
	}
	db, err := openIndexDBAtDefaultPath()
	if err != nil {
		return nil, err
	}
	a.semIndexDB = db
	return db, nil
}

// semanticDeps returns the *IndexDB and Embedder that the indexer needs.
func (a *App) semanticDeps() (*indexer.IndexDB, indexer.Embedder, error) {
	a.semMu.Lock()
	defer a.semMu.Unlock()

	db, dbOK := a.semIndexDB.(*indexer.IndexDB)
	emb, embOK := a.semEmbedder.(embedding.Embedder)
	if !dbOK || db == nil {
		return nil, nil, fmt.Errorf("index database is not initialized")
	}
	if !embOK || emb == nil {
		return nil, nil, fmt.Errorf("embedder is not initialized")
	}
	return db, emb, nil
}

// closeSemanticResources releases the index DB, the embedder, and
// the tokenizer in that order.
func (a *App) closeSemanticResources() {
	a.semMu.Lock()
	db, _ := a.semIndexDB.(*indexer.IndexDB)
	emb, _ := a.semEmbedder.(embedding.Embedder)
	tok, _ := a.semTokenizer.(embedding.Tokenizer)
	a.semIndexDB = nil
	a.semEmbedder = nil
	a.semTokenizer = nil
	a.semMu.Unlock()

	if db != nil {
		_ = db.Close()
	}
	if emb != nil {
		_ = emb.Close()
	}
	if tok != nil {
		_ = tok.Close()
	}
}

// formatBackendStatus turns a BackendStats value into the status line.
func formatBackendStatus(s indexer.BackendStats) string {
	if s.Chunks == 0 {
		return "Not indexed"
	}
	return fmt.Sprintf(
		"Indexed: %d session(s), %s",
		s.Sessions,
		humanBytes(s.BytesOnDisk),
	)
}

// formatIndexProgress renders an indexer.Progress tick into the
// single-line status string.
func formatIndexProgress(p indexer.Progress) string {
	if p.Stage == "done" {
		return "Indexed ✓"
	}
	return fmt.Sprintf("Indexing: %s", p.Stage)
}

// openIndexDBAtDefaultPath returns a freshly opened IndexDB at the
// conventional location (<config dir>/index.db).
func openIndexDBAtDefaultPath() (*indexer.IndexDB, error) {
	path, err := defaultIndexDBPath()
	if err != nil {
		return nil, err
	}
	return indexer.OpenIndexDB(path)
}

// defaultIndexDBPath returns the absolute path where the semantic
// index database lives.
func defaultIndexDBPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "index.db"), nil
}

// semanticCacheRoot returns the root directory where the download
// manager stores the ONNX runtime and the model.
func semanticCacheRoot() (string, error) {
	base, err := userCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "beddel-desk"), nil
}
