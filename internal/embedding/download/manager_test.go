package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testManifest builds a manifest whose runtime entry matches the current
// GOOS/GOARCH and whose model/tokenizer URLs point at the supplied
// server. The bodies and hashes are pre-computed by the caller.
func testManifest(srv *httptest.Server, runtimeBody, modelBody, tokBody []byte) Manifest {
	return Manifest{
		Runtimes: map[PlatformKey]Entry{
			{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}: {
				URL:     srv.URL + "/runtime",
				SHA256:  sha256Hex(runtimeBody),
				Size:    int64(len(runtimeBody)),
				Version: "test-rt-1",
			},
		},
		Model: Entry{
			URL:     srv.URL + "/model",
			SHA256:  sha256Hex(modelBody),
			Size:    int64(len(modelBody)),
			Version: "test-model-1",
		},
		Tokenizer: Entry{
			URL:     srv.URL + "/tokenizer",
			SHA256:  sha256Hex(tokBody),
			Size:    int64(len(tokBody)),
			Version: "test-model-1",
		},
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newBodyServer returns an httptest.Server that serves fixed bodies per
// route. It records hit counts so tests can detect unintended network
// reuse on a cached run.
type bodyServer struct {
	*httptest.Server
	hits map[string]*int64
}

func newBodyServer(t *testing.T, routes map[string][]byte) *bodyServer {
	t.Helper()
	hits := map[string]*int64{}
	for route := range routes {
		var n int64
		hits[route] = &n
	}
	mux := http.NewServeMux()
	for route, body := range routes {
		route := route
		body := body
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(hits[route], 1)
			w.Header().Set("Content-Length", itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &bodyServer{Server: srv, hits: hits}
}

func itoa(n int) string {
	// strconv.Itoa would require an import; keep the test file lean.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// newManagerForTest returns a manager that points at cacheRoot and
// disables the system runtime probe so the test can drive the download
// path deterministically.
func newManagerForTest(t *testing.T, cacheRoot string, m Manifest) *Manager {
	t.Helper()
	mgr := NewManager(cacheRoot, &http.Client{Timeout: 5 * time.Second})
	mgr.manifest = m
	mgr.probeFn = func() (string, error) { return "", nil }
	return mgr
}

// TestEnsureAssetsFreshDownloadValidChecksum exercises the happy path:
// fresh cache, valid checksum, three downloads, final files in place.
func TestEnsureAssetsFreshDownloadValidChecksum(t *testing.T) {
	runtimeBody := []byte("runtime-bytes")
	modelBody := []byte("model-bytes")
	tokBody := []byte("tokenizer-bytes")
	srv := newBodyServer(t, map[string][]byte{
		"/runtime":   runtimeBody,
		"/model":     modelBody,
		"/tokenizer": tokBody,
	})
	cacheRoot := t.TempDir()
	mgr := newManagerForTest(t, cacheRoot, testManifest(srv.Server, runtimeBody, modelBody, tokBody))

	assets, err := mgr.EnsureAssets(context.Background(), nil)
	if err != nil {
		t.Fatalf("EnsureAssets: %v", err)
	}
	if assets.RuntimeSource != SourceDownloaded {
		t.Fatalf("expected SourceDownloaded, got %q", assets.RuntimeSource)
	}
	if !fileExists(assets.RuntimeLibraryPath) {
		t.Fatalf("runtime library missing at %q", assets.RuntimeLibraryPath)
	}
	if !fileExists(assets.ModelPath) {
		t.Fatalf("model missing at %q", assets.ModelPath)
	}
	if !fileExists(assets.TokenizerPath) {
		t.Fatalf("tokenizer missing at %q", assets.TokenizerPath)
	}
	// No partial files must remain after a successful install.
	matches, _ := filepath.Glob(filepath.Join(cacheRoot, "**", "*.partial"))
	if len(matches) != 0 {
		t.Fatalf("expected no partial files, found %v", matches)
	}
}

// TestEnsureAssetsBadChecksumRemovesPartial validates that a corrupt
// response removes the partial file and returns an error naming the
// asset.
func TestEnsureAssetsBadChecksumRemovesPartial(t *testing.T) {
	runtimeBody := []byte("runtime-bytes")
	modelBody := []byte("model-bytes")
	tokBody := []byte("tokenizer-bytes")
	srv := newBodyServer(t, map[string][]byte{
		"/runtime":   runtimeBody,
		"/model":     modelBody,
		"/tokenizer": tokBody,
	})
	manifest := testManifest(srv.Server, runtimeBody, modelBody, tokBody)
	// Force a mismatch on the runtime entry.
	key := PlatformKey{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	rt := manifest.Runtimes[key]
	rt.SHA256 = strings.Repeat("a", 64)
	manifest.Runtimes[key] = rt

	cacheRoot := t.TempDir()
	mgr := newManagerForTest(t, cacheRoot, manifest)

	_, err := mgr.EnsureAssets(context.Background(), nil)
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), string(assetRuntime)) {
		t.Fatalf("expected error to name the asset, got %v", err)
	}
	// The partial file must not linger in the cache.
	var stragglers []string
	_ = filepath.Walk(cacheRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info != nil && !info.IsDir() && strings.HasSuffix(path, ".partial") {
			stragglers = append(stragglers, path)
		}
		return nil
	})
	if len(stragglers) != 0 {
		t.Fatalf("partial files left behind: %v", stragglers)
	}
}

// TestEnsureAssetsCachedCallSkipsNetwork runs EnsureAssets twice and
// asserts the second call hits the server zero times.
func TestEnsureAssetsCachedCallSkipsNetwork(t *testing.T) {
	runtimeBody := []byte("runtime-bytes")
	modelBody := []byte("model-bytes")
	tokBody := []byte("tokenizer-bytes")
	srv := newBodyServer(t, map[string][]byte{
		"/runtime":   runtimeBody,
		"/model":     modelBody,
		"/tokenizer": tokBody,
	})
	cacheRoot := t.TempDir()
	mgr := newManagerForTest(t, cacheRoot, testManifest(srv.Server, runtimeBody, modelBody, tokBody))

	if _, err := mgr.EnsureAssets(context.Background(), nil); err != nil {
		t.Fatalf("first EnsureAssets: %v", err)
	}
	// Record the baseline request counts.
	baseline := map[string]int64{}
	for route, counter := range srv.hits {
		baseline[route] = atomic.LoadInt64(counter)
	}
	if _, err := mgr.EnsureAssets(context.Background(), nil); err != nil {
		t.Fatalf("second EnsureAssets: %v", err)
	}
	for route, counter := range srv.hits {
		if atomic.LoadInt64(counter) != baseline[route] {
			t.Fatalf("route %q was fetched on the cached call", route)
		}
	}
}

// slowHandler writes body one byte at a time with a small delay so that
// context cancellation can interleave reliably.
func slowHandler(body []byte, perByteDelay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		flusher, _ := w.(http.Flusher)
		for i := 0; i < len(body); i++ {
			if _, err := w.Write(body[i : i+1]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(perByteDelay):
			}
		}
	})
}

// TestEnsureAssetsContextCancellationMidDownload cancels the caller's
// context after the first few bytes and asserts EnsureAssets returns
// the context error.
func TestEnsureAssetsContextCancellationMidDownload(t *testing.T) {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte(i % 251)
	}
	mux := http.NewServeMux()
	mux.Handle("/runtime", slowHandler(body, 5*time.Millisecond))
	mux.Handle("/model", slowHandler(body, 5*time.Millisecond))
	mux.Handle("/tokenizer", slowHandler(body, 5*time.Millisecond))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	manifest := Manifest{
		Runtimes: map[PlatformKey]Entry{
			{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}: {
				URL: srv.URL + "/runtime", SHA256: sha256Hex(body), Size: int64(len(body)), Version: "test-rt-1",
			},
		},
		Model:     Entry{URL: srv.URL + "/model", SHA256: sha256Hex(body), Size: int64(len(body)), Version: "test-model-1"},
		Tokenizer: Entry{URL: srv.URL + "/tokenizer", SHA256: sha256Hex(body), Size: int64(len(body)), Version: "test-model-1"},
	}
	cacheRoot := t.TempDir()
	mgr := newManagerForTest(t, cacheRoot, manifest)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := mgr.EnsureAssets(ctx, nil)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestEnsureAssetsUnsupportedPlatform swaps the manifest for one that
// does not list the current GOOS/GOARCH and asserts the typed error is
// surfaced.
func TestEnsureAssetsUnsupportedPlatform(t *testing.T) {
	cacheRoot := t.TempDir()
	mgr := NewManager(cacheRoot, &http.Client{})
	mgr.probeFn = func() (string, error) { return "", nil }
	mgr.manifest = Manifest{
		Runtimes:  map[PlatformKey]Entry{{GOOS: "plan9", GOARCH: "mips64"}: {URL: "http://example.invalid", SHA256: sha256Hex(nil), Size: 1, Version: "x"}},
		Model:     DefaultManifest.Model,
		Tokenizer: DefaultManifest.Tokenizer,
	}

	_, err := mgr.EnsureAssets(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ErrUnsupportedPlatform, got nil")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("expected ErrUnsupportedPlatform, got %v", err)
	}
}

// TestEnsureAssetsRuntimeFromSystemProbe points the probe at a planted
// library file and asserts no download is performed and RuntimeSource
// is SourceSystem.
func TestEnsureAssetsRuntimeFromSystemProbe(t *testing.T) {
	modelBody := []byte("model-bytes")
	tokBody := []byte("tokenizer-bytes")
	srv := newBodyServer(t, map[string][]byte{
		"/model":     modelBody,
		"/tokenizer": tokBody,
	})
	cacheRoot := t.TempDir()
	fakeLib := filepath.Join(t.TempDir(), runtimeLibFileName(runtime.GOOS))
	if err := os.WriteFile(fakeLib, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{
		Runtimes: map[PlatformKey]Entry{
			{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}: {
				// A placeholder SHA256 here is fine: the probe hit means
				// we never try to download or verify the runtime.
				URL: "http://example.invalid/runtime", SHA256: PlaceholderSHA256, Size: 1, Version: "test-rt-1",
			},
		},
		Model:     Entry{URL: srv.URL + "/model", SHA256: sha256Hex(modelBody), Size: int64(len(modelBody)), Version: "test-model-1"},
		Tokenizer: Entry{URL: srv.URL + "/tokenizer", SHA256: sha256Hex(tokBody), Size: int64(len(tokBody)), Version: "test-model-1"},
	}
	mgr := NewManager(cacheRoot, &http.Client{Timeout: 5 * time.Second})
	mgr.manifest = manifest
	mgr.probeFn = func() (string, error) { return fakeLib, nil }

	assets, err := mgr.EnsureAssets(context.Background(), nil)
	if err != nil {
		t.Fatalf("EnsureAssets: %v", err)
	}
	if assets.RuntimeSource != SourceSystem {
		t.Fatalf("expected SourceSystem, got %q", assets.RuntimeSource)
	}
	if assets.RuntimeLibraryPath != fakeLib {
		t.Fatalf("runtime path %q, want %q", assets.RuntimeLibraryPath, fakeLib)
	}
}

// TestEnsureAssetsPlaceholderChecksumFailsLoudly verifies the sentinel
// check fires for an asset that is both uncached and not found by the
// probe.
func TestEnsureAssetsPlaceholderChecksumFailsLoudly(t *testing.T) {
	srv := newBodyServer(t, map[string][]byte{})
	cacheRoot := t.TempDir()
	manifest := Manifest{
		Runtimes: map[PlatformKey]Entry{
			{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}: {
				URL: srv.URL + "/runtime", SHA256: PlaceholderSHA256, Size: 1, Version: "placeholder-rt",
			},
		},
		Model:     DefaultManifest.Model,
		Tokenizer: DefaultManifest.Tokenizer,
	}
	mgr := newManagerForTest(t, cacheRoot, manifest)

	_, err := mgr.EnsureAssets(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ErrPlaceholderChecksum, got nil")
	}
	if !errors.Is(err, ErrPlaceholderChecksum) {
		t.Fatalf("expected ErrPlaceholderChecksum, got %v", err)
	}
}

// TestClearCacheRemovesSubtree writes a file below the cache root and
// verifies ClearCache wipes it.
func TestClearCacheRemovesSubtree(t *testing.T) {
	cacheRoot := t.TempDir()
	nested := filepath.Join(cacheRoot, "onnxruntime", "v1", "dir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "lib.so")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(cacheRoot, &http.Client{})
	if err := mgr.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if _, err := os.Stat(cacheRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache root still exists: err=%v", err)
	}
	// A second call must also succeed (idempotent).
	if err := mgr.ClearCache(); err != nil {
		t.Fatalf("second ClearCache: %v", err)
	}
}

// TestEnsureAssetsNilReportCallback asserts that passing nil for the
// progress callback is tolerated.
func TestEnsureAssetsNilReportCallback(t *testing.T) {
	runtimeBody := []byte("r")
	modelBody := []byte("m")
	tokBody := []byte("t")
	srv := newBodyServer(t, map[string][]byte{
		"/runtime":   runtimeBody,
		"/model":     modelBody,
		"/tokenizer": tokBody,
	})
	cacheRoot := t.TempDir()
	mgr := newManagerForTest(t, cacheRoot, testManifest(srv.Server, runtimeBody, modelBody, tokBody))
	if _, err := mgr.EnsureAssets(context.Background(), nil); err != nil {
		t.Fatalf("EnsureAssets with nil callback: %v", err)
	}
}

// TestEnsureAssetsReportCallbackDebounced asserts the progress callback
// fires at least once for stage transitions and is not called more than
// once per debounce interval for mid-download byte updates.
func TestEnsureAssetsReportCallbackDebounced(t *testing.T) {
	// A body large enough to ensure io.Copy yields multiple writes.
	body := make([]byte, 64*1024)
	for i := range body {
		body[i] = byte(i)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/runtime", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/model", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/tokenizer", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cacheRoot := t.TempDir()
	manifest := testManifest(srv, body, body, body)
	mgr := newManagerForTest(t, cacheRoot, manifest)

	// Use a controllable clock so the debounce logic is deterministic.
	var clock time.Time
	clock = time.Unix(0, 0)
	mgr.now = func() time.Time { return clock }

	stages := map[string]int{}
	var callbackCount int
	report := func(p Progress) {
		callbackCount++
		stages[p.Stage]++
		// Advance the clock by less than the debounce window between
		// consecutive byte updates so the debounce actually filters.
		clock = clock.Add(10 * time.Millisecond)
	}

	if _, err := mgr.EnsureAssets(context.Background(), report); err != nil {
		t.Fatalf("EnsureAssets: %v", err)
	}
	for _, want := range []string{stageProbing, stageDownloading, stageVerifying, stageReady} {
		if stages[want] == 0 {
			t.Errorf("expected at least one %q event", want)
		}
	}
	// Forced stage events amount to 1 probing + 3 downloading + 3
	// verifying + 1 ready = 8. Debounced byte updates add at most one
	// call per 200 ms of simulated time; with 10 ms per tick and 3 x
	// 64 KiB writes that still leaves bounded room. A generous ceiling
	// (much smaller than one callback per Write) keeps the assertion
	// robust across Go versions.
	if callbackCount > 64 {
		t.Fatalf("progress callback fired %d times, expected debounce to keep it small", callbackCount)
	}
}

// TestProgressEmitterDebounce covers the emitter directly with a fake
// clock.
func TestProgressEmitterDebounce(t *testing.T) {
	var clock time.Time
	clock = time.Unix(0, 0)
	var count int
	emitter := newProgressEmitter(func(Progress) { count++ }, 200*time.Millisecond, func() time.Time { return clock })

	// First emit is always delivered because last is the zero value.
	emitter.emit(Progress{Stage: stageDownloading})
	if count != 1 {
		t.Fatalf("expected 1 call after first emit, got %d", count)
	}
	// A second emit within the interval must be dropped.
	clock = clock.Add(50 * time.Millisecond)
	emitter.emit(Progress{Stage: stageDownloading})
	if count != 1 {
		t.Fatalf("expected debounce to drop call, got %d total", count)
	}
	// After the interval has passed, the next emit is delivered.
	clock = clock.Add(200 * time.Millisecond)
	emitter.emit(Progress{Stage: stageDownloading})
	if count != 2 {
		t.Fatalf("expected 2 calls after interval, got %d", count)
	}
	// emitForced bypasses the debounce.
	emitter.emitForced(Progress{Stage: stageVerifying})
	if count != 3 {
		t.Fatalf("expected forced emit to deliver, got %d", count)
	}
}

// TestProgressEmitterNilCallback asserts nil is tolerated.
func TestProgressEmitterNilCallback(t *testing.T) {
	emitter := newProgressEmitter(nil, progressInterval, time.Now)
	// Must not panic.
	emitter.emit(Progress{Stage: stageDownloading})
	emitter.emitForced(Progress{Stage: stageReady})
}
