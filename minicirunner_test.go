package minicirunner_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trysac/u919-mini-ci-runner"
)

func assertErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", wantCode)
	}
	var e *minicirunner.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *minicirunner.Error, got %T (%v)", err, err)
	}
	if e.Code != wantCode {
		t.Fatalf("error code = %q, want %q (message: %s)", e.Code, wantCode, e.Message)
	}
}

func TestAcceptanceMatrixExpansion(t *testing.T) {
	yml := []byte(`version: 1
jobs:
  test:
    image: alpine:3
    matrix:
      os:
        - linux
      go:
        - "1.22"
        - "1.23"
    steps:
      - name: test
        run: go test ./...
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(p.Jobs) != 2 {
		t.Fatalf("len(p.Jobs) = %d, want 2", len(p.Jobs))
	}
	wantIDs := []string{
		"test (go=1.22, os=linux)",
		"test (go=1.23, os=linux)",
	}
	for i, want := range wantIDs {
		if p.Jobs[i].ID != want {
			t.Fatalf("p.Jobs[%d].ID = %q, want %q", i, p.Jobs[i].ID, want)
		}
	}
}

func TestAcceptanceSchedulingOrderAndReport(t *testing.T) {
	yml := []byte(`version: 1
jobs:
  build:
    image: alpine:3
    steps:
      - name: build
        run: build-cmd
  test:
    image: alpine:3
    needs:
      - build
    matrix:
      go:
        - "1.22"
        - "1.23"
      os:
        - linux
    steps:
      - name: test
        run: test-cmd
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	var mu sync.Mutex
	var startOrder []string
	fake := minicirunner.FakeRuntime{
		"build-cmd": func(ctx context.Context, workdir string, out io.Writer) int {
			mu.Lock()
			startOrder = append(startOrder, "build")
			mu.Unlock()
			return 0
		},
		"test-cmd": func(ctx context.Context, workdir string, out io.Writer) int {
			mu.Lock()
			startOrder = append(startOrder, "test")
			mu.Unlock()
			return 0
		},
	}

	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:   fake,
		SourceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !rep.OK {
		t.Fatalf("Report.OK = false, want true")
	}
	if len(startOrder) != 3 || startOrder[0] != "build" {
		t.Fatalf("startOrder = %v, want build first", startOrder)
	}
	wantIDs := []string{
		"build",
		"test (go=1.22, os=linux)",
		"test (go=1.23, os=linux)",
	}
	if len(rep.Jobs) != 3 {
		t.Fatalf("len(rep.Jobs) = %d, want 3", len(rep.Jobs))
	}
	for i, want := range wantIDs {
		if rep.Jobs[i].ID != want {
			t.Fatalf("rep.Jobs[%d].ID = %q, want %q", i, rep.Jobs[i].ID, want)
		}
		if rep.Jobs[i].Status != "success" {
			t.Fatalf("rep.Jobs[%d].Status = %q, want \"success\"", i, rep.Jobs[i].Status)
		}
	}
}

func TestAcceptanceFailureSkipsDependentsAndCLIExit1(t *testing.T) {
	yml := `version: 1
jobs:
  build:
    image: alpine:3
    steps:
      - name: build
        run: fail-build
  test:
    image: alpine:3
    needs:
      - build
    matrix:
      go:
        - "1.22"
        - "1.23"
    steps:
      - name: test
        run: run-test
`
	p, err := minicirunner.Parse([]byte(yml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	fake := minicirunner.FakeRuntime{
		"fail-build": func(ctx context.Context, workdir string, out io.Writer) int {
			return 1
		},
		"run-test": func(ctx context.Context, workdir string, out io.Writer) int {
			t.Errorf("test step should not run when build fails")
			return 0
		},
	}

	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:   fake,
		SourceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if rep.OK {
		t.Fatalf("Report.OK = true, want false")
	}
	statusMap := map[string]string{}
	for _, j := range rep.Jobs {
		statusMap[j.ID] = j.Status
	}
	if statusMap["build"] != "failure" {
		t.Fatalf("build status = %q, want failure", statusMap["build"])
	}
	if statusMap["test (go=1.22)"] != "skipped" || statusMap["test (go=1.23)"] != "skipped" {
		t.Fatalf("test leg statuses = %v, want skipped", statusMap)
	}

	// Verify CLI returns 1 when a job fails.
	tmpDir := t.TempDir()
	pipelineFile := filepath.Join(tmpDir, "pipeline.yml")
	if err := os.WriteFile(pipelineFile, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a fake docker script that exits 1.
	fakeDocker := filepath.Join(tmpDir, "fake-docker")
	if runtime.GOOS == "windows" {
		fakeDocker += ".bat"
		if err := os.WriteFile(fakeDocker, []byte("@exit /b 1\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	exitCode := minicirunner.RunCLI([]string{"run", pipelineFile, "-source", tmpDir, "-out", filepath.Join(tmpDir, ".ci"), "-docker", fakeDocker}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("RunCLI exitCode = %d, want 1 (stderr: %s)", exitCode, stderr.String())
	}
}

func TestAcceptanceCacheHashFilesSaveAndRestore(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.sum"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	yml := []byte(`version: 1
jobs:
  build:
    image: alpine:3
    matrix:
      go:
        - "1.22"
    cache:
      key: "go-${{ matrix.go }}-${{ hashFiles(\"go.sum\") }}"
      paths:
        - .cache
    steps:
      - name: cache-step
        run: cache-cmd
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	cacheDir := t.TempDir()
	wantKey := "go-1.22-bdb25f13a1871b9523d4dc25e421c83cd5c93c270b1f83bd7bf17e29057b90ac" // gitleaks:allow

	// First run: cache miss, creates .cache/data.txt, saves cache after success.
	var events1 []minicirunner.Event
	run1 := minicirunner.FakeRuntime{
		"cache-cmd": func(ctx context.Context, workdir string, out io.Writer) int {
			cDir := filepath.Join(workdir, ".cache")
			_ = os.MkdirAll(cDir, 0o755)
			_ = os.WriteFile(filepath.Join(cDir, "data.txt"), []byte("cached-payload"), 0o644)
			return 0
		},
	}
	rep1, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:   run1,
		SourceDir: srcDir,
		CacheDir:  cacheDir,
		OnEvent: func(e minicirunner.Event) {
			events1 = append(events1, e)
		},
	})
	if err != nil || !rep1.OK {
		t.Fatalf("first Run failed: err=%v, ok=%v", err, rep1 != nil && rep1.OK)
	}

	foundSaved := false
	for _, ev := range events1 {
		if ev.Kind == "cache_saved" && ev.Detail == wantKey {
			foundSaved = true
		}
	}
	if !foundSaved {
		t.Fatalf("expected cache_saved event with key %q, got %+v", wantKey, events1)
	}

	// Second run: restores cache before step runs and emits cache_hit.
	var events2 []minicirunner.Event
	restoredContent := ""
	run2 := minicirunner.FakeRuntime{
		"cache-cmd": func(ctx context.Context, workdir string, out io.Writer) int {
			b, err := os.ReadFile(filepath.Join(workdir, ".cache", "data.txt"))
			if err != nil {
				return 1
			}
			restoredContent = string(b)
			// Mutate file in workspace to verify cache immutability.
			_ = os.WriteFile(filepath.Join(workdir, ".cache", "data.txt"), []byte("mutated"), 0o644)
			return 0
		},
	}
	rep2, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:   run2,
		SourceDir: srcDir,
		CacheDir:  cacheDir,
		OnEvent: func(e minicirunner.Event) {
			events2 = append(events2, e)
		},
	})
	if err != nil || !rep2.OK {
		t.Fatalf("second Run failed: err=%v, ok=%v", err, rep2 != nil && rep2.OK)
	}
	if restoredContent != "cached-payload" {
		t.Fatalf("restoredContent = %q, want %q", restoredContent, "cached-payload")
	}

	foundHit := false
	for _, ev := range events2 {
		if ev.Kind == "cache_hit" && ev.Detail == wantKey {
			foundHit = true
		}
		if ev.Kind == "cache_saved" {
			t.Fatalf("unexpected cache_saved on second run (caches are immutable)")
		}
	}
	if !foundHit {
		t.Fatalf("expected cache_hit event with key %q, got %+v", wantKey, events2)
	}
}

func TestAcceptanceArtifactsAndInvalidPaths(t *testing.T) {
	yml := []byte(`version: 1
jobs:
  build:
    image: alpine:3
    steps:
      - name: produce
        run: make-artifact
    artifacts:
      - dist/app.txt
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	artDir := t.TempDir()
	fake := minicirunner.FakeRuntime{
		"make-artifact": func(ctx context.Context, workdir string, out io.Writer) int {
			d := filepath.Join(workdir, "dist")
			_ = os.MkdirAll(d, 0o755)
			_ = os.WriteFile(filepath.Join(d, "app.txt"), []byte("hello-artifact"), 0o644)
			return 0
		},
	}
	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:      fake,
		SourceDir:    t.TempDir(),
		ArtifactsDir: artDir,
	})
	if err != nil || !rep.OK {
		t.Fatalf("Run failed: err=%v, ok=%v", err, rep != nil && rep.OK)
	}
	gotBytes, err := os.ReadFile(filepath.Join(artDir, "build", "dist", "app.txt"))
	if err != nil {
		t.Fatalf("failed reading copied artifact: %v", err)
	}
	if string(gotBytes) != "hello-artifact" {
		t.Fatalf("artifact content = %q, want %q", string(gotBytes), "hello-artifact")
	}

	// Invalid paths in artifacts and cache.paths.
	badPaths := []string{"../x", "/etc", "a/../b", "./foo", ""}
	for _, bp := range badPaths {
		artYML := fmt.Sprintf("version: 1\njobs:\n  b:\n    image: alpine\n    steps:\n      - name: s\n        run: echo\n    artifacts:\n      - %q\n", bp)
		_, err := minicirunner.Parse([]byte(artYML))
		assertErrorCode(t, err, "invalid_path")

		cacheYML := fmt.Sprintf("version: 1\njobs:\n  b:\n    image: alpine\n    cache:\n      key: k\n      paths:\n        - %q\n    steps:\n      - name: s\n        run: echo\n", bp)
		_, err = minicirunner.Parse([]byte(cacheYML))
		assertErrorCode(t, err, "invalid_path")
	}
}

func TestAcceptanceParseErrorCodesAndLimits(t *testing.T) {
	// 1. Cycle between a and b
	cycleYML := []byte(`version: 1
jobs:
  a:
    image: alpine
    needs: [b]
    steps:
      - name: s
        run: echo
  b:
    image: alpine
    needs: [a]
    steps:
      - name: s
        run: echo
`)
	_, err := minicirunner.Parse(cycleYML)
	assertErrorCode(t, err, "cycle")

	// 2. Unknown job in needs
	unknownYML := []byte(`version: 1
jobs:
  a:
    image: alpine
    needs: [missing]
    steps:
      - name: s
        run: echo
`)
	_, err = minicirunner.Parse(unknownYML)
	assertErrorCode(t, err, "unknown_job")

	// 3. Matrix too large (257 combinations)
	var items strings.Builder
	for i := 0; i < 257; i++ {
		fmt.Fprintf(&items, "        - v%d\n", i)
	}
	matrixYML := []byte("version: 1\njobs:\n  a:\n    image: alpine\n    matrix:\n      k:\n" + items.String() + "    steps:\n      - name: s\n        run: echo\n")
	_, err = minicirunner.Parse(matrixYML)
	assertErrorCode(t, err, "matrix_too_large")

	// 4. YAML alias refused
	aliasYML := []byte(`version: 1
jobs:
  a: &jobdef
    image: alpine
    steps:
      - name: s
        run: echo
  b: *jobdef
`)
	_, err = minicirunner.Parse(aliasYML)
	assertErrorCode(t, err, "aliases_not_allowed")

	// 5. Input size boundary: 1_048_576 parses, 1_048_577 returns input_too_large
	prefix := "version: 1\njobs:\n  a:\n    image: alpine\n    steps:\n      - name: s\n        run: "
	padLen := 1_048_576 - len(prefix) - 1
	exact1MiB := []byte(prefix + strings.Repeat("a", padLen) + "\n")
	if len(exact1MiB) != 1_048_576 {
		t.Fatalf("len(exact1MiB) = %d, want 1048576", len(exact1MiB))
	}
	if _, err := minicirunner.Parse(exact1MiB); err != nil {
		t.Fatalf("expected 1_048_576 bytes to parse, got: %v", err)
	}
	over1MiB := append(append([]byte{}, exact1MiB...), 'a')
	if len(over1MiB) != 1_048_577 {
		t.Fatalf("len(over1MiB) = %d, want 1048577", len(over1MiB))
	}
	_, err = minicirunner.Parse(over1MiB)
	assertErrorCode(t, err, "input_too_large")

	// 6. YAML depth > 32 returns too_deep
	var deep strings.Builder
	for i := 0; i < 35; i++ {
		deep.WriteString(strings.Repeat("  ", i) + "k:\n")
	}
	_, err = minicirunner.Parse([]byte(deep.String()))
	assertErrorCode(t, err, "too_deep")

	// 7. Invalid timeout
	badTimeoutYML := []byte(`version: 1
jobs:
  a:
    image: alpine
    timeout: 48h
    steps:
      - name: s
        run: echo
`)
	_, err = minicirunner.Parse(badTimeoutYML)
	assertErrorCode(t, err, "invalid_timeout")

	// 8. Unknown field rejected
	unknownFieldYML := []byte(`version: 1
jobs:
  a:
    image: alpine
    bogus: 123
    steps:
      - name: s
        run: echo
`)
	_, err = minicirunner.Parse(unknownFieldYML)
	assertErrorCode(t, err, "parse")
}

func TestAcceptanceTimeoutAndCancellation(t *testing.T) {
	// Timeout 20ms test
	yml := []byte(`version: 1
jobs:
  slow:
    image: alpine:3
    timeout: "20ms"
    steps:
      - name: wait
        run: block
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	fake := minicirunner.FakeRuntime{
		"block": func(ctx context.Context, workdir string, out io.Writer) int {
			<-ctx.Done()
			return -1
		},
	}
	start := time.Now()
	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:   fake,
		SourceDir: t.TempDir(),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("Run took %v, want < 1s", elapsed)
	}
	if len(rep.Jobs) != 1 || rep.Jobs[0].Status != "timed_out" {
		t.Fatalf("got job result %+v, want Status \"timed_out\"", rep.Jobs)
	}

	// Cancellation test
	cancelYML := []byte(`version: 1
jobs:
  running:
    image: alpine:3
    steps:
      - name: wait
        run: block-cancel
`)
	pCancel, err := minicirunner.Parse(cancelYML)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	fakeCancel := minicirunner.FakeRuntime{
		"block-cancel": func(stepCtx context.Context, workdir string, out io.Writer) int {
			close(started)
			<-stepCtx.Done()
			return -1
		},
	}
	go func() {
		<-started
		cancel()
	}()
	repCancel, err := minicirunner.Run(ctx, pCancel, minicirunner.Options{
		Runtime:   fakeCancel,
		SourceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(repCancel.Jobs) != 1 || repCancel.Jobs[0].Status != "cancelled" {
		t.Fatalf("got job result %+v, want Status \"cancelled\"", repCancel.Jobs)
	}
}

func TestAcceptanceLiveLogsAndTruncation(t *testing.T) {
	yml := []byte(`version: 1
jobs:
  build:
    image: alpine:3
    steps:
      - name: log
        run: log-cmd
`)
	p, err := minicirunner.Parse(yml)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	var lines []minicirunner.LogLine
	fake := minicirunner.FakeRuntime{
		"log-cmd": func(ctx context.Context, workdir string, out io.Writer) int {
			_, _ = io.WriteString(out, "hello\n")
			// Write more than 1024 bytes.
			_, _ = io.WriteString(out, strings.Repeat("x", 1500)+"\n")
			_, _ = io.WriteString(out, "after-truncation\n")
			return 0
		},
	}
	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:     fake,
		SourceDir:   t.TempDir(),
		MaxLogBytes: 1024,
		Logs: func(l minicirunner.LogLine) {
			lines = append(lines, l)
		},
	})
	if err != nil || !rep.OK {
		t.Fatalf("Run failed: err=%v, ok=%v", err, rep != nil && rep.OK)
	}
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 log lines, got %+v", lines)
	}
	if lines[0].Job != "build" || lines[0].Text != "hello" {
		t.Fatalf("first log line = %+v, want {Job:build Text:hello}", lines[0])
	}
	last := lines[len(lines)-1]
	if last.Job != "build" || last.Text != "[log truncated]" {
		t.Fatalf("last log line = %+v, want {Job:build Text:[log truncated]}", last)
	}
}

func TestExamplePipelineWithFakeRuntime(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "example.yml"))
	if err != nil {
		t.Fatalf("ReadFile testdata/example.yml: %v", err)
	}
	p, err := minicirunner.Parse(data)
	if err != nil {
		t.Fatalf("Parse example.yml: %v", err)
	}
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.sum"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := minicirunner.FakeRuntime{
		"*": func(ctx context.Context, workdir string, out io.Writer) int {
			_ = os.MkdirAll(filepath.Join(workdir, ".cache"), 0o755)
			_ = os.WriteFile(filepath.Join(workdir, ".cache", "state.txt"), []byte("cached\n"), 0o644)
			_ = os.MkdirAll(filepath.Join(workdir, "dist"), 0o755)
			_ = os.WriteFile(filepath.Join(workdir, "dist", "app.txt"), []byte("binary\n"), 0o644)
			_, _ = io.WriteString(out, "ok\n")
			return 0
		},
	}
	artDir := t.TempDir()
	cacheDir := t.TempDir()
	rep, err := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime:      fake,
		SourceDir:    srcDir,
		CacheDir:     cacheDir,
		ArtifactsDir: artDir,
	})
	if err != nil || !rep.OK {
		t.Fatalf("Run example.yml failed: err=%v, ok=%v", err, rep != nil && rep.OK)
	}
	if _, err := os.Stat(filepath.Join(artDir, "build", "dist", "app.txt")); err != nil {
		t.Fatalf("expected artifact dist/app.txt: %v", err)
	}
}

