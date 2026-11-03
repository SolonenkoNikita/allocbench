package recipe

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadValid(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := len(s.Envs), 1; got != want {
		t.Errorf("envs: got %d, want %d", got, want)
	}
	if got, want := len(s.Allocators), 3; got != want {
		t.Errorf("allocators: got %d, want %d", got, want)
	}
	if got, want := len(s.Benches), 1; got != want {
		t.Errorf("benches: got %d, want %d", got, want)
	}
	if got, want := len(s.Tools), 1; got != want {
		t.Errorf("tools: got %d, want %d", got, want)
	}

	env := s.Envs["gcc13"]
	if env.Snapshot != "20260101T000000Z" || len(env.Packages) != 4 {
		t.Errorf("env gcc13: %+v", env)
	}

	mi := s.Allocators["mi"]
	if mi.Version != "v1.8.2" || mi.BuildEnv != "gcc13" || mi.Source.Rev != "b66e3214" {
		t.Errorf("allocator mi: %+v", mi)
	}
	if mi.Build.Vars["CMAKE_FLAGS"] != "-DCMAKE_BUILD_TYPE=Release" {
		t.Errorf("mi vars: %v", mi.Build.Vars)
	}
	if mi.Use.Preload != "lib/libmimalloc.so" || mi.Use.Env["MIMALLOC_VERBOSE"] != "0" {
		t.Errorf("mi use: %+v", mi.Use)
	}

	if smi := s.Allocators["smi"]; smi.Extends != "mi" {
		t.Errorf("allocator smi: %+v", smi)
	}

	cfrac := s.Benches["cfrac"]
	if cfrac.Run.Timeout.Duration != 5*time.Minute {
		t.Errorf("cfrac timeout: %v", cfrac.Run.Timeout)
	}
	if !reflect.DeepEqual(cfrac.Run.Threads, []int{1, 4}) {
		t.Errorf("cfrac threads: %v", cfrac.Run.Threads)
	}
	if cfrac.Source.Path != "bench-src/cfrac" {
		t.Errorf("cfrac source: %+v", cfrac.Source)
	}

	if tool := s.Tools["cmake-3.31"]; tool.Source.URL == "" {
		t.Errorf("tool cmake-3.31: %+v", tool)
	}
}

func TestLoadSkipsNonRecipeFiles(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s.Allocators["README"]; ok {
		t.Error("README.md was loaded as a recipe")
	}
}

func TestLoadMissingKindDirs(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "unknown", "allocators"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Envs)+len(s.Allocators)+len(s.Benches)+len(s.Tools) != 0 {
		t.Errorf("expected no recipes, got %+v", s)
	}
}

func TestLoadMissingDir(t *testing.T) {
	_, err := Load(filepath.Join("testdata", "no-such-dir"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got %v, want fs.ErrNotExist", err)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		dir  string
		want []string
	}{
		{"unknown", []string{"allocators/mi.toml", "unknown fields", "buidl_env", "use.preloat"}},
		{"syntax", []string{"benches/cfrac.toml", "line"}},
		{"badtimeout", []string{"benches/cfrac.toml", "five minutes"}},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			_, err := Load(filepath.Join("testdata", tt.dir))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}
