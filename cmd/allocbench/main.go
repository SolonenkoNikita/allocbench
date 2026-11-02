package main

import (
	"os"

	"github.com/SolonenkoNikita/allocbench/internal/cli"
)

var version = "dev"

func main() {
	if err := cli.NewRootCommand(version).Execute(); err != nil {
		os.Exit(1)
	}
}
