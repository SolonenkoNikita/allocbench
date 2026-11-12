package cell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/elfcheck"
	"github.com/SolonenkoNikita/allocbench/internal/plan"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/runner"
	"github.com/SolonenkoNikita/allocbench/internal/sandbox"
)

const (
	DefaultTimeout     = 10 * time.Minute
	ThreadsVar         = "THREADS"
	ThreadsMark        = "{threads}"
	WorkDisk           = "disk"
	StatusOK           = "ok"
	StatusWrongOutput  = "wrong output"
	StatusIncompatible = "incompatible"

	allocDir   = "/alloc"
	benchDir   = "/bench"
	runnerDir  = "/runner"
	resultsDir = "/results"
	workDir    = "/work"
	runnerBin  = runnerDir + "/bin/runner"

	stdoutFile  = "stdout"
	stderrFile  = "stderr"
	resultFile  = "result.json"
	sandboxFile = "sandbox.log"
	problemFile = "incompatible.txt"
	workSubdir  = "work"
)

type Executor struct {
	Rootfs string
	Runner string
	Bwrap  string

	checker  *elfcheck.Checker
	problems map[string][]elfcheck.Problem
}

type Input struct {
	Cell     plan.Cell
	Alloc    recipe.Allocator
	AllocDir string
	Bench    recipe.Bench
	BenchDir string
	Out      string
}

type Result struct {
	Cell         plan.Cell
	Status       string
	Measure      runner.Result
	StdoutSHA256 string
	Dir          string
}

func (r Result) OK() bool {
	return r.Status == StatusOK
}

func (e *Executor) Run(in Input) (Result, error) {
	if err := os.MkdirAll(in.Out, 0o755); err != nil {
		return Result{}, err
	}
	out, err := filepath.Abs(in.Out)
	if err != nil {
		return Result{}, err
	}
	in.Out = out

	problems, err := e.Compatibility(in)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", in.Cell.ID(), err)
	}
	if len(problems) > 0 {
		return e.incompatible(in, problems)
	}

	spec, cleanup, err := e.Spec(in)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", in.Cell.ID(), err)
	}
	defer cleanup()

	args, err := sandbox.Args(spec)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", in.Cell.ID(), err)
	}
	log, err := os.Create(filepath.Join(in.Out, sandboxFile))
	if err != nil {
		return Result{}, err
	}
	defer log.Close()

	cmd := exec.Command(e.bwrap(), args...)
	var stderr bytes.Buffer
	cmd.Stdout = log
	cmd.Stderr = io.MultiWriter(log, &stderr)
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("%s: sandbox: %w: %s", in.Cell.ID(), err, strings.TrimSpace(stderr.String()))
	}

	data, err := os.ReadFile(filepath.Join(in.Out, resultFile))
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", in.Cell.ID(), err)
	}
	measure, err := runner.Parse(data)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", in.Cell.ID(), err)
	}
	sum, err := fileSHA256(filepath.Join(in.Out, stdoutFile))
	if err != nil {
		return Result{}, err
	}

	res := Result{Cell: in.Cell, Status: measure.Status(), Measure: measure, StdoutSHA256: sum, Dir: in.Out}
	if res.Status == StatusOK && in.Bench.Check.StdoutSHA256 != "" && sum != in.Bench.Check.StdoutSHA256 {
		res.Status = StatusWrongOutput
	}
	return res, nil
}

func (e *Executor) Compatibility(in Input) ([]elfcheck.Problem, error) {
	if e.checker == nil {
		e.checker = &elfcheck.Checker{Root: e.Rootfs}
		e.problems = map[string][]elfcheck.Problem{}
	}
	var all []elfcheck.Problem
	for _, d := range []struct{ dir, mount string }{{in.AllocDir, allocDir}, {in.BenchDir, benchDir}} {
		if d.dir == "" {
			continue
		}
		problems, ok := e.problems[d.dir]
		if !ok {
			var err error
			problems, err = e.checker.CheckDir(d.dir)
			if err != nil {
				return nil, err
			}
			e.problems[d.dir] = problems
		}
		for _, p := range problems {
			p.File = path.Join(d.mount, p.File)
			all = append(all, p)
		}
	}
	return all, nil
}

