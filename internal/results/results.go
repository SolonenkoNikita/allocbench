package results

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/cell"
)

const (
	ManifestFile = "manifest.json"
	JSONLFile    = "results.jsonl"
	CSVFile      = "results.csv"
	SummaryFile  = "summary.txt"
)

type Manifest struct {
	Version  string            `json:"allocbench_version"`
	Started  time.Time         `json:"started"`
	Finished *time.Time        `json:"finished,omitempty"`
	Plan     Plan              `json:"plan"`
	RunEnv   Env               `json:"run_env"`
	Builds   map[string]string `json:"builds"`
	Host     Host              `json:"host"`
	Warnings []string          `json:"warnings"`
}

type Plan struct {
	Allocators []string `json:"allocators"`
	Benches    []string `json:"benches"`
	Repeat     int      `json:"repeat"`
	Order      string   `json:"order"`
	CPUs       string   `json:"cpus"`
	Cells      int      `json:"cells"`
}

type Env struct {
	Name     string   `json:"name"`
	Distro   string   `json:"distro"`
	Suite    string   `json:"suite"`
	Snapshot string   `json:"snapshot"`
	Packages []string `json:"packages"`
	Hash     string   `json:"hash"`
	Tree     string   `json:"tree"`
	Mirror   string   `json:"mirror"`
}

type Host struct {
	Kernel      string `json:"kernel"`
	Arch        string `json:"arch"`
	CPUModel    string `json:"cpu_model"`
	CPUs        int    `json:"cpus"`
	AllowedCPUs string `json:"allowed_cpus"`
	MemoryKB    int64  `json:"memory_kb"`
	THP         string `json:"transparent_hugepages"`
	Governor    string `json:"cpu_governor"`
	Overcommit  string `json:"overcommit_memory"`
}

type Record struct {
	Bench        string `json:"bench"`
	Alloc        string `json:"alloc"`
	Threads      int    `json:"threads"`
	Repeat       int    `json:"repeat"`
	Status       string `json:"status"`
	OK           bool   `json:"ok"`
	WallNs       int64  `json:"wall_ns"`
	UserUs       int64  `json:"user_us"`
	SysUs        int64  `json:"sys_us"`
	MaxRSSKB     int64  `json:"max_rss_kb"`
	MinFlt       int64  `json:"minflt"`
	MajFlt       int64  `json:"majflt"`
	NVCSW        int64  `json:"nvcsw"`
	NIVCSW       int64  `json:"nivcsw"`
	ExitCode     int    `json:"exit_code"`
	Signal       int    `json:"signal"`
	TimedOut     bool   `json:"timed_out"`
	ExecError    string `json:"exec_error"`
	StdoutSHA256 string `json:"stdout_sha256"`
	Dir          string `json:"dir"`
}

var csvHeader = []string{
	"bench", "alloc", "threads", "repeat", "status", "ok",
	"wall_ns", "user_us", "sys_us", "max_rss_kb", "minflt", "majflt", "nvcsw", "nivcsw",
	"exit_code", "signal", "timed_out", "exec_error", "stdout_sha256", "dir",
}

func (r Record) csvRow() []string {
	i64 := func(v int64) string { return strconv.FormatInt(v, 10) }
	return []string{
		r.Bench, r.Alloc, strconv.Itoa(r.Threads), strconv.Itoa(r.Repeat), r.Status, strconv.FormatBool(r.OK),
		i64(r.WallNs), i64(r.UserUs), i64(r.SysUs), i64(r.MaxRSSKB), i64(r.MinFlt), i64(r.MajFlt), i64(r.NVCSW), i64(r.NIVCSW),
		strconv.Itoa(r.ExitCode), strconv.Itoa(r.Signal), strconv.FormatBool(r.TimedOut), r.ExecError, r.StdoutSHA256, r.Dir,
	}
}

func NewRecord(dir string, r cell.Result) Record {
	rel, err := filepath.Rel(dir, r.Dir)
	if err != nil {
		rel = r.Dir
	}
	m := r.Measure
	return Record{
		Bench: r.Cell.Bench, Alloc: r.Cell.Alloc, Threads: r.Cell.Threads, Repeat: r.Cell.Repeat,
		Status: r.Status, OK: r.OK(),
		WallNs: m.WallNs, UserUs: m.UserUs, SysUs: m.SysUs, MaxRSSKB: m.MaxRSSKB,
		MinFlt: m.MinFlt, MajFlt: m.MajFlt, NVCSW: m.NVCSW, NIVCSW: m.NIVCSW,
		ExitCode: m.ExitCode, Signal: m.Signal, TimedOut: m.TimedOut, ExecError: m.ExecError,
		StdoutSHA256: r.StdoutSHA256, Dir: filepath.ToSlash(rel),
	}
}

type Writer struct {
	dir   string
	jsonl *os.File
	file  *os.File
	csv   *csv.Writer
}

func Create(dir string) (*Writer, error) {
	jsonl, err := os.OpenFile(filepath.Join(dir, JSONLFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, CSVFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		jsonl.Close()
		return nil, err
	}
	w := &Writer{dir: dir, jsonl: jsonl, file: file, csv: csv.NewWriter(file)}
	if err := w.writeCSV(csvHeader); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) Add(r cell.Result) error {
	rec := NewRecord(w.dir, r)
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := w.jsonl.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.writeCSV(rec.csvRow())
}

func (w *Writer) writeCSV(row []string) error {
	if err := w.csv.Write(row); err != nil {
		return err
	}
	w.csv.Flush()
	return w.csv.Error()
}

func (w *Writer) Close() error {
	errJSONL := w.jsonl.Close()
	errCSV := w.file.Close()
	if errJSONL != nil {
		return errJSONL
	}
	return errCSV
}

func WriteManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ManifestFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ManifestFile))
}

func ReadHost(root string) Host {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return Host{
		Kernel:      read("proc/sys/kernel/osrelease"),
		Arch:        runtime.GOARCH,
		CPUModel:    field(filepath.Join(root, "proc/cpuinfo"), "model name"),
		CPUs:        runtime.NumCPU(),
		AllowedCPUs: field(filepath.Join(root, "proc/self/status"), "Cpus_allowed_list"),
		MemoryKB:    memTotal(filepath.Join(root, "proc/meminfo")),
		THP:         selected(read("sys/kernel/mm/transparent_hugepage/enabled")),
		Governor:    read("sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
		Overcommit:  read("proc/sys/vm/overcommit_memory"),
	}
}

func field(name, key string) string {
	f, err := os.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func memTotal(name string) int64 {
	v := strings.TrimSuffix(field(name, "MemTotal"), " kB")
	kb, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return kb
}

func selected(choices string) string {
	start := strings.Index(choices, "[")
	end := strings.Index(choices, "]")
	if start < 0 || end < start {
		return choices
	}
	return choices[start+1 : end]
}

func (h Host) String() string {
	return fmt.Sprintf("%s, %d CPUs, %s, kernel %s", h.CPUModel, h.CPUs, h.Arch, h.Kernel)
}
