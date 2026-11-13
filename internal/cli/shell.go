package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/build"
	"github.com/SolonenkoNikita/allocbench/internal/sandbox"
)

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

func newShellCommand(opts *options) *cobra.Command {
	jobs := runtime.NumCPU()
	var out string
	cmd := &cobra.Command{
		Use:   "shell <recipe> [-- command...]",
		Short: "Open a shell in the sandbox of a recipe",
		Long: "Open a shell in the same sandbox a recipe is built in: its environment, /src, /deps and /out.\n" +
			"The recipe may also be an env, then only the environment is mounted.\n" +
			"Without --out the /out directory is temporary and removed on exit.",
		Args: func(cmd *cobra.Command, args []string) error {
			n := len(args)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				n = dash
			}
			if n != 1 {
				return fmt.Errorf("expected one recipe before --, got %d", n)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadRecipes(opts.recipes)
			if err != nil {
				return err
			}
			ref, err := resolveRef(s, args[0], slices.Concat(buildKinds, []string{"envs"}))
			if err != nil {
				return err
			}
			b, err := newBuilder(opts, s, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			b.Jobs = jobs

			spec, cleanup, err := shellSpec(cmd.Context(), b, ref, out, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer cleanup()

			spec.Cmd = shellCommand(args[1:])
			spec.Interactive = isTerminal(os.Stdin)
			spec.Env["debian_chroot"] = ref
			if term := os.Getenv("TERM"); term != "" {
				spec.Env["TERM"] = term
			}

			c, err := b.Command(cmd.Context(), spec)
			if err != nil {
				return err
			}
			c.Stdin = cmd.InOrStdin()
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			err = c.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				cmd.SilenceErrors = true
				return &ExitError{Code: exit.ExitCode()}
			}
			return err
		},
	}
	cmd.Flags().IntVarP(&jobs, "jobs", "j", jobs, "value of $JOBS inside the shell")
	cmd.Flags().StringVar(&out, "out", "", "directory to mount as /out, kept after exit")
	return cmd
}

func shellSpec(ctx context.Context, b *build.Builder, ref, out string, progress io.Writer) (sandbox.Spec, func(), error) {
	if kind, name, _ := strings.Cut(ref, "/"); kind == "envs" {
		spec, err := b.EnvShell(ctx, name)
		return spec, func() {}, err
	}
	dir, cleanup, err := outDir(out)
	if err != nil {
		return sandbox.Spec{}, nil, err
	}
	spec, err := b.Shell(ctx, ref, dir)
	if err != nil {
		cleanup()
		return sandbox.Spec{}, nil, err
	}
	if out == "" {
		fmt.Fprintln(progress, "/out is temporary and removed on exit")
	} else {
		fmt.Fprintf(progress, "/out is %s\n", dir)
	}
	return spec, cleanup, nil
}

func shellCommand(args []string) []string {
	if len(args) == 0 {
		return []string{"/bin/bash"}
	}
	if filepath.IsAbs(args[0]) {
		return args
	}
	return append([]string{"/usr/bin/env"}, args...)
}

func outDir(out string) (string, func(), error) {
	if out != "" {
		dir, err := filepath.Abs(out)
		if err != nil {
			return "", nil, err
		}
		return dir, func() {}, os.MkdirAll(dir, 0o755)
	}
	dir, err := os.MkdirTemp("", "allocbench-shell-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
