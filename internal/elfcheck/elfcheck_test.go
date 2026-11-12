package elfcheck

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	libDir   = "usr/lib/x86_64-linux-gnu"
	scriptV1 = "DEMO_1 { global: f1; local: *; };\n"
	scriptV2 = "DEMO_1 { global: f1; local: *; };\nDEMO_2 { global: f2; } DEMO_1;\n"
	sourceV1 = "int f1(void) { return 1; }\n"
	sourceV2 = "int f1(void) { return 1; }\nint f2(void) { return 2; }\n"
	program  = "int f2(void);\nint g(void) { return f2(); }\n"
)

type fixtures struct {
	v1, v2, prog, origin string
}

func build(t *testing.T) fixtures {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) {
		if out, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
			t.Fatalf("cc %v: %v\n%s", args, err, out)
		}
	}
	lib := func(name, src, script string) string {
		out := filepath.Join(dir, name, "libdemo.so.1")
		os.MkdirAll(filepath.Dir(out), 0o755)
		run("-shared", "-fPIC", "-nostdlib", "-Wl,-soname,libdemo.so.1", "-Wl,--version-script,"+write(name+".map", script), "-o", out, write(name+".c", src))
		return out
	}
	f := fixtures{v1: lib("v1", sourceV1, scriptV1), v2: lib("v2", sourceV2, scriptV2)}
	os.Symlink("libdemo.so.1", filepath.Join(dir, "v2", "libdemo.so"))

	f.prog = filepath.Join(dir, "prog", "prog.so")
	os.MkdirAll(filepath.Dir(f.prog), 0o755)
	run("-shared", "-fPIC", "-nostdlib", "-o", f.prog, write("prog.c", program), "-L"+filepath.Join(dir, "v2"), "-ldemo")

	f.origin = filepath.Join(dir, "out")
	os.MkdirAll(filepath.Join(f.origin, "bin"), 0o755)
	os.MkdirAll(filepath.Join(f.origin, "lib"), 0o755)
	run("-shared", "-fPIC", "-nostdlib", "-o", filepath.Join(f.origin, "bin", "prog.so"), write("prog2.c", program),
		"-L"+filepath.Join(dir, "v2"), "-ldemo", "-Wl,-rpath,$ORIGIN/../lib")
	copyFile(t, f.v2, filepath.Join(f.origin, "lib", "libdemo.so.1"))
	return f
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func rootfs(t *testing.T, lib string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, libDir), 0o755)
	if lib != "" {
		copyFile(t, lib, filepath.Join(root, libDir, "libdemo.so.1"))
	}
	return root
}

func check(t *testing.T, root, file string) []string {
	t.Helper()
	problems, err := (&Checker{Root: root}).CheckFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range problems {
		out = append(out, p.Message)
	}
	return out
}

func TestVersions(t *testing.T) {
	f := build(t)
	for lib, want := range map[string]map[string]bool{
		f.v1: {"DEMO_1": true},
		f.v2: {"DEMO_1": true, "DEMO_2": true},
	} {
		ef, err := elf.Open(lib)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Versions(ef)
		ef.Close()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, %v, want %v", lib, got, err, want)
		}
	}
}

func TestCheckFile(t *testing.T) {
	f := build(t)
	if got := check(t, rootfs(t, f.v2), f.prog); len(got) != 0 {
		t.Errorf("new library: %v", got)
	}
	if got := check(t, rootfs(t, f.v1), f.prog); !reflect.DeepEqual(got, []string{"needs DEMO_2 from libdemo.so.1, not in the run environment"}) {
		t.Errorf("old library: %v", got)
	}
	if got := check(t, rootfs(t, ""), f.prog); !reflect.DeepEqual(got, []string{"needs libdemo.so.1, not in the run environment"}) {
		t.Errorf("no library: %v", got)
	}
}

