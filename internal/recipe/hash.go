package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Bump when the hash input changes, so old store entries are never reused by mistake.
const hashVersion = "allocbench/v1"

type envInput struct {
	Version  string   `json:"version"`
	Distro   string   `json:"distro"`
	Suite    string   `json:"suite"`
	Snapshot string   `json:"snapshot"`
	Packages []string `json:"packages"`
}

type buildInput struct {
	Version string            `json:"version"`
	Env     string            `json:"env"`
	Source  sourceInput       `json:"source"`
	Script  string            `json:"script"`
	Vars    map[string]string `json:"vars"`
	Deps    map[string]string `json:"deps"`
}

type sourceInput struct {
	Git     string   `json:"git"`
	Rev     string   `json:"rev"`
	URL     string   `json:"url"`
	Path    string   `json:"path"`
	SHA256  string   `json:"sha256"`
	Patches []string `json:"patches"`
}

func (s *Set) EnvHash(name string) (string, error) {
	e, ok := s.Envs[name]
	if !ok {
		return "", fmt.Errorf("envs/%s: no such recipe", name)
	}

	pkgs := slices.Clone(e.Packages)
	sort.Strings(pkgs)
	pkgs = slices.Compact(pkgs)

	return digest(envInput{
		Version:  hashVersion,
		Distro:   e.Distro,
		Suite:    e.Suite,
		Snapshot: e.Snapshot,
		Packages: nilIfEmpty(pkgs),
	})
}

// AllocatorHash returns the build hash of an allocator, or "" if it has nothing to build.
func (s *Set) AllocatorHash(name string) (string, error) {
	a, ok := s.Allocators[name]
	if !ok {
		return "", fmt.Errorf("allocators/%s: no such recipe", name)
	}
	return s.buildHash("allocators/"+name, a.Source, a.Build, a.BuildEnv, nil)
}

// BenchHash returns the build hash of a bench, or "" if it has nothing to build.
func (s *Set) BenchHash(name string) (string, error) {
	b, ok := s.Benches[name]
	if !ok {
		return "", fmt.Errorf("benches/%s: no such recipe", name)
	}
	return s.buildHash("benches/"+name, b.Source, b.Build, b.BuildEnv, nil)
}

func (s *Set) ToolHash(name string) (string, error) {
	return s.toolHash(name, nil)
}

func (s *Set) toolHash(name string, chain []string) (string, error) {
	t, ok := s.Tools[name]
	if !ok {
		return "", fmt.Errorf("tools/%s: no such recipe", name)
	}
	return s.buildHash("tools/"+name, t.Source, t.Build, t.BuildEnv, append(chain, name))
}

func (s *Set) buildHash(where string, src Source, b Build, buildEnv string, chain []string) (string, error) {
	if b.Script == "" {
		return "", nil
	}

	env, err := s.EnvHash(buildEnv)
	if err != nil {
		return "", fmt.Errorf("%s: %w", where, err)
	}

	var deps map[string]string
	for _, d := range b.Deps {
		if slices.Contains(chain, d) {
			return "", fmt.Errorf("%s: deps cycle: %s", where, strings.Join(append(chain, d), " -> "))
		}
		h, err := s.toolHash(d, chain)
		if err != nil {
			return "", err
		}
		if deps == nil {
			deps = map[string]string{}
		}
		deps[d] = h
	}

	return digest(buildInput{
		Version: hashVersion,
		Env:     env,
		Source: sourceInput{
			Git:     src.Git,
			Rev:     src.Rev,
			URL:     src.URL,
			Path:    src.Path,
			SHA256:  src.SHA256,
			Patches: nilIfEmpty(src.Patches),
		},
		Script: b.Script,
		Vars:   nilIfEmptyMap(b.Vars),
		Deps:   deps,
	})
}

func digest(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func nilIfEmptyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
