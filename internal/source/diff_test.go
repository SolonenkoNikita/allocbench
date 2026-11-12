package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bin", "tool"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "lib", "libx.so"), "elf", 0o644)
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref", 0o644)
	os.Symlink("libx.so", filepath.Join(dir, "lib", "libx.so.1"))
	return dir
}

func TestDiffTreesSame(t *testing.T) {
	changes, err := DiffTrees(tree(t), tree(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("identical trees differ: %v", changes)
	}
}

func TestDiffTrees(t *testing.T) {
	a, b := tree(t), tree(t)
	writeFile(t, filepath.Join(a, "only-a"), "a", 0o644)
	writeFile(t, filepath.Join(b, "only-b"), "b", 0o644)
	writeFile(t, filepath.Join(b, "lib", "libx.so"), "elf2", 0o644)
	os.Chmod(filepath.Join(b, "bin", "tool"), 0o644)
	os.Remove(filepath.Join(b, "lib", "libx.so.1"))
	os.Symlink("other.so", filepath.Join(b, "lib", "libx.so.1"))
	writeFile(t, filepath.Join(a, "share"), "file", 0o644)
	os.MkdirAll(filepath.Join(b, "share"), 0o755)
	writeFile(t, filepath.Join(b, ".git", "HEAD"), "other", 0o644)

	changes, err := DiffTrees(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, c.String())
	}
	want := []string{
		"bin/tool: executable bits differ",
		"lib/libx.so: contents differ",
		"lib/libx.so.1: symlink targets differ: libx.so and other.so",
		"only-a: only in the first tree",
		"only-b: only in the second tree",
		"share: file in the first tree, directory in the second",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%v\nwant:\n%v", got, want)
	}
}

func TestDiffTreesMissing(t *testing.T) {
	if _, err := DiffTrees(tree(t), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error")
	}
}
