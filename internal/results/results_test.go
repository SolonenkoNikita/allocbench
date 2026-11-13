package results

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/cell"
	"github.com/SolonenkoNikita/allocbench/internal/plan"
	"github.com/SolonenkoNikita/allocbench/internal/runner"
)

func cellResult(dir, alloc, status string) cell.Result {
	c := plan.Cell{Alloc: alloc, Bench: "cfrac", Threads: 2, Repeat: 1}
	return cell.Result{
		Cell:   c,
		Status: status,
		Measure: runner.Result{
			Runner: 1, ExitCode: 0, WallNs: 7589305364, UserUs: 7576513, SysUs: 8998, MaxRSSKB: 2892,
			MinFlt: 307, MajFlt: 1, NVCSW: 2, NIVCSW: 128,
		},
		StdoutSHA256: "99eb",
		Dir:          filepath.Join(dir, "cells", c.ID()),
	}
}

func TestNewRecord(t *testing.T) {
	r := cellResult("/results/run", "mi", "ok")
	got := NewRecord("/results/run", r)
	want := Record{
		Bench: "cfrac", Alloc: "mi", Threads: 2, Repeat: 1, Status: "ok", OK: true,
		WallNs: 7589305364, UserUs: 7576513, SysUs: 8998, MaxRSSKB: 2892, MinFlt: 307, MajFlt: 1, NVCSW: 2, NIVCSW: 128,
		StdoutSHA256: "99eb", Dir: "cells/cfrac/mi/t2/r1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if rec := NewRecord("/results/run", cellResult("/results/run", "mi", "wrong output")); rec.OK {
		t.Error("wrong output is ok")
	}
}

func TestNewRecordRelativeDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	abs := cellResult(filepath.Join(root, "results", "run"), "mi", "ok")
	if got := NewRecord(filepath.Join("results", "run"), abs).Dir; got != "cells/cfrac/mi/t2/r1" {
		t.Errorf("absolute cell dir: %q", got)
	}
	rel := cellResult(filepath.Join("results", "run"), "mi", "ok")
	if got := NewRecord(filepath.Join("results", "run"), rel).Dir; got != "cells/cfrac/mi/t2/r1" {
		t.Errorf("relative cell dir: %q", got)
	}
	if got := NewRecord(filepath.Join("results", "run"), cellResult("/elsewhere", "mi", "ok")).Dir; !strings.HasPrefix(got, "../") || !strings.HasSuffix(got, "/elsewhere/cells/cfrac/mi/t2/r1") {
		t.Errorf("outside: %q", got)
	}
}

func TestWriter(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	failed := cellResult(dir, "je", "exec error: No such file, or \"directory\"")
	failed.Measure.ExecError = "No such file, or \"directory\""
	failed.Measure.ExitCode = 127
	for _, r := range []cell.Result{cellResult(dir, "sys", "ok"), failed} {
		if err := w.Add(r); err != nil {
			t.Fatal(err)
		}
	}

	lines := readLines(t, filepath.Join(dir, JSONLFile))
	if len(lines) != 2 {
		t.Fatalf("jsonl lines: %d", len(lines))
	}
	var rec Record
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Alloc != "je" || rec.OK || rec.ExitCode != 127 || rec.ExecError != failed.Measure.ExecError {
		t.Errorf("jsonl record: %+v", rec)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, CSVFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || !reflect.DeepEqual(rows[0], csvHeader) {
		t.Fatalf("csv: %v", rows)
	}
	if rows[1][1] != "sys" || rows[1][6] != "7589305364" || rows[2][17] != failed.Measure.ExecError || rows[2][5] != "false" {
		t.Errorf("csv rows: %v", rows[1:])
	}
	for _, row := range rows {
		if len(row) != len(csvHeader) {
			t.Errorf("row has %d columns: %v", len(row), row)
		}
	}
}

func TestWriterVisibleBeforeClose(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Add(cellResult(dir, "sys", "ok"))
	if n := len(readLines(t, filepath.Join(dir, JSONLFile))); n != 1 {
		t.Errorf("jsonl has %d lines before Close", n)
	}
	if n := len(readLines(t, filepath.Join(dir, CSVFile))); n != 2 {
		t.Errorf("csv has %d lines before Close", n)
	}
}

func TestCreateRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, CSVFile), []byte("old"), 0o644)
	if _, err := Create(dir); err == nil {
		t.Fatal("existing results are overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, JSONLFile)); err != nil {
		t.Errorf("jsonl is left open or missing: %v", err)
	}
}

func TestReadRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []cell.Result{cellResult(dir, "sys", "ok"), cellResult(dir, "mi", "exit code 1")}
	for _, r := range want {
		if err := w.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	got, err := ReadRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records", len(got))
	}
	for i := range want {
		if got[i] != NewRecord(dir, want[i]) {
			t.Errorf("record %d:\n got %+v\nwant %+v", i, got[i], NewRecord(dir, want[i]))
		}
	}

	if _, err := ReadRecords(t.TempDir()); !os.IsNotExist(err) {
		t.Errorf("missing: %v", err)
	}
	os.WriteFile(filepath.Join(dir, JSONLFile), []byte("{}\n{oops\n"), 0o644)
	if _, err := ReadRecords(dir); err == nil || !strings.Contains(err.Error(), JSONLFile) {
		t.Errorf("broken: %v", err)
	}
}

func TestWriteManifest(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 11, 12, 20, 10, 28, 0, time.UTC)
	m := Manifest{
		Version: "v1",
		Started: started,
		Plan:    Plan{Allocators: []string{"sys", "mi"}, Benches: []string{"cfrac"}, Repeat: 3, Order: "interleaved", Cells: 6},
		RunEnv:  Env{Name: "gcc13", Snapshot: "20260101T000000Z", Hash: "c4d1"},
		Builds:  map[string]string{"allocators/mi": "c05b", "allocators/sys": ""},
		Host:    Host{Kernel: "6.14.0", CPUs: 8},
	}
	if err := WriteManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	if strings.Contains(string(data), "finished") {
		t.Errorf("unfinished run has finished:\n%s", data)
	}

	finished := started.Add(time.Minute)
	m.Finished = &finished
	if err := WriteManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	data, _ = os.ReadFile(filepath.Join(dir, ManifestFile))
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["finished"] != "2026-11-12T20:11:28Z" || got["allocbench_version"] != "v1" {
		t.Errorf("manifest:\n%s", data)
	}
	if env := got["run_env"].(map[string]any); env["snapshot"] != "20260101T000000Z" {
		t.Errorf("run_env: %v", env)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile+".tmp")); !os.IsNotExist(err) {
		t.Error("temporary manifest is left behind")
	}
}

func TestReadHost(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"proc/sys/kernel/osrelease": "6.14.0-36-generic\n",
		"proc/cpuinfo":              "processor\t: 0\nmodel name\t: Intel(R) Core(TM) i5-10310U CPU @ 1.70GHz\nflags\t\t: fpu\n\nprocessor\t: 1\nmodel name\t: other\n",
		"proc/meminfo":              "MemTotal:       15969024 kB\nMemFree:          100 kB\n",
		"sys/kernel/mm/transparent_hugepage/enabled":           "always [madvise] never\n",
		"sys/devices/system/cpu/cpu0/cpufreq/scaling_governor": "powersave\n",
		"proc/sys/vm/overcommit_memory":                        "0\n",
		"proc/self/status":                                     "Name:\tallocbench\nCpus_allowed:\tff\nCpus_allowed_list:\t0-7\n",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	got := ReadHost(root)
	want := Host{
		Kernel: "6.14.0-36-generic", Arch: runtime.GOARCH, CPUModel: "Intel(R) Core(TM) i5-10310U CPU @ 1.70GHz",
		CPUs: runtime.NumCPU(), AllowedCPUs: "0-7", MemoryKB: 15969024, THP: "madvise", Governor: "powersave", Overcommit: "0",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if !strings.Contains(got.String(), "kernel 6.14.0-36-generic") {
		t.Errorf("String: %s", got)
	}
}

func TestReadHostMissing(t *testing.T) {
	got := ReadHost(t.TempDir())
	if got.Kernel != "" || got.CPUModel != "" || got.MemoryKB != 0 || got.THP != "" || got.AllowedCPUs != "" || got.CPUs != runtime.NumCPU() {
		t.Errorf("got %+v", got)
	}
}

func TestSelected(t *testing.T) {
	tests := map[string]string{
		"always [madvise] never": "madvise",
		"[always] madvise never": "always",
		"plain":                  "plain",
		"":                       "",
	}
	for in, want := range tests {
		if got := selected(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func readLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}
