package cli

import "github.com/spf13/cobra"

type options struct {
	recipes string
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
	cmd.PersistentFlags().StringVar(&opts.recipes, "recipes", "recipes", "directory with recipes")
	cmd.AddCommand(newListCommand(opts))
	return cmd
}
