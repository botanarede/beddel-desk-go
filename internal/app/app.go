// Package app wires the Beddel Desk desktop UI to the local-only domain packages.
//
// As of 0.2.0 the UI is single-window: every "view" (home, search, favorites,
// recent, settings, result detail) is a fyne.CanvasObject rendered into the
// main window via the Navigator. The old pattern of a.fyneApp.NewWindow() for
// each surface is gone.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/botanarede/beddel-desk-go/internal/config"
	"github.com/botanarede/beddel-desk-go/internal/embedding/download"
	"github.com/botanarede/beddel-desk-go/internal/search"
	"github.com/botanarede/beddel-desk-go/internal/storage"
	"github.com/botanarede/beddel-desk-go/internal/version"
)

const appID = "com.botanarede.beddel-desk"

// App owns the desktop UI state and lightweight local persistence.
type App struct {
	fyneApp fyne.App
	main    fyne.Window
	nav     *Navigator
	cfg     *config.AppConfig
	store   *storage.Store

	// Semantic-search resources. These are populated only when the
	// binary is built with the sqlite_fts5 tag and the user has
	// triggered an indexing or search action. The concrete types live
	// in index_view_indexed.go so that the default build never imports
	// sqlite-vec, onnxruntime, or FTS5-tagged code. any is used here to
	// keep the struct declaration tag-free.
	semMu                 sync.Mutex
	semIndexDB            any // *indexer.IndexDB
	semEmbedder           any // embedding.Embedder
	semTokenizer          any // embedding.Tokenizer
	semDownloadManager    *download.Manager
	semAssets             *download.Assets
	semDisclosureAccepted bool
}

// Run starts the cross-platform desktop application.
func Run() {
	fyneApp := app.NewWithID(appID)
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))

	cfg, cfgErr := config.Load()
	store, storeErr := storage.LoadStore()
	desk := &App{
		fyneApp: fyneApp,
		cfg:     cfg,
		store:   store,
	}
	if desk.cfg == nil {
		desk.cfg = &config.AppConfig{}
	}
	if desk.store == nil {
		desk.store = &storage.Store{}
	}

	desk.main = fyneApp.NewWindow("Beddel Desk")
	desk.main.Resize(fyne.NewSize(900, 620))
	desk.nav = newNavigator(desk.main)
	desk.main.SetMainMenu(desk.mainMenu())
	if desktopApp, ok := fyneApp.(desktop.App); ok {
		desktopApp.SetSystemTrayMenu(desk.trayMenu())
	}
	desk.nav.Reset(desk.homeView())
	desk.main.SetCloseIntercept(func() {
		desk.main.Hide()
	})

	defer desk.closeSemanticResources()

	if cfgErr != nil || storeErr != nil {
		desk.showError("Startup", errors.Join(cfgErr, storeErr))
	}

	desk.main.ShowAndRun()
}

func (a *App) mainMenu() *fyne.MainMenu {
	return fyne.NewMainMenu(a.trayMenu())
}

