package lock

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	FileName = "allocbench.lock"
	Version  = 1
)

type Env struct {
	Hash string `toml:"hash"`
	Tree string `toml:"tree"`
}

type Build struct {
	Hash string `toml:"hash"`
	Out  string `toml:"out"`
}

type File struct {
	Version int              `toml:"version"`
	Envs    map[string]Env   `toml:"envs"`
	Builds  map[string]Build `toml:"builds"`
}

type Problem struct {
	Ref     string
	Message string
}

func (p Problem) String() string {
	return p.Ref + ": " + p.Message
}

func New() *File {
	return &File{Version: Version, Envs: map[string]Env{}, Builds: map[string]Build{}}
}

func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &File{}
	md, err := toml.Decode(string(data), f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown fields: %s", path, strings.Join(names, ", "))
	}
	if f.Version != Version {
		return nil, fmt.Errorf("%s: version %d, want %d", path, f.Version, Version)
	}
	if f.Envs == nil {
		f.Envs = map[string]Env{}
	}
	if f.Builds == nil {
		f.Builds = map[string]Build{}
	}
	return f, nil
}

func (f *File) Write(path string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (f *File) Merge(other *File) {
	for name, e := range other.Envs {
		f.Envs[name] = e
	}
	for ref, b := range other.Builds {
		f.Builds[ref] = b
	}
}

func (f *File) Check(got *File) []Problem {
	var problems []Problem
	for _, name := range sortedKeys(got.Envs) {
		ref := "envs/" + name
		g := got.Envs[name]
		want, ok := f.Envs[name]
		switch {
		case !ok:
			problems = append(problems, Problem{ref, "not in the lock file"})
		case want.Hash != g.Hash:
			problems = append(problems, Problem{ref, fmt.Sprintf("recipe changed since the lock: hash %s, lock %s", short(g.Hash), short(want.Hash))})
		case want.Tree != g.Tree:
			problems = append(problems, Problem{ref, fmt.Sprintf("rootfs differs from the lock: tree %s, lock %s", short(g.Tree), short(want.Tree))})
		}
	}
	for _, ref := range sortedKeys(got.Builds) {
		g := got.Builds[ref]
		want, ok := f.Builds[ref]
		switch {
		case !ok:
			problems = append(problems, Problem{ref, "not in the lock file"})
		case want.Hash != g.Hash:
			problems = append(problems, Problem{ref, fmt.Sprintf("inputs changed since the lock: hash %s, lock %s", short(g.Hash), short(want.Hash))})
		case want.Out != g.Out:
			problems = append(problems, Problem{ref, fmt.Sprintf("output differs from the lock: out %s, lock %s", short(g.Out), short(want.Out))})
		}
	}
	return problems
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func short(h string) string {
	const n = 12
	if len(h) > n {
		return h[:n]
	}
	return h
}
