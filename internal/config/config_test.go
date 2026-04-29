package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadUsesCrossPlatformConfigDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("AppData", tmp)
	t.Setenv("HOME", tmp)

	source := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &AppConfig{}
	if err := cfg.AddBackend(Backend{Name: " Codex ", Category: " agent ", Paths: []string{source, source, ""}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(loaded.Backends))
	}
	backend := loaded.Backends[0]
	if backend.Name != "Codex" {
		t.Fatalf("backend name was not normalized: %q", backend.Name)
	}
	if backend.Category != "agent" {
		t.Fatalf("backend category was not normalized: %q", backend.Category)
	}
	if len(backend.Paths) != 1 || backend.Paths[0] != source {
		t.Fatalf("backend paths were not normalized: %#v", backend.Paths)
	}
}

func TestAddBackendRejectsInvalidAndDuplicateBackends(t *testing.T) {
	tmp := t.TempDir()
	cfg := &AppConfig{}

	if err := cfg.AddBackend(Backend{Name: "", Paths: []string{tmp}}); err == nil {
		t.Fatal("expected empty backend name to fail")
	}
	if err := cfg.AddBackend(Backend{Name: "Codex", Paths: []string{"relative"}}); err == nil {
		t.Fatal("expected relative path to fail")
	}
	if err := cfg.AddBackend(Backend{Name: "Codex", Paths: []string{tmp}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddBackend(Backend{Name: "Codex", Paths: []string{tmp}}); err == nil {
		t.Fatal("expected duplicate backend name to fail")
	}
}

