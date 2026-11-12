package lock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() *File {
	f := New()
	f.Envs["gcc13"] = Env{Hash: strings.Repeat("e", 64), Tree: strings.Repeat("t", 64)}
	f.Builds["tools/runner"] = Build{Hash: strings.Repeat("r", 64), Out: strings.Repeat("1", 64)}
	f.Builds["allocators/mi"] = Build{Hash: strings.Repeat("m", 64), Out: strings.Repeat("2", 64)}
	return f
}

func TestWriteRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := sample().Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Errorf("got %+v", got)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.HasPrefix(text, "version = 1\n") || strings.Index(text, `"allocators/mi"`) > strings.Index(text, `"tools/runner"`) {
		t.Errorf("lock file is not sorted:\n%s", text)
	}
	if err := sample().Write(path); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != text {
		t.Error("writing the same lock twice gives different files")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files are left: %v", entries)
	}
}

func TestReadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	tests := map[string]string{
		"version = 2\n":                         "version 2, want 1",
		"version = 1\nsurprise = true\n":        "unknown fields: surprise",
		"version = 1\n[envs.gcc13]\nsize = 1\n": "unknown fields: envs.gcc13.size",
		"version = \n":                          "lock",
		"":                                      "version 0, want 1",
	}
	for content, want := range tests {
		path := filepath.Join(dir, "lock")
		os.WriteFile(path, []byte(content), 0o644)
		if _, err := Read(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", content, err, want)
		}
	}
}

func TestMerge(t *testing.T) {
	f := sample()
	other := New()
	other.Builds["allocators/mi"] = Build{Hash: "new", Out: "new"}
	other.Builds["benches/cfrac"] = Build{Hash: "c", Out: "c"}
	other.Envs["gcc14"] = Env{Hash: "g", Tree: "g"}
	f.Merge(other)
	if f.Builds["allocators/mi"].Hash != "new" || f.Builds["benches/cfrac"].Hash != "c" || f.Builds["tools/runner"].Hash == "" {
		t.Errorf("builds: %+v", f.Builds)
	}
	if len(f.Envs) != 2 {
		t.Errorf("envs: %+v", f.Envs)
	}
}

func TestCheck(t *testing.T) {
	want := sample()
	if problems := want.Check(sample()); len(problems) != 0 {
		t.Errorf("same lock: %v", problems)
	}

	got := sample()
	delete(got.Builds, "tools/runner")
	if problems := want.Check(got); len(problems) != 0 {
		t.Errorf("subset of the lock: %v", problems)
	}

	got = sample()
	got.Envs["gcc13"] = Env{Hash: want.Envs["gcc13"].Hash, Tree: strings.Repeat("x", 64)}
	got.Envs["gcc14"] = Env{Hash: "h", Tree: "t"}
	got.Builds["allocators/mi"] = Build{Hash: strings.Repeat("n", 64), Out: want.Builds["allocators/mi"].Out}
	got.Builds["tools/runner"] = Build{Hash: want.Builds["tools/runner"].Hash, Out: strings.Repeat("9", 64)}
	got.Builds["benches/cfrac"] = Build{Hash: "c", Out: "c"}

	var lines []string
	for _, p := range want.Check(got) {
		lines = append(lines, p.String())
	}
	wantLines := []string{
		"envs/gcc13: rootfs differs from the lock: tree xxxxxxxxxxxx, lock tttttttttttt",
		"envs/gcc14: not in the lock file",
		"allocators/mi: inputs changed since the lock: hash nnnnnnnnnnnn, lock mmmmmmmmmmmm",
		"benches/cfrac: not in the lock file",
		"tools/runner: output differs from the lock: out 999999999999, lock 111111111111",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(wantLines, "\n"))
	}
}

func TestCheckEnvRecipeChanged(t *testing.T) {
	got := sample()
	got.Envs["gcc13"] = Env{Hash: "other", Tree: "other"}
	problems := sample().Check(got)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "recipe changed since the lock") {
		t.Errorf("got %v", problems)
	}
}

func TestReadEmptyLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	os.WriteFile(path, []byte("version = 1\n"), 0o644)
	f, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Merge(sample())
	if len(f.Builds) != 2 || len(f.Envs) != 1 {
		t.Errorf("empty lock cannot be merged into: %+v", f)
	}
}
