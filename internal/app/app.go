// Package app wires the Beddel Desk desktop UI to the local-only domain packages.
package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/botanarede/beddel-desk-go/internal/config"
	"github.com/botanarede/beddel-desk-go/internal/search"
	"github.com/botanarede/beddel-desk-go/internal/storage"
	"github.com/botanarede/beddel-desk-go/internal/version"
)

const appID = "com.botanarede.beddel-desk"

// App owns the desktop UI state and lightweight local persistence.
type App struct {
	fyneApp fyne.App
	main    fyne.Window
	cfg     *config.AppConfig
	store   *storage.Store
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
	desk.main.SetMainMenu(desk.mainMenu())
	if desktopApp, ok := fyneApp.(desktop.App); ok {
		desktopApp.SetSystemTrayMenu(desk.trayMenu())
	}
	desk.main.SetContent(desk.homeView())
	desk.main.SetCloseIntercept(func() {
		desk.main.Hide()
	})

	if cfgErr != nil || storeErr != nil {
		desk.showError("Startup", errors.Join(cfgErr, storeErr))
	}

	desk.main.ShowAndRun()
}

func (a *App) mainMenu() *fyne.MainMenu {
	return fyne.NewMainMenu(a.trayMenu())
}

func (a *App) trayMenu() *fyne.Menu {
	return fyne.NewMenu("Beddel Desk",
		fyne.NewMenuItem("Search", a.showSearch),
		fyne.NewMenuItem("Favorites", a.showFavorites),
		fyne.NewMenuItem("Recent", a.showRecent),
		fyne.NewMenuItem("Settings", a.showSettings),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", func() { a.fyneApp.Quit() }),
	)
}

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
			widget.NewButton("Search", a.showSearch),
			widget.NewButton("Favorites", a.showFavorites),
			widget.NewButton("Recent", a.showRecent),
			widget.NewButton("Settings", a.showSettings),
			widget.NewLabel(version.String()),
		)),
	)
}

func (a *App) showSearch() {
	win := a.fyneApp.NewWindow("Search - Beddel Desk")
	win.Resize(fyne.NewSize(1100, 720))

	backendNames := a.backendNames()
	backendSelect := widget.NewSelect(backendNames, nil)
	if len(backendNames) > 0 {
		backendSelect.SetSelected(backendNames[0])
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
		go func() {
			resp, err := search.Search(q)
			searchButton.Enable()
			if err != nil {
				status.SetText(err.Error())
				return
			}
			for _, result := range resp.Results {
				resultsBox.Add(a.resultCard(result, win))
			}
			status.SetText(fmt.Sprintf("%d result(s)", len(resp.Results)))
			if len(resp.Warnings) > 0 {
				warnings.SetText("Warnings:\n" + strings.Join(resp.Warnings, "\n"))
			}
			if len(resp.Results) == 0 && len(resp.Warnings) == 0 {
				status.SetText("No results.")
			}
		}()
	}

	searchButton.OnTapped = runSearch

	form := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Backend", backendSelect),
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
		form.Add(widget.NewButton("Open Settings", a.showSettings))
	}

	win.SetContent(container.NewBorder(form, nil, nil, nil, container.NewVScroll(resultsBox)))
	win.Show()
}

func (a *App) resultCard(result search.Result, parent fyne.Window) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(filepath.Base(result.FilePath), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	meta := widget.NewLabel(fmt.Sprintf("%s | line %d | %s", result.BackendName, result.LineNumber, result.FileModTime.Format(time.RFC3339)))
	meta.Wrapping = fyne.TextWrapWord
	path := widget.NewLabel(result.FilePath)
	path.Wrapping = fyne.TextWrapWord
	match := widget.NewLabel(result.MatchLine)
	match.Wrapping = fyne.TextWrapWord

	openButton := widget.NewButton("Open", func() {
		if err := a.openSession(result.BackendName, result.FilePath); err != nil {
			a.showErrorIn(parent, "Open Session", err)
		}
	})
	favoriteButton := widget.NewButton("Add Favorite", func() {
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
			a.showErrorIn(parent, "Add Favorite", err)
			return
		}
		dialog.ShowInformation("Favorite", "Favorite saved.", parent)
	})

	return widget.NewCard("", "", container.NewVBox(title, meta, path, match, container.NewHBox(openButton, favoriteButton)))
}

