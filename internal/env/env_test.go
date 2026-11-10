package env

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

var (
	testHash = strings.Repeat("e", 64)
	testEnv  = recipe.Env{
		Distro:   "ubuntu",
		Suite:    "noble",
		Snapshot: "20260101T000000Z",
		Packages: []string{"gcc-13", "build-essential", "gcc-13"},
	}
)

func TestArgs(t *testing.T) {
	got, err := Args(testEnv, "/out/rootfs.tar", "https://mirror/ubuntu", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--mode=unshare",
		"--variant=apt",
		"--include=build-essential,gcc-13",
		"noble",
		"/out/rootfs.tar",
		"deb [check-valid-until=no] https://mirror/ubuntu noble main universe",
		"deb [check-valid-until=no] https://mirror/ubuntu noble-updates main universe",
		"deb [check-valid-until=no] https://mirror/ubuntu noble-security main universe",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if testEnv.Packages[0] != "gcc-13" {
		t.Error("Args reordered the recipe packages")
	}
}

func TestArgsProxy(t *testing.T) {
	got, err := Args(testEnv, "/out/rootfs.tar", "https://mirror/ubuntu", "http://127.0.0.1:10809/")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, w := range []string{
		`--aptopt=Acquire::http::Proxy "http://127.0.0.1:10809/"`,
		`--aptopt=Acquire::https::Proxy "http://127.0.0.1:10809/"`,
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %s in:\n%s", w, joined)
		}
	}
}

func TestArgsUnsupportedDistro(t *testing.T) {
	e := testEnv
	e.Distro = "fedora"
	if _, err := Args(e, "/out/rootfs.tar", "", ""); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("got %v", err)
	}
}

func TestSnapshotEpoch(t *testing.T) {
	got, err := SnapshotEpoch("20260101T000000Z")
	if err != nil || got != 1767225600 {
		t.Errorf("got %d, %v", got, err)
	}
	if _, err := SnapshotEpoch("2026-01-01"); err == nil {
		t.Error("expected an error")
	}
}

const fakeMmdebstrap = `#!/bin/sh
set -eu
dir=$(dirname "$0")
echo call >> "$dir/calls"
printf '%s\n' "$@" > "$dir/args"
echo "$SOURCE_DATE_EPOCH" > "$dir/epoch"
[ -f "$dir/fail" ] && { echo "E: download failed" >&2; exit 1; }
out=""
for a in "$@"; do case "$a" in *.tar) out=$a ;; esac; done
tar -cf "$out" -C "$dir/tree" .
`

type fake struct {
	dir string
	b   *Builder
}

