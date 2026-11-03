package recipe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const ext = ".toml"

// Load reads all recipes from dir. The recipe name is the file name without the extension.
func Load(dir string) (*Set, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	s := &Set{
		Envs:       map[string]Env{},
		Allocators: map[string]Allocator{},
		Benches:    map[string]Bench{},
		Tools:      map[string]Tool{},
	}
	if err := loadKind(filepath.Join(dir, "envs"), s.Envs); err != nil {
		return nil, err
	}
	if err := loadKind(filepath.Join(dir, "allocators"), s.Allocators); err != nil {
		return nil, err
	}
	if err := loadKind(filepath.Join(dir, "benches"), s.Benches); err != nil {
		return nil, err
	}
	if err := loadKind(filepath.Join(dir, "tools"), s.Tools); err != nil {
		return nil, err
	}
	return s, nil
}

func loadKind[T any](dir string, out map[string]T) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ext {
			continue
		}
		var v T
		if err := decodeFile(filepath.Join(dir, e.Name()), &v); err != nil {
			return err
		}
		out[strings.TrimSuffix(e.Name(), ext)] = v
	}
	return nil
}

func decodeFile(path string, v any) error {
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return fmt.Errorf("%s: unknown fields: %s", path, strings.Join(names, ", "))
	}
	return nil
}
