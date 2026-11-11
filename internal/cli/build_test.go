package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/build"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

func TestResolveRef(t *testing.T) {
	s := &recipe.Set{
		Allocators: map[string]recipe.Allocator{"mi": {}, "same": {}},
		Benches:    map[string]recipe.Bench{"cfrac": {}, "same": {}},
		Tools:      map[string]recipe.Tool{"cmake": {}},
	}
	ok := map[string]string{
		"mi":              "allocators/mi",
		"cfrac":           "benches/cfrac",
		"cmake":           "tools/cmake",
		"allocators/same": "allocators/same",
		"benches/same":    "benches/same",
	}
	for arg, want := range ok {
		if got, err := resolveRef(s, arg, buildKinds); err != nil || got != want {
			t.Errorf("%s: got %q, %v, want %q", arg, got, err, want)
		}
	}

	bad := map[string]string{
		"same":            "ambiguous, use one of: allocators/same, benches/same",
		"nope":            "no allocator, bench or tool",
		"allocators/nope": "no such recipe",
		"envs/mi":         "no such recipe",
	}
	for arg, want := range bad {
		if _, err := resolveRef(s, arg, buildKinds); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", arg, err, want)
		}
	}
}

func TestPrintBuilds(t *testing.T) {
	var out strings.Builder
	err := printBuilds(&out, []build.Result{
		{Recipe: "allocators/mi", Hash: strings.Repeat("a", 64), Dir: "/store/build/a"},
		{Recipe: "benches/cfrac", Hash: strings.Repeat("b", 64), Dir: "/store/build/b", Cached: true},
		{Recipe: "allocators/sys"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "RECIPE          STATUS            HASH          OUTPUT\n" +
		"allocators/mi   built             aaaaaaaaaaaa  /store/build/a\n" +
		"benches/cfrac   cached            bbbbbbbbbbbb  /store/build/b\n" +
		"allocators/sys  nothing to build  -             -\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestBuildRequiresArgs(t *testing.T) {
	out, err := runCLI(t, "build", "--recipes", testdata("valid"))
	if err == nil || !strings.Contains(err.Error(), "requires at least 1 arg") {
		t.Errorf("got %v\n%s", err, out)
	}
}

func TestBuildUnknownName(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	out, err := runCLI(t, "build", "--recipes", testdata("valid"), "--store", store, "nope")
	if err == nil || !strings.Contains(err.Error(), "nope: no allocator, bench or tool") {
		t.Errorf("got %v\n%s", err, out)
	}
}

func TestProxyFromEnv(t *testing.T) {
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY"} {
		t.Setenv(k, "")
	}
	if got := proxyFromEnv(); got != "" {
		t.Errorf("got %q", got)
	}
	t.Setenv("HTTP_PROXY", "http://plain:1/")
	t.Setenv("https_proxy", "http://secure:2/")
	if got := proxyFromEnv(); got != "http://secure:2/" {
		t.Errorf("got %q", got)
	}
}

const projectEnv = `distro = "ubuntu"
suite = "noble"
snapshot = "20260101T000000Z"
packages = ["gcc"]
`

const projectTool = `build_env = "host"
[source]
path = "files/greet"
[build]
script = "install -Dm755 /src/greet.sh /out/bin/greet"
`

const projectAlloc = `build_env = "host"
[source]
path = "files/hello"
[build]
deps = ["greet"]
script = """
greet
cp /src/message /out/message
"""
`

func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"recipes/envs/host.toml":        projectEnv,
		"recipes/tools/greet.toml":      projectTool,
		"recipes/allocators/hello.toml": projectAlloc,
		"recipes/allocators/sys.toml":   "",
		"files/hello/message":           "hello\n",
		"files/greet/greet.sh":          "#!/bin/sh\necho greet from dep\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func seedHostEnv(t *testing.T, recipes, storeDir string) {
	t.Helper()
	s, err := recipe.Load(recipes)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.EnvHash("host")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	dir := st.Path(store.Env, h)
	os.MkdirAll(dir, 0o755)
	if err := os.Symlink("/", filepath.Join(dir, "rootfs")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"tree":"host","mirror":"none"}`), 0o644)
}

func TestBuildCommand(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap does not work here: %v %s", err, out)
	}

	dir := writeProject(t)
	recipes := filepath.Join(dir, "recipes")
	storeDir := filepath.Join(dir, "store")
	seedHostEnv(t, recipes, storeDir)

	out, err := runCLI(t, "build", "--recipes", recipes, "--store", storeDir, "-v", "-j", "2", "hello", "sys")
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, w := range []string{
		"building tools/greet\n",
		"greet from dep\n",
		"building allocators/hello\n",
		"allocators/sys    nothing to build  -",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if !strings.Contains(out, "allocators/hello  built") {
		t.Errorf("hello is not built:\n%s", out)
	}
	matches, _ := filepath.Glob(filepath.Join(storeDir, "build", "*", "message"))
	if len(matches) != 1 {
		t.Fatalf("outputs: %v", matches)
	}
	if data, _ := os.ReadFile(matches[0]); string(data) != "hello\n" {
		t.Errorf("output: %q", data)
	}

	out, err = runCLI(t, "build", "--recipes", recipes, "--store", storeDir, "allocators/hello")
	if err != nil {
		t.Fatalf("second build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "allocators/hello  cached") || strings.Contains(out, "building") {
		t.Errorf("second build is not cached:\n%s", out)
	}
}