func newFake(t *testing.T) *fake {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not installed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "mmdebstrap")
	if err := os.WriteFile(script, []byte(fakeMmdebstrap), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(dir, "tree")
	for path, content := range map[string]string{
		"usr/bin/gcc":       "#!/bin/sh\n",
		"etc/os-release":    "NAME=Ubuntu\n",
		"usr/lib/libc.so.6": "elf",
	} {
		p := filepath.Join(tree, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	os.Chmod(filepath.Join(tree, "usr/bin/gcc"), 0o755)
	os.Symlink("usr/bin", filepath.Join(tree, "bin"))

	s, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return &fake{dir: dir, b: &Builder{Store: s, Mmdebstrap: script, Mirror: "https://mirror/ubuntu"}}
}

func (f *fake) calls(t *testing.T) int {
	data, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "call")
}

func TestEnsure(t *testing.T) {
	f := newFake(t)

	e, err := f.b.Ensure(testEnv, testHash)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if e.Hash != testHash || e.Rootfs != filepath.Join(f.b.Store.Path(store.Env, testHash), "rootfs") {
		t.Errorf("env: %+v", e)
	}
	if data, err := os.ReadFile(filepath.Join(e.Rootfs, "etc", "os-release")); err != nil || string(data) != "NAME=Ubuntu\n" {
		t.Errorf("rootfs content: %q, %v", data, err)
	}
	if link, err := os.Readlink(filepath.Join(e.Rootfs, "bin")); err != nil || link != "usr/bin" {
		t.Errorf("symlink: %q, %v", link, err)
	}
	if want, _ := source.TreeHash(filepath.Join(f.dir, "tree")); e.Tree != want {
		t.Errorf("tree: got %s, want %s", e.Tree, want)
	}
	if e.Mirror != "https://mirror/ubuntu" {
		t.Errorf("mirror: %q", e.Mirror)
	}
	if _, err := os.Stat(filepath.Join(f.b.Store.Path(store.Env, testHash), "rootfs.tar")); !os.IsNotExist(err) {
		t.Error("tarball was not removed")
	}

	var info map[string]string
	data, _ := os.ReadFile(filepath.Join(f.b.Store.Path(store.Env, testHash), infoFile))
	if err := json.Unmarshal(data, &info); err != nil || info["tree"] != e.Tree {
		t.Errorf("info file: %s, %v", data, err)
	}

	if epoch, _ := os.ReadFile(filepath.Join(f.dir, "epoch")); strings.TrimSpace(string(epoch)) != "1767225600" {
		t.Errorf("SOURCE_DATE_EPOCH: %q", epoch)
	}
	args, _ := os.ReadFile(filepath.Join(f.dir, "args"))
	if !strings.Contains(string(args), "--include=build-essential,gcc-13") {
		t.Errorf("args:\n%s", args)
	}
}

func TestEnsureUsesStore(t *testing.T) {
	f := newFake(t)
	first, err := f.b.Ensure(testEnv, testHash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.b.Ensure(testEnv, testHash)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("got %+v, want %+v", second, first)
	}
	if n := f.calls(t); n != 1 {
		t.Errorf("mmdebstrap was called %d times, want 1", n)
	}
}

func TestEnsureFailure(t *testing.T) {
	f := newFake(t)
	os.WriteFile(filepath.Join(f.dir, "fail"), nil, 0o644)

	_, err := f.b.Ensure(testEnv, testHash)
	if err == nil {
		t.Fatal("expected an error")
	}
	logPath := f.b.Store.Log(store.Env, testHash)
	for _, w := range []string{"mmdebstrap: exit status 1", "  | E: download failed", "full log: " + logPath} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
	if data, _ := os.ReadFile(logPath); string(data) != "E: download failed\n" {
		t.Errorf("log: %q", data)
	}
	if f.b.Store.Has(store.Env, testHash) {
		t.Error("failed env is in the store")
	}

	os.Remove(filepath.Join(f.dir, "fail"))
	if _, err := f.b.Ensure(testEnv, testHash); err != nil {
		t.Errorf("Ensure after a failure: %v", err)
	}
}

func TestEnsureDefaultMirror(t *testing.T) {
	f := newFake(t)
	f.b.Mirror = ""
	e, err := f.b.Ensure(testEnv, testHash)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://snapshot.ubuntu.com/ubuntu/20260101T000000Z"; e.Mirror != want {
		t.Errorf("mirror: got %q, want %q", e.Mirror, want)
	}
}

func TestEnsureBadRecipe(t *testing.T) {
	f := newFake(t)
	e := testEnv
	e.Distro = "fedora"
	if _, err := f.b.Ensure(e, testHash); err == nil {
		t.Error("expected an error")
	}
	if n := f.calls(t); n != 0 {
		t.Errorf("mmdebstrap was called %d times for a bad recipe", n)
	}
}

func TestEnsureLogCopy(t *testing.T) {
	f := newFake(t)
	var out strings.Builder
	f.b.Log = &out
	os.WriteFile(filepath.Join(f.dir, "fail"), nil, 0o644)
	f.b.Ensure(testEnv, testHash)
	if out.String() != "E: download failed\n" {
		t.Errorf("Log writer got %q", out.String())
	}
}
