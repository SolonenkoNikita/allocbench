package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	runnerBin string
	allocBin  string
)

const allocSource = `#include <stdlib.h>
#include <string.h>

#define BYTES_PER_MB (1024 * 1024)
#define FILL 1

int main(int argc, char **argv)
{
	size_t size;
	char *buffer;

	if (argc != 2)
		return EXIT_FAILURE;
	size = (size_t)atoi(argv[1]) * BYTES_PER_MB;
	buffer = malloc(size);
	if (!buffer)
		return EXIT_FAILURE;
	memset(buffer, FILL, size);
	return buffer[size - 1] == FILL ? EXIT_SUCCESS : EXIT_FAILURE;
}
`

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "runner-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 0
	if cc, err := exec.LookPath("cc"); err == nil {
		runnerBin, allocBin = compile(cc, dir)
	}
	code = m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func compile(cc, dir string) (string, string) {
	src, _ := filepath.Abs(filepath.Join("..", "..", "runner", "runner.c"))
	runner := filepath.Join(dir, "runner")
	flags := []string{"-O2", "-std=c11", "-Wall", "-Wextra", "-Wshadow", "-Wconversion", "-Wpedantic", "-Werror"}
	if exec.Command(cc, append(append(flags, "-static"), "-o", runner, src)...).Run() != nil {
		if out, err := exec.Command(cc, append(flags, "-o", runner, src)...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "runner.c does not compile: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	allocSrc := filepath.Join(dir, "alloc.c")
	alloc := filepath.Join(dir, "alloc")
	os.WriteFile(allocSrc, []byte(allocSource), 0o644)
	if out, err := exec.Command(cc, "-O0", "-o", alloc, allocSrc).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "alloc.c does not compile: %v\n%s", err, out)
		os.Exit(1)
	}
	return runner, alloc
}

func needRunner(t *testing.T) {
	t.Helper()
	if runnerBin == "" {
		t.Skip("no C compiler")
	}
}

