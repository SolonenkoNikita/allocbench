package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/cell"
	"github.com/SolonenkoNikita/allocbench/internal/doctor"
	"github.com/SolonenkoNikita/allocbench/internal/plan"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/runner"
)

func runSet() *recipe.Set {
	return &recipe.Set{
		Envs:       map[string]recipe.Env{"gcc13": {}},
		Allocators: map[string]recipe.Allocator{"sys": {}, "mi": {}, "je": {}},
		Benches:    map[string]recipe.Bench{"cfrac": {}, "larson": {}},
		Tools:      map[string]recipe.Tool{},
	}
}

func parsePlan(t *testing.T, s *recipe.Set, args ...string) (*plan.Plan, error) {
	t.Helper()
	var got *plan.Plan
	var planErr error
	cmd := newRunCommand(&options{})
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		ro := &runOptions{}
		f := c.Flags()
		ro.plan, _ = f.GetString("plan")
		ro.allocs, _ = f.GetStringSlice("alloc")
		ro.benches, _ = f.GetStringSlice("bench")
		ro.runEnv, _ = f.GetString("run-env")
		ro.repeat, _ = f.GetInt("repeat")
		ro.order, _ = f.GetString("order")
		ro.cpus, _ = f.GetString("cpus")
		got, planErr = runPlan(c, ro, s)
		return nil
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return got, planErr
}

func TestRunPlanFlags(t *testing.T) {
	p, err := parsePlan(t, runSet(), "--alloc", "sys,mi", "--bench", "cfrac", "--repeat", "3", "--order", "sequential", "--cpus", "2-3")
	if err != nil {
		t.Fatal(err)
	}
	want := &plan.Plan{Allocators: []string{"sys", "mi"}, Benches: []string{"cfrac"}, RunEnv: "gcc13", Repeat: 3, Order: "sequential", CPUs: "2-3"}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

func TestRunPlanAll(t *testing.T) {
	p, err := parsePlan(t, runSet(), "--alloc", "all", "--bench", "all")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Allocators, []string{"je", "mi", "sys"}) || !reflect.DeepEqual(p.Benches, []string{"cfrac", "larson"}) {
		t.Errorf("got %+v", p)
	}
	if p.Repeat != 1 || p.Order != plan.OrderInterleaved {
		t.Errorf("defaults: %+v", p)
	}
}

func TestRunPlanFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan.toml")
	os.WriteFile(file, []byte(`allocators = ["sys", "je"]
benches = ["larson"]
run_env = "gcc13"
repeat = 5
cpus = "1-2"
`), 0o644)

	p, err := parsePlan(t, runSet(), "--plan", file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Allocators, []string{"sys", "je"}) || p.Repeat != 5 || p.CPUs != "1-2" {
		t.Errorf("from file: %+v", p)
	}

	p, err = parsePlan(t, runSet(), "--plan", file, "--alloc", "mi", "--repeat", "2", "--cpus", "4")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Allocators, []string{"mi"}) || p.Repeat != 2 || p.CPUs != "4" || !reflect.DeepEqual(p.Benches, []string{"larson"}) {
		t.Errorf("flags do not override the file: %+v", p)
	}

	if _, err := parsePlan(t, runSet(), "--plan", filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("expected an error for a missing plan file")
	}
}

func TestRunPlanErrors(t *testing.T) {
	s := runSet()
	if _, err := parsePlan(t, s, "--bench", "cfrac"); err == nil || !strings.Contains(err.Error(), "--alloc: je, mi, sys or all") {
		t.Errorf("no allocators: %v", err)
	}
	if _, err := parsePlan(t, s, "--alloc", "sys"); err == nil || !strings.Contains(err.Error(), "--bench: cfrac, larson or all") {
		t.Errorf("no benches: %v", err)
	}

	s.Envs["gcc14"] = recipe.Env{}
	if _, err := parsePlan(t, s, "--alloc", "sys", "--bench", "cfrac"); err == nil || !strings.Contains(err.Error(), "--run-env: gcc13, gcc14") {
		t.Errorf("two envs: %v", err)
	}
	if p, err := parsePlan(t, s, "--alloc", "sys", "--bench", "cfrac", "--run-env", "gcc14"); err != nil || p.RunEnv != "gcc14" {
		t.Errorf("explicit env: %+v, %v", p, err)
	}
}

