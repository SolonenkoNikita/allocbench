package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/env"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

type fixture struct {
	root  string
	set   *recipe.Set
	store *store.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		root:  root,
		store: st,
		set: &recipe.Set{
			Envs: map[string]recipe.Env{
				"host": {Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"gcc"}},
			},
			Allocators: map[string]recipe.Allocator{
				"sys": {},
				"hello": {
					BuildEnv: "host",
					Source:   recipe.Source{Path: "src/hello"},
					Build: recipe.Build{
						Deps: []string{"greet"},
						Script: `greet
echo "jobs=$JOBS epoch=$SOURCE_DATE_EPOCH home=$HOME pwd=$(pwd) flag=$FLAG"
test -w /build
touch /src/x 2>/dev/null && echo src-writable || echo src-readonly
mkdir -p /out/lib
cp /src/message /out/lib/message`,
						Vars: map[string]string{"FLAG": "on", "JOBS": "999"},
					},
				},
				"broken": {
					BuildEnv: "host",
					Source:   recipe.Source{Path: "src/hello"},
					Build:    recipe.Build{Script: "echo first line\necho second line\nexit 3"},
				},
				"empty": {
					BuildEnv: "host",
					Source:   recipe.Source{Path: "src/hello"},
					Build:    recipe.Build{Script: "true"},
				},
			},
			Benches: map[string]recipe.Bench{},
			Tools: map[string]recipe.Tool{
				"greet": {
					BuildEnv: "host",
					Source:   recipe.Source{Path: "src/greet"},
					Build:    recipe.Build{Script: "install -Dm755 /src/greet.sh /out/bin/greet"},
				},
			},
		},
	}
	f.write(t, "src/hello/message", "v1\n", 0o644)
	f.write(t, "src/greet/greet.sh", "#!/bin/sh\necho greet from dep\n", 0o755)
	return f
}

func (f *fixture) write(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) hostEnv(t *testing.T) {
	t.Helper()
	h, err := f.set.EnvHash("host")
	if err != nil {
		t.Fatal(err)
	}
	dir := f.store.Path(store.Env, h)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(dir, "rootfs")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"tree":"host","mirror":"none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) builder() *Builder {
	return &Builder{
		Set:     f.set,
		Store:   f.store,
		Sources: &source.Fetcher{Store: f.store, Root: f.root},
		Envs:    &env.Builder{Store: f.store, Mmdebstrap: "/nonexistent/mmdebstrap"},
		Jobs:    4,
	}
}

func needBwrap(t *testing.T) {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap does not work here: %v %s", err, out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func dirEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestBuild(t *testing.T) {
	needBwrap(t)
	f := newFixture(t)
	f.hostEnv(t)
	var progress strings.Builder
	b := f.builder()
	b.Progress = &progress

	r, err := b.Build("allocators/hello")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Recipe != "allocators/hello" || r.Cached || r.Dir != f.store.Path(store.Build, r.Hash) {
		t.Errorf("result: %+v", r)
	}
	if got := readFile(t, filepath.Join(r.Dir, "lib", "message")); got != "v1\n" {
		t.Errorf("output: %q", got)
	}

	want := "greet from dep\njobs=4 epoch=1767225600 home=/build pwd=/build flag=on\nsrc-readonly\n"
	if got := readFile(t, r.Log); got != want {
		t.Errorf("log:\n%s\nwant:\n%s", got, want)
	}
	if got := progress.String(); got != "building tools/greet\nbuilding allocators/hello\n" {
		t.Errorf("progress:\n%s", got)
	}
	if n := dirEntries(t, filepath.Dir(r.Dir)); n != 2 {
		t.Errorf("store has %d builds, want 2", n)
	}
}

func TestBuildUsesStore(t *testing.T) {
	needBwrap(t)
	f := newFixture(t)
	f.hostEnv(t)
	first, err := f.builder().Build("allocators/hello")
	if err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	b := f.builder()
	b.Progress = &progress
	b.Bwrap = "/nonexistent/bwrap"
	second, err := b.Build("allocators/hello")
	if err != nil {
		t.Fatalf("cached build: %v", err)
	}
	if !second.Cached || second.Hash != first.Hash {
		t.Errorf("got %+v, want a cached %s", second, first.Hash)
	}
	if progress.Len() != 0 {
		t.Errorf("cached build printed:\n%s", progress.String())
	}
}

