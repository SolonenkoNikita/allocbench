package main

import (
	"errors"
	"os"

	"github.com/SolonenkoNikita/allocbench/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := cli.InterruptContext(os.Stderr)
	err := cli.NewRootCommand(version).ExecuteContext(ctx)
	interrupted := ctx.Err() != nil
	stop()
	if err == nil {
		return
	}
	var exit *cli.ExitError
	switch {
	case interrupted:
		os.Exit(cli.ExitInterrupted)
	case errors.As(err, &exit):
		os.Exit(exit.Code)
	}
	os.Exit(1)
}
