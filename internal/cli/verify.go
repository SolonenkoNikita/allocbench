package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/build"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

const maxShownChanges = 20

type verdict struct {
	ref     string
	hash    string
	out     string
	changes []source.Change
	vsStore []source.Change
	kept    string
	nothing bool
	inStore bool
}

func (v verdict) ok() bool {
	return len(v.changes) == 0 && len(v.vsStore) == 0
}

func newVerifyCommand(opts *options) *cobra.Command {
	jobs := runtime.NumCPU()
	var keep bool
	cmd := &cobra.Command{
		Use:   "verify [recipe]...",
		Short: "Build recipes twice and check that the outputs are identical",
		Long: "Build every recipe twice from scratch, the second time with a different $JOBS, and compare\n" +
			"the outputs file by file. If the store already has the build, compare with it too.\n" +
			"Without arguments every allocator, bench and tool is verified.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			refs, err := verifyTargets(s, args)
			if err != nil {
				return err
			}
			b, err := newBuilder(opts, s, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			b.Jobs = jobs

			out := cmd.OutOrStdout()
			failed := 0
			for _, ref := range refs {
				v, err := verify(cmd.Context(), b, ref, jobs, keep)
				if err != nil {
					return err
				}
				printVerdict(out, v)
				if !v.ok() {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d recipes are not reproducible", failed, len(refs))
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&jobs, "jobs", "j", jobs, "$JOBS of the first build, the second one uses another value")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep both outputs of a recipe that is not reproducible")
	return cmd
}

func verifyTargets(s *recipe.Set, args []string) ([]string, error) {
	if len(args) > 0 {
		return resolveRefs(s, args)
	}
	var refs []string
	for _, name := range keys(s.Allocators) {
		refs = append(refs, "allocators/"+name)
	}
	for _, name := range keys(s.Benches) {
		refs = append(refs, "benches/"+name)
	}
	for _, name := range keys(s.Tools) {
		refs = append(refs, "tools/"+name)
	}
	return slices.Compact(refs), nil
}

func otherJobs(jobs int) int {
	if jobs > 1 {
		return 1
	}
	return 2
}

func verify(ctx context.Context, b *build.Builder, ref string, jobs int, keep bool) (verdict, error) {
	tmp, err := os.MkdirTemp("", "allocbench-verify-")
	if err != nil {
		return verdict{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(tmp)
		}
	}()

	var dirs [2]string
	var results [2]build.Result
	for i, j := range []int{jobs, otherJobs(jobs)} {
		dirs[i] = filepath.Join(tmp, fmt.Sprintf("build-%d", i+1))
		if err := os.Mkdir(dirs[i], 0o755); err != nil {
			return verdict{}, err
		}
		results[i], err = b.BuildTo(ctx, ref, dirs[i], filepath.Join(tmp, fmt.Sprintf("build-%d.log", i+1)), j)
		if err != nil {
			return verdict{}, err
		}
	}

	v := verdict{ref: ref, hash: results[0].Hash}
	if v.hash == "" {
		v.nothing = true
		return v, nil
	}
	if v.changes, err = source.DiffTrees(dirs[0], dirs[1]); err != nil {
		return verdict{}, err
	}
	if v.out, err = source.TreeHash(dirs[0]); err != nil {
		return verdict{}, err
	}
	if b.Store.Has(store.Build, v.hash) {
		v.inStore = true
		if v.vsStore, err = source.DiffTrees(dirs[0], b.Store.Path(store.Build, v.hash)); err != nil {
			return verdict{}, err
		}
	}
	if !v.ok() && keep {
		cleanup = false
		v.kept = tmp
	}
	return v, nil
}

func printVerdict(w io.Writer, v verdict) {
	switch {
	case v.nothing:
		fmt.Fprintf(w, "%s: nothing to build\n", v.ref)
		return
	case v.ok():
		store := ""
		if v.inStore {
			store = ", same as the store"
		}
		fmt.Fprintf(w, "%s: reproducible, out %s%s\n", v.ref, short(v.out), store)
		return
	}

	fmt.Fprintf(w, "%s: NOT reproducible\n", v.ref)
	if len(v.changes) > 0 {
		fmt.Fprintf(w, "  two fresh builds differ in %d paths:\n", len(v.changes))
		printChanges(w, v.changes)
	}
	if len(v.vsStore) > 0 {
		fmt.Fprintf(w, "  a fresh build differs from the store in %d paths:\n", len(v.vsStore))
		printChanges(w, v.vsStore)
	}
	if v.kept != "" {
		fmt.Fprintf(w, "  outputs kept in %s\n", v.kept)
	}
}

func printChanges(w io.Writer, changes []source.Change) {
	for i, c := range changes {
		if i == maxShownChanges {
			fmt.Fprintf(w, "    … and %d more\n", len(changes)-maxShownChanges)
			return
		}
		fmt.Fprintf(w, "    %s\n", c)
	}
}
