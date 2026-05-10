package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// progressInterval caps the rate at which the download routine is
// allowed to invoke the user-provided progress callback. The architecture
// document flags high-frequency progress updates as a source of UI
// jitter on Fyne; the package debounces to at most one call per
// progressInterval.
const progressInterval = 200 * time.Millisecond

// modelDirName is the per-version cache directory holding the model and
// tokenizer files. The directory name embeds the version so an
// incompatible upgrade can be downloaded side by side without clobbering
// the previous copy.
const modelBaseName = "all-MiniLM-L6-v2"

// Assets collects the absolute paths of the resolved files. Callers
// forward these to the embedder without needing to know whether the
// runtime was reused from the system or downloaded on demand.
type Assets struct {
	RuntimeLibraryPath string
	RuntimeSource      Source
	ModelPath          string
	TokenizerPath      string
}

// Manager coordinates the probe, download, verification, and cache
// layout for the ONNX runtime and the all-MiniLM-L6-v2 model. A Manager
// is safe for serial use by a single goroutine; concurrent calls to
// EnsureAssets against the same cache root are not supported by design,
// because the UI runs one indexing action at a time.
type Manager struct {
	cacheRoot string
	client    *http.Client
	manifest  Manifest

	// probeFn is swapped in tests to bypass host-dependent probing.
	probeFn func() (string, error)

	// now is swapped in tests that want a deterministic clock; it is
	// reserved for future use and currently only drives the debounced
	// progress emitter.
	now func() time.Time
}

// NewManager returns a Manager bound to the given cache root and HTTP
// client. The client is required so tests can inject an httptest
// transport; production callers should construct a client with the
// desired timeouts rather than rely on http.DefaultClient.
func NewManager(cacheRoot string, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{}
	}
	return &Manager{
		cacheRoot: cacheRoot,
		client:    client,
		manifest:  DefaultManifest,
		probeFn:   probeSystemRuntime,
		now:       time.Now,
	}
}

// EnsureAssets resolves every asset required to run semantic search. It
// short-circuits when the cache already contains every file; otherwise
// it probes the system for a compatible runtime and downloads what is
// still missing. The report callback is optional and may be nil.
func (m *Manager) EnsureAssets(ctx context.Context, report func(Progress)) (*Assets, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	runtimeEntry, err := runtimeEntryFor(m.manifest, goos, goarch)
	if err != nil {
		return nil, err
	}

	emitter := newProgressEmitter(report, progressInterval, m.now)
	emitter.emitForced(Progress{Stage: stageProbing, AssetName: string(assetRuntime)})

	runtimePath, runtimeSource, err := m.resolveRuntime(ctx, goos, goarch, runtimeEntry, emitter)
	if err != nil {
		return nil, err
	}

	modelPath, err := m.resolveGlobalAsset(ctx, assetModel, m.manifest.Model, "model.onnx", emitter)
	if err != nil {
		return nil, err
	}
	tokenizerPath, err := m.resolveGlobalAsset(ctx, assetTokenizer, m.manifest.Tokenizer, "tokenizer.json", emitter)
	if err != nil {
		return nil, err
	}

	emitter.emitForced(Progress{Stage: stageReady})
	return &Assets{
		RuntimeLibraryPath: runtimePath,
		RuntimeSource:      runtimeSource,
		ModelPath:          modelPath,
		TokenizerPath:      tokenizerPath,
	}, nil
}

// ClearCache removes the entire cache subtree managed by this Manager.
// A missing directory is treated as success so the caller can call this
// defensively on startup.
func (m *Manager) ClearCache() error {
	if m.cacheRoot == "" {
		return errors.New("download: empty cache root")
	}
	if err := os.RemoveAll(m.cacheRoot); err != nil {
		return fmt.Errorf("download: clear cache: %w", err)
	}
	return nil
}

// runtimeCachePath returns the cache-resident path for the runtime
// library given a specific (goos, goarch, version) triple.
func (m *Manager) runtimeCachePath(goos, goarch, version string) string {
	return filepath.Join(
		m.cacheRoot,
		"onnxruntime",
		version,
		goos+"-"+goarch,
		runtimeLibFileName(goos),
	)
}

// modelCacheDir returns the directory that holds the model and
// tokenizer files for the given model version.
func (m *Manager) modelCacheDir(version string) string {
	return filepath.Join(m.cacheRoot, "models", modelBaseName+"-"+version)
}

// resolveRuntime applies the cache, probe, download precedence for the
// ONNX runtime shared library.
func (m *Manager) resolveRuntime(ctx context.Context, goos, goarch string, entry Entry, emitter *progressEmitter) (string, Source, error) {
	cachePath := m.runtimeCachePath(goos, goarch, entry.Version)
	if fileExists(cachePath) {
		return cachePath, SourceDownloaded, nil
	}
	if m.probeFn != nil {
		systemPath, err := m.probeFn()
		if err != nil {
			return "", "", fmt.Errorf("download: probe system runtime: %w", err)
		}
		if systemPath != "" {
			return systemPath, SourceSystem, nil
		}
	}
	if entry.SHA256 == PlaceholderSHA256 {
		return "", "", fmt.Errorf("%w: %s", ErrPlaceholderChecksum, assetRuntime)
	}

	ext := strings.ToLower(filepath.Ext(entry.URL))
	isArchive := ext == ".tgz" || ext == ".zip" || strings.HasSuffix(strings.ToLower(entry.URL), ".tar.gz")

	destPath := cachePath
	if isArchive {
		destPath = cachePath + ".archive" + ext
	}

	if err := m.downloadTo(ctx, string(assetRuntime), entry, destPath, emitter); err != nil {
		return "", "", err
	}

	if isArchive {
		if err := extractSharedLib(destPath, cachePath); err != nil {
			_ = os.Remove(destPath)
			return "", "", fmt.Errorf("download: extract runtime: %w", err)
		}
		_ = os.Remove(destPath)
	}

	return cachePath, SourceDownloaded, nil
}

