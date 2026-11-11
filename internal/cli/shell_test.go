package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
)

func runShell(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewRootCommand("dev")
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"shell"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func shellProject(t *testing.T) (recipes, storeDir string) {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap does not work here: %v %s", err, out)
	}
	dir := writeProject(t)
	recipes = filepath.Join(dir, "recipes")
	storeDir = filepath.Join(dir, "store")
	seedHostEnv(t, recipes, storeDir)
	return recipes, storeDir
}

func TestShellCommand(t *testing.T) {
	recipes, storeDir := shellProject(t)
	out, errOut, err := runShell(t, "--recipes", recipes, "--store", storeDir, "-j", "3", "hello", "--",
		"sh", "-c", `greet; echo "pwd=$(pwd) jobs=$JOBS chroot=$debian_chroot"; cat /src/message; ls /out | wc -l`)
	if err != nil {
		t.Fatalf("shell: %v\n%s", err, errOut)
	}
	want := "greet from dep\npwd=/build jobs=3 chroot=allocators/hello\nhello\n0\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if !strings.Contains(errOut, "/out is temporary") {
		t.Errorf("stderr: %s", errOut)
	}
	if matches, _ := filepath.Glob(filepath.Join(storeDir, "build", "*", "message")); len(matches) != 0 {
		t.Errorf("shell built the recipe itself: %v", matches)
	}
}

func TestShellExitCode(t *testing.T) {
	recipes, storeDir := shellProject(t)
	_, errOut, err := runShell(t, "--recipes", recipes, "--store", storeDir, "hello", "--", "sh", "-c", "exit 7")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(errOut, "Error:") {
		t.Errorf("exit code is reported as an error:\n%s", errOut)
	}
}

func TestShellOut(t *testing.T) {
	recipes, storeDir := shellProject(t)
	keep := filepath.Join(t.TempDir(), "out")
	_, errOut, err := runShell(t, "--recipes", recipes, "--store", storeDir, "--out", keep, "hello", "--", "sh", "-c", "echo kept > /out/file")
	if err != nil {
		t.Fatalf("shell: %v\n%s", err, errOut)
	}
	if data, err := os.ReadFile(filepath.Join(keep, "file")); err != nil || string(data) != "kept\n" {
		t.Errorf("--out: %q, %v", data, err)
	}
	if !strings.Contains(errOut, "/out is "+keep) {
		t.Errorf("stderr: %s", errOut)
	}
}

func TestShellEnv(t *testing.T) {
	recipes, storeDir := shellProject(t)
	out, errOut, err := runShell(t, "--recipes", recipes, "--store", storeDir, "envs/host", "--", "sh", "-c", "pwd; test -e /src && echo src || echo no-src")
	if err != nil {
		t.Fatalf("shell: %v\n%s", err, errOut)
	}
	if out != "/build\nno-src\n" {
		t.Errorf("got %q", out)
	}
}

func TestShellErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, "expected one recipe before --, got 0"},
		{[]string{"a", "b"}, "expected one recipe before --, got 2"},
		{[]string{"--", "sh"}, "expected one recipe before --, got 0"},
		{[]string{"nope"}, "nope: no allocator, bench, tool or env with this name"},
		{[]string{"sys"}, "allocators/sys has nothing to build"},
	}
	for _, tt := range tests {
		args := append([]string{"--recipes", testdata("valid"), "--store", filepath.Join(t.TempDir(), "store")}, tt.args...)
		_, _, err := runShell(t, args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: got %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestShellCommandArgs(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{nil, []string{"/bin/bash"}},
		{[]string{"/usr/bin/make", "-j2"}, []string{"/usr/bin/make", "-j2"}},
		{[]string{"cmake", "--version"}, []string{"/usr/bin/env", "cmake", "--version"}},
	}
	for _, tt := range tests {
		if got := shellCommand(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%v: got %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestOutDir(t *testing.T) {
	dir, cleanup, err := outDir("")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("temporary dir: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temporary dir is not removed: %v", err)
	}

	keep := filepath.Join(t.TempDir(), "a", "b")
	dir, cleanup, err = outDir(keep)
	if err != nil || dir != keep {
		t.Fatalf("got %q, %v", dir, err)
	}
	cleanup()
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("--out dir is removed: %v", err)
	}
}

func TestResolveRefEnvs(t *testing.T) {
	s := &recipe.Set{
		Envs:       map[string]recipe.Env{"gcc13": {}, "same": {}},
		Allocators: map[string]recipe.Allocator{"same": {}},
	}
	kinds := []string{"allocators", "benches", "tools", "envs"}
	if got, err := resolveRef(s, "gcc13", kinds); err != nil || got != "envs/gcc13" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := resolveRef(s, "same", kinds); err == nil || !strings.Contains(err.Error(), "allocators/same, envs/same") {
		t.Errorf("got %v", err)
	}
	if _, err := resolveRef(s, "gcc13", buildKinds); err == nil || !strings.Contains(err.Error(), "no allocator, bench or tool") {
		t.Errorf("build accepts an env: %v", err)
	}
	if _, err := resolveRef(s, "envs/gcc13", buildKinds); err == nil {
		t.Error("build accepts envs/gcc13")
	}
}

func TestDescribeKinds(t *testing.T) {
	tests := map[string][]string{
		"allocator":                     {"allocators"},
		"allocator or env":              {"allocators", "envs"},
		"allocator, bench, tool or env": {"allocators", "benches", "tools", "envs"},
	}
	for want, kinds := range tests {
		if got := describeKinds(kinds); got != want {
			t.Errorf("%v: got %q, want %q", kinds, got, want)
		}
	}
}
