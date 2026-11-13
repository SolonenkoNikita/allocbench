package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SolonenkoNikita/allocbench/internal/doctor"
)

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine can build and run benchmarks",
		Long: "Check the tools and kernel settings allocbench needs and the conditions that make\n" +
			"measurements noisy. Errors mean allocbench cannot work, warnings come with a command\n" +
			"that fixes them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return printFindings(cmd.OutOrStdout(), doctor.New().Check())
		},
	}
}

func printFindings(w io.Writer, findings []doctor.Finding) error {
	levelWidth := len(doctor.Warning.String())
	checkWidth := 0
	for _, f := range findings {
		checkWidth = max(checkWidth, len(f.Check))
	}
	indent := strings.Repeat(" ", levelWidth+checkWidth+4)

	failed, warned := 0, 0
	for _, f := range findings {
		fmt.Fprintf(w, "%-*s  %-*s  %s\n", levelWidth, f.Level, checkWidth, f.Check, f.Detail)
		if f.Fix != "" {
			fmt.Fprintf(w, "%sfix: %s\n", indent, f.Fix)
		}
		switch f.Level {
		case doctor.Error:
			failed++
		case doctor.Warning:
			warned++
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed, allocbench cannot work on this machine", failed, len(findings))
	}
	if warned > 0 {
		fmt.Fprintf(w, "%d of %d checks gave warnings, measurements will be noisy\n", warned, len(findings))
		return nil
	}
	fmt.Fprintln(w, "the machine is ready")
	return nil
}
