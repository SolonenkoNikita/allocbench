package plan

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	OrderInterleaved = "interleaved"
	OrderSequential  = "sequential"
)

type Plan struct {
	Allocators []string `toml:"allocators"`
	Benches    []string `toml:"benches"`
	RunEnv     string   `toml:"run_env"`
	Repeat     int      `toml:"repeat"`
	CPUs       string   `toml:"cpus"`
	Order      string   `toml:"order"`
}

func New() *Plan {
	return &Plan{Repeat: 1, Order: OrderInterleaved}
}

func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	p := New()
	md, err := toml.Decode(string(data), p)
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
	return p, nil
}
