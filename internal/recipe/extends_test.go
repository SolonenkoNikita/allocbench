package recipe

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func loadExtends(t *testing.T) *Set {
	t.Helper()
	s, err := Load(filepath.Join("testdata", "extends"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestExtendsInheritsUnsetFields(t *testing.T) {
	smi := loadExtends(t).Allocators["smi"]

	if smi.Version != "v1.8.2" || smi.BuildEnv != "gcc13" {
		t.Errorf("top-level fields not inherited: %+v", smi)
	}
	if smi.Source.Git != "https://github.com/microsoft/mimalloc" || smi.Source.Rev != "b66e3214" {
		t.Errorf("source not inherited: %+v", smi.Source)
	}
	if smi.Build.Script != "cmake -S /src -B . $CMAKE_FLAGS && ninja" {
		t.Errorf("script not inherited: %q", smi.Build.Script)
	}
	if smi.Use.Preload != "lib/libmimalloc.so" {
		t.Errorf("preload not inherited: %q", smi.Use.Preload)
	}
}

func TestExtendsMergesMapsByKey(t *testing.T) {
	smi := loadExtends(t).Allocators["smi"]

	want := map[string]string{"CMAKE_FLAGS": "-DMI_SECURE=ON", "JOBS": "8"}
	if !reflect.DeepEqual(smi.Build.Vars, want) {
		t.Errorf("vars: got %v, want %v", smi.Build.Vars, want)
	}
}

func TestExtendsChain(t *testing.T) {
	s := loadExtends(t)
	e := s.Allocators["smi-eager"]

	if e.Extends != "smi" {
		t.Errorf("extends: got %q, want %q", e.Extends, "smi")
	}
	if e.Build.Vars["CMAKE_FLAGS"] != "-DMI_SECURE=ON" {
		t.Errorf("vars from smi not inherited: %v", e.Build.Vars)
	}
	want := map[string]string{"MIMALLOC_VERBOSE": "0", "MIMALLOC_ARENA_EAGER_COMMIT": "1"}
	if !reflect.DeepEqual(e.Use.Env, want) {
		t.Errorf("env: got %v, want %v", e.Use.Env, want)
	}
}

func TestExtendsDoesNotChangeParent(t *testing.T) {
	mi := loadExtends(t).Allocators["mi"]

	if mi.Build.Vars["CMAKE_FLAGS"] != "-DCMAKE_BUILD_TYPE=Release" {
		t.Errorf("parent vars changed: %v", mi.Build.Vars)
	}
	if _, ok := mi.Use.Env["MIMALLOC_ARENA_EAGER_COMMIT"]; ok {
		t.Errorf("parent env changed: %v", mi.Use.Env)
	}
}

func TestExtendsOverridesWithEmptyValue(t *testing.T) {
	a := loadExtends(t).Allocators["mi-nopreload"]

	if a.Use.Preload != "" {
		t.Errorf("preload: got %q, want empty", a.Use.Preload)
	}
	if a.Use.Env["MIMALLOC_VERBOSE"] != "0" {
		t.Errorf("env not inherited: %v", a.Use.Env)
	}
}

func TestExtendsReplacesLists(t *testing.T) {
	b := loadExtends(t).Benches["larson-big"]

	if !reflect.DeepEqual(b.Run.Threads, []int{64}) {
		t.Errorf("threads: got %v, want [64]", b.Run.Threads)
	}
	if !reflect.DeepEqual(b.Run.Cmd, []string{"/bench/bin/larson", "5", "8"}) {
		t.Errorf("cmd not inherited: %v", b.Run.Cmd)
	}
	if b.Run.Timeout.Duration != 5*time.Minute {
		t.Errorf("timeout not inherited: %v", b.Run.Timeout)
	}
}

func TestExtendsErrors(t *testing.T) {
	tests := []struct {
		dir  string
		want []string
	}{
		{"cycle", []string{"allocators/c.toml", "extends cycle: a -> b -> c -> a"}},
		{"noparent", []string{"allocators/smi.toml", `extends unknown recipe "mi"`}},
		{"envextends", []string{"envs/gcc14.toml", "unknown fields: extends"}},
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
