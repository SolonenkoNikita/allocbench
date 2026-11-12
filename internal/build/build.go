package build

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/SolonenkoNikita/allocbench/internal/env"
	"github.com/SolonenkoNikita/allocbench/internal/logs"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/sandbox"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

const (
	keyVersion = "allocbench-build/v1"
	logTail    = 20
)

type Builder struct {
	Set      *recipe.Set
	Store    *store.Store
	Sources  *source.Fetcher
	Envs     *env.Builder
	Bwrap    string
	Jobs     int
	Output   io.Writer
	Progress io.Writer

	done map[string]Result
}

type Result struct {
	Recipe string
	Hash   string
	Dir    string
	Log    string
	Cached bool
}

type target struct {
	src      recipe.Source
	build    recipe.Build
	buildEnv string
	hash     string
}

func (b *Builder) Build(ref string) (Result, error) {
	if r, ok := b.done[ref]; ok {
		return r, nil
	}

	t, err := b.target(ref)
	if err != nil {
		return Result{}, err
	}
	if t.hash == "" {
		return b.remember(Result{Recipe: ref, Cached: true}), nil
	}

	deps, err := b.deps(t)
	if err != nil {
		return Result{}, err
	}
	src, err := b.fetch(ref, t)
	if err != nil {
		return Result{}, err
	}

	key := inputKey(t, src.Key, deps)
	res := Result{Recipe: ref, Hash: key, Dir: b.Store.Path(store.Build, key), Log: b.Store.Log(store.Build, key)}

	entry, err := b.Store.Create(store.Build, key)
	if errors.Is(err, store.ErrExists) {
		res.Cached = true
		return b.remember(res), nil
	}
	if err != nil {
		return Result{}, err
	}
	defer entry.Abort()

	e, err := b.env(ref, t.buildEnv)
	if err != nil {
		return Result{}, err
	}

	b.progress("building %s", ref)
	spec, err := b.spec(t, e, src, deps, entry.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", ref, err)
	}
	spec.Cmd = []string{"/bin/sh", "-euc", t.build.Script}
	if err := b.run(spec, entry.Dir, res.Log); err != nil {
		return Result{}, fmt.Errorf("%s: %w", ref, err)
	}
	if err := entry.Commit(); err != nil {
		return Result{}, err
	}
	return b.remember(res), nil
}

func (b *Builder) Shell(ref, out string) (sandbox.Spec, error) {
	t, err := b.target(ref)
	if err != nil {
		return sandbox.Spec{}, err
	}
	if t.hash == "" {
		return sandbox.Spec{}, fmt.Errorf("%s has nothing to build, open a shell in an environment instead", ref)
	}
	deps, err := b.deps(t)
	if err != nil {
		return sandbox.Spec{}, err
	}
	src, err := b.fetch(ref, t)
	if err != nil {
		return sandbox.Spec{}, err
	}
	e, err := b.env(ref, t.buildEnv)
	if err != nil {
		return sandbox.Spec{}, err
	}
	spec, err := b.spec(t, e, src, deps, out)
	if err != nil {
		return sandbox.Spec{}, fmt.Errorf("%s: %w", ref, err)
	}
	return spec, nil
}

func (b *Builder) Env(name string) (env.Env, error) {
	if _, ok := b.Set.Envs[name]; !ok {
		return env.Env{}, fmt.Errorf("envs/%s: no such recipe", name)
	}
	return b.env("envs/"+name, name)
}

func (b *Builder) EnvShell(name string) (sandbox.Spec, error) {
	e, err := b.Env(name)
	if err != nil {
		return sandbox.Spec{}, err
	}
	return sandbox.Spec{
		Rootfs: e.Rootfs,
		Tmpfs:  []string{"/build"},
		Env:    map[string]string{"HOME": "/build"},
		Dir:    "/build",
	}, nil
}

func (b *Builder) Command(spec sandbox.Spec) (*exec.Cmd, error) {
	args, err := sandbox.Args(spec)
	if err != nil {
		return nil, err
	}
	return exec.Command(b.bwrap(), args...), nil
}

func (b *Builder) remember(r Result) Result {
	if b.done == nil {
		b.done = map[string]Result{}
	}
	b.done[r.Recipe] = r
	return r
}

