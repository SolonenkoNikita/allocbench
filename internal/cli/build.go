package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/build"
	"github.com/SolonenkoNikita/allocbench/internal/env"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

var buildKinds = []string{"allocators", "benches", "tools"}

func newBuildCommand(opts *options) *cobra.Command {
	jobs := runtime.NumCPU()
	cmd := &cobra.Command{
		Use:   "build <recipe>...",
		Short: "Build allocators, benches or tools",
		Long: "Build recipes in a sandbox and put the results into the store.\n" +
			"A recipe is a name like mi or a kind and a name like allocators/mi.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			refs, err := resolveRefs(s, args)
			if err != nil {
				return err
			}
			b, err := newBuilder(opts, s, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			b.Jobs = jobs

			var results []build.Result
			for _, ref := range refs {
				r, err := b.Build(cmd.Context(), ref)
				if err != nil {
					return err
				}
				results = append(results, r)
			}
			return printBuilds(cmd.OutOrStdout(), results)
		},
	}
	cmd.Flags().IntVarP(&jobs, "jobs", "j", jobs, "parallel jobs inside a build ($JOBS)")
	return cmd
}

func newBuilder(opts *options, s *recipe.Set, progress io.Writer) (*build.Builder, error) {
	st, err := store.Open(opts.store)
	if err != nil {
		return nil, err
	}
	recipes, err := filepath.Abs(opts.recipes)
	if err != nil {
		return nil, err
	}
	b := &build.Builder{
		Set:      s,
		Store:    st,
		Sources:  &source.Fetcher{Store: st, Root: filepath.Dir(recipes)},
		Envs:     &env.Builder{Store: st, Mirror: opts.mirror, Proxy: opts.proxy},
		Progress: progress,
	}
	if opts.verbose {
		b.Output = progress
		b.Envs.Log = progress
	}
	return b, nil
}

func resolveRefs(s *recipe.Set, args []string) ([]string, error) {
	var refs []string
	for _, arg := range args {
		ref, err := resolveRef(s, arg, buildKinds)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func resolveRef(s *recipe.Set, arg string, kinds []string) (string, error) {
	has := func(kind, name string) bool {
		if !slices.Contains(kinds, kind) {
			return false
		}
		switch kind {
		case "envs":
			_, ok := s.Envs[name]
			return ok
		case "allocators":
			_, ok := s.Allocators[name]
			return ok
		case "benches":
			_, ok := s.Benches[name]
			return ok
		case "tools":
			_, ok := s.Tools[name]
			return ok
		}
		return false
	}

	if kind, name, ok := strings.Cut(arg, "/"); ok {
		if !has(kind, name) {
			return "", fmt.Errorf("%s: no such recipe", arg)
		}
		return arg, nil
	}

	var found []string
	for _, kind := range kinds {
		if has(kind, arg) {
			found = append(found, kind+"/"+arg)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s: no %s with this name", arg, describeKinds(kinds))
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%s is ambiguous, use one of: %s", arg, strings.Join(found, ", "))
}

func describeKinds(kinds []string) string {
	names := map[string]string{"allocators": "allocator", "benches": "bench", "tools": "tool", "envs": "env"}
	var out []string
	for _, k := range kinds {
		out = append(out, names[k])
	}
	if len(out) < 2 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}

func printBuilds(out io.Writer, results []build.Result) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RECIPE\tSTATUS\tHASH\tOUTPUT")
	for _, r := range results {
		status := "built"
		switch {
		case r.Hash == "":
			status = "nothing to build"
		case r.Cached:
			status = "cached"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Recipe, status, dash(short(r.Hash)), dash(r.Dir))
	}
	return w.Flush()
}

func proxyFromEnv() string {
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