func TestSymlinksStayInRoot(t *testing.T) {
	f := build(t)

	root := t.TempDir()
	copyFile(t, f.v2, filepath.Join(root, "opt", "demo", "libdemo.so.1.0"))
	os.MkdirAll(filepath.Join(root, "usr", "lib"), 0o755)
	os.Symlink("usr/lib", filepath.Join(root, "lib"))
	os.Symlink("/opt/demo/libdemo.so.1.0", filepath.Join(root, "usr", "lib", "libdemo.so.1"))
	if got := check(t, root, f.prog); len(got) != 0 {
		t.Errorf("relative and absolute symlinks inside the root: %v", got)
	}

	escape := t.TempDir()
	os.MkdirAll(filepath.Join(escape, "usr", "lib"), 0o755)
	os.Symlink(f.v2, filepath.Join(escape, "usr", "lib", "libdemo.so.1"))
	if got := check(t, escape, f.prog); len(got) != 1 || !strings.Contains(got[0], "needs libdemo.so.1") {
		t.Errorf("a symlink to a host file is followed: %v", got)
	}

	loop := t.TempDir()
	os.MkdirAll(filepath.Join(loop, "usr", "lib"), 0o755)
	os.Symlink("libdemo.so.1", filepath.Join(loop, "usr", "lib", "libdemo.so.1"))
	if got := check(t, loop, f.prog); len(got) != 1 {
		t.Errorf("symlink loop: %v", got)
	}
}

func TestOrigin(t *testing.T) {
	f := build(t)
	if got := check(t, rootfs(t, ""), filepath.Join(f.origin, "bin", "prog.so")); len(got) != 0 {
		t.Errorf("$ORIGIN runpath: %v", got)
	}
	os.Remove(filepath.Join(f.origin, "lib", "libdemo.so.1"))
	if got := check(t, rootfs(t, ""), filepath.Join(f.origin, "bin", "prog.so")); len(got) != 1 {
		t.Errorf("$ORIGIN without the library: %v", got)
	}
}

func TestCheckDir(t *testing.T) {
	f := build(t)
	out := t.TempDir()
	copyFile(t, f.prog, filepath.Join(out, "lib", "prog.so"))
	os.WriteFile(filepath.Join(out, "README"), []byte("text"), 0o644)
	os.WriteFile(filepath.Join(out, "empty"), nil, 0o644)
	os.WriteFile(filepath.Join(out, "tiny"), []byte("\x7fE"), 0o644)
	os.Symlink("lib/prog.so", filepath.Join(out, "link.so"))

	problems, err := (&Checker{Root: rootfs(t, f.v1)}).CheckDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].String() != "lib/prog.so: needs DEMO_2 from libdemo.so.1, not in the run environment" {
		t.Errorf("problems: %v", problems)
	}
}

func TestObjectFilesAreSkipped(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "x.c")
	os.WriteFile(src, []byte(program), 0o644)
	obj := filepath.Join(dir, "x.o")
	if out, err := exec.Command(cc, "-c", "-o", obj, src).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := check(t, t.TempDir(), obj); len(got) != 0 {
		t.Errorf("object file: %v", got)
	}
}

func TestHostBinaryAgainstHost(t *testing.T) {
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skip(err)
	}
	if got := check(t, "/", sh); len(got) != 0 {
		t.Errorf("%s does not fit its own system: %v", sh, got)
	}
	if got := check(t, t.TempDir(), sh); len(got) == 0 || !strings.Contains(got[0], "libc.so.6") {
		t.Errorf("%s in an empty root: %v", sh, got)
	}
}

func TestCstring(t *testing.T) {
	data := []byte("\x00abc\x00def")
	tests := []struct {
		off  int
		want string
		ok   bool
	}{{1, "abc", true}, {0, "", true}, {5, "", false}, {-1, "", false}, {100, "", false}}
	for _, tt := range tests {
		if got, ok := cstring(data, tt.off); got != tt.want || ok != tt.ok {
			t.Errorf("%d: got %q %v", tt.off, got, ok)
		}
	}
}
