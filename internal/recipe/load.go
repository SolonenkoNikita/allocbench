package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const ext = ".toml"

type file struct {
	path string
	raw  map[string]any
}

// Load reads all recipes from dir. The recipe name is the file name without the extension.
// A recipe with extends gets every field of its parent that it does not set itself.
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
	files, err := readKind[T](dir)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		merged, err := flatten(files, name, nil)
		if err != nil {
			return err
		}
		var v T
		if err := remarshal(merged, &v); err != nil {
			return fmt.Errorf("%s: %w", files[name].path, err)
		}
		out[name] = v
	}
	return nil
}

func readKind[T any](dir string) (map[string]file, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	files := map[string]file{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ext {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var v T
		if err := decodeStrict(string(data), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var raw map[string]any
		if _, err := toml.Decode(string(data), &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		files[strings.TrimSuffix(e.Name(), ext)] = file{path: path, raw: raw}
	}
	return files, nil
}

func decodeStrict(data string, v any) error {
	md, err := toml.Decode(data, v)
	if err != nil {
		return err
	}

	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return fmt.Errorf("unknown fields: %s", strings.Join(names, ", "))
	}
	return nil
}

func flatten(files map[string]file, name string, chain []string) (map[string]any, error) {
	f := files[name]
	chain = append(chain, name)

	parent, _ := f.raw["extends"].(string)
	if parent == "" {
		return f.raw, nil
	}
	if slices.Contains(chain, parent) {
		return nil, fmt.Errorf("%s: extends cycle: %s", f.path, strings.Join(append(chain, parent), " -> "))
	}
	if _, ok := files[parent]; !ok {
		return nil, fmt.Errorf("%s: extends unknown recipe %q", f.path, parent)
	}

	base, err := flatten(files, parent, chain)
	if err != nil {
		return nil, err
	}
	return merge(base, f.raw), nil
}

// merge returns base overridden by over. Tables are merged key by key, other values are replaced.
func merge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = merge(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

func remarshal(raw map[string]any, v any) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return err
	}
	_, err := toml.Decode(buf.String(), v)
	return err
}
