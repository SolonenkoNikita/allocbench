package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand("dev")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func testdata(name string) string {
	return filepath.Join("..", "recipe", "testdata", name)
}

func TestListValid(t *testing.T) {
	out, err := runCLI(t, "list", "--recipes", testdata("valid"))
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got, want := len(lines), 7; got != want {
		t.Fatalf("got %d lines, want %d:\n%s", got, want, out)
	}

	wantRows := [][]string{
		{"KIND", "NAME", "VERSION", "BUILD", "ENV", "SOURCE", "HASH"},
		{"env", "gcc13", "-", "-", "ubuntu", "noble", "20260101T000000Z"},
		{"allocator", "mi", "v1.8.2", "gcc13", "git", "b66e3214d8a1"},
		{"allocator", "smi", "v1.8.2", "gcc13", "git", "b66e3214d8a1"},
		{"allocator", "sys", "-", "-", "-", "-"},
		{"bench", "cfrac", "-", "gcc13", "path", "bench-src/cfrac"},
		{"tool", "cmake-3.31", "-", "gcc13", "url", "https://example.com/cmake.tar.gz"},
	}
	for i, want := range wantRows {
		fields := strings.Fields(lines[i])
		if len(fields) < len(want) {
			t.Errorf("line %d: %q", i, lines[i])
			continue
		}
		for j, w := range want {
			if fields[j] != w {
				t.Errorf("line %d field %d: got %q, want %q\n%s", i, j, fields[j], w, lines[i])
			}
		}
	}
}

func TestListShowsShortHashes(t *testing.T) {
	out, err := runCLI(t, "list", "--recipes", testdata("valid"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		fields := strings.Fields(line)
		hash := fields[len(fields)-1]
		if fields[1] == "sys" {
			if hash != "-" {
				t.Errorf("sys hash: got %q, want -", hash)
			}
			continue
		}
		if len(hash) != shortHash {
			t.Errorf("%s: hash %q is not %d characters", fields[1], hash, shortHash)
		}
	}
}

func TestListEmptyDir(t *testing.T) {
	out, err := runCLI(t, "list", "--recipes", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "no recipes found" {
		t.Errorf("got %q", out)
	}
}

func TestListErrors(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		want []string
	}{
		{"missing dir", filepath.Join(t.TempDir(), "nope"), []string{"not found, set it with --recipes"}},
		{"invalid recipes", testdata("extends"), []string{"invalid recipes:", "allocators/mi: source.rev", "benches/larson:"}},
		{"syntax error", testdata("syntax"), []string{"benches/cfrac.toml", "line 4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runCLI(t, "list", "--recipes", tt.dir)
			if err == nil {
				t.Fatalf("expected an error, got output:\n%s", out)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestListDefaultRecipesDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "recipes"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	out, err := runCLI(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.TrimSpace(out) != "no recipes found" {
		t.Errorf("got %q", out)
	}
}

func TestListRejectsArgs(t *testing.T) {
	if _, err := runCLI(t, "list", "extra"); err == nil {
		t.Error("expected an error for an extra argument")
	}
}

func TestListRepoRecipes(t *testing.T) {
	out, err := runCLI(t, "list", "--recipes", filepath.Join("..", "..", "recipes"))
	if err != nil {
		t.Fatalf("repo recipes are invalid: %v\n%s", err, out)
	}
	rows := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 4 {
			rows[strings.Join(f[:4], " ")] = true
		}
	}
	for _, w := range []string{"env gcc13 - -", "allocator mi v1.8.2 gcc13", "allocator sys - -", "bench cfrac - gcc13", "tool runner - gcc13"} {
		if !rows[w] {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}
