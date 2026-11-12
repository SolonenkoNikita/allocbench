package source

import (
	"io/fs"
	"os"
	"sort"
)

type entryKind string

const (
	kindFile    entryKind = "file"
	kindDir     entryKind = "directory"
	kindSymlink entryKind = "symlink"
)

type entry struct {
	kind   entryKind
	sum    string
	exec   bool
	target string
}

type Change struct {
	Path string
	What string
}

func (c Change) String() string {
	return c.Path + ": " + c.What
}

func DiffTrees(a, b string) ([]Change, error) {
	left, err := listTree(a)
	if err != nil {
		return nil, err
	}
	right, err := listTree(b)
	if err != nil {
		return nil, err
	}

	var changes []Change
	for path, l := range left {
		r, ok := right[path]
		if !ok {
			changes = append(changes, Change{path, "only in the first tree"})
			continue
		}
		if what := compare(l, r); what != "" {
			changes = append(changes, Change{path, what})
		}
	}
	for path := range right {
		if _, ok := left[path]; !ok {
			changes = append(changes, Change{path, "only in the second tree"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func compare(l, r entry) string {
	switch {
	case l.kind != r.kind:
		return string(l.kind) + " in the first tree, " + string(r.kind) + " in the second"
	case l.target != r.target:
		return "symlink targets differ: " + l.target + " and " + r.target
	case l.sum != r.sum:
		return "contents differ"
	case l.exec != r.exec:
		return "executable bits differ"
	}
	return ""
}

func listTree(dir string) (map[string]entry, error) {
	out := map[string]entry{}
	err := walk(dir, func(rel, path string, d fs.DirEntry) error {
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = entry{kind: kindSymlink, target: target}
		case d.IsDir():
			out[rel] = entry{kind: kindDir}
		default:
			sum, exec, err := fileHash(path)
			if err != nil {
				return err
			}
			out[rel] = entry{kind: kindFile, sum: sum, exec: exec}
		}
		return nil
	})
	return out, err
}
