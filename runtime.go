package minicirunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// StepSpec describes a single container command execution.
type StepSpec struct {
	Image   string
	Command string
	Workdir string
	Env     map[string]string
}

// Runtime executes a pipeline step inside an isolated environment.
type Runtime interface {
	Run(ctx context.Context, spec StepSpec, out io.Writer) (exitCode int, err error)
}

type dockerRuntime struct {
	binary string
}

// NewDockerRuntime returns a Runtime that executes steps using the given docker-compatible CLI binary.
//
// It invokes:
//
//	exec.CommandContext(ctx, binary, "run", "--rm", "--user", "UID:GID", "-v", workdir+":/workspace", "-w", "/workspace", "-e", "K=V"..., image, "sh", "-c", command)
//
// directly via an argv list without invoking a host shell. On POSIX hosts "--user" is the invoking
// user's IDs, so files a step writes into the workdir stay owned (and removable) by that user; on
// Windows, where user IDs are unavailable, it is omitted.
func NewDockerRuntime(binary string) Runtime {
	if binary == "" {
		binary = "docker"
	}
	return &dockerRuntime{binary: binary}
}

func (d *dockerRuntime) Run(ctx context.Context, spec StepSpec, out io.Writer) (int, error) {
	absWorkdir, err := filepath.Abs(spec.Workdir)
	if err != nil {
		return -1, newErrorf(ErrCodeRuntime, "failed to resolve workdir: %v", err)
	}
	args := []string{
		"run",
		"--rm",
	}
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 && gid >= 0 {
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, gid))
	}
	args = append(args,
		"-v", absWorkdir+":/workspace",
		"-w", "/workspace",
	)
	if len(spec.Env) > 0 {
		keys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, spec.Env[k]))
		}
	}
	args = append(args, spec.Image, "sh", "-c", spec.Command)

	cmd := exec.CommandContext(ctx, d.binary, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	err = cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, newErrorf(ErrCodeRuntime, "docker execution failed: %v", err)
	}
	return 0, nil
}

// FakeRuntime maps step command strings to in-memory handler functions for testing and documentation.
type FakeRuntime map[string]func(ctx context.Context, workdir string, out io.Writer) int

// Run executes the handler matching spec.Command, or matching "*" as a fallback.
// If no handler is registered, it returns a runtime error.
func (f FakeRuntime) Run(ctx context.Context, spec StepSpec, out io.Writer) (int, error) {
	if f == nil {
		return -1, newError(ErrCodeRuntime, "FakeRuntime has no handlers")
	}
	handler, ok := f[spec.Command]
	if !ok {
		handler, ok = f["*"]
	}
	if !ok {
		return -1, newErrorf(ErrCodeRuntime, "FakeRuntime has no handler for command %q", spec.Command)
	}
	code := handler(ctx, spec.Workdir, out)
	if ctx.Err() != nil {
		return code, ctx.Err()
	}
	return code, nil
}
