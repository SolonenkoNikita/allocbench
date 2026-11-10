package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	var lines []string
	for _, c := range "abcdef" {
		lines = append(lines, string(c))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	if got, want := Tail(path, 3), "  | d\n  | e\n  | f"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Tail(path, 100), "  | a\n  | b\n  | c\n  | d\n  | e\n  | f"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTailNoNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	os.WriteFile(path, []byte("one\ntwo"), 0o644)
	if got, want := Tail(path, 5), "  | one\n  | two"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTailLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	long := strings.Repeat("x", 200000)
	os.WriteFile(path, []byte("first\n"+long+"\nlast\n"), 0o644)
	if got := Tail(path, 1); got != "  | last" {
		t.Errorf("got %d bytes, want the last line", len(got))
	}
}

func TestTailMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := Tail(filepath.Join(dir, "missing.log"), 5); got != "" {
		t.Errorf("missing file: %q", got)
	}
	empty := filepath.Join(dir, "empty.log")
	os.WriteFile(empty, nil, 0o644)
	if got := Tail(empty, 5); got != "" {
		t.Errorf("empty file: %q", got)
	}
}
