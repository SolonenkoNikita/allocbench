package cell

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/plan"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/sandbox"
)

const preloadSource = `#include <stdio.h>
#include <stdlib.h>

__attribute__((constructor)) static void loaded(void)
{
	if (getenv("PRELOAD_MARK"))
		fprintf(stderr, "preload loaded\n");
}
`

const benchScript = `#!/bin/sh
echo "args=$* threads=$THREADS preload=${LD_PRELOAD:-none} mark=${PRELOAD_MARK:-none} pwd=$(pwd)"
case "$1" in
fail) exit 3 ;;
hang) sleep 30 ;;
write) echo data > file; ls ;;
esac
`

func writeFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func cell(alloc string) plan.Cell {
	return plan.Cell{Alloc: alloc, Bench: "fake", Threads: 4, Repeat: 2}
}

func bench(args ...string) recipe.Bench {
	return recipe.Bench{Run: recipe.Run{Cmd: append([]string{"/bench/bin/bench"}, args...)}}
}

func mi() recipe.Allocator {
	return recipe.Allocator{Use: recipe.Use{Preload: "lib/libfake.so", Env: map[string]string{"PRELOAD_MARK": "1"}}}
}

func mountsOf(spec sandbox.Spec) map[string]sandbox.Mount {
	out := map[string]sandbox.Mount{}
	for _, m := range spec.Mounts {
		out[m.Dst] = m
	}
	return out
}

func TestSpecSys(t *testing.T) {
	out := t.TempDir()
	e := &Executor{Rootfs: "/rootfs", Runner: "/store/runner"}
	spec, cleanup, err := e.Spec(Input{Cell: cell("sys"), Bench: bench("{threads}", "x{threads}y"), BenchDir: "/store/bench", Out: out})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	want := []string{
		"/runner/bin/runner",
		"--env", "THREADS=4",
		"--timeout", "600",
		"--stdout", "/results/stdout",
		"--stderr", "/results/stderr",
		"--result", "/results/result.json",
		"--", "/bench/bin/bench", "4", "x4y",
	}
	if !reflect.DeepEqual(spec.Cmd, want) {
		t.Errorf("cmd:\n%v\nwant:\n%v", spec.Cmd, want)
	}
	m := mountsOf(spec)
	if _, ok := m["/alloc"]; ok {
		t.Error("sys has /alloc")
	}
	if m["/runner"].Src != "/store/runner" || m["/runner"].RW || m["/bench"].Src != "/store/bench" || m["/bench"].RW {
		t.Errorf("mounts: %+v", spec.Mounts)
	}
	if m["/results"].Src != out || !m["/results"].RW {
		t.Errorf("/results: %+v", m["/results"])
	}
	if spec.Rootfs != "/rootfs" || spec.Dir != "/work" || spec.Env["HOME"] != "/work" || !slices.Equal(spec.Tmpfs, []string{"/work"}) {
		t.Errorf("spec: %+v", spec)
	}
	if _, ok := spec.Env["LD_PRELOAD"]; ok {
		t.Error("LD_PRELOAD is set for the whole sandbox")
	}
}

func TestSpecPreload(t *testing.T) {
	alloc := t.TempDir()
	writeFile(t, filepath.Join(alloc, "lib", "libfake.so"), "", 0o644)
	e := &Executor{Rootfs: "/rootfs", Runner: "/store/runner"}
	b := bench()
	b.Run.Timeout = recipe.Duration{Duration: 1500 * time.Millisecond}
	spec, cleanup, err := e.Spec(Input{Cell: cell("mi"), Alloc: mi(), AllocDir: alloc, Bench: b, BenchDir: "/store/bench", Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	joined := strings.Join(spec.Cmd, " ")
	for _, w := range []string{"--env LD_PRELOAD=/alloc/lib/libfake.so", "--env PRELOAD_MARK=1", "--env THREADS=4", "--timeout 1.5"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in %s", w, joined)
		}
	}
	if m := mountsOf(spec)["/alloc"]; m.Src != alloc || m.RW {
		t.Errorf("/alloc: %+v", m)
	}
}

func TestSpecDiskWork(t *testing.T) {
	out := t.TempDir()
	b := bench()
	b.Run.Work = WorkDisk
	spec, cleanup, err := (&Executor{Rootfs: "/rootfs", Runner: "/r"}).Spec(Input{Cell: cell("sys"), Bench: b, BenchDir: "/b", Out: out})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(out, "work")
	if m := mountsOf(spec)["/work"]; m.Src != work || !m.RW || len(spec.Tmpfs) != 0 {
		t.Errorf("work: %+v, tmpfs %v", m, spec.Tmpfs)
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("work dir is not created: %v", err)
	}
	cleanup()
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("work dir is not removed: %v", err)
	}
}

