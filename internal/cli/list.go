package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
)

const shortHash = 12

func newListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recipes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			return printRecipes(cmd.OutOrStdout(), s)
		},
	}
}

func loadRecipes(dir string) (*recipe.Set, error) {
	s, err := recipe.Load(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("recipes directory %q not found, set it with --recipes", dir)
	}
	if err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("invalid recipes:\n%w", err)
	}
	return s, nil
}

type row struct {
	kind, name, version, env, source, hash string
}

func printRecipes(out io.Writer, s *recipe.Set) error {
	var rows []row

	for _, name := range keys(s.Envs) {
		e := s.Envs[name]
		h, err := s.EnvHash(name)
		if err != nil {
			return err
		}
		rows = append(rows, row{"env", name, "", "", e.Distro + " " + e.Suite + " " + e.Snapshot, h})
	}
	for _, name := range keys(s.Allocators) {
		a := s.Allocators[name]
		h, err := s.AllocatorHash(name)
		if err != nil {
			return err
		}
		rows = append(rows, row{"allocator", name, a.Version, a.BuildEnv, describeSource(a.Source), h})
	}
	for _, name := range keys(s.Benches) {
		b := s.Benches[name]
		h, err := s.BenchHash(name)
		if err != nil {
			return err
		}
		rows = append(rows, row{"bench", name, "", b.BuildEnv, describeSource(b.Source), h})
	}
	for _, name := range keys(s.Tools) {
		t := s.Tools[name]
		h, err := s.ToolHash(name)
		if err != nil {
			return err
		}
		rows = append(rows, row{"tool", name, "", t.BuildEnv, describeSource(t.Source), h})
	}

	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no recipes found")
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAME\tVERSION\tBUILD ENV\tSOURCE\tHASH")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.kind, r.name, dash(r.version), dash(r.env), dash(r.source), dash(short(r.hash)))
	}
	return w.Flush()
}

func describeSource(src recipe.Source) string {
	switch {
	case src.Git != "":
		return "git " + short(src.Rev)
	case src.URL != "":
		return "url " + src.URL
	case src.Path != "":
		return "path " + src.Path
	}
	return ""
}

func short(h string) string {
	if len(h) > shortHash {
		return h[:shortHash]
	}
	return h
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
