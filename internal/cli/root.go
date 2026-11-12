package cli

import "github.com/spf13/cobra"

type options struct {
	recipes string
	store   string
	mirror  string
	proxy   string
	verbose bool
}

func NewRootCommand(version string) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:           "allocbench",
		Short:         "Reproducible allocator benchmarks in isolated environments",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.recipes, "recipes", "recipes", "directory with recipes")
	flags.StringVar(&opts.store, "store", "store", "directory for sources, environments and builds")
	flags.StringVar(&opts.mirror, "mirror", "", "package mirror instead of the snapshot from the env recipe")
	flags.StringVar(&opts.proxy, "proxy", proxyFromEnv(), "HTTP proxy for package downloads")
	flags.BoolVarP(&opts.verbose, "verbose", "v", false, "show build and environment output")
	cmd.AddCommand(newListCommand(opts), newBuildCommand(opts), newShellCommand(opts), newRunCommand(opts))
	return cmd
}
