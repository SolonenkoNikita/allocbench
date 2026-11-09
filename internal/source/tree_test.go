package source

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func sampleTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.c"), "int main() { return 0; }\n", 0o644)
	writeFile(t, filepath.Join(dir, "src", "util.c"), "void f(void) {}\n", 0o644)
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/bin/sh\nmake\n", 0o755)
	if err := os.Symlink("src/util.c", filepath.Join(dir, "util.c")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustTreeHash(t *testing.T, dir string) string {
	t.Helper()
	h, err := TreeHash(dir)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	return h
}

func TestTreeHashSameContentSameHash(t *testing.T) {
	a := mustTreeHash(t, sampleTree(t))
	b := mustTreeHash(t, sampleTree(t))
	if a != b || len(a) != 64 {
		t.Errorf("two identical trees: %s and %s", a, b)
	}
}

func TestTreeHashChanges(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"content", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "main.c"), "int main() { return 1; }\n", 0o644)
		}},
		{"new file", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "extra.c"), "", 0o644)
		}},
		{"removed file", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "main.c"))
		}},
		{"renamed file", func(t *testing.T, dir string) {
			os.Rename(filepath.Join(dir, "main.c"), filepath.Join(dir, "start.c"))
		}},
		{"executable bit", func(t *testing.T, dir string) {
			os.Chmod(filepath.Join(dir, "main.c"), 0o755)
		}},
		{"symlink target", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "util.c"))
			os.Symlink("main.c", filepath.Join(dir, "util.c"))
		}},
		{"empty dir", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "empty"))
		}},
		{"file moved between dirs", func(t *testing.T, dir string) {
			os.Rename(filepath.Join(dir, "src", "util.c"), filepath.Join(dir, "util2.c"))
		}},
	}
	base := mustTreeHash(t, sampleTree(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := sampleTree(t)
			tt.change(t, dir)
			if mustTreeHash(t, dir) == base {
				t.Error("hash did not change")
			}
		})
	}
}

func TestTreeHashIgnores(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"mtime", func(t *testing.T, dir string) {
			old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			os.Chtimes(filepath.Join(dir, "main.c"), old, old)
		}},
		{"read and write bits", func(t *testing.T, dir string) {
			os.Chmod(filepath.Join(dir, "main.c"), 0o600)
		}},
		{"git dir", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
		}},
		{"git file", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "src", ".git"), "gitdir: ../.git/modules/src\n", 0o644)
		}},
	}
	base := mustTreeHash(t, sampleTree(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := sampleTree(t)
			tt.change(t, dir)
			if got := mustTreeHash(t, dir); got != base {
				t.Error("hash changed")
			}
		})
	}
}

func TestTreeHashEmptyDir(t *testing.T) {
	a := mustTreeHash(t, t.TempDir())
	b := mustTreeHash(t, sampleTree(t))
	if a == b {
		t.Error("empty and non-empty trees have the same hash")
	}
}

func TestTreeHashNoAmbiguity(t *testing.T) {
	a := t.TempDir()
	writeFile(t, filepath.Join(a, "ab"), "c", 0o644)
	b := t.TempDir()
	writeFile(t, filepath.Join(b, "a"), "bc", 0o644)
	if mustTreeHash(t, a) == mustTreeHash(t, b) {
		t.Error("different trees have the same hash")
	}
}

func TestTreeHashErrors(t *testing.T) {
	if _, err := TreeHash(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing dir: expected an error")
	}

	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	_, err := TreeHash(dir)
	if err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Errorf("fifo: got %v", err)
	}
}
