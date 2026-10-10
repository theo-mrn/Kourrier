package fsstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/theo-mrn/kourrier/internal/storage"
)

func TestGetIsConfinedToRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "templates")
	if err := os.MkdirAll(filepath.Join(root, "yner"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "yner", "subject.txt"), []byte("Hello"), 0o644)
	_ = os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("secret"), 0o644)

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if data, err := s.Get(ctx, "yner/subject.txt"); err != nil || string(data) != "Hello" {
		t.Fatalf("Get = %q, %v", data, err)
	}
	if _, err := s.Get(ctx, "yner/missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := s.Get(ctx, "../secret.txt"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}
