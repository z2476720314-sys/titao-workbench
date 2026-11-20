package fileutil

import (
	"errors"
	"os"

	"path/filepath"

	"testing"
)

func TestRevealRejectsMissingPath(t *testing.T) {
	called := false
	err := reveal(filepath.Join(t.TempDir(), "missing.jpg"), func(string) error { called = true; return nil })
	if !errors.Is(err, os.ErrNotExist) || called {
		t.Fatalf("missing file: called=%t err=%v", called, err)
	}
}

func TestRevealPreservesUnicodeAndShellCharacters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "中文 Кот's [原图], & # 01.jpg")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("shell selection failed")
	called := false
	err := reveal(filepath.ToSlash(path), func(got string) error {
		called = true
		if got != path {
			t.Errorf("path changed: %q != %q", got, path)
		}
		return failure
	})
	if !called || !errors.Is(err, failure) {
		t.Fatalf("selection result lost: called=%t err=%v", called, err)
	}
	if err := reveal("", func(string) error { t.Fatal("empty path invoked shell"); return nil }); err == nil {
		t.Fatal("empty path accepted")
	}
}
