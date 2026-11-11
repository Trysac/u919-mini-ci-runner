# Architecture

`mini-ci-runner` is structured as an embeddable Go library (`package minicirunner`) and a thin CLI (`cmd/mini-ci-runner`).

## Components

1. **Parser & Validator (`minicirunner.go`)**:
   - Enforces a 1 MiB input size limit (`input_too_large`).
   - Walks the `yaml.Node` AST prior to decoding to enforce a maximum nesting depth of 32 (`too_deep`) and reject YAML aliases (`aliases_not_allowed`).
   - Uses `yaml.Decoder` with `KnownFields(true)` so unknown keys produce a `parse` error.
   - Validates relative clean paths without `..` (`invalid_path`), timeouts (`1ms`–`24h`, `invalid_timeout`), DAG cycles (`cycle`), missing dependencies (`unknown_job`), and matrix expansion limits (256 combinations per job, 100 total expanded jobs, `matrix_too_large`).
   - Expands matrix combinations deterministically: matrix keys sorted alphabetically, values in YAML declaration order.

2. **DAG Scheduler & Runner (`runner.go`)**:
   - Ready jobs are launched in sorted job ID order with at most `MaxParallel` running concurrently.
   - Each expanded job leg gets an isolated copy of `SourceDir` under `WorkDir/<sanitised-id>` capped at 512 MiB.
   - Restores immutable content-keyed caches from `CacheDir/<sha256(key)>` prior to steps and saves after job success only if absent.
   - Streams logs line-by-line via `Options.Logs`, truncating output past `MaxLogBytes` with `[log truncated]`.

3. **Runtimes (`runtime.go`)**:
   - `NewDockerRuntime(binary)` runs containers via an explicit argv slice (`exec.CommandContext`) without host shell interpolation.
   - `FakeRuntime` maps command strings to Go callbacks for deterministic testing.
