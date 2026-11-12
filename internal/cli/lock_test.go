package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/lock"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
)

func TestLockTargets(t *testing.T) {
	s := &recipe.Set{
		Envs:       map[string]recipe.Env{"gcc13": {}, "gcc14": {}, "run": {}},
		Allocators: map[string]recipe.Allocator{"mi": {BuildEnv: "gcc13"}, "sys": {}},
		Benches:    map[string]recipe.Bench{"cfrac": {BuildEnv: "gcc14"}},
		Tools:      map[string]recipe.Tool{"runner": {BuildEnv: "gcc13"}},
	}

	builds, envs, err := lockTargets(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(builds, []string{"allocators/mi", "allocators/sys", "benches/cfrac", "tools/runner"}) {
		t.Errorf("all builds: %v", builds)
	}
	if !reflect.DeepEqual(envs, []string{"gcc13", "gcc14", "run"}) {
		t.Errorf("all envs: %v", envs)
	}

	builds, envs, err = lockTargets(s, []string{"mi", "runner", "allocators/mi", "run", "sys"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(builds, []string{"allocators/mi", "tools/runner", "allocators/sys"}) {
		t.Errorf("builds: %v", builds)
	}
	if !reflect.DeepEqual(envs, []string{"gcc13", "run"}) {
		t.Errorf("envs: %v", envs)
	}

	if _, _, err := lockTargets(s, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "no allocator, bench, tool or env") {
		t.Errorf("unknown: %v", err)
	}
}

func TestPrintLock(t *testing.T) {
	f := lock.New()
	f.Envs["gcc13"] = lock.Env{Hash: strings.Repeat("e", 64), Tree: strings.Repeat("t", 64)}
	f.Builds["allocators/mi"] = lock.Build{Hash: strings.Repeat("m", 64), Out: strings.Repeat("o", 64)}
	var out strings.Builder
	if err := printLock(&out, f); err != nil {
		t.Fatal(err)
	}
	want := "RECIPE         HASH          CONTENT\n" +
		"envs/gcc13     eeeeeeeeeeee  tttttttttttt\n" +
		"allocators/mi  mmmmmmmmmmmm  oooooooooooo\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestLockCommand(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	base := []string{"--recipes", recipes, "--store", storeDir, "lock"}
	lockPath := filepath.Join(dir, lock.FileName)

	out, err := runCLI(t, base...)
	if err != nil {
		t.Fatalf("lock: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning: envs/host was created from none, not from the snapshot") || !strings.Contains(out, "wrote "+lockPath) {
		t.Errorf("output:\n%s", out)
	}
	f, err := lock.Read(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for ref, b := range f.Builds {
		refs = append(refs, ref)
		if len(b.Hash) != 64 || len(b.Out) != 64 {
			t.Errorf("%s: %+v", ref, b)
		}
	}
	if want := []string{"allocators/hello", "benches/echo", "benches/fails", "tools/greet", "tools/runner"}; !reflect.DeepEqual(keys(f.Builds), want) {
		t.Errorf("builds: %v", refs)
	}
	if e := f.Envs["host"]; len(e.Hash) != 64 || e.Tree == "" {
		t.Errorf("env: %+v", e)
	}

	out, err = runCLI(t, append(base, "--check")...)
	if err != nil || !strings.Contains(out, "matches: 1 envs, 5 builds") {
		t.Fatalf("check: %v\n%s", err, out)
	}

	os.WriteFile(filepath.Join(dir, "files", "hello", "message"), []byte("changed\n"), 0o644)
	out, err = runCLI(t, append(base, "--check")...)
	if err == nil || !strings.Contains(out, "allocators/hello: inputs changed since the lock") || !strings.Contains(err.Error(), "1 of 6 entries do not match") {
		t.Fatalf("check after a change: %v\n%s", err, out)
	}

	before := f.Builds["tools/runner"]
	if out, err := runCLI(t, append(base, "hello")...); err != nil {
		t.Fatalf("partial lock: %v\n%s", err, out)
	}
	f, _ = lock.Read(lockPath)
	if f.Builds["tools/runner"] != before || len(f.Builds) != 5 {
		t.Errorf("partial lock lost entries: %+v", f.Builds)
	}
	if out, err := runCLI(t, append(base, "--check")...); err != nil {
		t.Fatalf("check after a partial lock: %v\n%s", err, out)
	}

	os.Remove(filepath.Join(recipes, "benches", "fails.toml"))
	if out, err := runCLI(t, base...); err != nil {
		t.Fatalf("full lock: %v\n%s", err, out)
	}
	f, _ = lock.Read(lockPath)
	if _, ok := f.Builds["benches/fails"]; ok || len(f.Builds) != 4 {
		t.Errorf("full lock keeps a removed recipe: %v", keys(f.Builds))
	}
}

func TestLockCheckWithoutFile(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	custom := filepath.Join(dir, "custom.lock")
	out, err := runCLI(t, "--recipes", recipes, "--store", storeDir, "lock", "--check", "--lock", custom, "hello")
	if err == nil || !strings.Contains(err.Error(), custom+" not found, create it with allocbench lock") {
		t.Errorf("got %v\n%s", err, out)
	}
}
