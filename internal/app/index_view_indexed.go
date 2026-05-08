//go:build sqlite_fts5

// Package app: index_view_indexed.go is the real Index Manager UI.
//
// Build tag: this file is compiled only when the binary is built with
// the sqlite_fts5 tag. It imports the indexer and the ONNX embedder
// directly, so it must not be linked into the default build. The
// stub sibling index_view_stub.go provides the same function surface
// (indexManagerView, closeSemanticResources) for the default build.
//
// Flow overview:
//
//  1. The view lists every configured backend with a status line
//     derived from IndexDB.Stats when the DB has been opened, or
//     "Not indexed" when it has not.
//  2. "Index" triggers a goroutine that:
//     - on first use, shows a disclosure modal describing what will
//     be downloaded (model + ONNX runtime), pulls the numbers
//     from the download manifest, and aborts cleanly on Cancel.
//     - calls ensureSemanticRuntime to build the embedder and
//     open the index DB lazily.
//     - streams indexer.Progress into the status label via fyne.Do.
//  3. "Clear" asks for confirmation, then calls
//     indexer.ClearBackend and refreshes the row's status.
//  4. "Clear All" asks for confirmation, then calls
//     indexer.ClearAll, discards the embedder, and refreshes the
//     whole view.
//
// UI updates never run on a worker goroutine; every widget mutation
// goes through fyne.Do. The progress callback is debounced to 200 ms
// by the indexer itself, so the UI sees a bounded rate of updates.
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

// indexManagerView builds the Index Manager surface. It does not
// start any indexing action by itself; the user must tap Index on a
// backend's row.
func (a *App) indexManagerView() fyne.CanvasObject {
	header := a.viewHeader("Index Manager")

	if len(a.cfg.Backends) == 0 {
		note := widget.NewLabel(
			"No backends configured. Add one in Settings first, then return here to index it.")
		note.Wrapping = fyne.TextWrapWord
		return container.NewBorder(container.NewVBox(header, note), nil, nil, nil, nil)
	}

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
		container.NewVBox(header),
		clearAllButton,
		nil,
		nil,
		container.NewVScroll(rows),
	)
}

// buildBackendRow builds one card for a single backend. It captures
// the backend by value and the refresh callback so the row can update
// itself after Index / Clear actions without rebuilding the whole
// view.
func (a *App) buildBackendRow(backend config.Backend, refreshAll func()) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(backend.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	status := widget.NewLabel(a.statusFor(backend.Name))
	status.Wrapping = fyne.TextWrapWord

	var indexBtn *widget.Button
	var clearBtn *widget.Button

	refreshRow := func() {
		fyne.Do(func() {
			status.SetText(a.statusFor(backend.Name))
			indexBtn.Enable()
			clearBtn.Enable()
		})
	}

	indexBtn = widget.NewButton("Index", func() {
		a.onIndexTapped(backend, status, indexBtn, clearBtn, refreshRow)
	})
	clearBtn = widget.NewButton("Clear", func() {
		dialog.ShowConfirm(
			"Clear index for "+backend.Name,
			"Remove every indexed session for this backend.",
			func(ok bool) {
				if !ok {
					return
				}
				indexBtn.Disable()
				clearBtn.Disable()
				go a.runClearBackend(backend.Name, refreshRow)
				_ = refreshAll // retained for symmetry; single-row refresh is enough here.
			},
			a.main,
		)
	})

	return widget.NewCard("", "", container.NewVBox(
		name,
		status,
		container.NewHBox(indexBtn, clearBtn),
	))
}

// onIndexTapped implements the disclosure + index flow. It is called
// on the UI thread; the heavy work runs in a goroutine launched from
// here.
func (a *App) onIndexTapped(
	backend config.Backend,
	status *widget.Label,
	indexBtn, clearBtn *widget.Button,
	refreshRow func(),
) {
	start := func() {
		indexBtn.Disable()
		clearBtn.Disable()
		status.SetText("Indexing: preparing...")
		go a.runIndexBackend(backend, status, refreshRow)
	}

	a.semMu.Lock()
	accepted := a.semDisclosureAccepted
	a.semMu.Unlock()

	if accepted {
		start()
		return
	}

	dialog.ShowCustomConfirm(
		"Download required",
		"Download and Index",
		"Cancel",
		a.disclosureContent(),
		func(ok bool) {
			if !ok {
				return
			}
			a.semMu.Lock()
			a.semDisclosureAccepted = true
			a.semMu.Unlock()
			start()
		},
		a.main,
	)
}

// disclosureContent builds the body of the first-run disclosure
// modal. The sizes and version numbers come from the download
// manifest so the UI and the real download stay in sync.
func (a *App) disclosureContent() fyne.CanvasObject {
	msg := widget.NewLabel(disclosureMessage(download.DefaultManifest))
	msg.Wrapping = fyne.TextWrapWord
	return msg
}

