//go:build conformance

package minicirunner_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Trysac/u919-mini-ci-runner"
)

func TestConformanceDockerExamplePipeline(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not found on PATH; skipping conformance test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not available (%v); skipping conformance test", err)
	}

	data, err := os.ReadFile(filepath.Join("testdata", "example.yml"))
	if err != nil {
		t.Fatalf("failed to read testdata/example.yml: %v", err)
	}
	p, err := minicirunner.Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.sum"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	cacheDir := t.TempDir()
	artDir := t.TempDir()

	var events []minicirunner.Event
	opts := minicirunner.Options{
		Runtime:      minicirunner.NewDockerRuntime("docker"),
		SourceDir:    srcDir,
		WorkDir:      workDir,
		CacheDir:     cacheDir,
		ArtifactsDir: artDir,
		OnEvent: func(e minicirunner.Event) {
			events = append(events, e)
		},
	}

	// First run: populates cache and writes artifact.
	rep1, err := minicirunner.Run(context.Background(), p, opts)
	if err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
	if !rep1.OK {
		t.Fatalf("first Run Report.OK = false: %+v", rep1.Jobs)
	}

	artFile := filepath.Join(artDir, "build", "dist", "app.txt")
	if _, err := os.Stat(artFile); err != nil {
		t.Fatalf("expected artifact at %s: %v", artFile, err)
	}

	// Second run: hits cache.
	events = nil
	rep2, err := minicirunner.Run(context.Background(), p, opts)
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if !rep2.OK {
		t.Fatalf("second Run Report.OK = false: %+v", rep2.Jobs)
	}
	foundHit := false
	for _, ev := range events {
		if ev.Kind == "cache_hit" {
			foundHit = true
		}
	}
	if !foundHit {
		t.Fatalf("expected cache_hit on second conformance run, got events: %+v", events)
	}

	t.Cleanup(func() {
		_ = filepath.WalkDir(workDir, func(p string, d os.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(p, 0o777)
			}
			return nil
		})
	})
}