func (a *App) trayMenu() *fyne.Menu {
	// Tray actions jump between top-level surfaces, so they Reset() the
	// navigation stack rather than Push() onto it. This keeps the back-stack
	// focused on in-flow navigation (e.g. search result -> detail view).
	return fyne.NewMenu("Beddel Desk",
		fyne.NewMenuItem("Home", func() { a.nav.Reset(a.homeView()) }),
		fyne.NewMenuItem("Search", func() { a.nav.Reset(a.searchView()) }),
		fyne.NewMenuItem("Favorites", func() { a.nav.Reset(a.favoritesView()) }),
		fyne.NewMenuItem("Recent", func() { a.nav.Reset(a.recentView()) }),
		fyne.NewMenuItem("Index Manager", func() { a.nav.Reset(a.indexManagerView()) }),
		fyne.NewMenuItem("Settings", func() { a.nav.Reset(a.settingsView()) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", func() { a.fyneApp.Quit() }),
	)
}

// homeView renders the landing page. It never shows a back button because it
// is always the navigation root (tray menu items call nav.Reset with it).
func (a *App) homeView() fyne.CanvasObject {
	title := widget.NewLabelWithStyle("Beddel Desk", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := widget.NewLabel("Local-first desktop search for local agent session history. Configure a backend, then run searches on demand.")
	body.Wrapping = fyne.TextWrapWord

	return container.NewBorder(
		container.NewVBox(title, body),
		nil,
		nil,
		nil,
		container.NewCenter(container.NewVBox(
			widget.NewButton("Search", a.SafeAction(func() { a.nav.Push(a.searchView()) })),
			widget.NewButton("Favorites", a.SafeAction(func() { a.nav.Push(a.favoritesView()) })),
			widget.NewButton("Recent", a.SafeAction(func() { a.nav.Push(a.recentView()) })),
			widget.NewButton("Index Manager", a.SafeAction(func() { a.nav.Push(a.indexManagerView()) })),
			widget.NewButton("Settings", a.SafeAction(func() { a.nav.Push(a.settingsView()) })),
			widget.NewLabel(version.String()),
		)),
	)
}

// searchView builds the search surface as a CanvasObject. Previously this
// method created a new fyne.Window; now all layout is returned for the caller
// to render via the navigator.
//
// In 0.2.0 the view also owns the "Mode" label and the silent-fallback
// logic from Story 11: it tries the semantic engine first via
// trySemanticSearch and degrades to search.Search whenever the engine
// is unavailable or reports an error. The tag-free stubs in
// search_view_semantic_stub.go make sure the view compiles and works
// the same way in the default (lexical-only) build.
func (a *App) searchView() fyne.CanvasObject {
	log.Println("searchView: building")

	backendNames := a.backendNames()
	backendSelect := widget.NewSelect(backendNames, nil)
	// The Mode label starts empty/lexical and updates whenever the
	// user picks a backend. Building both widgets together lets us
	// wire OnChanged without a second lookup later on.
	modeLabel := widget.NewLabel("lexical")
	modeLabel.Wrapping = fyne.TextWrapWord
	if len(backendNames) > 0 {
		backendSelect.SetSelected(backendNames[0])
		modeLabel.SetText(a.currentSearchMode(backendNames[0]))
	}
	backendSelect.OnChanged = func(selected string) {
		// The mode depends on whether the backend has been indexed,
		// which is cheap to look up; refresh on every selection
		// change so the label never lies.
		modeLabel.SetText(a.currentSearchMode(selected))
	}
	queryEntry := widget.NewEntry()
	queryEntry.SetPlaceHolder("Plain-text query")
	pathFilterEntry := widget.NewEntry()
	pathFilterEntry.SetPlaceHolder("Optional path or directory filter")
	fromEntry := widget.NewEntry()
	fromEntry.SetPlaceHolder("From date YYYY-MM-DD")
	toEntry := widget.NewEntry()
	toEntry.SetPlaceHolder("To date YYYY-MM-DD")
	favoritesOnly := widget.NewCheck("Favorites only", nil)
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	warnings := widget.NewLabel("")
	warnings.Wrapping = fyne.TextWrapWord
	resultsBox := container.NewVBox()

	searchButton := widget.NewButton("Run Search", nil)

	runSearch := func() {
		resultsBox.RemoveAll()
		warnings.SetText("")
		searchButton.Disable()
		status.SetText("Searching...")

		backend := a.cfg.FindBackend(backendSelect.Selected)
		if backend == nil {
			status.SetText("Configure and select a backend before searching.")
			searchButton.Enable()
			return
		}
		// Refresh the mode label at search time too: the backend may
		// have become indexed since the view was built (the user
		// could have visited the Index Manager in between).
		modeLabel.SetText(a.currentSearchMode(backend.Name))
		from, err := parseOptionalDate(fromEntry.Text, false)
		if err != nil {
			status.SetText(err.Error())
			searchButton.Enable()
			return
		}
		to, err := parseOptionalDate(toEntry.Text, true)
		if err != nil {
			status.SetText(err.Error())
			searchButton.Enable()
			return
		}
		favs := map[string]struct{}{}
		if favoritesOnly.Checked {
			for _, f := range a.store.Favorites {
				if f.BackendName == backend.Name {
					favs[filepath.Clean(f.SessionPath)] = struct{}{}
				}
			}
			if len(favs) == 0 {
				status.SetText("No favorites exist for this backend.")
				searchButton.Enable()
				return
			}
		}

		q := search.Query{
			Text:        queryEntry.Text,
			BackendName: backend.Name,
			Paths:       backend.Paths,
			PathFilter:  pathFilterEntry.Text,
			From:        from,
			To:          to,
			Favorites:   favs,
		}
		a.SafeGo(func() {
			log.Println("search: starting")
			ctx := context.Background()

			// Story 11 decision tree: try semantic first, then fall
			// back. "ok=false" means the engine is not linked in or
			// no index exists — silent fallback, no warning.
			// "ok=true, err!=nil" means the engine misbehaved — we
			// log the error and surface a non-fatal warning before
			// falling back, so the user knows the fallback happened.
			resp, ok, semErr := a.trySemanticSearch(ctx, q)
			var (
				searchErr      error
				fallbackReason string
			)
			if ok && semErr == nil {
				searchErr = nil
				log.Printf("search: semantic returned %d result(s)", len(resp.Results))
			} else {
				if ok && semErr != nil {
					log.Printf("search: semantic failed, falling back to lexical: %v", semErr)
					fallbackReason = fmt.Sprintf(
						"semantic search failed, falling back to lexical: %v",
						semErr,
					)
				}
				resp, searchErr = search.Search(q)
				log.Printf("search: lexical returned %d result(s), err=%v",
					len(resp.Results), searchErr)
				if fallbackReason != "" && searchErr == nil {
					// Prepend so the UI shows the fallback reason
					// first; the V1 warnings (file too big, etc.)
					// come after.
					resp.Warnings = append(
						[]string{fallbackReason},
						resp.Warnings...,
					)
				}
			}

			// Build cards outside the UI thread; hand them to fyne.Do for render.
			var cards []fyne.CanvasObject
			if searchErr == nil {
				for _, result := range resp.Results {
					cards = append(cards, a.resultCard(result))
				}
			}

			fyne.Do(func() {
				searchButton.Enable()
				if searchErr != nil {
					status.SetText(searchErr.Error())
					return
				}
				resultsBox.Objects = cards
				resultsBox.Refresh()
				status.SetText(fmt.Sprintf("%d result(s)", len(cards)))
				if len(resp.Warnings) > 0 {
					warnings.SetText("Warnings:\n" + strings.Join(resp.Warnings, "\n"))
				}
				if len(cards) == 0 && len(resp.Warnings) == 0 {
					status.SetText("No results.")
				}
			})
		})
	}

	searchButton.OnTapped = runSearch

	form := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Backend", backendSelect),
			widget.NewFormItem("Mode", modeLabel),
			widget.NewFormItem("Query", queryEntry),
			widget.NewFormItem("Path filter", pathFilterEntry),
			widget.NewFormItem("From", fromEntry),
			widget.NewFormItem("To", toEntry),
		),
		favoritesOnly,
		searchButton,
		status,
		warnings,
	)
	if len(backendNames) == 0 {
		form.Add(widget.NewButton("Open Settings", a.SafeAction(func() { a.nav.Push(a.settingsView()) })))
	}

	header := a.viewHeader("Search")
	return container.NewBorder(
		container.NewVBox(header, form),
		nil,
		nil,
		nil,
		container.NewVScroll(resultsBox),
	)
}

// resultCard builds a card widget for a single search result. Clicking "Open"
// still launches the external viewer; "Details" pushes a detail view onto the
// navigator instead of opening a dialog so the user can read longer content
// comfortably and then navigate back.
func (a *App) resultCard(result search.Result) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(filepath.Base(result.FilePath), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	meta := widget.NewLabel(fmt.Sprintf("%s | line %d | %s", result.BackendName, result.LineNumber, result.FileModTime.Format(time.RFC3339)))
	meta.Wrapping = fyne.TextWrapWord
	path := widget.NewLabel(result.FilePath)
	path.Wrapping = fyne.TextWrapWord
	match := widget.NewLabel(truncateText(result.MatchLine, 300))
	match.Wrapping = fyne.TextWrapWord

	openButton := widget.NewButton("Open", a.SafeAction(func() {
		if err := a.openSession(result.BackendName, result.FilePath); err != nil {
			a.showError("Open Session", err)
		}
	}))
	detailsButton := widget.NewButton("Details", a.SafeAction(func() {
		a.nav.Push(a.resultDetailView(result))
	}))
	favoriteButton := widget.NewButton("Add Favorite", a.SafeAction(func() {
		err := a.store.AddFavorite(storage.Favorite{
			SessionPath: result.FilePath,
			BackendName: result.BackendName,
			Label:       filepath.Base(result.FilePath),
			AddedAt:     time.Now(),
		})
		if err == nil {
			err = a.store.Save()
		}
		if err != nil {
			a.showError("Add Favorite", err)
			return
		}
		dialog.ShowInformation("Favorite", "Favorite saved.", a.main)
	}))

	var indexButton *widget.Button
	if a.isSessionIndexed(result.FilePath) {
		indexButton = widget.NewButton("Indexed ✓", nil)
		indexButton.Disable()
	} else {
		indexButton = widget.NewButton("Index", a.SafeAction(func() {}))
		indexButton.OnTapped = a.SafeAction(func() {
			indexButton.Disable()
			indexButton.SetText("Preparing...")
			a.SafeGo(func() {
				a.indexSessionFromResult(result.BackendName, result.FilePath, func(status string) {
					indexButton.SetText(status)
					if status == "Error" || status == "Cancelled" {
						indexButton.Enable()
						if status == "Cancelled" {
							indexButton.SetText("Index")
						}
					}
				})
			})
		})
	}

	return widget.NewCard("", "", container.NewVBox(title, meta, path, match, container.NewHBox(openButton, detailsButton, favoriteButton, indexButton)))
}

// resultDetailView shows a single result with the full match line and
// metadata. It is pushed onto the navigator stack so the back button returns
// to the search results.
func (a *App) resultDetailView(result search.Result) fyne.CanvasObject {
	header := a.viewHeader("Result")

	title := widget.NewLabelWithStyle(filepath.Base(result.FilePath), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	meta := widget.NewLabel(fmt.Sprintf("Backend: %s\nLine: %d\nModified: %s",
		result.BackendName, result.LineNumber, result.FileModTime.Format(time.RFC3339)))
	meta.Wrapping = fyne.TextWrapWord
	path := widget.NewLabel(result.FilePath)
	path.Wrapping = fyne.TextWrapWord

	match := widget.NewMultiLineEntry()
	match.SetText(truncateText(result.MatchLine, 2000))
	match.Wrapping = fyne.TextWrapWord
	match.Disable() // read-only view of the match content

	actions := container.NewHBox(
		widget.NewButton("Open", a.SafeAction(func() {
			if err := a.openSession(result.BackendName, result.FilePath); err != nil {
				a.showError("Open Session", err)
			}
		})),
		widget.NewButton("Add Favorite", a.SafeAction(func() {
			err := a.store.AddFavorite(storage.Favorite{
				SessionPath: result.FilePath,
				BackendName: result.BackendName,
				Label:       filepath.Base(result.FilePath),
				AddedAt:     time.Now(),
			})
			if err == nil {
				err = a.store.Save()
			}
			if err != nil {
				a.showError("Add Favorite", err)
				return
			}
			dialog.ShowInformation("Favorite", "Favorite saved.", a.main)
		})),
	)

	var indexButton *widget.Button
	if a.isSessionIndexed(result.FilePath) {
		indexButton = widget.NewButton("Indexed ✓", nil)
		indexButton.Disable()
	} else {
		indexButton = widget.NewButton("Index", a.SafeAction(func() {}))
		indexButton.OnTapped = a.SafeAction(func() {
			indexButton.Disable()
			indexButton.SetText("Preparing...")
			a.SafeGo(func() {
				a.indexSessionFromResult(result.BackendName, result.FilePath, func(status string) {
					indexButton.SetText(status)
					if status == "Error" || status == "Cancelled" {
						indexButton.Enable()
						if status == "Cancelled" {
							indexButton.SetText("Index")
						}
					}
				})
			})
		})
	}
	actions.Add(indexButton)

	return container.NewBorder(
		container.NewVBox(header, title, meta, path, actions),
		nil,
		nil,
		nil,
		container.NewVScroll(match),
	)
}

func (a *App) settingsView() fyne.CanvasObject {
	nameEntry := widget.NewEntry()
	categoryEntry := widget.NewEntry()
	pathsEntry := widget.NewMultiLineEntry()
	pathsEntry.SetPlaceHolder("One absolute local source path per line")
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	listBox := container.NewVBox()
	var selectedName string

	refreshList := func() {}
	loadBackend := func(b config.Backend) {
		selectedName = b.Name
		nameEntry.SetText(b.Name)
		categoryEntry.SetText(b.Category)
		pathsEntry.SetText(strings.Join(b.Paths, "\n"))
		status.SetText("Editing " + b.Name)
	}
	refreshList = func() {
		listBox.RemoveAll()
		for _, b := range a.cfg.Backends {
			backend := b
			line := widget.NewButton(fmt.Sprintf("%s (%d path(s))", backend.Name, len(backend.Paths)), a.SafeAction(func() {
				loadBackend(backend)
			}))
			listBox.Add(line)
		}
		if len(a.cfg.Backends) == 0 {
			listBox.Add(widget.NewLabel("No backends configured."))
		}
	}

	saveButton := widget.NewButton("Save Backend", a.SafeAction(func() {
		backend := config.Backend{
			Name:     nameEntry.Text,
			Category: categoryEntry.Text,
			Paths:    splitLines(pathsEntry.Text),
		}
		if err := a.cfg.UpsertBackend(selectedName, backend); err != nil {
			status.SetText(err.Error())
			return
		}
		if err := a.cfg.Save(); err != nil {
			status.SetText(err.Error())
			return
		}
		selectedName = config.NormalizeBackend(backend).Name
		status.SetText("Backend saved.")
		refreshList()
	}))
	newButton := widget.NewButton("New Backend", a.SafeAction(func() {
		selectedName = ""
		nameEntry.SetText("")
		categoryEntry.SetText("")
		pathsEntry.SetText("")
		status.SetText("Creating new backend.")
	}))
	deleteButton := widget.NewButton("Delete Backend", a.SafeAction(func() {
		if selectedName == "" {
			status.SetText("Select a backend before deleting.")
			return
		}
		if !a.cfg.RemoveBackend(selectedName) {
			status.SetText("Selected backend no longer exists.")
			return
		}
		if err := a.cfg.Save(); err != nil {
			status.SetText(err.Error())
			return
		}
		selectedName = ""
		nameEntry.SetText("")
		categoryEntry.SetText("")
		pathsEntry.SetText("")
		status.SetText("Backend deleted.")
		refreshList()
	}))

	refreshList()
	form := widget.NewForm(
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Category", categoryEntry),
		widget.NewFormItem("Source paths", pathsEntry),
	)
	header := a.viewHeader("Settings")
	return container.NewBorder(
		container.NewVBox(header, widget.NewLabelWithStyle("Backends", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), listBox),
		container.NewVBox(status),
		nil,
		nil,
		container.NewVBox(form, container.NewHBox(saveButton, newButton, deleteButton)),
	)
}

func (a *App) favoritesView() fyne.CanvasObject {
	box := container.NewVBox()
	var refresh func()
	refresh = func() {
		box.RemoveAll()
		if len(a.store.Favorites) == 0 {
			box.Add(widget.NewLabel("No favorites saved."))
			box.Refresh()
			return
		}
		for _, favorite := range a.store.Favorites {
			f := favorite
			title := widget.NewLabelWithStyle(f.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			meta := widget.NewLabel(fmt.Sprintf("%s | %s", f.BackendName, f.AddedAt.Format(time.RFC3339)))
			path := widget.NewLabel(f.SessionPath)
			path.Wrapping = fyne.TextWrapWord
			openButton := widget.NewButton("Open", a.SafeAction(func() {
				if err := a.openSession(f.BackendName, f.SessionPath); err != nil {
					a.showError("Open Favorite", err)
				}
			}))
			removeButton := widget.NewButton("Remove", a.SafeAction(func() {
				a.store.RemoveFavorite(f.BackendName, f.SessionPath)
				if err := a.store.Save(); err != nil {
					a.showError("Remove Favorite", err)
					return
				}
				refresh()
			}))
			box.Add(widget.NewCard("", "", container.NewVBox(title, meta, path, container.NewHBox(openButton, removeButton))))
		}
		box.Refresh()
	}
	refresh()
	header := a.viewHeader("Favorites")
	return container.NewBorder(header, nil, nil, nil, container.NewVScroll(box))
}

func (a *App) recentView() fyne.CanvasObject {
	box := container.NewVBox()
	var refresh func()
	refresh = func() {
		box.RemoveAll()
		if len(a.store.Recents) == 0 {
			box.Add(widget.NewLabel("No recent sessions."))
			box.Refresh()
			return
		}
		for _, recent := range a.store.Recents {
			r := recent
			title := widget.NewLabelWithStyle(filepath.Base(r.SessionPath), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			meta := widget.NewLabel(fmt.Sprintf("%s | %s", r.BackendName, r.OpenedAt.Format(time.RFC3339)))
			path := widget.NewLabel(r.SessionPath)
			path.Wrapping = fyne.TextWrapWord
			openButton := widget.NewButton("Open", a.SafeAction(func() {
				if err := a.openSession(r.BackendName, r.SessionPath); err != nil {
					a.showError("Open Recent", err)
				}
			}))
			box.Add(widget.NewCard("", "", container.NewVBox(title, meta, path, openButton)))
		}
		box.Refresh()
	}
	clearButton := widget.NewButton("Clear Recent", a.SafeAction(func() {
		a.store.ClearRecents()
		if err := a.store.Save(); err != nil {
			a.showError("Clear Recent", err)
			return
		}
		refresh()
	}))
	refresh()
	header := a.viewHeader("Recent")
	return container.NewBorder(header, clearButton, nil, nil, container.NewVScroll(box))
}

// viewHeader returns a consistent header row with the back button (hidden at
// root) and a title label. Used by every non-home view.
func (a *App) viewHeader(title string) fyne.CanvasObject {
	back := a.nav.backButton()
	label := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewHBox(back, label)
}

func (a *App) openSession(backendName, sessionPath string) error {
	sessionPath = filepath.Clean(strings.TrimSpace(sessionPath))
	if sessionPath == "" || sessionPath == "." {
		return errors.New("session path is required")
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, expected a session file", sessionPath)
	}
	if !filepath.IsAbs(sessionPath) {
		return fmt.Errorf("session path must be absolute: %s", sessionPath)
	}
	if strings.Contains(sessionPath, "://") {
		return fmt.Errorf("session path must not contain a URI scheme: %s", sessionPath)
	}
	if err := openPath(sessionPath); err != nil {
		return err
	}
	if err := a.store.AddRecent(storage.RecentRef{
		SessionPath: sessionPath,
		BackendName: backendName,
		OpenedAt:    time.Now(),
	}); err != nil {
		return err
	}
	return a.store.Save()
}

func (a *App) backendNames() []string {
	names := make([]string, 0, len(a.cfg.Backends))
	for _, b := range a.cfg.Backends {
		names = append(names, b.Name)
	}
	return names
}

// showError displays an error dialog anchored to the single main window. The
// old two-window variant (showErrorIn) was removed along with the per-view
// windows.
func (a *App) showError(title string, err error) {
	if err == nil {
		return
	}
	if a.main == nil {
		log.Printf("%s: %v", title, err)
		return
	}
	dialog.ShowError(fmt.Errorf("%s: %w", title, err), a.main)
}

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func parseOptionalDate(text string, endOfDay bool) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q, use YYYY-MM-DD", text)
	}
	if endOfDay {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t, nil
}

func openPath(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// recoverPanic catches panics from background goroutines and safe UI callbacks.
func (a *App) recoverPanic() {
	if r := recover(); r != nil {
		stack := make([]byte, 4096)
		length := runtime.Stack(stack, false)
		errStr := fmt.Sprintf("Panic: %v\n\nStack Trace:\n%s", r, stack[:length])

		log.Println(errStr)

		fyne.Do(func() {
			a.showCrashDialog(errStr)
		})
	}
}

// showCrashDialog displays the crash details and allows copying to the clipboard.
func (a *App) showCrashDialog(errStr string) {
	if a.fyneApp == nil {
		return
	}
	w := a.fyneApp.NewWindow("Application Error")

	errText := widget.NewMultiLineEntry()
	errText.SetText(errStr)
	errText.Wrapping = fyne.TextWrapWord

	copyBtn := widget.NewButton("Copy to Clipboard", a.SafeAction(func() {
		w.Clipboard().SetContent(errStr)
	}))

	closeBtn := widget.NewButton("Close", a.SafeAction(func() {
		w.Close()
	}))

	content := container.NewBorder(
		widget.NewLabelWithStyle("An unexpected error occurred. Please copy the details below and report to the admin.", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(copyBtn, closeBtn),
		nil, nil,
		errText,
	)
	w.SetContent(content)
	w.Resize(fyne.NewSize(600, 400))
	w.Show()
}

// SafeGo spawns a goroutine wrapped with the global panic handler.
func (a *App) SafeGo(fn func()) {
	go func() {
		defer a.recoverPanic()
		fn()
	}()
}

// SafeAction wraps a UI callback with the global panic handler.
func (a *App) SafeAction(fn func()) func() {
	if fn == nil {
		return nil
	}
	return func() {
		defer a.recoverPanic()
		fn()
	}
}

// truncateText safely truncates a string to a maximum number of runes
// without allocating large temporary rune slices.
func truncateText(text string, maxRunes int) string {
	if len(text) <= maxRunes { // fast path if byte length is smaller than max runes
		return text
	}
	count := 0
	for i := range text {
		if count >= maxRunes {
			return text[:i] + "..."
		}
		count++
	}
	return text
}
