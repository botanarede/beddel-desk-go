package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsOnlyLightweightReferences(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("AppData", tmp)
	t.Setenv("HOME", tmp)

	sessionPath := filepath.Join(tmp, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("secret transcript content"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &Store{}
	if err := store.AddFavorite(Favorite{
		SessionPath: sessionPath,
		BackendName: "Codex",
		Label:       "session.jsonl",
		AddedAt:     time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRecent(RecentRef{
		SessionPath: sessionPath,
		BackendName: "Codex",
		OpenedAt:    time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	path, err := StorePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret transcript content") {
		t.Fatal("store persisted processed session content")
	}
	if !strings.Contains(string(data), sessionPath) {
		t.Fatal("store did not persist the lightweight session path reference")
	}
}

func TestStoreDeduplicatesByBackendAndPath(t *testing.T) {
	tmp := t.TempDir()
	sessionPath := filepath.Join(tmp, "session.jsonl")
	store := &Store{}

	for i := 0; i < 2; i++ {
		if err := store.AddFavorite(Favorite{SessionPath: sessionPath, BackendName: "Codex"}); err != nil {
			t.Fatal(err)
		}
		if err := store.AddRecent(RecentRef{SessionPath: sessionPath, BackendName: "Codex"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddFavorite(Favorite{SessionPath: sessionPath, BackendName: "Other"}); err != nil {
		t.Fatal(err)
	}
	if len(store.Favorites) != 2 {
		t.Fatalf("expected backend-aware favorite dedupe, got %d", len(store.Favorites))
	}
	if len(store.Recents) != 1 {
		t.Fatalf("expected recent dedupe, got %d", len(store.Recents))
	}
}

func TestStoreRemovesFavoriteByBackendAndPath(t *testing.T) {
	tmp := t.TempDir()
	sessionPath := filepath.Join(tmp, "session.jsonl")
	store := &Store{}
	if err := store.AddFavorite(Favorite{SessionPath: sessionPath, BackendName: "Codex"}); err != nil {
		t.Fatal(err)
	}
	if !store.RemoveFavorite("Codex", sessionPath) {
		t.Fatal("expected favorite to be removed")
	}
	if len(store.Favorites) != 0 {
		t.Fatalf("expected no favorites, got %d", len(store.Favorites))
	}
}