func run(t *testing.T, o Options, cmd ...string) Result {
	t.Helper()
	needRunner(t)
	args := Args(runnerBin, o, cmd)
	c := exec.Command(args[0], args[1:]...)
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if o.Result != "" {
		if len(out) != 0 {
			t.Errorf("stdout with --result: %q", out)
		}
		if out, err = os.ReadFile(o.Result); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return r
}

func TestRunOK(t *testing.T) {
	r := run(t, Options{}, "/bin/true")
	if !r.OK() || r.Status() != "ok" || r.WallNs <= 0 {
		t.Errorf("result: %+v", r)
	}
}

func TestRunExitCode(t *testing.T) {
	r := run(t, Options{}, "/bin/sh", "-c", "exit 3")
	if r.ExitCode != 3 || r.OK() || r.Status() != "exit code 3" {
		t.Errorf("result: %+v", r)
	}
}

func TestRunSignal(t *testing.T) {
	r := run(t, Options{}, "/bin/sh", "-c", "kill -TERM $$")
	if r.Signal != 15 || r.ExitCode != -1 || r.Status() != "killed by signal 15" {
		t.Errorf("result: %+v", r)
	}
}

func TestRunTimeoutKillsGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	r := run(t, Options{Timeout: 300 * time.Millisecond}, "/bin/sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	if !r.TimedOut || r.Signal != 9 || r.Status() != "timeout" {
		t.Errorf("result: %+v", r)
	}
	if r.Wall() < 300*time.Millisecond || time.Since(start) > 5*time.Second {
		t.Errorf("wall %v, elapsed %v", r.Wall(), time.Since(start))
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(2 * time.Second)
	for {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil || strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunNoTimeout(t *testing.T) {
	r := run(t, Options{}, "/bin/sh", "-c", "sleep 0.2")
	if !r.OK() || r.Wall() < 200*time.Millisecond {
		t.Errorf("result: %+v", r)
	}
}

func TestRunExecError(t *testing.T) {
	r := run(t, Options{}, "/nonexistent/bench")
	if r.ExecError != "No such file or directory" || r.ExitCode != 127 || r.Status() != "exec error: No such file or directory" {
		t.Errorf("result: %+v", r)
	}
}

func TestRunEnvAndOutput(t *testing.T) {
	dir := t.TempDir()
	o := Options{
		Env:    map[string]string{"ALLOC_A": "1", "ALLOC_B": "x=y"},
		Stdout: filepath.Join(dir, "out"),
		Stderr: filepath.Join(dir, "err"),
		Result: filepath.Join(dir, "result.json"),
	}
	r := run(t, o, "/bin/sh", "-c", `echo "a=$ALLOC_A b=$ALLOC_B"; echo oops >&2; cat`)
	if !r.OK() {
		t.Fatalf("result: %+v", r)
	}
	if data, _ := os.ReadFile(o.Stdout); string(data) != "a=1 b=x=y\n" {
		t.Errorf("stdout: %q", data)
	}
	if data, _ := os.ReadFile(o.Stderr); string(data) != "oops\n" {
		t.Errorf("stderr: %q", data)
	}
}

func TestRunEnvDoesNotReachRunner(t *testing.T) {
	needRunner(t)
	args := Args(runnerBin, Options{Env: map[string]string{"LD_PRELOAD": "/nonexistent.so"}}, []string{"/bin/sh", "-c", "echo $LD_PRELOAD"})
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "\n/nonexistent.so\n") {
		t.Errorf("child did not get LD_PRELOAD: %q", out)
	}
	if n := strings.Count(string(out), "cannot be preloaded"); n != 1 {
		t.Errorf("LD_PRELOAD was applied %d times, want only in the child: %q", n, out)
	}
}

func TestRunMaxRSS(t *testing.T) {
	needRunner(t)
	small := run(t, Options{}, "/bin/true")
	big := run(t, Options{}, allocBin, "64")
	if !big.OK() {
		t.Fatalf("alloc: %+v", big)
	}
	if big.MaxRSSKB < 64*1024 {
		t.Errorf("max_rss_kb %d for 64 MB", big.MaxRSSKB)
	}
	if small.MaxRSSKB > 8*1024 {
		t.Errorf("max_rss_kb %d for /bin/true, the runner leaks its own memory into the result", small.MaxRSSKB)
	}
	if big.MinFlt < 64*1024/4 {
		t.Errorf("minflt %d is too small for 64 MB", big.MinFlt)
	}
}

func TestRunCPUTime(t *testing.T) {
	r := run(t, Options{}, "/bin/sh", "-c", "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done")
	if r.User() <= 0 || r.User()+r.Sys() > r.Wall()+10*time.Millisecond {
		t.Errorf("user %v sys %v wall %v", r.User(), r.Sys(), r.Wall())
	}
}

func TestRunnerUsage(t *testing.T) {
	needRunner(t)
	tests := []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "no command after --"},
		{[]string{"--"}, 2, "no command after --"},
		{[]string{"--bogus", "--", "/bin/true"}, 2, "unknown option"},
		{[]string{"--timeout", "abc", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--timeout", "-1", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--timeout", "nan", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--timeout", "inf", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--timeout", "1e30", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--timeout", "2592001", "--", "/bin/true"}, 2, "--timeout needs"},
		{[]string{"--env", "NOEQUALS", "--", "/bin/true"}, 2, "--env needs KEY=VALUE"},
		{[]string{"--env", "=x", "--", "/bin/true"}, 2, "--env needs KEY=VALUE"},
		{[]string{"--stdout"}, 2, "option needs a value"},
		{[]string{"--stdout", "/nonexistent/dir/out", "--", "/bin/true"}, 1, "/nonexistent/dir/out"},
	}
	for _, tt := range tests {
		out, err := exec.Command(runnerBin, tt.args...).CombinedOutput()
		code := 0
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		if code != tt.code || !strings.Contains(string(out), tt.want) {
			t.Errorf("%v: exit %d, %q; want exit %d with %q", tt.args, code, out, tt.code, tt.want)
		}
	}
}

func TestArgs(t *testing.T) {
	got := Args("/runner/bin/runner", Options{
		Env:     map[string]string{"LD_PRELOAD": "/alloc/lib/libmimalloc.so", "MIMALLOC_VERBOSE": "0"},
		Timeout: 1500 * time.Millisecond,
		Stdout:  "/results/out",
		Result:  "/results/result.json",
	}, []string{"/bench/bin/cfrac", "17"})
	want := []string{
		"/runner/bin/runner",
		"--env", "LD_PRELOAD=/alloc/lib/libmimalloc.so",
		"--env", "MIMALLOC_VERBOSE=0",
		"--timeout", "1.5",
		"--stdout", "/results/out",
		"--result", "/results/result.json",
		"--", "/bench/bin/cfrac", "17",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if got := Args("/r", Options{}, []string{"/bin/true"}); !reflect.DeepEqual(got, []string{"/r", "--", "/bin/true"}) {
		t.Errorf("no options: %v", got)
	}
}

func TestParse(t *testing.T) {
	r, err := Parse([]byte(`{"runner":1,"exit_code":0,"signal":0,"timed_out":false,"exec_error":"","wall_ns":2500000000,"user_us":1500000,"sys_us":250,"max_rss_kb":2840,"minflt":1,"majflt":2,"nvcsw":3,"nivcsw":4}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Wall() != 2500*time.Millisecond || r.User() != 1500*time.Millisecond || r.Sys() != 250*time.Microsecond || r.MaxRSSKB != 2840 || r.NIVCSW != 4 {
		t.Errorf("result: %+v", r)
	}

	for _, bad := range []string{
		``,
		`not json`,
		`{"runner":2,"exit_code":0}`,
		`{"exit_code":0}`,
		`{"runner":1,"surprise":1}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestStatusOrder(t *testing.T) {
	r := Result{ExitCode: -1, Signal: 9, TimedOut: true}
	if r.Status() != "timeout" {
		t.Errorf("timeout is not reported first: %s", r.Status())
	}
}

func TestRunOnlyStandardFds(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	r := run(t, Options{Stdout: out}, "/bin/sh", "-c", `ls /proc/$$/fd | tr '\n' ' '`)
	if !r.OK() {
		t.Fatalf("result: %+v", r)
	}
	if data, _ := os.ReadFile(out); string(data) != "0 1 2 " {
		t.Errorf("open fds in the test: %q, want only 0 1 2", data)
	}
}

func TestRunWithClosedStandardFds(t *testing.T) {
	needRunner(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	result := filepath.Join(dir, "result.json")
	args := Args(runnerBin, Options{Stdout: out, Result: result}, []string{"/bin/sh", "-c", "echo to-stdout; cat; echo cat=$?"})
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + a + "'"
	}
	if err := exec.Command("/bin/sh", "-c", "exec 0<&- 1>&- 2>&-; exec "+strings.Join(quoted, " ")).Run(); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if data, _ := os.ReadFile(out); string(data) != "to-stdout\ncat=0\n" {
		t.Errorf("stdout of the test: %q", data)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := Parse(data); err != nil || !r.OK() {
		t.Errorf("result: %+v, %v", r, err)
	}
}

func TestRunMaxTimeout(t *testing.T) {
	r := run(t, Options{Timeout: 30 * 24 * time.Hour}, "/bin/true")
	if !r.OK() {
		t.Errorf("result: %+v", r)
	}
}