func (e *Executor) incompatible(in Input, problems []elfcheck.Problem) (Result, error) {
	var text strings.Builder
	for _, p := range problems {
		fmt.Fprintln(&text, p)
	}
	if err := os.WriteFile(filepath.Join(in.Out, problemFile), []byte(text.String()), 0o644); err != nil {
		return Result{}, err
	}
	status := StatusIncompatible + ": " + problems[0].String()
	if len(problems) > 1 {
		status += fmt.Sprintf(" and %d more", len(problems)-1)
	}
	return Result{Cell: in.Cell, Status: status, Dir: in.Out}, nil
}

func (e *Executor) Spec(in Input) (sandbox.Spec, func(), error) {
	noop := func() {}
	if len(in.Bench.Run.Cmd) == 0 {
		return sandbox.Spec{}, noop, fmt.Errorf("bench has no run.cmd")
	}
	if in.BenchDir == "" && in.Bench.Build.Script != "" {
		return sandbox.Spec{}, noop, fmt.Errorf("bench is not built")
	}

	mounts := []sandbox.Mount{{Src: e.Runner, Dst: runnerDir}}
	if in.BenchDir != "" {
		mounts = append(mounts, sandbox.Mount{Src: in.BenchDir, Dst: benchDir})
	}

	childEnv := map[string]string{}
	for k, v := range in.Alloc.Use.Env {
		childEnv[k] = v
	}
	if in.Alloc.Use.Preload != "" {
		if in.AllocDir == "" {
			return sandbox.Spec{}, noop, fmt.Errorf("allocator is not built")
		}
		if _, err := os.Stat(filepath.Join(in.AllocDir, in.Alloc.Use.Preload)); err != nil {
			return sandbox.Spec{}, noop, fmt.Errorf("use.preload: %w", err)
		}
		childEnv["LD_PRELOAD"] = path.Join(allocDir, in.Alloc.Use.Preload)
	}
	if in.AllocDir != "" {
		mounts = append(mounts, sandbox.Mount{Src: in.AllocDir, Dst: allocDir})
	}
	threads := strconv.Itoa(in.Cell.Threads)
	childEnv[ThreadsVar] = threads

	var tmpfs []string
	cleanup := noop
	if in.Bench.Run.Work == WorkDisk {
		work := filepath.Join(in.Out, workSubdir)
		if err := os.MkdirAll(work, 0o755); err != nil {
			return sandbox.Spec{}, noop, err
		}
		cleanup = func() { os.RemoveAll(work) }
		mounts = append(mounts, sandbox.Mount{Src: work, Dst: workDir, RW: true})
	} else {
		tmpfs = append(tmpfs, workDir)
	}
	mounts = append(mounts, sandbox.Mount{Src: in.Out, Dst: resultsDir, RW: true})

	timeout := in.Bench.Run.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	cmd := make([]string, len(in.Bench.Run.Cmd))
	for i, arg := range in.Bench.Run.Cmd {
		cmd[i] = strings.ReplaceAll(arg, ThreadsMark, threads)
	}

	return sandbox.Spec{
		Rootfs: e.Rootfs,
		Mounts: mounts,
		Tmpfs:  tmpfs,
		Env:    map[string]string{"HOME": workDir},
		Dir:    workDir,
		Cmd: runner.Args(runnerBin, runner.Options{
			Env:     childEnv,
			Timeout: timeout,
			Stdout:  path.Join(resultsDir, stdoutFile),
			Stderr:  path.Join(resultsDir, stderrFile),
			Result:  path.Join(resultsDir, resultFile),
		}, cmd),
	}, cleanup, nil
}

func (e *Executor) bwrap() string {
	if e.Bwrap != "" {
		return e.Bwrap
	}
	return "bwrap"
}

func fileSHA256(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
