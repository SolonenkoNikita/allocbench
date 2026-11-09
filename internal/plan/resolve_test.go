package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
)

func testSet() *recipe.Set {
	src := func(path string) recipe.Source { return recipe.Source{Path: path} }
	build := func(script string, deps ...string) recipe.Build {
		return recipe.Build{Script: script, Deps: deps}
	}
	return &recipe.Set{
		Envs: map[string]recipe.Env{
			"gcc13": {Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"gcc-13"}},
			"run":   {Distro: "ubuntu", Suite: "noble", Snapshot: "20260101T000000Z", Packages: []string{"libc6"}},
		},
		Tools: map[string]recipe.Tool{
			"cmake": {BuildEnv: "gcc13", Source: src("tools/cmake"), Build: build("cp")},
			"bazel": {BuildEnv: "gcc13", Source: src("tools/bazel"), Build: build("cp", "cmake")},
		},
		Allocators: map[string]recipe.Allocator{
			"sys": {},
			"mi":  {BuildEnv: "gcc13", Source: src("mi"), Build: build("ninja", "cmake"), Use: recipe.Use{Preload: "lib/mi.so"}},
			"mi-eager": {BuildEnv: "gcc13", Source: src("mi"), Build: build("ninja", "cmake"),
				Use: recipe.Use{Preload: "lib/mi.so", Env: map[string]string{"MIMALLOC_ARENA_EAGER_COMMIT": "1"}}},
			"tcg": {BuildEnv: "gcc13", Source: src("tcg"), Build: build("bazel build", "bazel")},
		},
		Benches: map[string]recipe.Bench{
			"cfrac":  {BuildEnv: "gcc13", Source: src("cfrac"), Build: build("make"), Run: recipe.Run{Cmd: []string{"cfrac"}}},
			"larson": {BuildEnv: "gcc13", Source: src("larson"), Build: build("make"), Run: recipe.Run{Cmd: []string{"larson"}, Threads: []int{1, 4}}},
		},
	}
}

func newPlan(allocs, benches []string, repeat int) *Plan {
	p := New()
	p.Allocators = allocs
	p.Benches = benches
	p.RunEnv = "run"
	p.Repeat = repeat
	return p
}

func ids(cells []Cell) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.ID()
	}
	return out
}

func TestCellID(t *testing.T) {
	c := Cell{Alloc: "mi", Bench: "cfrac", Threads: 4, Repeat: 2}
	if got, want := c.ID(), "cfrac/mi/t4/r2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveInterleaved(t *testing.T) {
	r, err := Resolve(newPlan([]string{"sys", "mi"}, []string{"cfrac", "larson"}, 2), testSet())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{
		"cfrac/sys/t1/r0", "cfrac/mi/t1/r0",
		"larson/sys/t1/r0", "larson/mi/t1/r0",
		"larson/sys/t4/r0", "larson/mi/t4/r0",
		"cfrac/sys/t1/r1", "cfrac/mi/t1/r1",
		"larson/sys/t1/r1", "larson/mi/t1/r1",
		"larson/sys/t4/r1", "larson/mi/t4/r1",
	}
	if got := ids(r.Cells); !reflect.DeepEqual(got, want) {
		t.Errorf("cells:\n got %v\nwant %v", got, want)
	}
	if r.RunEnv != "run" {
		t.Errorf("run env: %q", r.RunEnv)
	}
}

func TestResolveSequential(t *testing.T) {
	p := newPlan([]string{"sys", "mi"}, []string{"larson"}, 2)
	p.Order = OrderSequential
	r, err := Resolve(p, testSet())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{
		"larson/sys/t1/r0", "larson/sys/t1/r1", "larson/sys/t4/r0", "larson/sys/t4/r1",
		"larson/mi/t1/r0", "larson/mi/t1/r1", "larson/mi/t4/r0", "larson/mi/t4/r1",
	}
	if got := ids(r.Cells); !reflect.DeepEqual(got, want) {
		t.Errorf("cells:\n got %v\nwant %v", got, want)
	}
}

func TestResolveBuildsDependenciesFirst(t *testing.T) {
	r, err := Resolve(newPlan([]string{"tcg", "mi"}, []string{"cfrac"}, 1), testSet())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var got [][]string
	for _, b := range r.Builds {
		got = append(got, b.Recipes)
	}
	want := [][]string{
		{"tools/cmake"},
		{"tools/bazel"},
		{"allocators/tcg"},
		{"allocators/mi"},
		{"benches/cfrac"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("builds:\n got %v\nwant %v", got, want)
	}
	for _, b := range r.Builds {
		if len(b.Hash) != 64 {
			t.Errorf("%v: bad hash %q", b.Recipes, b.Hash)
		}
	}
}

func TestResolveSharesSameBuild(t *testing.T) {
	r, err := Resolve(newPlan([]string{"sys", "mi", "mi-eager"}, []string{"cfrac"}, 1), testSet())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var shared *Build
	for i := range r.Builds {
		if reflect.DeepEqual(r.Builds[i].Recipes, []string{"allocators/mi", "allocators/mi-eager"}) {
			shared = &r.Builds[i]
		}
	}
	if shared == nil {
		t.Errorf("mi and mi-eager do not share a build: %+v", r.Builds)
	}
	for _, b := range r.Builds {
		for _, name := range b.Recipes {
			if name == "allocators/sys" {
				t.Error("sys has nothing to build but got a build")
			}
		}
	}
	if got, want := len(r.Cells), 3; got != want {
		t.Errorf("got %d cells, want %d", got, want)
	}
}

func TestResolveErrors(t *testing.T) {
	p := &Plan{
		Allocators: []string{"mi", "mi", "je"},
		Benches:    []string{"nope"},
		RunEnv:     "gcc99",
		Repeat:     0,
		Order:      "random",
	}
	_, err := Resolve(p, testSet())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, w := range []string{
		`allocators: "mi" is listed twice`,
		`allocators: "je" is not a recipe in allocators`,
		`benches: "nope" is not a recipe in benches`,
		`run_env "gcc99" is not a recipe in envs`,
		"repeat 0 must be at least 1",
		`order "random" must be interleaved or sequential`,
	} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not contain %q:\n%v", w, err)
		}
	}
}

func TestResolveEmptyPlan(t *testing.T) {
	_, err := Resolve(New(), testSet())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, w := range []string{"allocators must not be empty", "benches must not be empty", "run_env is required"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not contain %q:\n%v", w, err)
		}
	}
}