func TestPinnedCPUs(t *testing.T) {
	for _, tc := range []struct {
		text, allowed string
		want          []int
		err           string
	}{
		{text: "", allowed: "0-7"},
		{text: "2-3,5", allowed: "0-7", want: []int{2, 3, 5}},
		{text: "2-3", allowed: "", want: []int{2, 3}},
		{text: "2-3", allowed: "garbage", want: []int{2, 3}},
		{text: "6-9,12", allowed: "0-7", err: "cpus 8-9,12 are not available, this process may use 0-7"},
		{text: "x", allowed: "0-7", err: `cpus "x"`},
	} {
		got, err := pinnedCPUs(tc.text, tc.allowed)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q in %q: got %v, want %q", tc.text, tc.allowed, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q in %q: got %v, %v", tc.text, tc.allowed, got, err)
		}
	}
}

func TestRunWarnings(t *testing.T) {
	conditions := []doctor.Finding{
		{Level: doctor.OK, Check: "load", Detail: "0.10"},
		{Level: doctor.Warning, Check: "turbo boost", Detail: "enabled", Fix: "echo 1"},
	}
	cells := []plan.Cell{
		{Bench: "larson", Alloc: "sys", Threads: 1},
		{Bench: "larson", Alloc: "sys", Threads: 4},
		{Bench: "larson", Alloc: "mi", Threads: 4},
		{Bench: "larson", Alloc: "sys", Threads: 8},
		{Bench: "xmalloc", Alloc: "sys", Threads: 4},
	}
	checks := func(findings []doctor.Finding) []string {
		var out []string
		for _, f := range findings {
			if f.Level != doctor.Warning || f.Fix == "" {
				t.Errorf("finding: %+v", f)
			}
			out = append(out, f.Check+": "+f.Detail)
		}
		return out
	}

	got := checks(runWarnings(conditions, cells, []int{2, 3}, 8))
	want := []string{
		"turbo boost: enabled",
		"threads: larson runs 4 threads on 2 pinned CPUs, they wait for each other",
		"threads: larson runs 8 threads on 2 pinned CPUs, they wait for each other",
		"threads: xmalloc runs 4 threads on 2 pinned CPUs, they wait for each other",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pinned:\n got %q\nwant %q", got, want)
	}

	got = checks(runWarnings(nil, cells, nil, 4))
	want = []string{
		"cpus: not pinned, the scheduler moves benches between CPUs",
		"threads: larson runs 8 threads on 4 CPUs, they wait for each other",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("not pinned:\n got %q\nwant %q", got, want)
	}

	if got := runWarnings(conditions[:1], cells[:1], []int{0}, 8); len(got) != 0 {
		t.Errorf("quiet: %+v", got)
	}
}

func stubConditions(t *testing.T, findings ...doctor.Finding) {
	t.Helper()
	saved := hostConditions
	hostConditions = func() []doctor.Finding { return findings }
	t.Cleanup(func() { hostConditions = saved })
}

func readManifest(t *testing.T, results string) map[string]any {
	t.Helper()
	runs, _ := filepath.Glob(filepath.Join(results, "*", "manifest.json"))
	if len(runs) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	var manifest map[string]any
	data := readText(t, runs[0])
	if err := json.Unmarshal([]byte(data), &manifest); err != nil {
		t.Fatalf("manifest: %v\n%s", err, data)
	}
	return manifest
}

