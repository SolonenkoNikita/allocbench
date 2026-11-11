package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

const Protocol = 1

type Options struct {
	Env     map[string]string
	Timeout time.Duration
	Stdout  string
	Stderr  string
	Result  string
}

type Result struct {
	Runner    int    `json:"runner"`
	ExitCode  int    `json:"exit_code"`
	Signal    int    `json:"signal"`
	TimedOut  bool   `json:"timed_out"`
	ExecError string `json:"exec_error"`
	WallNs    int64  `json:"wall_ns"`
	UserUs    int64  `json:"user_us"`
	SysUs     int64  `json:"sys_us"`
	MaxRSSKB  int64  `json:"max_rss_kb"`
	MinFlt    int64  `json:"minflt"`
	MajFlt    int64  `json:"majflt"`
	NVCSW     int64  `json:"nvcsw"`
	NIVCSW    int64  `json:"nivcsw"`
}

func Args(runner string, o Options, cmd []string) []string {
	args := []string{runner}
	keys := make([]string, 0, len(o.Env))
	for k := range o.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+o.Env[k])
	}
	if o.Timeout > 0 {
		args = append(args, "--timeout", strconv.FormatFloat(o.Timeout.Seconds(), 'f', -1, 64))
	}
	for _, f := range []struct{ flag, path string }{
		{"--stdout", o.Stdout},
		{"--stderr", o.Stderr},
		{"--result", o.Result},
	} {
		if f.path != "" {
			args = append(args, f.flag, f.path)
		}
	}
	args = append(args, "--")
	return append(args, cmd...)
}

func Parse(data []byte) (Result, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Result
	if err := dec.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("runner result: %w", err)
	}
	if r.Runner != Protocol {
		return Result{}, fmt.Errorf("runner result: protocol %d, want %d", r.Runner, Protocol)
	}
	return r, nil
}

func (r Result) Wall() time.Duration {
	return time.Duration(r.WallNs)
}

func (r Result) User() time.Duration {
	return time.Duration(r.UserUs) * time.Microsecond
}

func (r Result) Sys() time.Duration {
	return time.Duration(r.SysUs) * time.Microsecond
}

func (r Result) OK() bool {
	return r.ExitCode == 0 && r.Signal == 0 && !r.TimedOut && r.ExecError == ""
}

func (r Result) Status() string {
	switch {
	case r.ExecError != "":
		return "exec error: " + r.ExecError
	case r.TimedOut:
		return "timeout"
	case r.Signal != 0:
		return "killed by signal " + strconv.Itoa(r.Signal)
	case r.ExitCode != 0:
		return "exit code " + strconv.Itoa(r.ExitCode)
	}
	return "ok"
}
