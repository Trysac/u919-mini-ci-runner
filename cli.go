package minicirunner

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RunCLI executes the mini-ci-runner command-line interface.
//
// Usage:
//
//	mini-ci-runner run <pipeline.yml> -source . -out .ci [-parallel 4] [-docker docker]
//
// Exit codes:
//   - 0: all jobs succeeded
//   - 1: one or more jobs did not succeed
//   - 2: usage, parse, or I/O error
func RunCLI(args []string, stdout, stderr io.Writer) int {
	return runCLIWithRuntime(context.Background(), args, stdout, stderr, nil)
}

func runCLIWithRuntime(ctx context.Context, args []string, stdout, stderr io.Writer, overrideRuntime Runtime) int {
	if len(args) < 2 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: mini-ci-runner run <pipeline.yml> [-source .] [-out .ci] [-parallel 4] [-docker docker]")
		return 2
	}

	// Support flags placed either before or after <pipeline.yml>.
	var pipelinePath string
	var flagArgs []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if pipelinePath == "" && len(a) > 0 && a[0] != '-' {
			pipelinePath = a
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	if pipelinePath == "" {
		fmt.Fprintln(stderr, "error: missing pipeline YAML path")
		return 2
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sourceDir := fs.String("source", ".", "source directory")
	outDir := fs.String("out", ".ci", "output directory for workspaces, cache, and artifacts")
	parallel := fs.Int("parallel", DefaultMaxParallel, "maximum parallel jobs")
	dockerBin := fs.String("docker", "docker", "docker binary")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected arguments: %v\n", fs.Args())
		return 2
	}
	if *parallel <= 0 {
		fmt.Fprintln(stderr, "error: -parallel must be >= 1")
		return 2
	}

	data, err := os.ReadFile(pipelinePath)
	if err != nil {
		fmt.Fprintf(stderr, "error reading %s: %v\n", pipelinePath, err)
		return 2
	}
	p, err := Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "error parsing %s: %v\n", pipelinePath, err)
		return 2
	}

	rt := overrideRuntime
	if rt == nil {
		rt = NewDockerRuntime(*dockerBin)
	}

	opts := Options{
		Runtime:      rt,
		SourceDir:    *sourceDir,
		WorkDir:      filepath.Join(*outDir, "work"),
		CacheDir:     filepath.Join(*outDir, "cache"),
		ArtifactsDir: filepath.Join(*outDir, "artifacts"),
		MaxParallel:  *parallel,
		Logs: func(l LogLine) {
			fmt.Fprintf(stdout, "[%s] %s\n", l.Job, l.Text)
		},
	}

	rep, err := Run(ctx, p, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error running pipeline: %v\n", err)
		return 2
	}

	fmt.Fprintln(stdout, "JOB\tSTATUS\tEXIT\tDURATION")
	for _, j := range rep.Jobs {
		fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\n", j.ID, j.Status, j.ExitCode, j.Duration.Round(1e6))
	}
	if !rep.OK {
		return 1
	}
	return 0
}
