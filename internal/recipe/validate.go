package recipe

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	revPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	snapshotPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
	kernelPattern   = regexp.MustCompile(`^>=[0-9]+\.[0-9]+$`)
)

func (s *Set) Validate() error {
	var errs []error
	for _, name := range sortedKeys(s.Envs) {
		c := newChecker("envs", name)
		c.env(s.Envs[name])
		errs = append(errs, c.errs...)
	}
	for _, name := range sortedKeys(s.Allocators) {
		c := newChecker("allocators", name)
		c.allocator(s, s.Allocators[name])
		errs = append(errs, c.errs...)
	}
	for _, name := range sortedKeys(s.Benches) {
		c := newChecker("benches", name)
		c.bench(s, s.Benches[name])
		errs = append(errs, c.errs...)
	}
	for _, name := range sortedKeys(s.Tools) {
		c := newChecker("tools", name)
		c.tool(s, s.Tools[name])
		errs = append(errs, c.errs...)
	}
	return errors.Join(errs...)
}

type checker struct {
	where string
	errs  []error
}

func newChecker(kind, name string) *checker {
	c := &checker{where: kind + "/" + name}
	if !namePattern.MatchString(name) {
		c.fail("name %q may only contain a-z, 0-9, '.', '_' and '-'", name)
	}
	return c
}

func (c *checker) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s", c.where, fmt.Sprintf(format, args...)))
}

func (c *checker) env(e Env) {
	if e.Distro == "" {
		c.fail("distro is required")
	}
	if e.Suite == "" {
		c.fail("suite is required")
	}
	if !snapshotPattern.MatchString(e.Snapshot) {
		c.fail("snapshot %q must look like 20260101T000000Z", e.Snapshot)
	}
	if len(e.Packages) == 0 {
		c.fail("packages must not be empty")
	}
}

func (c *checker) allocator(s *Set, a Allocator) {
	c.build(s, a.Source, a.Build, a.BuildEnv)
	c.kernel(a.RequiresKernel)

	if a.Use.Preload != "" {
		if a.Build.Script == "" {
			c.fail("use.preload requires build.script")
		}
		if !filepath.IsLocal(a.Use.Preload) {
			c.fail("use.preload %q must be a relative path inside the build output", a.Use.Preload)
		}
	}
}

func (c *checker) bench(s *Set, b Bench) {
	c.build(s, b.Source, b.Build, b.BuildEnv)
	c.kernel(b.RequiresKernel)

	if len(b.Run.Cmd) == 0 {
		c.fail("run.cmd is required")
	}
	for _, t := range b.Run.Threads {
		if t < 1 {
			c.fail("run.threads: %d must be at least 1", t)
		}
	}
	if b.Run.Timeout.Duration < 0 {
		c.fail("run.timeout %v must not be negative", b.Run.Timeout.Duration)
	}
	switch b.Run.Work {
	case "", "tmpfs", "disk":
	default:
		c.fail("run.work %q must be tmpfs or disk", b.Run.Work)
	}
	if b.Check.StdoutSHA256 != "" && !sha256Pattern.MatchString(b.Check.StdoutSHA256) {
		c.fail("check.stdout_sha256 %q must be 64 lowercase hex characters", b.Check.StdoutSHA256)
	}
}

func (c *checker) tool(s *Set, t Tool) {
	if t.Build.Script == "" {
		c.fail("build.script is required")
	}
	c.build(s, t.Source, t.Build, t.BuildEnv)
}

func (c *checker) build(s *Set, src Source, b Build, buildEnv string) {
	if b.Script == "" {
		if !src.empty() {
			c.fail("source is set but build.script is empty")
		}
		if buildEnv != "" {
			c.fail("build_env is set but build.script is empty")
		}
		if len(b.Deps) > 0 {
			c.fail("build.deps is set but build.script is empty")
		}
		return
	}

	if src.empty() {
		c.fail("build.script requires a source")
	} else {
		c.source(src)
	}
	if buildEnv == "" {
		c.fail("build_env is required to build")
	} else if _, ok := s.Envs[buildEnv]; !ok {
		c.fail("build_env %q is not a recipe in envs", buildEnv)
	}
	for _, d := range b.Deps {
		if _, ok := s.Tools[d]; !ok {
			c.fail("build.deps: %q is not a recipe in tools", d)
		}
	}
}

func (c *checker) source(src Source) {
	n := 0
	for _, v := range []string{src.Git, src.URL, src.Path} {
		if v != "" {
			n++
		}
	}
	if n != 1 {
		c.fail("source must have exactly one of git, url and path")
	}

	if src.Git != "" && !revPattern.MatchString(src.Rev) {
		c.fail("source.rev %q must be a full 40-character commit hash", src.Rev)
	}
	if src.Git == "" && src.Rev != "" {
		c.fail("source.rev is only allowed with source.git")
	}
	if src.URL != "" && !strings.HasPrefix(src.URL, "https://") && !strings.HasPrefix(src.URL, "http://") {
		c.fail("source.url %q must start with http:// or https://", src.URL)
	}
	if src.Path != "" && !filepath.IsLocal(src.Path) {
		c.fail("source.path %q must be a relative path inside the project", src.Path)
	}
	if src.SHA256 != "" && !sha256Pattern.MatchString(src.SHA256) {
		c.fail("source.sha256 %q must be 64 lowercase hex characters", src.SHA256)
	}
	for _, p := range src.Patches {
		if !filepath.IsLocal(p) {
			c.fail("source.patches: %q must be a relative path inside the project", p)
		}
	}
}

func (c *checker) kernel(v string) {
	if v != "" && !kernelPattern.MatchString(v) {
		c.fail("requires_kernel %q must look like >=6.10", v)
	}
}

func (s Source) empty() bool {
	return s.Git == "" && s.URL == "" && s.Path == "" && s.Rev == "" && s.SHA256 == "" && len(s.Patches) == 0
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
