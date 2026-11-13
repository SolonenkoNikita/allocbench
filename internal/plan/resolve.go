package plan

import (
	"errors"
	"fmt"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
)

type Build struct {
	Hash    string
	Recipes []string
}

type Cell struct {
	Alloc   string
	Bench   string
	Threads int
	Repeat  int
}

func (c Cell) ID() string {
	return fmt.Sprintf("%s/%s/t%d/r%d", c.Bench, c.Alloc, c.Threads, c.Repeat)
}

type Resolved struct {
	RunEnv string
	Builds []Build
	Cells  []Cell
}

// Resolve checks the plan against the recipes and expands it into builds and cells.
// Builds are ordered so that dependencies come first.
func Resolve(p *Plan, s *recipe.Set) (*Resolved, error) {
	if err := check(p, s); err != nil {
		return nil, err
	}

	r := &Resolved{RunEnv: p.RunEnv}
	b := &builder{set: s, index: map[string]int{}, visited: map[string]bool{}}
	for _, name := range p.Allocators {
		if err := b.allocator(name); err != nil {
			return nil, err
		}
	}
	for _, name := range p.Benches {
		if err := b.bench(name); err != nil {
			return nil, err
		}
	}
	r.Builds = b.builds
	r.Cells = cells(p, s)
	return r, nil
}

func check(p *Plan, s *recipe.Set) error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if len(p.Allocators) == 0 {
		fail("allocators must not be empty")
	}
	if len(p.Benches) == 0 {
		fail("benches must not be empty")
	}
	checkNames("allocators", p.Allocators, s.Allocators, fail)
	checkNames("benches", p.Benches, s.Benches, fail)

	if p.RunEnv == "" {
		fail("run_env is required")
	} else if _, ok := s.Envs[p.RunEnv]; !ok {
		fail("run_env %q is not a recipe in envs", p.RunEnv)
	}
	if p.Repeat < 1 {
		fail("repeat %d must be at least 1", p.Repeat)
	}
	switch p.Order {
	case OrderInterleaved, OrderSequential:
	default:
		fail("order %q must be %s or %s", p.Order, OrderInterleaved, OrderSequential)
	}
	if _, err := ParseCPUs(p.CPUs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func checkNames[T any](kind string, names []string, recipes map[string]T, fail func(string, ...any)) {
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			fail("%s: %q is listed twice", kind, n)
			continue
		}
		seen[n] = true
		if _, ok := recipes[n]; !ok {
			fail("%s: %q is not a recipe in %s", kind, n, kind)
		}
	}
}

type builder struct {
	set     *recipe.Set
	builds  []Build
	index   map[string]int
	visited map[string]bool
}

func (b *builder) add(where, hash string) {
	if hash == "" {
		return
	}
	if i, ok := b.index[hash]; ok {
		b.builds[i].Recipes = append(b.builds[i].Recipes, where)
		return
	}
	b.index[hash] = len(b.builds)
	b.builds = append(b.builds, Build{Hash: hash, Recipes: []string{where}})
}

func (b *builder) deps(deps []string) error {
	for _, d := range deps {
		if err := b.tool(d); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) tool(name string) error {
	if b.visited[name] {
		return nil
	}
	b.visited[name] = true
	if err := b.deps(b.set.Tools[name].Build.Deps); err != nil {
		return err
	}
	h, err := b.set.ToolHash(name)
	if err != nil {
		return err
	}
	b.add("tools/"+name, h)
	return nil
}

func (b *builder) allocator(name string) error {
	if err := b.deps(b.set.Allocators[name].Build.Deps); err != nil {
		return err
	}
	h, err := b.set.AllocatorHash(name)
	if err != nil {
		return err
	}
	b.add("allocators/"+name, h)
	return nil
}

func (b *builder) bench(name string) error {
	if err := b.deps(b.set.Benches[name].Build.Deps); err != nil {
		return err
	}
	h, err := b.set.BenchHash(name)
	if err != nil {
		return err
	}
	b.add("benches/"+name, h)
	return nil
}

func threads(b recipe.Bench) []int {
	if len(b.Run.Threads) == 0 {
		return []int{1}
	}
	return b.Run.Threads
}

// cells expands the plan. Interleaved order runs every allocator once per round,
// so slow drift during the run does not favour one allocator.
func cells(p *Plan, s *recipe.Set) []Cell {
	var out []Cell
	if p.Order == OrderSequential {
		for _, a := range p.Allocators {
			for _, bn := range p.Benches {
				for _, t := range threads(s.Benches[bn]) {
					for r := 0; r < p.Repeat; r++ {
						out = append(out, Cell{Alloc: a, Bench: bn, Threads: t, Repeat: r})
					}
				}
			}
		}
		return out
	}

	for r := 0; r < p.Repeat; r++ {
		for _, bn := range p.Benches {
			for _, t := range threads(s.Benches[bn]) {
				for _, a := range p.Allocators {
					out = append(out, Cell{Alloc: a, Bench: bn, Threads: t, Repeat: r})
				}
			}
		}
	}
	return out
}
