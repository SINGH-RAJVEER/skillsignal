// Package storage keeps uploaded originals outside the web root under
// generated keys, on local disk or behind the edge object gateway.
package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscape marks a key that resolves outside the storage root.
var ErrEscape = errors.New("storage key escapes the configured root")

// Store persists objects by key. Get reports absent keys with fs.ErrNotExist,
// and Delete ignores them.
type Store interface {
	Put(key string, content []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// Open selects the remote gateway when a URL is configured and local disk otherwise.
func Open(root, remoteURL, remoteToken string) (Store, error) {
	if remoteURL != "" {
		return NewRemote(remoteURL, remoteToken)
	}
	return New(root)
}

// Local is an object store rooted at one directory.
type Local struct{ root string }

// New resolves the root once so later joins cannot escape it.
func New(root string) (*Local, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Local{root: absolute}, nil
}

// Put writes content, creating parent directories as needed.
func (s *Local) Put(key string, content []byte) error {
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
func (s *Local) Get(key string) ([]byte, error) {
	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Delete removes a stored object, ignoring absent keys.
func (s *Local) Delete(key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Local) resolve(key string) (string, error) {
	clean := filepath.Clean(string(filepath.Separator) + key)
	path := filepath.Join(s.root, clean)
	// Like the Python guard, the root itself is not a valid object path.
	if !strings.HasPrefix(path, s.root+string(filepath.Separator)) {
		return "", ErrEscape
	}
	return path, nil
}
