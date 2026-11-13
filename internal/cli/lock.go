package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/env"
	"github.com/SolonenkoNikita/allocbench/internal/lock"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
)

func newLockCommand(opts *options) *cobra.Command {
	var (
		check    bool
		lockPath string
	)
	cmd := &cobra.Command{
		Use:   "lock [recipe]...",
		Short: "Record or check the hashes of environments and builds",
		Long: "Build recipes and record what came out in allocbench.lock: the content hash of every\n" +
			"environment rootfs and of every build output. With --check compare instead of writing.\n" +
			"Without arguments every recipe is locked and recipes that are gone are dropped.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			builds, envs, err := lockTargets(s, args)
			if err != nil {
				return err
			}
			if lockPath == "" {
				recipes, err := filepath.Abs(opts.recipes)
				if err != nil {
					return err
				}
				lockPath = filepath.Join(filepath.Dir(recipes), lock.FileName)
			}

			got, warnings, err := collectLock(cmd.Context(), opts, s, cmd.ErrOrStderr(), builds, envs)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
			}
			out := cmd.OutOrStdout()

			existing, err := lock.Read(lockPath)
			missing := errors.Is(err, fs.ErrNotExist)
			if err != nil && !missing {
				return err
			}

			if check {
				if missing {
					return fmt.Errorf("%s not found, create it with allocbench lock", lockPath)
				}
				problems := existing.Check(got)
				for _, p := range problems {
					fmt.Fprintln(out, p)
				}
				if len(problems) > 0 {
					return fmt.Errorf("%d of %d entries do not match %s", len(problems), len(got.Envs)+len(got.Builds), lockPath)
				}
				fmt.Fprintf(out, "%s matches: %d envs, %d builds\n", lockPath, len(got.Envs), len(got.Builds))
				return nil
			}

			final := got
			if len(args) > 0 && !missing {
				existing.Merge(got)
				final = existing
			}
			if err := final.Write(lockPath); err != nil {
				return err
			}
			if err := printLock(out, got); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s\n", lockPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "compare with the lock file instead of writing it")
	cmd.Flags().StringVar(&lockPath, "lock", "", "lock file (default: allocbench.lock next to the recipes directory)")
	return cmd
}

func lockTargets(s *recipe.Set, args []string) ([]string, []string, error) {
	var builds, envs []string
	addEnv := func(name string) {
		if name != "" && !slices.Contains(envs, name) {
			envs = append(envs, name)
		}
	}

	if len(args) == 0 {
		for _, name := range keys(s.Allocators) {
			builds = append(builds, "allocators/"+name)
		}
		for _, name := range keys(s.Benches) {
			builds = append(builds, "benches/"+name)
		}
		for _, name := range keys(s.Tools) {
			builds = append(builds, "tools/"+name)
		}
		for _, name := range keys(s.Envs) {
			addEnv(name)
		}
		return builds, envs, nil
	}

	kinds := slices.Concat(buildKinds, []string{"envs"})
	for _, arg := range args {
		ref, err := resolveRef(s, arg, kinds)
		if err != nil {
			return nil, nil, err
		}
		kind, name, _ := strings.Cut(ref, "/")
		if kind == "envs" {
			addEnv(name)
			continue
		}
		if !slices.Contains(builds, ref) {
			builds = append(builds, ref)
		}
		addEnv(buildEnvOf(s, kind, name))
	}
	return builds, envs, nil
}

func buildEnvOf(s *recipe.Set, kind, name string) string {
	switch kind {
	case "allocators":
		return s.Allocators[name].BuildEnv
	case "benches":
		return s.Benches[name].BuildEnv
	case "tools":
		return s.Tools[name].BuildEnv
	}
	return ""
}

func collectLock(ctx context.Context, opts *options, s *recipe.Set, progress io.Writer, builds, envs []string) (*lock.File, []string, error) {
	b, err := newBuilder(opts, s, progress)
	if err != nil {
		return nil, nil, err
	}
	got := lock.New()
	var warnings []string
	for _, ref := range builds {
		r, err := b.Build(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		if r.Hash == "" {
			continue
		}
		out, err := source.TreeHash(r.Dir)
		if err != nil {
			return nil, nil, err
		}
		got.Builds[ref] = lock.Build{Hash: r.Hash, Out: out}
	}
	for _, name := range envs {
		e, err := b.Env(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		got.Envs[name] = lock.Env{Hash: e.Hash, Tree: e.Tree}
		if want := env.SnapshotMirror(s.Envs[name].Snapshot); e.Mirror != want {
			warnings = append(warnings, fmt.Sprintf("envs/%s was created from %s, not from the snapshot, other machines may get a different rootfs", name, e.Mirror))
		}
	}
	return got, warnings, nil
}

func printLock(w io.Writer, f *lock.File) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPE\tHASH\tCONTENT")
	for _, name := range keys(f.Envs) {
		e := f.Envs[name]
		fmt.Fprintf(tw, "envs/%s\t%s\t%s\n", name, short(e.Hash), short(e.Tree))
	}
	for _, ref := range keys(f.Builds) {
		b := f.Builds[ref]
		fmt.Fprintf(tw, "%s\t%s\t%s\n", ref, short(b.Hash), short(b.Out))
	}
	return tw.Flush()
}
