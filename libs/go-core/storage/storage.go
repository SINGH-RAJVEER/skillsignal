// Package storage keeps uploaded originals outside the web root under
// generated keys, with a traversal guard on every access.
package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscape marks a key that resolves outside the storage root.
var ErrEscape = errors.New("storage key escapes the configured root")

// Store is a local object store rooted at one directory.
type Store struct{ root string }

// New resolves the root once so later joins cannot escape it.
func New(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Store{root: absolute}, nil
}

// Put writes content, creating parent directories as needed.
func (s *Store) Put(key string, content []byte) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// Get reads a stored object.
func (s *Store) Get(key string) ([]byte, error) {
	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Delete removes a stored object, ignoring absent keys.
func (s *Store) Delete(key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) resolve(key string) (string, error) {
	clean := filepath.Clean(string(filepath.Separator) + key)
	path := filepath.Join(s.root, clean)
	// Like the Python guard, the root itself is not a valid object path.
	if !strings.HasPrefix(path, s.root+string(filepath.Separator)) {
		return "", ErrEscape
	}
	return path, nil
}
