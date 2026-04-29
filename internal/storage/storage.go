// Package storage manages lightweight local references such as favorites and recents.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/botanarede/beddel-desk-go/internal/config"
)

const maxRecents = 50

// Favorite represents a bookmarked session reference.
type Favorite struct {
	SessionPath string    `json:"session_path"`
	BackendName string    `json:"backend_name"`
	Label       string    `json:"label"`
	AddedAt     time.Time `json:"added_at"`
}

// RecentRef represents a recently opened session reference.
type RecentRef struct {
	SessionPath string    `json:"session_path"`
	BackendName string    `json:"backend_name"`
	OpenedAt    time.Time `json:"opened_at"`
}

// Store holds favorites and recent session references.
type Store struct {
	Favorites []Favorite  `json:"favorites"`
	Recents   []RecentRef `json:"recents"`
}

// StorePath returns the full path to storage.json.
func StorePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "storage.json"), nil
}

// LoadStore reads the store from disk. Returns an empty Store if the file does not exist.
func LoadStore() (*Store, error) {
	path, err := StorePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Store{}, nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the store to disk with indented JSON.
func (s *Store) Save() error {
	if err := s.Validate(); err != nil {
		return err
	}
	path, err := StorePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// AddFavorite adds a favorite, deduplicating by SessionPath.
func (s *Store) AddFavorite(f Favorite) error {
	f = normalizeFavorite(f)
	if err := validateRef(f.BackendName, f.SessionPath); err != nil {
		return err
	}
	for _, existing := range s.Favorites {
		if sameRef(existing.BackendName, existing.SessionPath, f.BackendName, f.SessionPath) {
			return nil
		}
	}
	if f.AddedAt.IsZero() {
		f.AddedAt = time.Now()
	}
	s.Favorites = append(s.Favorites, f)
	return nil
}

// RemoveFavorite removes a favorite by session path. Returns true if found.
func (s *Store) RemoveFavorite(backendName, sessionPath string) bool {
	for i, f := range s.Favorites {
		if sameRef(f.BackendName, f.SessionPath, backendName, sessionPath) {
			s.Favorites = append(s.Favorites[:i], s.Favorites[i+1:]...)
			return true
		}
	}
	return false
}

// AddRecent adds a recent ref to the front, deduplicates, and caps at 50 items.
func (s *Store) AddRecent(r RecentRef) error {
	r = normalizeRecent(r)
	if err := validateRef(r.BackendName, r.SessionPath); err != nil {
		return err
	}
	if r.OpenedAt.IsZero() {
		r.OpenedAt = time.Now()
	}
	filtered := []RecentRef{r}
	for _, existing := range s.Recents {
		if !sameRef(existing.BackendName, existing.SessionPath, r.BackendName, r.SessionPath) {
			filtered = append(filtered, existing)
		}
	}
	if len(filtered) > maxRecents {
		filtered = filtered[:maxRecents]
	}
	s.Recents = filtered
	return nil
}

// ListFavorites returns all favorites.
func (s *Store) ListFavorites() []Favorite {
	return s.Favorites
}

// ListRecents returns all recent references.
func (s *Store) ListRecents() []RecentRef {
	return s.Recents
}

// Validate ensures the store contains only usable lightweight local references.
func (s *Store) Validate() error {
	for _, f := range s.Favorites {
		if err := validateRef(f.BackendName, f.SessionPath); err != nil {
			return err
		}
	}
	for _, r := range s.Recents {
		if err := validateRef(r.BackendName, r.SessionPath); err != nil {
			return err
		}
	}
	return nil
}

// ClearRecents removes all recent references.
func (s *Store) ClearRecents() {
	s.Recents = nil
}

func normalizeFavorite(f Favorite) Favorite {
	f.BackendName = strings.TrimSpace(f.BackendName)
	f.SessionPath = filepath.Clean(strings.TrimSpace(f.SessionPath))
	f.Label = strings.TrimSpace(f.Label)
	if f.Label == "" && f.SessionPath != "." {
		f.Label = filepath.Base(f.SessionPath)
	}
	return f
}

func normalizeRecent(r RecentRef) RecentRef {
	r.BackendName = strings.TrimSpace(r.BackendName)
	r.SessionPath = filepath.Clean(strings.TrimSpace(r.SessionPath))
	return r
}

func validateRef(backendName, sessionPath string) error {
	if strings.TrimSpace(backendName) == "" {
		return errors.New("backend name is required")
	}
	if strings.TrimSpace(sessionPath) == "" || sessionPath == "." {
		return errors.New("session path is required")
	}
	if !filepath.IsAbs(sessionPath) {
		return fmt.Errorf("session path %q must be absolute", sessionPath)
	}
	return nil
}

func sameRef(leftBackend, leftPath, rightBackend, rightPath string) bool {
	return leftBackend == rightBackend && filepath.Clean(leftPath) == filepath.Clean(rightPath)
}