// runIndexBackend runs the full ensureSemanticRuntime +
// IndexBackend sequence on a worker goroutine. UI mutations go
// through fyne.Do. The function never panics: every failure path
// ends with an error dialog and a row refresh.
func (a *App) runIndexBackend(backend config.Backend, status *widget.Label, refreshRow func()) {
	ctx := context.Background()

	if err := a.ensureSemanticRuntime(ctx, func(p download.Progress) {
		text := formatDownloadProgress(p)
		fyne.Do(func() { status.SetText(text) })
	}); err != nil {
		fyne.Do(func() { a.showError("Prepare semantic runtime", err) })
		refreshRow()
		return
	}

	db, emb, err := a.semanticDeps()
	if err != nil {
		fyne.Do(func() { a.showError("Prepare semantic runtime", err) })
		refreshRow()
		return
	}

	idx := indexer.NewIndexer(db, emb)
	err = idx.IndexBackend(ctx, backend, func(p indexer.Progress) {
		text := formatIndexProgress(p)
		fyne.Do(func() { status.SetText(text) })
	})
	if err != nil {
		fyne.Do(func() { a.showError("Index "+backend.Name, err) })
	}
	refreshRow()
}

// runClearBackend calls Indexer.ClearBackend on a worker goroutine.
// If the embedder has never been constructed we still need an
// indexer, so we open the DB without booting ONNX (that is all
// ClearBackend needs).
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
// and refreshes the whole view. A fresh DB is lazily re-opened on the
// next user action; we do not eagerly reopen here.
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

// statusFor returns the status line for a backend. It never opens
// the DB: if it has not been opened yet, the status simply reads
// "Not indexed" so the view can render before any user action.
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
// download manager, the tokenizer, and the embedder. Subsequent
// calls short-circuit once every asset is present and every object
// is constructed.
//
// The report callback is forwarded to the download manager unchanged.
// When the assets are already cached and the embedder is already
// built, report may never fire, which is correct.
func (a *App) ensureSemanticRuntime(ctx context.Context, report func(download.Progress)) error {
	a.semMu.Lock()
	defer a.semMu.Unlock()

	// Build the download manager once per process. Cache root lives
	// under the user's config directory so the Index Manager's
	// "Clear All" covers the on-disk footprint (the assets
	// themselves are not deleted here by design; the manifest lets
	// the user delete the whole cache out-of-band).
	if a.semDownloadManager == nil {
		root, err := semanticCacheRoot()
		if err != nil {
			return fmt.Errorf("cache path: %w", err)
		}
		a.semDownloadManager = download.NewManager(root, nil)
	}

	// Assets may already have been resolved by a previous call; if
	// not, do it now. This is the only step that can block on the
	// network.
	if a.semAssets == nil {
		assets, err := a.semDownloadManager.EnsureAssets(ctx, report)
		if err != nil {
			return err
		}
		a.semAssets = assets
	}

	// Tokenizer is cheap but not free: keep one per process.
	if a.semTokenizer == nil {
		tok, err := embedding.NewTokenizer(a.semAssets.TokenizerPath)
		if err != nil {
			return fmt.Errorf("tokenizer: %w", err)
		}
		a.semTokenizer = tok
	}

	// Embedder construction loads the ONNX model and initializes the
	// runtime environment; keep one per process.
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

	// IndexDB depends on CGO but not on the embedder; open it here
	// so the Indexer constructor finds both dependencies ready.
	if a.semIndexDB == nil {
		db, err := openIndexDBAtDefaultPath()
		if err != nil {
			return fmt.Errorf("open index database: %w", err)
		}
		a.semIndexDB = db
	}
	return nil
}

// ensureIndexDB opens the index database (if not already open)
// without booting the embedder. Used by Clear / Clear All, neither of
// which needs ONNX.
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

// semanticDeps returns the *IndexDB and Embedder that the indexer
// needs. Callers should invoke ensureSemanticRuntime first; this
// helper only type-asserts the cached interfaces.
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
// the tokenizer in that order. Safe to call on a zero-value App: all
// fields default to nil interfaces and the method tolerates that.
//
// Ordering matters: the DB closes cleanly without the embedder; the
// embedder's Destroy call releases the ONNX session; the tokenizer
// is closed last because it has no dependents.
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

// formatBackendStatus turns a BackendStats value into the status
// line shown next to the backend name. Extracted so
// index_view_test.go can assert the wording without a live Fyne
// runtime.
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
// single-line status string shown in the Index Manager row while an
// indexing run is in flight. The "done" stage reads "Indexed"
// instead of "Indexing" so the user knows the run is over.
func formatIndexProgress(p indexer.Progress) string {
	if p.Stage == "done" {
		return fmt.Sprintf("Indexed: %d session(s) processed", p.Total)
	}
	return fmt.Sprintf("Indexing: %d / %d (%s)", p.Done, p.Total, p.Stage)
}

// openIndexDBAtDefaultPath returns a freshly opened IndexDB at the
// conventional location (<config dir>/index.db). Extracted so tests
// and Clear All share the exact path.
func openIndexDBAtDefaultPath() (*indexer.IndexDB, error) {
	path, err := defaultIndexDBPath()
	if err != nil {
		return nil, err
	}
	return indexer.OpenIndexDB(path)
}

// defaultIndexDBPath returns the absolute path where the semantic
// index database lives. We use the config directory (not the cache
// directory) because the file is user data: "Clear All" deletes it
// explicitly; the OS must not evict it under cache pressure.
func defaultIndexDBPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "index.db"), nil
}

// semanticCacheRoot returns the root directory where the download
// manager stores the ONNX runtime and the model. Unlike the index
// database, these are regeneratable from the pinned manifest, so we
// put them under the user's cache directory.
func semanticCacheRoot() (string, error) {
	base, err := userCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "beddel-desk"), nil
}
