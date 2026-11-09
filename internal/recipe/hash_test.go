package recipe

import (
	"strings"
	"testing"
	"time"
)

func mustAllocatorHash(t *testing.T, s *Set, name string) string {
	t.Helper()
	h, err := s.AllocatorHash(name)
	if err != nil {
		t.Fatalf("AllocatorHash(%s): %v", name, err)
	}
	return h
}

func TestHashIsStable(t *testing.T) {
	const want = "347f0d6280ead5153ac02f9dda2b8ea5f28d2b221d42916b797121bbd5abc104"
	if got := mustAllocatorHash(t, validSet(), "mi"); got != want {
		t.Errorf("hash of mi changed: got %s, want %s\nIf the hash input changed on purpose, bump hashVersion.", got, want)
	}
}

func TestHashIsDeterministic(t *testing.T) {
	a := mustAllocatorHash(t, validSet(), "mi")
	for i := 0; i < 20; i++ {
		if b := mustAllocatorHash(t, validSet(), "mi"); a != b {
			t.Fatalf("hash differs between runs: %s and %s", a, b)
		}
	}
}

func TestHashChangesWithBuildInputs(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *Set)
	}{
		{"script", func(s *Set) {
			a := s.Allocators["mi"]
			a.Build.Script = "ninja -v"
			s.Allocators["mi"] = a
		}},
		{"vars", func(s *Set) {
			a := s.Allocators["mi"]
			a.Build.Vars = map[string]string{"CMAKE_FLAGS": "-DMI_SECURE=ON"}
			s.Allocators["mi"] = a
		}},
		{"rev", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Rev = strings.Repeat("a", 40)
			s.Allocators["mi"] = a
		}},
		{"patches", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Patches = []string{"patches/mi.patch"}
			s.Allocators["mi"] = a
		}},
		{"env snapshot", func(s *Set) {
			e := s.Envs["gcc13"]
			e.Snapshot = "20260201T000000Z"
			s.Envs["gcc13"] = e
		}},
		{"env packages", func(s *Set) {
			e := s.Envs["gcc13"]
			e.Packages = []string{"gcc-13", "cmake"}
			s.Envs["gcc13"] = e
		}},
		{"dependency", func(s *Set) {
			tl := s.Tools["cmake"]
			tl.Build.Script = "cp -a /src/. /out/"
			s.Tools["cmake"] = tl
		}},
	}
	base := mustAllocatorHash(t, validSet(), "mi")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSet()
			tt.change(s)
			if mustAllocatorHash(t, s, "mi") == base {
				t.Error("hash did not change")
			}
		})
	}
}

func TestHashIgnoresNonBuildFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *Set)
	}{
		{"use", func(s *Set) {
			a := s.Allocators["mi"]
			a.Use = Use{Preload: "lib/other.so", Env: map[string]string{"X": "1"}}
			s.Allocators["mi"] = a
		}},
		{"version label", func(s *Set) {
			a := s.Allocators["mi"]
			a.Version = "v9"
			s.Allocators["mi"] = a
		}},
		{"requires_kernel", func(s *Set) {
			a := s.Allocators["mi"]
			a.RequiresKernel = ">=6.10"
			s.Allocators["mi"] = a
		}},
		{"package order and duplicates", func(s *Set) {
			e := s.Envs["gcc13"]
			e.Packages = []string{"gcc-13", "gcc-13"}
			s.Envs["gcc13"] = e
		}},
		{"empty vars", func(s *Set) {
			a := s.Allocators["mi"]
			a.Build.Vars = map[string]string{}
			s.Allocators["mi"] = a
		}},
		{"empty patches", func(s *Set) {
			a := s.Allocators["mi"]
			a.Source.Patches = []string{}
			s.Allocators["mi"] = a
		}},
	}
	base := mustAllocatorHash(t, validSet(), "mi")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSet()
			tt.change(s)
			if got := mustAllocatorHash(t, s, "mi"); got != base {
				t.Error("hash changed")
			}
		})
	}
}

func TestHashSameBuildSameHash(t *testing.T) {
	s := validSet()
	variant := s.Allocators["mi"]
	variant.Use.Env = map[string]string{"MIMALLOC_ARENA_EAGER_COMMIT": "1"}
	s.Allocators["mi-eager"] = variant

	if mustAllocatorHash(t, s, "mi") != mustAllocatorHash(t, s, "mi-eager") {
		t.Error("recipes with the same build got different hashes")
	}
}

func TestEnvHashIgnoresPackageOrder(t *testing.T) {
	a, b := validSet(), validSet()
	a.Envs["gcc13"] = Env{Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"cmake", "gcc-13"}}
	b.Envs["gcc13"] = Env{Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"gcc-13", "cmake"}}

	ha, err := a.EnvHash("gcc13")
	if err != nil {
		t.Fatal(err)
	}
	hb, err := b.EnvHash("gcc13")
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Error("package order changed the env hash")
	}
	if got := a.Envs["gcc13"].Packages[0]; got != "cmake" {
		t.Errorf("EnvHash reordered the recipe packages: %v", a.Envs["gcc13"].Packages)
	}
}

func TestHashOfOtherKinds(t *testing.T) {
	s := validSet()

	if h := mustAllocatorHash(t, s, "sys"); h != "" {
		t.Errorf("sys has nothing to build, got hash %s", h)
	}
	bench, err := s.BenchHash("cfrac")
	if err != nil || len(bench) != 64 {
		t.Errorf("BenchHash: %q, %v", bench, err)
	}
	tool, err := s.ToolHash("cmake")
	if err != nil || len(tool) != 64 {
		t.Errorf("ToolHash: %q, %v", tool, err)
	}
	if bench == tool {
		t.Error("different builds got the same hash")
	}
}

func TestHashErrors(t *testing.T) {
	s := validSet()
	s.Tools["a"] = Tool{BuildEnv: "gcc13", Source: Source{Path: "a"}, Build: Build{Script: "x", Deps: []string{"b"}}}
	s.Tools["b"] = Tool{BuildEnv: "gcc13", Source: Source{Path: "b"}, Build: Build{Script: "x", Deps: []string{"a"}}}
	s.Benches["bad-env"] = Bench{BuildEnv: "gcc99", Source: Source{Path: "x"}, Build: Build{Script: "x"}, Run: Run{Timeout: Duration{time.Second}}}

	tests := []struct {
		name string
		hash func() (string, error)
		want string
	}{
		{"unknown allocator", func() (string, error) { return s.AllocatorHash("nope") }, "allocators/nope: no such recipe"},
		{"unknown env", func() (string, error) { return s.EnvHash("nope") }, "envs/nope: no such recipe"},
		{"bad build env", func() (string, error) { return s.BenchHash("bad-env") }, "benches/bad-env: envs/gcc99: no such recipe"},
		{"deps cycle", func() (string, error) { return s.ToolHash("a") }, "tools/b: deps cycle: a -> b -> a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.hash()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