func TestBuildRebuildsOnSourceChange(t *testing.T) {
	needBwrap(t)
	f := newFixture(t)
	f.hostEnv(t)
	first, err := f.builder().Build("allocators/hello")
	if err != nil {
		t.Fatal(err)
	}

	f.write(t, "src/hello/message", "v2\n", 0o644)
	second, err := f.builder().Build("allocators/hello")
	if err != nil {
		t.Fatal(err)
	}
	if second.Cached || second.Hash == first.Hash {
		t.Errorf("source change did not rebuild: %+v", second)
	}
	if got := readFile(t, filepath.Join(second.Dir, "lib", "message")); got != "v2\n" {
		t.Errorf("output: %q", got)
	}

	f.write(t, "src/greet/greet.sh", "#!/bin/sh\necho new greet\n", 0o755)
	third, err := f.builder().Build("allocators/hello")
	if err != nil {
		t.Fatal(err)
	}
	if third.Cached || third.Hash == second.Hash {
		t.Errorf("dependency change did not rebuild: %+v", third)
	}
}

func TestBuildFailure(t *testing.T) {
	needBwrap(t)
	f := newFixture(t)
	f.hostEnv(t)

	_, err := f.builder().Build("allocators/broken")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, w := range []string{"allocators/broken", "exit status 3", "  | first line\n  | second line", "full log: "} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
	if n := dirEntries(t, filepath.Dir(f.store.Path(store.Build, strings.Repeat("0", 64)))); n != 0 {
		t.Errorf("failed build is in the store: %d entries", n)
	}
	if n := dirEntries(t, filepath.Join(filepath.Dir(filepath.Dir(f.store.Path(store.Build, strings.Repeat("0", 64)))), "tmp")); n != 0 {
		t.Errorf("tmp is not cleaned: %d entries", n)
	}
}

func TestBuildEmptyOutput(t *testing.T) {
	needBwrap(t)
	f := newFixture(t)
	f.hostEnv(t)
	_, err := f.builder().Build("allocators/empty")
	if err == nil || !strings.Contains(err.Error(), "nothing into /out") {
		t.Errorf("got %v", err)
	}
}

func TestBuildNothingToBuild(t *testing.T) {
	f := newFixture(t)
	r, err := f.builder().Build("allocators/sys")
	if err != nil {
		t.Fatal(err)
	}
	if r.Hash != "" || r.Dir != "" || r.Recipe != "allocators/sys" {
		t.Errorf("result: %+v", r)
	}
}

func TestBuildEnvFailure(t *testing.T) {
	f := newFixture(t)
	_, err := f.builder().Build("allocators/hello")
	if err == nil || !strings.Contains(err.Error(), "tools/greet: environment host") {
		t.Errorf("got %v", err)
	}
	if n := dirEntries(t, filepath.Dir(f.store.Path(store.Build, strings.Repeat("0", 64)))); n != 0 {
		t.Errorf("store has %d builds after a failure", n)
	}
}

func TestBuildUnknownRecipe(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"allocators/nope": "no such recipe",
		"tools/nope":      "no such recipe",
		"benches/nope":    "no such recipe",
		"envs/host":       "must be allocators, benches or tools",
		"hello":           "must be allocators, benches or tools",
	}
	for ref, want := range tests {
		if _, err := f.builder().Build(ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", ref, err, want)
		}
	}
}

func TestBuildDepsCycle(t *testing.T) {
	f := newFixture(t)
	f.set.Tools["a"] = recipe.Tool{BuildEnv: "host", Source: recipe.Source{Path: "src/greet"}, Build: recipe.Build{Script: "true", Deps: []string{"b"}}}
	f.set.Tools["b"] = recipe.Tool{BuildEnv: "host", Source: recipe.Source{Path: "src/greet"}, Build: recipe.Build{Script: "true", Deps: []string{"a"}}}
	if _, err := f.builder().Build("tools/a"); err == nil || !strings.Contains(err.Error(), "deps cycle") {
		t.Errorf("got %v", err)
	}
}

func TestInputKey(t *testing.T) {
	base := target{hash: "r", build: recipe.Build{Deps: []string{"x"}}}
	deps := []Result{{Hash: "d1"}}
	k := inputKey(base, "s", deps)
	if len(k) != 64 || k != inputKey(base, "s", deps) {
		t.Fatalf("key %q is not a stable sha256", k)
	}
	changed := map[string]string{
		"recipe": inputKey(target{hash: "r2", build: base.build}, "s", deps),
		"source": inputKey(base, "s2", deps),
		"dep":    inputKey(base, "s", []Result{{Hash: "d2"}}),
	}
	for what, other := range changed {
		if other == k {
			t.Errorf("changing the %s does not change the key", what)
		}
	}
}
