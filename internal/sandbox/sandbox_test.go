package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func fakeRootfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"usr/bin", "usr/lib", "etc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"bin": "usr/bin", "lib": "usr/lib", "lib64": "usr/lib64"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestArgs(t *testing.T) {
	root := fakeRootfs(t)
	got, err := Args(Spec{
		Rootfs: root,
		Mounts: []Mount{
			{Src: "/store/src/abc", Dst: "/src"},
			{Src: "/store/tmp/out", Dst: "/out", RW: true},
		},
		Tmpfs: []string{"/build"},
		Env:   map[string]string{"JOBS": "8", "HOME": "/build"},
		Dir:   "/build",
		Cmd:   []string{"/bin/sh", "-euc", "make"},
	})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}

	want := []string{
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", filepath.Join(root, "usr"), "/usr",
		"--ro-bind", filepath.Join(root, "etc"), "/etc",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var", "--tmpfs", "/run",
		"--tmpfs", "/build",
		"--ro-bind", "/store/src/abc", "/src",
		"--bind", "/store/tmp/out", "/out",
		"--setenv", "HOME", "/build",
		"--setenv", "JOBS", "8",
		"--setenv", "LC_ALL", "C.UTF-8",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "TZ", "UTC",
		"--hostname", "sandbox", "--chdir", "/build",
		"/bin/sh", "-euc", "make",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestArgsDefaults(t *testing.T) {
	got, err := Args(Spec{Rootfs: fakeRootfs(t), Cmd: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, w := range []string{"--chdir / /bin/true", "--setenv HOME /tmp", "--clearenv"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in %s", w, joined)
		}
	}
	if strings.Contains(joined, "--share-net") || strings.Contains(joined, "--bind ") {
		t.Errorf("unexpected network or writable mount: %s", joined)
	}
}

func TestArgsRootfsWithRealDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"usr", "etc", "bin", "lib"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	got, err := Args(Spec{Rootfs: root, Cmd: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, d := range []string{"bin", "lib"} {
		if !strings.Contains(joined, "--ro-bind "+filepath.Join(root, d)+" /"+d) {
			t.Errorf("/%s is not bound: %s", d, joined)
		}
	}
}

func TestArgsErrors(t *testing.T) {
	root := fakeRootfs(t)
	ok := Spec{Rootfs: root, Cmd: []string{"/bin/true"}}
	tests := []struct {
		name   string
		change func(s *Spec)
		want   string
	}{
		{"no rootfs", func(s *Spec) { s.Rootfs = "" }, "rootfs is required"},
		{"rootfs without usr", func(s *Spec) { s.Rootfs = t.TempDir() }, "has no usr"},
		{"no command", func(s *Spec) { s.Cmd = nil }, "absolute path"},
		{"relative command", func(s *Spec) { s.Cmd = []string{"make"} }, "absolute path"},
		{"option as command", func(s *Spec) { s.Cmd = []string{"--bind", "/", "/"} }, "absolute path"},
		{"relative mount point", func(s *Spec) { s.Mounts = []Mount{{Src: "/a", Dst: "src"}} }, "clean absolute path"},
		{"unclean mount point", func(s *Spec) { s.Mounts = []Mount{{Src: "/a", Dst: "/a/../usr"}} }, "clean absolute path"},
		{"root mount point", func(s *Spec) { s.Mounts = []Mount{{Src: "/a", Dst: "/"}} }, "reserved"},
		{"over usr", func(s *Spec) { s.Mounts = []Mount{{Src: "/a", Dst: "/usr"}} }, "read-only rootfs"},
		{"inside usr", func(s *Spec) { s.Mounts = []Mount{{Src: "/a", Dst: "/usr/local/x"}} }, "read-only rootfs"},
		{"inside proc", func(s *Spec) { s.Tmpfs = []string{"/proc/x"} }, "read-only rootfs"},
		{"relative source", func(s *Spec) { s.Mounts = []Mount{{Src: "store/src", Dst: "/src"}} }, "absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ok
			tt.change(&s)
			_, err := Args(s)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestArgsUsrPrefixIsNotUsr(t *testing.T) {
	_, err := Args(Spec{Rootfs: fakeRootfs(t), Mounts: []Mount{{Src: "/a", Dst: "/usrdata"}}, Cmd: []string{"/bin/true"}})
	if err != nil {
		t.Errorf("/usrdata should be allowed: %v", err)
	}
}

func TestRealBwrap(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap does not work here: %v %s", err, out)
	}

	in := t.TempDir()
	out := t.TempDir()
	os.WriteFile(filepath.Join(in, "input"), []byte("hello"), 0o644)
	os.Setenv("ALLOCBENCH_SECRET", "leak")
	defer os.Unsetenv("ALLOCBENCH_SECRET")

	script := `
cat /src/input > /out/copy
echo "home=$HOME host=$(hostname) secret=${ALLOCBENCH_SECRET:-none} pwd=$(pwd)"
touch /src/x 2>/dev/null && echo src-writable || echo src-readonly
touch /usr/x 2>/dev/null && echo usr-writable || echo usr-readonly
test -e ` + in + ` && echo host-visible || echo host-hidden
tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '
`
	args, err := Args(Spec{
		Rootfs: "/",
		Mounts: []Mount{{Src: in, Dst: "/src"}, {Src: out, Dst: "/out", RW: true}},
		Tmpfs:  []string{"/build"},
		Dir:    "/build",
		Cmd:    []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command(bwrap, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap: %v\n%s", err, got)
	}

	want := "home=/tmp host=sandbox secret=none pwd=/build\nsrc-readonly\nusr-readonly\nhost-hidden\nlo\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(out, "copy")); err != nil || string(data) != "hello" {
		t.Errorf("/out was not written: %q, %v", data, err)
	}
}

func TestArgsInteractive(t *testing.T) {
	root := fakeRootfs(t)
	batch, err := Args(Spec{Rootfs: root, Cmd: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	interactive, err := Args(Spec{Rootfs: root, Cmd: []string{"/bin/sh"}, Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(batch, "--new-session") {
		t.Errorf("batch sandbox has no --new-session: %v", batch)
	}
	if slices.Contains(interactive, "--new-session") {
		t.Errorf("interactive sandbox has --new-session: %v", interactive)
	}
	if want := slices.DeleteFunc(slices.Clone(batch), func(a string) bool { return a == "--new-session" }); !reflect.DeepEqual(interactive, want) {
		t.Errorf("interactive differs in more than --new-session:\n%v\n%v", interactive, want)
	}
}