func TestSpecErrors(t *testing.T) {
	e := &Executor{Rootfs: "/rootfs", Runner: "/r"}
	built := bench()
	built.Build.Script = "make"
	tests := []struct {
		name string
		in   Input
		want string
	}{
		{"no command", Input{Cell: cell("sys"), Bench: recipe.Bench{}}, "no run.cmd"},
		{"bench not built", Input{Cell: cell("sys"), Bench: built}, "bench is not built"},
		{"allocator not built", Input{Cell: cell("mi"), Alloc: mi(), Bench: bench(), BenchDir: "/b"}, "allocator is not built"},
		{"preload missing", Input{Cell: cell("mi"), Alloc: mi(), AllocDir: t.TempDir(), Bench: bench(), BenchDir: "/b"}, "use.preload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Out = t.TempDir()
			_, cleanup, err := e.Spec(tt.in)
			cleanup()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

type world struct {
	exec  *Executor
	alloc string
	bench string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap does not work here: %v %s", err, out)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}

	w := &world{alloc: t.TempDir(), bench: t.TempDir()}
	runnerDir := t.TempDir()
	src, _ := filepath.Abs(filepath.Join("..", "..", "runner", "runner.c"))
	os.MkdirAll(filepath.Join(runnerDir, "bin"), 0o755)
	if out, err := exec.Command(cc, "-O2", "-o", filepath.Join(runnerDir, "bin", "runner"), src).CombinedOutput(); err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
	soSrc := filepath.Join(t.TempDir(), "fake.c")
	writeFile(t, soSrc, preloadSource, 0o644)
	os.MkdirAll(filepath.Join(w.alloc, "lib"), 0o755)
	if out, err := exec.Command(cc, "-shared", "-fPIC", "-o", filepath.Join(w.alloc, "lib", "libfake.so"), soSrc).CombinedOutput(); err != nil {
		t.Fatalf("preload: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(w.bench, "bin", "bench"), benchScript, 0o755)
	w.exec = &Executor{Rootfs: "/", Runner: runnerDir}
	return w
}

func (w *world) run(t *testing.T, alloc string, b recipe.Bench) Result {
	t.Helper()
	in := Input{Cell: cell(alloc), Bench: b, BenchDir: w.bench, Out: filepath.Join(t.TempDir(), "cell")}
	if alloc == "mi" {
		in.Alloc = mi()
		in.AllocDir = w.alloc
	}
	r, err := w.exec.Run(in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return r
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestRunSys(t *testing.T) {
	w := newWorld(t)
	r := w.run(t, "sys", bench("{threads}"))
	want := "args=4 threads=4 preload=none mark=none pwd=/work\n"
	if !r.OK() || r.Status != "ok" || r.Measure.WallNs <= 0 {
		t.Fatalf("result: %+v", r)
	}
	if got := read(t, r.Dir, "stdout"); got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if r.StdoutSHA256 != sum(want) || r.Cell != cell("sys") {
		t.Errorf("result: %+v", r)
	}
	for _, f := range []string{"stderr", "result.json", "sandbox.log"} {
		if _, err := os.Stat(filepath.Join(r.Dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestRunPreload(t *testing.T) {
	w := newWorld(t)
	r := w.run(t, "mi", bench())
	if !r.OK() {
		t.Fatalf("result: %+v\n%s", r, read(t, r.Dir, "stderr"))
	}
	if got := read(t, r.Dir, "stdout"); got != "args= threads=4 preload=/alloc/lib/libfake.so mark=1 pwd=/work\n" {
		t.Errorf("stdout %q", got)
	}
	if got := read(t, r.Dir, "stderr"); got != "preload loaded\n" {
		t.Errorf("stderr %q: the allocator is not loaded into the bench", got)
	}
}

func TestRunCheck(t *testing.T) {
	w := newWorld(t)
	b := bench()
	b.Check.StdoutSHA256 = sum("args= threads=4 preload=none mark=none pwd=/work\n")
	if r := w.run(t, "sys", b); r.Status != StatusOK {
		t.Errorf("matching output: %s", r.Status)
	}
	b.Check.StdoutSHA256 = sum("something else\n")
	if r := w.run(t, "sys", b); r.Status != StatusWrongOutput || r.OK() {
		t.Errorf("wrong output: %s", r.Status)
	}
}

func TestRunFailures(t *testing.T) {
	w := newWorld(t)
	if r := w.run(t, "sys", bench("fail")); r.Status != "exit code 3" || r.OK() {
		t.Errorf("fail: %+v", r)
	}

	b := bench("hang")
	b.Run.Timeout = recipe.Duration{Duration: 300 * time.Millisecond}
	b.Check.StdoutSHA256 = sum("never")
	start := time.Now()
	r := w.run(t, "sys", b)
	if r.Status != "timeout" || !r.Measure.TimedOut || time.Since(start) > 10*time.Second {
		t.Errorf("hang: %+v after %v", r, time.Since(start))
	}
}

func TestRunWork(t *testing.T) {
	w := newWorld(t)
	tmpfs := w.run(t, "sys", bench("write"))
	if got := read(t, tmpfs.Dir, "stdout"); !strings.HasSuffix(got, "\nfile\n") {
		t.Errorf("tmpfs work: %q", got)
	}

	b := bench("write")
	b.Run.Work = WorkDisk
	disk := w.run(t, "sys", b)
	if !disk.OK() || !strings.HasSuffix(read(t, disk.Dir, "stdout"), "\nfile\n") {
		t.Errorf("disk work: %+v", disk)
	}
	if _, err := os.Stat(filepath.Join(disk.Dir, "work")); !os.IsNotExist(err) {
		t.Errorf("disk work dir is left behind: %v", err)
	}
}

func TestRunIsolated(t *testing.T) {
	w := newWorld(t)
	secret := filepath.Join(t.TempDir(), "secret")
	writeFile(t, secret, "x", 0o644)
	os.Setenv("ALLOCBENCH_SECRET", "leak")
	defer os.Unsetenv("ALLOCBENCH_SECRET")
	script := filepath.Join(w.bench, "bin", "probe")
	writeFile(t, script, fmt.Sprintf(`#!/bin/sh
echo "secret=${ALLOCBENCH_SECRET:-none}"
test -e %s && echo host-visible || echo host-hidden
touch /bench/x 2>/dev/null && echo bench-writable || echo bench-readonly
tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '
`, secret), 0o755)
	r := w.run(t, "sys", recipe.Bench{Run: recipe.Run{Cmd: []string{"/bench/bin/probe"}}})
	if got := read(t, r.Dir, "stdout"); got != "secret=none\nhost-hidden\nbench-readonly\nlo\n" {
		t.Errorf("isolation:\n%s", got)
	}
}

func TestRunSandboxError(t *testing.T) {
	w := newWorld(t)
	w.exec.Bwrap = "/bin/false"
	_, err := w.exec.Run(Input{Cell: cell("sys"), Bench: bench(), BenchDir: w.bench, Out: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "fake/sys/t4/r2: sandbox") {
		t.Errorf("got %v", err)
	}
}

func TestRunIncompatible(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	benchDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "prog.c")
	writeFile(t, src, "int main(void) { return 0; }\n", 0o644)
	os.MkdirAll(filepath.Join(benchDir, "bin"), 0o755)
	if out, err := exec.Command(cc, "-o", filepath.Join(benchDir, "bin", "prog"), src).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	e := &Executor{Rootfs: t.TempDir(), Runner: "/r", Bwrap: "/nonexistent/bwrap"}
	in := Input{Cell: cell("sys"), Bench: recipe.Bench{Run: recipe.Run{Cmd: []string{"/bench/bin/prog"}}}, BenchDir: benchDir, Out: filepath.Join(t.TempDir(), "cell")}
	r, err := e.Run(in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(r.Status, "incompatible: /bench/bin/prog: needs libc.so.6, not in the run environment") || r.OK() {
		t.Errorf("status: %q", r.Status)
	}
	if got := read(t, r.Dir, "incompatible.txt"); !strings.HasPrefix(got, "/bench/bin/prog: needs libc.so.6") {
		t.Errorf("incompatible.txt: %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "result.json")); !os.IsNotExist(err) {
		t.Error("an incompatible cell was run")
	}

	os.Remove(filepath.Join(benchDir, "bin", "prog"))
	problems, err := e.Compatibility(in)
	if err != nil || len(problems) == 0 {
		t.Errorf("the check is not cached: %v, %v", problems, err)
	}
}

func TestCompatibilityOfScripts(t *testing.T) {
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "bin", "bench"), benchScript, 0o755)
	e := &Executor{Rootfs: t.TempDir()}
	problems, err := e.Compatibility(Input{BenchDir: bench})
	if err != nil || len(problems) != 0 {
		t.Errorf("a shell script is checked as ELF: %v, %v", problems, err)
	}
}
