# mini-ci-runner

Run CI pipelines from a YAML file in containers with job dependencies, matrix expansion, content-keyed caches, artifacts and live logs. Developers often cannot run their CI pipeline locally before pushing, so a typo in a matrix or a cache key costs a round trip to hosted CI. Unlike full GitHub Actions emulators (act) or programmable SDKs (Dagger), `mini-ci-runner` provides a single small, documented pipeline format and an embeddable Go API with an in-memory `FakeRuntime` so pipelines themselves can be unit-tested without containers.

## Installation

```bash
go get github.com/Trysac/u919-mini-ci-runner
go install github.com/Trysac/u919-mini-ci-runner/cmd/mini-ci-runner@latest
```

## Usage

### Go API

```go
p, err := minicirunner.Parse(yamlBytes)
rep, err := minicirunner.Run(ctx, p, minicirunner.Options{
	Runtime:      minicirunner.NewDockerRuntime("docker"),
	SourceDir:    ".",
	CacheDir:     ".ci/cache",
	ArtifactsDir: ".ci/artifacts",
})
fmt.Println(rep.OK, err)
```

## API Summary

- `Parse(data []byte) (*Pipeline, error)`: Parses YAML input with validation against limits (1 MiB size, 32 depth) and schema enforcement.
- `Run(ctx context.Context, p *Pipeline, opts Options) (*Report, error)`: Executes the pipeline via `Runtime` (Docker or Fake), managing concurrency and DAG scheduling.
- `Options`: Configures runtime, directories (`SourceDir`, `CacheDir`, `ArtifactsDir`), concurrency (`MaxParallel`), logging (`Logs`), and event hooks (`OnEvent`).
- Errors: Specific error codes include `parse`, `input_too_large`, `aliases_not_allowed`, `too_deep`, `invalid_path`, `cycle`, `unknown_job`, `matrix_too_large`, `invalid_timeout`, `runtime`.

## Why not act or Dagger?

Unlike full GitHub Actions emulators (`act`) or programmable SDKs (`Dagger`), `mini-ci-runner` provides a single small, documented pipeline format and an embeddable Go API with an in-memory `FakeRuntime` so pipelines themselves can be unit-tested without containers.

See [docs/SECURITY.md](docs/SECURITY.md) for security details.