func (b *Builder) target(ref string) (target, error) {
	kind, name, _ := strings.Cut(ref, "/")
	var (
		t   target
		err error
		ok  bool
	)
	switch kind {
	case "allocators":
		var a recipe.Allocator
		if a, ok = b.Set.Allocators[name]; ok {
			t = target{src: a.Source, build: a.Build, buildEnv: a.BuildEnv}
			t.hash, err = b.Set.AllocatorHash(name)
		}
	case "benches":
		var bn recipe.Bench
		if bn, ok = b.Set.Benches[name]; ok {
			t = target{src: bn.Source, build: bn.Build, buildEnv: bn.BuildEnv}
			t.hash, err = b.Set.BenchHash(name)
		}
	case "tools":
		var tl recipe.Tool
		if tl, ok = b.Set.Tools[name]; ok {
			t = target{src: tl.Source, build: tl.Build, buildEnv: tl.BuildEnv}
			t.hash, err = b.Set.ToolHash(name)
		}
	default:
		return target{}, fmt.Errorf("%q: recipe kind must be allocators, benches or tools", ref)
	}
	if !ok {
		return target{}, fmt.Errorf("%s: no such recipe", ref)
	}
	return t, err
}

func (b *Builder) deps(t target) ([]Result, error) {
	deps := make([]Result, len(t.build.Deps))
	for i, d := range t.build.Deps {
		r, err := b.Build("tools/" + d)
		if err != nil {
			return nil, err
		}
		deps[i] = r
	}
	return deps, nil
}

func (b *Builder) fetch(ref string, t target) (source.Result, error) {
	if t.src.Path == "" {
		if key, err := b.Sources.Key(t.src); err == nil && !b.Store.Has(store.Src, key) {
			b.progress("fetching source of %s", ref)
		}
	}
	src, err := b.Sources.Fetch(t.src)
	if err != nil {
		return source.Result{}, fmt.Errorf("%s: source: %w", ref, err)
	}
	return src, nil
}

func (b *Builder) env(ref, name string) (env.Env, error) {
	hash, err := b.Set.EnvHash(name)
	if err != nil {
		return env.Env{}, fmt.Errorf("%s: %w", ref, err)
	}
	if !b.Store.Has(store.Env, hash) {
		b.progress("creating environment %s, this takes a few minutes", name)
	}
	e, err := b.Envs.Ensure(b.Set.Envs[name], hash)
	if err != nil {
		return env.Env{}, fmt.Errorf("%s: environment %s: %w", ref, name, err)
	}
	return e, nil
}

func inputKey(t target, srcKey string, deps []Result) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\n", keyVersion, t.hash, srcKey)
	for i, d := range t.build.Deps {
		fmt.Fprintf(h, "dep\x00%s\x00%s\n", d, deps[i].Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (b *Builder) spec(t target, e env.Env, src source.Result, deps []Result, out string) (sandbox.Spec, error) {
	epoch, err := env.SnapshotEpoch(b.Set.Envs[t.buildEnv].Snapshot)
	if err != nil {
		return sandbox.Spec{}, err
	}

	vars := map[string]string{}
	for k, v := range t.build.Vars {
		vars[k] = v
	}
	path := sandbox.DefaultPath
	mounts := []sandbox.Mount{{Src: src.Dir, Dst: "/src"}}
	for i, d := range t.build.Deps {
		mounts = append(mounts, sandbox.Mount{Src: deps[i].Dir, Dst: "/deps/" + d})
		path = "/deps/" + d + "/bin:" + path
	}
	mounts = append(mounts, sandbox.Mount{Src: out, Dst: "/out", RW: true})
	vars["PATH"] = path
	vars["HOME"] = "/build"
	vars["JOBS"] = strconv.Itoa(b.jobs())
	vars["SOURCE_DATE_EPOCH"] = strconv.FormatInt(epoch, 10)

	return sandbox.Spec{
		Rootfs: e.Rootfs,
		Mounts: mounts,
		Tmpfs:  []string{"/build"},
		Env:    vars,
		Dir:    "/build",
	}, nil
}

func (b *Builder) run(spec sandbox.Spec, out, logPath string) error {
	cmd, err := b.Command(spec)
	if err != nil {
		return err
	}

	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	var w io.Writer = log
	if b.Output != nil {
		w = io.MultiWriter(log, b.Output)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build script failed: %w\n%s\nfull log: %s", err, logs.Tail(logPath, logTail), logPath)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("build script put nothing into /out, see %s", logPath)
	}
	return nil
}

func (b *Builder) bwrap() string {
	if b.Bwrap != "" {
		return b.Bwrap
	}
	return "bwrap"
}

func (b *Builder) jobs() int {
	if b.Jobs > 0 {
		return b.Jobs
	}
	return 1
}

func (b *Builder) progress(format string, args ...any) {
	if b.Progress != nil {
		fmt.Fprintf(b.Progress, format+"\n", args...)
	}
}