func TestRunCommandPinned(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	os.WriteFile(filepath.Join(recipes, "benches", "affinity.toml"),
		[]byte(`run.cmd = ["/bin/grep", "Cpus_allowed_list", "/proc/self/status"]`+"\n"), 0o644)
	stubConditions(t,
		doctor.Finding{Level: doctor.OK, Check: "load", Detail: "0.10"},
		doctor.Finding{Level: doctor.Warning, Check: "turbo boost", Detail: "enabled", Fix: "echo 1"})
	allowed, err := plan.ParseCPUs(strings.TrimSpace(fieldOf(t, "/proc/self/status", "Cpus_allowed_list")))
	if err != nil || len(allowed) == 0 {
		t.Skipf("allowed CPUs: %v %v", allowed, err)
	}
	cpu := allowed[len(allowed)-1]
	results := filepath.Join(dir, "results")

	out, err := runCLI(t, "run", "--recipes", recipes, "--store", storeDir, "--results", results,
		"--alloc", "sys", "--bench", "affinity", "--cpus", fmt.Sprint(cpu))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, w := range []string{
		"warning: turbo boost: enabled\n  fix: echo 1\n",
		fmt.Sprintf("pinned to CPUs %d\n", cpu),
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "warning: cpus") || strings.Contains(out, "load") {
		t.Errorf("output:\n%s", out)
	}

	manifest := readManifest(t, results)
	if p := manifest["plan"].(map[string]any); p["cpus"] != fmt.Sprint(cpu) {
		t.Errorf("plan: %v", p)
	}
	if w := manifest["warnings"].([]any); len(w) != 1 || w[0] != "turbo boost: enabled" {
		t.Errorf("warnings: %v", w)
	}
	if h := manifest["host"].(map[string]any); h["allowed_cpus"] == "" {
		t.Errorf("host: %v", h)
	}
	stdouts, _ := filepath.Glob(filepath.Join(results, "*", "cells", "affinity", "sys", "t1", "r0", "stdout"))
	if len(stdouts) != 1 {
		t.Fatalf("cells: %v", stdouts)
	}
	if got := readText(t, stdouts[0]); got != fmt.Sprintf("Cpus_allowed_list:\t%d\n", cpu) {
		t.Errorf("stdout: %q", got)
	}

	if out, err := runCLI(t, "run", "--recipes", recipes, "--store", storeDir, "--results", results,
		"--alloc", "sys", "--bench", "affinity", "--cpus", fmt.Sprint(plan.MaxCPU)); err == nil || !strings.Contains(err.Error(), "are not available") {
		t.Errorf("unavailable: %v\n%s", err, out)
	}
}

func fieldOf(t *testing.T, name, key string) string {
	t.Helper()
	for _, line := range strings.Split(readText(t, name), "\n") {
		if value, ok := strings.CutPrefix(line, key+":"); ok {
			return value
		}
	}
	t.Skipf("no %s in %s", key, name)
	return ""
}

func TestExpandAll(t *testing.T) {
	all := []string{"a", "b"}
	if got := expandAll([]string{"all"}, all); !reflect.DeepEqual(got, all) {
		t.Errorf("all: %v", got)
	}
	if got := expandAll([]string{"b", "all"}, all); !reflect.DeepEqual(got, []string{"b", "all"}) {
		t.Errorf("all among names is a name: %v", got)
	}
}

func TestStats(t *testing.T) {
	s := time.Second
	if got := median([]time.Duration{3 * s, 1 * s, 2 * s}); got != 2*s {
		t.Errorf("odd median: %v", got)
	}
	if got := median([]time.Duration{4 * s, 1 * s, 2 * s, 3 * s}); got != 2500*time.Millisecond {
		t.Errorf("even median: %v", got)
	}
	values := []time.Duration{3 * s, 1 * s, 2 * s}
	median(values)
	if values[0] != 3*s {
		t.Error("median sorted its input")
	}
	if minOf(values) != s || maxOf(values) != 3*s {
		t.Errorf("min %v max %v", minOf(values), maxOf(values))
	}
	if got := medianInt([]int64{5, 1, 3}); got != 3 {
		t.Errorf("medianInt: %d", got)
	}
}

