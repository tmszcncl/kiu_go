package main

import (
	"os"

	"github.com/tmszcncl/kiu_go/internal/cli"
	"github.com/tmszcncl/kiu_go/internal/process"
)

var version = "dev"

func main() {
	if internal, code := process.Internal(os.Args[1:]); internal {
		os.Exit(code)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version))
}