func (a *App) showSettings() {
	win := a.fyneApp.NewWindow("Settings - Beddel Desk")
	win.Resize(fyne.NewSize(900, 680))

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
			line := widget.NewButton(fmt.Sprintf("%s (%d path(s))", backend.Name, len(backend.Paths)), func() {
				loadBackend(backend)
			})
			listBox.Add(line)
		}
		if len(a.cfg.Backends) == 0 {
			listBox.Add(widget.NewLabel("No backends configured."))
		}
	}

	saveButton := widget.NewButton("Save Backend", func() {
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
	})
	newButton := widget.NewButton("New Backend", func() {
		selectedName = ""
		nameEntry.SetText("")
		categoryEntry.SetText("")
		pathsEntry.SetText("")
		status.SetText("Creating new backend.")
	})
	deleteButton := widget.NewButton("Delete Backend", func() {
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
	})

	refreshList()
	form := widget.NewForm(
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Category", categoryEntry),
		widget.NewFormItem("Source paths", pathsEntry),
	)
	win.SetContent(container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Backends", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), listBox),
		container.NewVBox(status),
		nil,
		nil,
		container.NewVBox(form, container.NewHBox(saveButton, newButton, deleteButton)),
	))
	win.Show()
}

func (a *App) showFavorites() {
	win := a.fyneApp.NewWindow("Favorites - Beddel Desk")
	win.Resize(fyne.NewSize(900, 620))
	box := container.NewVBox()
	refresh := func() {}
	refresh = func() {
		box.RemoveAll()
		if len(a.store.Favorites) == 0 {
			box.Add(widget.NewLabel("No favorites saved."))
			return
		}
		for _, favorite := range a.store.Favorites {
			f := favorite
			title := widget.NewLabelWithStyle(f.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			meta := widget.NewLabel(fmt.Sprintf("%s | %s", f.BackendName, f.AddedAt.Format(time.RFC3339)))
			path := widget.NewLabel(f.SessionPath)
			path.Wrapping = fyne.TextWrapWord
			openButton := widget.NewButton("Open", func() {
				if err := a.openSession(f.BackendName, f.SessionPath); err != nil {
					a.showErrorIn(win, "Open Favorite", err)
				}
			})
			removeButton := widget.NewButton("Remove", func() {
				a.store.RemoveFavorite(f.BackendName, f.SessionPath)
				if err := a.store.Save(); err != nil {
					a.showErrorIn(win, "Remove Favorite", err)
					return
				}
				refresh()
			})
			box.Add(widget.NewCard("", "", container.NewVBox(title, meta, path, container.NewHBox(openButton, removeButton))))
		}
	}
	refresh()
	win.SetContent(container.NewVScroll(box))
	win.Show()
}

func (a *App) showRecent() {
	win := a.fyneApp.NewWindow("Recent - Beddel Desk")
	win.Resize(fyne.NewSize(900, 620))
	box := container.NewVBox()
	refresh := func() {}
	refresh = func() {
		box.RemoveAll()
		if len(a.store.Recents) == 0 {
			box.Add(widget.NewLabel("No recent sessions."))
			return
		}
		for _, recent := range a.store.Recents {
			r := recent
			title := widget.NewLabelWithStyle(filepath.Base(r.SessionPath), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			meta := widget.NewLabel(fmt.Sprintf("%s | %s", r.BackendName, r.OpenedAt.Format(time.RFC3339)))
			path := widget.NewLabel(r.SessionPath)
			path.Wrapping = fyne.TextWrapWord
			openButton := widget.NewButton("Open", func() {
				if err := a.openSession(r.BackendName, r.SessionPath); err != nil {
					a.showErrorIn(win, "Open Recent", err)
				}
			})
			box.Add(widget.NewCard("", "", container.NewVBox(title, meta, path, openButton)))
		}
	}
	clearButton := widget.NewButton("Clear Recent", func() {
		a.store.ClearRecents()
		if err := a.store.Save(); err != nil {
			a.showErrorIn(win, "Clear Recent", err)
			return
		}
		refresh()
	})
	refresh()
	win.SetContent(container.NewBorder(nil, clearButton, nil, nil, container.NewVScroll(box)))
	win.Show()
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

func (a *App) showError(title string, err error) {
	if err == nil {
		return
	}
	a.showErrorIn(a.main, title, err)
}

func (a *App) showErrorIn(parent fyne.Window, title string, err error) {
	if err == nil {
		return
	}
	dialog.ShowError(fmt.Errorf("%s: %w", title, err), parent)
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