func TestFormat(t *testing.T) {
	durations := map[time.Duration]string{
		8395 * time.Millisecond: "8.395s",
		1500 * time.Microsecond: "1.50ms",
		250 * time.Microsecond:  "250µs",
	}
	for d, want := range durations {
		if got := formatDuration(d); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
	sizes := map[int64]string{512: "512 KB", 3072: "3.0 MB", 2 * 1024 * 1024: "2.0 GB"}
	for kb, want := range sizes {
		if got := formatKB(kb); got != want {
			t.Errorf("%d: got %q, want %q", kb, got, want)
		}
	}
}

func result(alloc string, threads int, wall time.Duration, rss int64, status string) cell.Result {
	return cell.Result{
		Cell:    plan.Cell{Alloc: alloc, Bench: "cfrac", Threads: threads},
		Status:  status,
		Measure: runner.Result{WallNs: int64(wall), MaxRSSKB: rss},
	}
}

func TestPrintSummary(t *testing.T) {
	s := time.Second
	p := &plan.Plan{Allocators: []string{"sys", "mi", "je"}, Benches: []string{"cfrac"}}
	results := []cell.Result{
		result("mi", 1, 9*s, 2048, "ok"),
		result("sys", 1, 10*s, 3072, "ok"),
		result("je", 1, 0, 0, "timeout"),
		result("mi", 1, 8*s, 2048, "ok"),
		result("sys", 1, 12*s, 3072, "ok"),
		result("je", 1, 0, 0, "killed by signal 11"),
		result("sys", 2, 6*s, 4096, "ok"),
		result("mi", 2, 3*s, 1024, "exit code 1"),
	}
	var out strings.Builder
	if err := printSummary(&out, p, results); err != nil {
		t.Fatal(err)
	}
	want := "BENCH  THREADS  ALLOC  OK   MEDIAN   MIN      MAX      RELATIVE  MAX RSS\n" +
		"cfrac  1        sys    2/2  11.000s  10.000s  12.000s  1.00      3.0 MB\n" +
		"cfrac  1        mi     2/2  8.500s   8.000s   9.000s   0.77      2.0 MB\n" +
		"cfrac  1        je     0/2  -        -        -        -         -\n" +
		"cfrac  2        sys    1/1  6.000s   6.000s   6.000s   1.00      4.0 MB\n" +
		"cfrac  2        mi     0/1  -        -        -        -         -\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestResultsDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "results")
	now := time.Date(2026, 11, 12, 20, 5, 39, 0, time.FixedZone("MSK", 3*60*60))
	dir, err := resultsDir(base, now)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "20261112T170539Z") {
		t.Errorf("dir: %s", dir)
	}
	if _, err := resultsDir(base, now); err == nil {
		t.Error("an existing results dir is reused")
	}
}

const projectRunner = `build_env = "host"
[source]
path = "runner"
[build]
script = "mkdir -p /out/bin && cc -O2 -o /out/bin/runner /src/runner.c"
`

const projectBench = `build_env = "host"
[source]
path = "files/bench"
[build]
script = "install -Dm755 /src/bench.sh /out/bin/bench"
[run]
cmd = ["/bench/bin/bench", "{threads}"]
threads = [1, 2]
`

const projectBroken = `build_env = "host"
[source]
path = "files/bench"
[build]
script = "install -Dm755 /src/bench.sh /out/bin/bench"
[run]
cmd = ["/bench/bin/bench", "fail"]
`

