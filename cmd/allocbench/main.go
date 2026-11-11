package main

import (
	"errors"
	"os"

	"github.com/SolonenkoNikita/allocbench/internal/cli"
)

var version = "dev"

func main() {
	if err := cli.NewRootCommand(version).Execute(); err != nil {
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
