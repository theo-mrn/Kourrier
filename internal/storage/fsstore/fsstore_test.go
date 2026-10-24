package fsstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

func TestList(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"yner/_layout.html", "yner/welcome/fr/subject.txt", "yner/welcome/fr/body.html", "other/x/en/subject.txt"} {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	keys, err := s.List(ctx, "yner/")
	want := []string{"yner/_layout.html", "yner/welcome/fr/body.html", "yner/welcome/fr/subject.txt"}
	if err != nil || !slices.Equal(keys, want) {
		t.Fatalf("List = %v, %v; want %v", keys, err, want)
	}
	if keys, err := s.List(ctx, "nobody/"); err != nil || len(keys) != 0 {
		t.Fatalf("missing prefix: %v, %v", keys, err)
	}
	if _, err := s.List(ctx, "../"); err == nil {
		t.Fatal("listing outside the root must fail")
	}
}