func runProject(t *testing.T) (dir, recipes, storeDir string) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler")
	}
	recipes, storeDir = shellProject(t)
	dir = filepath.Dir(recipes)
	src, err := os.ReadFile(filepath.Join("..", "..", "runner", "runner.c"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"runner/runner.c":            string(src),
		"recipes/tools/runner.toml":  projectRunner,
		"recipes/benches/echo.toml":  projectBench,
		"recipes/benches/fails.toml": projectBroken,
		"files/bench/bench.sh":       "#!/bin/sh\n[ \"$1\" = fail ] && exit 1\necho \"threads=$1 env=$THREADS\"\n",
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, recipes, storeDir
}

func TestRunCommand(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	stubConditions(t)
	results := filepath.Join(dir, "results")
	out, err := runCLI(t, "run", "--recipes", recipes, "--store", storeDir, "--results", results,
		"--alloc", "sys,hello", "--bench", "echo", "--repeat", "2")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, w := range []string{
		"running 8 cells, results in " + results,
		"[1/8] echo/sys/t1/r0",
		"[8/8] echo/hello/t2/r1",
		"echo   1        sys    2/2",
		"echo   1        hello  2/2",
		"echo   2        hello  2/2",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}

	runs, _ := filepath.Glob(filepath.Join(results, "*"))
	if len(runs) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	var manifest map[string]any
	data, err := os.ReadFile(filepath.Join(runs[0], "manifest.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatalf("manifest: %v\n%s", err, data)
	}
	builds := manifest["builds"].(map[string]any)
	if manifest["finished"] == nil || builds["allocators/sys"] != "" || len(builds["allocators/hello"].(string)) != 64 || len(builds["tools/runner"].(string)) != 64 {
		t.Errorf("manifest:\n%s", data)
	}
	if w := manifest["warnings"].([]any); len(w) != 1 || w[0] != "cpus: not pinned, the scheduler moves benches between CPUs" {
		t.Errorf("warnings: %v", w)
	}
	if p := manifest["plan"].(map[string]any); p["cpus"] != "" {
		t.Errorf("plan: %v", p)
	}
	if env := manifest["run_env"].(map[string]any); env["name"] != "host" || len(env["hash"].(string)) != 64 {
		t.Errorf("run_env: %v", env)
	}
	if lines := strings.Count(readText(t, filepath.Join(runs[0], "results.jsonl")), "\n"); lines != 8 {
		t.Errorf("results.jsonl has %d lines", lines)
	}
	if lines := strings.Count(readText(t, filepath.Join(runs[0], "results.csv")), "\n"); lines != 9 {
		t.Errorf("results.csv has %d lines", lines)
	}
	if summary := readText(t, filepath.Join(runs[0], "summary.txt")); !strings.HasPrefix(summary, "BENCH") || !strings.Contains(out, summary) {
		t.Errorf("summary.txt:\n%s", summary)
	}

	stdouts, _ := filepath.Glob(filepath.Join(results, "*", "cells", "echo", "hello", "t2", "r1", "stdout"))
	if len(stdouts) != 1 {
		t.Fatalf("cell dirs: %v", stdouts)
	}
	if data, _ := os.ReadFile(stdouts[0]); string(data) != "threads=2 env=2\n" {
		t.Errorf("stdout: %q", data)
	}
}

func TestRunCommandFailedCells(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	out, err := runCLI(t, "run", "--recipes", recipes, "--store", storeDir, "--results", filepath.Join(dir, "results"),
		"--alloc", "sys", "--bench", "echo,fails")
	if err == nil || !strings.Contains(err.Error(), "1 of 3 cells failed") {
		t.Fatalf("got %v\n%s", err, out)
	}
	if !strings.Contains(out, "fails/sys/t1/r0") || !strings.Contains(out, "exit code 1") || !strings.Contains(out, "fails  1        sys    0/1") {
		t.Errorf("output:\n%s", out)
	}
}

func TestRunCommandNoRunner(t *testing.T) {
	dir, recipes, storeDir := runProject(t)
	os.Remove(filepath.Join(recipes, "tools", "runner.toml"))
	out, err := runCLI(t, "run", "--recipes", recipes, "--store", storeDir, "--results", filepath.Join(dir, "results"),
		"--alloc", "sys", "--bench", "echo")
	if err == nil || !strings.Contains(err.Error(), "tools/runner: no such recipe, it builds the runner") {
		t.Fatalf("got %v\n%s", err, out)
	}
}

func readText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
