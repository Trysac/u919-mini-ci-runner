// Command mini-ci-runner runs a YAML pipeline locally in containers.
package main

import (
	"io"
	"os"

	"github.com/Trysac/u919-mini-ci-runner"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes the CLI with the provided arguments and output streams.
func Run(args []string, stdout, stderr io.Writer) int {
	return minicirunner.RunCLI(args, stdout, stderr)
}
