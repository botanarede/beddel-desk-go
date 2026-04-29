// Package config manages application configuration for Beddel Desk.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backend represents a configured session search backend.
type Backend struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Paths    []string `json:"paths"`
}

// AppConfig holds the top-level application configuration.
type AppConfig struct {
	Backends []Backend `json:"backends"`
}

// ConfigDir returns the cross-platform application config directory.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "beddel-desk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ConfigPath returns the full path to config.json.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config from disk. Returns an empty AppConfig if the file does not exist.
func Load() (*AppConfig, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &AppConfig{}, nil
		}
		return nil, err
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes the config to disk with indented JSON.
func (c *AppConfig) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// AddBackend appends a validated backend to the config.
func (c *AppConfig) AddBackend(b Backend) error {
	b = NormalizeBackend(b)
	if err := ValidateBackend(b); err != nil {
		return err
	}
	if c.FindBackend(b.Name) != nil {
		return fmt.Errorf("backend %q already exists", b.Name)
	}
	c.Backends = append(c.Backends, b)
	return nil
}

// UpsertBackend creates or replaces a backend by name.
func (c *AppConfig) UpsertBackend(originalName string, b Backend) error {
	b = NormalizeBackend(b)
	if err := ValidateBackend(b); err != nil {
		return err
	}
	for i, existing := range c.Backends {
		if existing.Name == b.Name && existing.Name != originalName {
			return fmt.Errorf("backend %q already exists", b.Name)
		}
		if existing.Name == originalName {
			c.Backends[i] = b
			return nil
		}
	}
	c.Backends = append(c.Backends, b)
	return nil
}

// RemoveBackend removes a backend by name. Returns true if found.
func (c *AppConfig) RemoveBackend(name string) bool {
	for i, b := range c.Backends {
		if b.Name == name {
			c.Backends = append(c.Backends[:i], c.Backends[i+1:]...)
			return true
		}
	}
	return false
}

// FindBackend returns a pointer to the backend with the given name, or nil.
func (c *AppConfig) FindBackend(name string) *Backend {
	for i := range c.Backends {
		if c.Backends[i].Name == name {
			return &c.Backends[i]
		}
	}
	return nil
}

// Validate checks the full application configuration.
func (c *AppConfig) Validate() error {
	seen := make(map[string]struct{}, len(c.Backends))
	for _, b := range c.Backends {
		b = NormalizeBackend(b)
		if err := ValidateBackend(b); err != nil {
			return err
		}
		if _, ok := seen[b.Name]; ok {
			return fmt.Errorf("backend %q is duplicated", b.Name)
		}
		seen[b.Name] = struct{}{}
	}
	return nil
}

// NormalizeBackend trims user-provided backend fields and removes empty paths.
func NormalizeBackend(b Backend) Backend {
	b.Name = strings.TrimSpace(b.Name)
	b.Category = strings.TrimSpace(b.Category)
	paths := make([]string, 0, len(b.Paths))
	seen := map[string]struct{}{}
	for _, p := range b.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	b.Paths = paths
	return b
}

// ValidateBackend ensures a backend can be used for local search.
func ValidateBackend(b Backend) error {
	if b.Name == "" {
		return errors.New("backend name is required")
	}
	if len(b.Paths) == 0 {
		return errors.New("at least one local source path is required")
	}
	for _, p := range b.Paths {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("source path %q must be absolute", p)
		}
	}
	return nil
}
