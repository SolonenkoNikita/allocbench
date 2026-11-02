package cli

import "github.com/spf13/cobra"

func NewRootCommand(version string) *cobra.Command {
	return &cobra.Command{
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
}