func TestDockerRuntimeMockBinaryAndCLI(t *testing.T) {
	tmpDir := t.TempDir()
	fakeDocker := filepath.Join(tmpDir, "mock-docker")
	if runtime.GOOS == "windows" {
		fakeDocker += ".bat"
		// On Windows, NewDockerRuntime passes:
		// "run" "--rm" "-v" ... "-w" ... "-e" ... image "sh" "-c" command
		// The command is the last argument %* or echo %~1. In batch:
		// We execute the last argument in sh or cmd.
		// For our test, run is "echo hello".
		script := `@echo off
setlocal enabledelayedexpansion
for %%a in (%*) do set last=%%a
:: strip quotes if present
if defined last (
    set "last=!last:"=!"
    !last!
)
`
		if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		script := `#!/bin/sh
# The last argument is the command passed to sh -c
for last; do :; done
sh -c "$last"
`
		if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.sum"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pipelinePath := filepath.Join(tmpDir, "ci.yml")
	yml := `version: 1
jobs:
  build:
    image: busybox
    env:
      GREETING: world
    steps:
      - name: greet
        run: echo hello
`
	if err := os.WriteFile(pipelinePath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := minicirunner.RunCLI([]string{"run", pipelinePath, "-source", srcDir, "-out", filepath.Join(tmpDir, "out"), "-parallel", "2", "-docker", fakeDocker},
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("RunCLI exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[build] hello") {
		t.Fatalf("stdout missing log line: %s", stdout.String())
	}

	// CLI error cases
	if c := minicirunner.RunCLI([]string{"help"}, &stdout, &stderr); c != 2 {
		t.Fatalf("want exit 2 for bad subcommand, got %d", c)
	}
	if c := minicirunner.RunCLI([]string{"run", "-parallel", "0", pipelinePath}, &stdout, &stderr); c != 2 {
		t.Fatalf("want exit 2 for invalid -parallel, got %d", c)
	}
	if c := minicirunner.RunCLI([]string{"run", filepath.Join(tmpDir, "nonexistent.yml")}, &stdout, &stderr); c != 2 {
		t.Fatalf("want exit 2 for missing file, got %d", c)
	}
}

func TestExpressionAndValidationEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		code string
	}{
		{
			name: "wrong version",
			yml:  "version: 2\njobs:\n  a:\n    image: x\n    steps:\n      - name: s\n        run: echo\n",
			code: "parse",
		},
		{
			name: "no jobs",
			yml:  "version: 1\njobs: {}\n",
			code: "parse",
		},
		{
			name: "missing image",
			yml:  "version: 1\njobs:\n  a:\n    steps:\n      - name: s\n        run: echo\n",
			code: "parse",
		},
		{
			name: "no steps",
			yml:  "version: 1\njobs:\n  a:\n    image: alpine\n    steps: []\n",
			code: "parse",
		},
		{
			name: "unknown matrix var in expression",
			yml:  "version: 1\njobs:\n  a:\n    image: alpine\n    steps:\n      - name: s\n        run: echo ${{ matrix.missing }}\n",
			code: "parse",
		},
		{
			name: "unsupported expression syntax",
			yml:  "version: 1\njobs:\n  a:\n    image: alpine\n    steps:\n      - name: s\n        run: echo ${{ secrets.TOKEN }}\n",
			code: "parse",
		},
		{
			name: "unclosed expression",
			yml:  "version: 1\njobs:\n  a:\n    image: alpine\n    steps:\n      - name: s\n        run: echo ${{ matrix.go\n",
			code: "parse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := minicirunner.Parse([]byte(tc.yml))
			assertErrorCode(t, err, tc.code)
		})
	}
}

func ExampleRun() {
	yml := []byte(`version: 1
jobs:
  build:
    image: alpine:3
    steps:
      - name: build
        run: echo building
`)
	p, _ := minicirunner.Parse(yml)
	fake := minicirunner.FakeRuntime{
		"echo building": func(ctx context.Context, workdir string, out io.Writer) int {
			_, _ = io.WriteString(out, "built\n")
			return 0
		},
	}
	rep, _ := minicirunner.Run(context.Background(), p, minicirunner.Options{
		Runtime: fake,
		Logs: func(l minicirunner.LogLine) {
			fmt.Printf("[%s] %s\n", l.Job, l.Text)
		},
	})
	fmt.Println("ok:", rep.OK)
	// Output:
	// [build] built
	// ok: true
}