// resolveGlobalAsset applies the cache, download precedence for the
// model and tokenizer, both of which are platform-independent.
func (m *Manager) resolveGlobalAsset(ctx context.Context, kind assetKind, entry Entry, fileName string, emitter *progressEmitter) (string, error) {
	dest := filepath.Join(m.modelCacheDir(entry.Version), fileName)
	if fileExists(dest) {
		return dest, nil
	}
	if entry.SHA256 == PlaceholderSHA256 {
		return "", fmt.Errorf("%w: %s", ErrPlaceholderChecksum, kind)
	}
	if err := m.downloadTo(ctx, string(kind), entry, dest, emitter); err != nil {
		return "", err
	}
	return dest, nil
}

// downloadTo fetches entry.URL into dest atomically. Bytes are streamed
// through a sha256.Hash; if the digest disagrees with entry.SHA256 the
// partial file is removed and ErrChecksumMismatch (wrapped with the
// asset name) is returned. The file lands in place via rename only
// after fsync.
func (m *Manager) downloadTo(ctx context.Context, assetName string, entry Entry, dest string, emitter *progressEmitter) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("download: %s: create cache dir: %w", assetName, err)
	}
	partial := dest + ".partial"
	// Remove any leftover partial from a previous aborted run so the
	// sha256 computation starts from a clean file.
	_ = os.Remove(partial)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return fmt.Errorf("download: %s: build request: %w", assetName, err)
	}
	// Force identity encoding so the bytes on the wire match the bytes
	// we hash. Transparent gzip would otherwise break SHA-256 equality.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %s: http error: %w", assetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download: %s: unexpected status %d", assetName, resp.StatusCode)
	}

	emitter.emitForced(Progress{
		Stage:     stageDownloading,
		AssetName: assetName,
		Total:     resp.ContentLength,
	})

	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("download: %s: open partial: %w", assetName, err)
	}

	hasher := sha256.New()
	counter := &progressWriter{
		emitter:   emitter,
		assetName: assetName,
		total:     resp.ContentLength,
	}
	writer := io.MultiWriter(f, hasher, counter)
	reader := &contextReader{ctx: ctx, src: resp.Body}

	if _, err := io.Copy(writer, reader); err != nil {
		f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("download: %s: copy body: %w", assetName, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("download: %s: fsync: %w", assetName, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("download: %s: close partial: %w", assetName, err)
	}

	emitter.emitForced(Progress{
		Stage:     stageVerifying,
		AssetName: assetName,
		Current:   counter.current,
		Total:     counter.total,
	})

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != entry.SHA256 {
		_ = os.Remove(partial)
		return fmt.Errorf("%w: %s: expected %s, got %s", ErrChecksumMismatch, assetName, entry.SHA256, got)
	}
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("download: %s: rename into place: %w", assetName, err)
	}
	return nil
}

// fileExists reports whether path resolves to a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// progressWriter is an io.Writer that counts bytes and emits debounced
// progress updates via the shared emitter.
type progressWriter struct {
	emitter   *progressEmitter
	assetName string
	current   int64
	total     int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.current += int64(n)
	p.emitter.emit(Progress{
		Stage:     stageDownloading,
		AssetName: p.assetName,
		Current:   p.current,
		Total:     p.total,
	})
	return n, nil
}

// contextReader is a tiny wrapper that returns ctx.Err() between
// reads, giving the downloader prompt cancellation without having to
// close the response body manually.
type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}

// progressEmitter debounces progress callbacks to at most one per
// interval. It is safe for use from a single goroutine, which matches
// the download loop. A nil callback is accepted and silently ignored.
type progressEmitter struct {
	mu       sync.Mutex
	cb       func(Progress)
	interval time.Duration
	now      func() time.Time
	last     time.Time
}

func newProgressEmitter(cb func(Progress), interval time.Duration, now func() time.Time) *progressEmitter {
	if now == nil {
		now = time.Now
	}
	return &progressEmitter{cb: cb, interval: interval, now: now}
}

// emit delivers p to the callback if enough time has passed since the
// last emission. Stage-change events should be delivered via
// emitForced instead, so the UI always sees the transition.
func (e *progressEmitter) emit(p Progress) {
	if e == nil || e.cb == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if !e.last.IsZero() && now.Sub(e.last) < e.interval {
		return
	}
	e.last = now
	e.cb(p)
}

// emitForced delivers p unconditionally, bypassing the debounce.
func (e *progressEmitter) emitForced(p Progress) {
	if e == nil || e.cb == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.last = e.now()
	e.cb(p)
}
