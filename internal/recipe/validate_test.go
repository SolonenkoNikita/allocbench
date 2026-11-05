package recipe

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRev    = "b66e3214d8a104669c2ec05ae91ebc26a8f5ab78"
	testSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func validSet() *Set {
	return &Set{
		Envs: map[string]Env{
			"gcc13": {Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"gcc-13"}},
		},
		Allocators: map[string]Allocator{
			"sys": {},
			"mi": {
				BuildEnv: "gcc13",
				Source:   Source{Git: "https://github.com/microsoft/mimalloc", Rev: testRev, SHA256: testSHA256},
				Build:    Build{Script: "ninja", Deps: []string{"cmake"}},
				Use:      Use{Preload: "lib/libmimalloc.so"},
			},
		},
		Benches: map[string]Bench{
			"cfrac": {
				BuildEnv: "gcc13",
				Source:   Source{Path: "bench-src/cfrac"},
				Build:    Build{Script: "make"},
				Run:      Run{Cmd: []string{"/bench/bin/cfrac"}, Threads: []int{1, 4}, Timeout: Duration{time.Minute}},
			},
		},
		Tools: map[string]Tool{
			"cmake": {
				BuildEnv: "gcc13",
				Source:   Source{URL: "https://example.com/cmake.tar.gz", SHA256: testSHA256},
				Build:    Build{Script: "cp -r /src/. /out/"},
			},
		},
	}
}

func TestValidateValidSet(t *testing.T) {
	if err := validSet().Validate(); err != nil {
		t.Errorf("unexpected error:\n%v", err)
	}
}

func TestValidateTestdata(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("unexpected error:\n%v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *Set)
		want   string
	}{
		{"branch as rev", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Rev = "master"
			s.Allocators["mi"] = a
		}, `allocators/mi: source.rev "master" must be a full 40-character commit hash`},
		{"short rev", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Rev = "b66e3214"
			s.Allocators["mi"] = a
		}, `source.rev "b66e3214" must be a full 40-character commit hash`},
		{"missing rev", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Rev = ""
			s.Allocators["mi"] = a
		}, `source.rev "" must be a full 40-character commit hash`},
		{"rev without git", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Source.Rev = testRev
			s.Benches["cfrac"] = b
		}, "benches/cfrac: source.rev is only allowed with source.git"},
		{"two sources", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Path = "src/mi"
			s.Allocators["mi"] = a
		}, "source must have exactly one of git, url and path"},
		{"bad sha256", func(s *Set) {
			tl := s.Tools["cmake"]
			tl.Source.SHA256 = "abc"
			s.Tools["cmake"] = tl
		}, `tools/cmake: source.sha256 "abc" must be 64 lowercase hex characters`},
		{"ftp url", func(s *Set) {
			tl := s.Tools["cmake"]
			tl.Source.URL = "ftp://example.com/cmake.tar.gz"
			s.Tools["cmake"] = tl
		}, "must start with http:// or https://"},
		{"path outside project", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Source.Path = "../cfrac"
			s.Benches["cfrac"] = b
		}, `source.path "../cfrac" must be a relative path inside the project`},
		{"absolute patch", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Patches = []string{"/tmp/fix.patch"}
			s.Allocators["mi"] = a
		}, `source.patches: "/tmp/fix.patch" must be a relative path`},
		{"unknown build env", func(s *Set) {
			a := s.Allocators["mi"]
			a.BuildEnv = "gcc99"
			s.Allocators["mi"] = a
		}, `allocators/mi: build_env "gcc99" is not a recipe in envs`},
		{"missing build env", func(s *Set) {
			a := s.Allocators["mi"]
			a.BuildEnv = ""
			s.Allocators["mi"] = a
		}, "build_env is required to build"},
		{"unknown dep", func(s *Set) {
			a := s.Allocators["mi"]
			a.Build.Deps = []string{"bazel"}
			s.Allocators["mi"] = a
		}, `build.deps: "bazel" is not a recipe in tools`},
		{"script without source", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source = Source{}
			s.Allocators["mi"] = a
		}, "build.script requires a source"},
		{"source without script", func(s *Set) {
			a := s.Allocators["mi"]
			a.Build.Script = ""
			a.Use.Preload = ""
			s.Allocators["mi"] = a
		}, "source is set but build.script is empty"},
		{"preload without build", func(s *Set) {
			s.Allocators["sys"] = Allocator{Use: Use{Preload: "lib/libc.so"}}
		}, "allocators/sys: use.preload requires build.script"},
		{"preload outside output", func(s *Set) {
			a := s.Allocators["mi"]
			a.Use.Preload = "/usr/lib/libmimalloc.so"
			s.Allocators["mi"] = a
		}, `use.preload "/usr/lib/libmimalloc.so" must be a relative path`},
		{"bad kernel", func(s *Set) {
			a := s.Allocators["mi"]
			a.RequiresKernel = "6.10"
			s.Allocators["mi"] = a
		}, `requires_kernel "6.10" must look like >=6.10`},
		{"empty cmd", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Run.Cmd = nil
			s.Benches["cfrac"] = b
		}, "benches/cfrac: run.cmd is required"},
		{"zero threads", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Run.Threads = []int{1, 0}
			s.Benches["cfrac"] = b
		}, "run.threads: 0 must be at least 1"},
		{"negative timeout", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Run.Timeout = Duration{-time.Second}
			s.Benches["cfrac"] = b
		}, "run.timeout -1s must not be negative"},
		{"bad work", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Run.Work = "ram"
			s.Benches["cfrac"] = b
		}, `run.work "ram" must be tmpfs or disk`},
		{"bad stdout hash", func(s *Set) {
			b := s.Benches["cfrac"]
			b.Check.StdoutSHA256 = "ABC"
			s.Benches["cfrac"] = b
		}, `check.stdout_sha256 "ABC" must be 64 lowercase hex characters`},
		{"tool without script", func(s *Set) {
			s.Tools["cmake"] = Tool{}
		}, "tools/cmake: build.script is required"},
		{"bad snapshot", func(s *Set) {
			e := s.Envs["gcc13"]
			e.Snapshot = "2026-01-01"
			s.Envs["gcc13"] = e
		}, `envs/gcc13: snapshot "2026-01-01" must look like 20260101T000000Z`},
		{"env without packages", func(s *Set) {
			e := s.Envs["gcc13"]
			e.Packages = nil
			s.Envs["gcc13"] = e
		}, "envs/gcc13: packages must not be empty"},
		{"bad name", func(s *Set) {
			s.Allocators["Mi Secure"] = Allocator{}
		}, `allocators/Mi Secure: name "Mi Secure" may only contain`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSet()
			tt.change(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error:\n%v\ndoes not contain:\n%s", err, tt.want)
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	s := validSet()
	a := s.Allocators["mi"]
	a.Source.Rev = "master"
	a.BuildEnv = "gcc99"
	s.Allocators["mi"] = a
	b := s.Benches["cfrac"]
	b.Run.Cmd = nil
	s.Benches["cfrac"] = b

	err := s.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := len(strings.Split(err.Error(), "\n")); got != 3 {
		t.Errorf("got %d problems, want 3:\n%v", got, err)
	}
}
