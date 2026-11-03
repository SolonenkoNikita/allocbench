package recipe

import "time"

type Env struct {
	Distro   string   `toml:"distro"`
	Suite    string   `toml:"suite"`
	Snapshot string   `toml:"snapshot"`
	Packages []string `toml:"packages"`
}

type Source struct {
	Git     string   `toml:"git"`
	Rev     string   `toml:"rev"`
	URL     string   `toml:"url"`
	Path    string   `toml:"path"`
	SHA256  string   `toml:"sha256"`
	Patches []string `toml:"patches"`
}

type Build struct {
	Deps   []string          `toml:"deps"`
	Script string            `toml:"script"`
	Vars   map[string]string `toml:"vars"`
}

type Use struct {
	Preload         string            `toml:"preload"`
	Env             map[string]string `toml:"env"`
	RuntimePackages []string          `toml:"runtime_packages"`
}

type Allocator struct {
	Extends        string `toml:"extends"`
	Version        string `toml:"version"`
	BuildEnv       string `toml:"build_env"`
	RequiresKernel string `toml:"requires_kernel"`
	Source         Source `toml:"source"`
	Build          Build  `toml:"build"`
	Use            Use    `toml:"use"`
}

type Run struct {
	Cmd             []string `toml:"cmd"`
	Timeout         Duration `toml:"timeout"`
	Threads         []int    `toml:"threads"`
	Work            string   `toml:"work"`
	RuntimePackages []string `toml:"runtime_packages"`
}

type Check struct {
	StdoutSHA256 string `toml:"stdout_sha256"`
}

type Bench struct {
	Extends        string `toml:"extends"`
	BuildEnv       string `toml:"build_env"`
	RequiresKernel string `toml:"requires_kernel"`
	Source         Source `toml:"source"`
	Build          Build  `toml:"build"`
	Run            Run    `toml:"run"`
	Check          Check  `toml:"check"`
}

type Tool struct {
	BuildEnv string `toml:"build_env"`
	Source   Source `toml:"source"`
	Build    Build  `toml:"build"`
}

type Set struct {
	Envs       map[string]Env
	Allocators map[string]Allocator
	Benches    map[string]Bench
	Tools      map[string]Tool
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}
