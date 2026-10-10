// Package fsstore serves templates from a local directory. It is meant for
// local development and for clusters that mount templates from a volume.
package fsstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/theo-mrn/kourrier/internal/storage"
)

// Store reads objects from a directory. Access is confined to that directory
// through os.Root, so a crafted key cannot escape it.
type Store struct {
	root *os.Root
}

// New opens dir as the storage root.
func New(dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("fsstore: open %q: %w", dir, err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	f, err := s.root.Open(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("fsstore: open %q: %w", key, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, storage.MaxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("fsstore: read %q: %w", key, err)
	}
	if len(data) > storage.MaxObjectSize {
		return nil, fmt.Errorf("fsstore: %q exceeds %d bytes", key, storage.MaxObjectSize)
	}
	return data, nil
}

func (s *Store) Ping(context.Context) error {
	_, err := s.root.Stat(".")
	return err
}

// Close releases the directory handle.
func (s *Store) Close() error { return s.root.Close() }
